//go:build integration

package roles

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// Fake secrets for the secret grep (SECURITY.md §3 "fixed fake values clearly marked"; gosec
// does not scan tests).
const (
	grepAcctA     = "fake-acct-GREP-A1"
	grepPassA     = "fake-pass-GREP-A1-not-real"
	grepAcctB     = "fake-acct-GREP-B1"
	grepPassB     = "fake-pass-GREP-B1-not-real"
	grepAcctA2    = "fake-acct-GREP-A2"
	grepPassA2    = "fake-pass-GREP-A2-rotated-not-real"
	grepSigning   = "whsec-grep-signing-secret-0123456789"
	grepMaskKey   = "grep-audit-mask-key-0123456789abcdef-not-real"
	grepWrongKey  = "hk_test_presented_but_unknown_key_grep01"
	grepStrayPass = "fake-pass-GREP-for-unknown-account"
)

// grepConfig copies the dev registry with its mockvendor and callback receiver addresses
// replaced, so the roles talk to this test's own mockvendor and receiver.
func grepConfig(t *testing.T, dir string, replace map[string]string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		for from, to := range replace {
			s = strings.ReplaceAll(s, from, to)
		}
		return writeFile(filepath.Join(dst, rel), s)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestRoleLogsCarryNoSecrets is HUB-234's "secret grep over logs is empty" across all roles:
// gateway, worker, scheduler and admin run together on credentials from the encrypted localfile
// source and exercise every path that handles a secret (sync call, a vendor error that echoes
// the passcode, async delivery with a signed callback, admin connection test, credential
// rotation and use of the rotated value, unknown API key). Every secret is declared with forbid;
// this test greps its own window of the logs, and TestMain greps the whole package run.
func TestRoleLogsCarryNoSecrets(t *testing.T) {
	freshArchive(t)
	mark := len(roleLogs.String())

	// Encrypted credential source: vendor accounts and the registrar signing secret.
	dir := t.TempDir()
	keyFile, secretsFile := filepath.Join(dir, "dev.key"), filepath.Join(dir, "secrets.enc")
	key, err := creds.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.WriteKeyFile(keyFile, key, false); err != nil {
		t.Fatal(err)
	}
	store, err := creds.NewLocalFile(secretsFile, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAll(map[string]creds.AccountSecrets{
		"ct-brandA-in":  {Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": grepAcctA, "passcode": grepPassA}}},
		"ct-brandB-us":  {Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": grepAcctB, "passcode": grepPassB}}},
		"reg_orders_cb": {Active: creds.Slot{Version: 1, Values: map[string]string{creds.SigningSecretKey: grepSigning}}},
	}); err != nil {
		t.Fatal(err)
	}
	forbid("vendor credential", grepAcctA, grepPassA, grepAcctB, grepPassB, grepAcctA2, grepPassA2, grepStrayPass)
	forbid("registrar signing secret", grepSigning)
	forbid("audit mask key", grepMaskKey)
	forbid("presented API key", grepWrongKey)
	forbid("dev key (seal + credential master key)", base64.StdEncoding.EncodeToString(key),
		base64.RawStdEncoding.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key), hex.EncodeToString(key))

	// The vendor and the tenant's callback receiver.
	mv := mockvendor.Start(t)
	var cbMu sync.Mutex
	var callbackSigs []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cbMu.Lock()
		callbackSigs = append(callbackSigs, r.Header.Get(callbacks.SignatureHeader))
		cbMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(receiver.Close)
	vendorOK := func() {
		mv.Reset(t)
		mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
			Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)}})
	}
	vendorOK()

	configDir := grepConfig(t, testConfig(t, Gateway).ConfigDir, map[string]string{
		"http://127.0.0.1:8099":               mv.VendorURL,
		"http://127.0.0.1:8097/hub/callbacks": receiver.URL + "/hub/callbacks",
	})
	blobURL := "file://" + t.TempDir()
	role := func(r Role) Config {
		cfg := composeConfig(t, r)
		cfg.ConfigDir, cfg.BlobURL = configDir, blobURL
		cfg.CredSource, cfg.DevKeyFile = "localfile://"+secretsFile, keyFile
		cfg.AuditMaskKey = Secret(grepMaskKey)
		return cfg
	}
	adminCfg := role(Admin)
	adminCfg.AdminDevOIDC = true
	admin := "http://" + startRoleWith(t, adminCfg, Options{})["http"]
	gateway := "http://" + startRoleWith(t, role(Gateway), Options{})["http"]
	workerCfg := role(Worker)
	workerCfg.WorkerPool, workerCfg.CallbackAllowLoopback = "messaging", true
	startRoleWith(t, workerCfg, Options{})
	startRoleWith(t, role(Scheduler), Options{})

	invoke := func(key, integration string) (int, string) {
		return send(t, http.MethodPost, gateway+"/v1/integrations/"+integration+"/invoke", trackBody,
			map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json"})
	}
	passcodes := func() []string {
		var out []string
		for _, r := range mv.Requests(t, nil) {
			out = append(out, r.Headers["X-Clevertap-Passcode"]...)
		}
		return out
	}

	// 1. Gateway, sync: the passcode reaches the vendor (the secret is really in flight).
	if code, body := invoke(devAPIKey, "int_evt_brandA"); code != http.StatusOK {
		t.Fatalf("sync invoke: %d %s", code, body)
	}
	if got := passcodes(); len(got) != 1 || got[0] != grepPassA {
		t.Fatalf("vendor saw passcodes %q, want the active credential", got)
	}

	// 2. Gateway → worker, async with a signed callback to the tenant.
	if code, body := invoke(devAPIKey, "int_evt_brandB"); code != http.StatusAccepted {
		t.Fatalf("async invoke: %d %s", code, body)
	}
	waitFor(t, 15*time.Second, "the signed callback", func() bool {
		cbMu.Lock()
		defer cbMu.Unlock()
		return len(callbackSigs) > 0 && strings.Contains(callbackSigs[0], "v1=")
	})

	// 3. A vendor that echoes the credentials in its error body (an auth failure and a reject),
	// seen by the gateway (wait mode) and by the worker (async, which then calls back
	// request.failed).
	for _, status := range []int{400, 401} {
		mv.Reset(t)
		mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
			Behaviour: mockvendor.Behaviour{Status: status, Body: json.RawMessage(fmt.Sprintf(
				`{"status":"fail","error":"Invalid passcode %s / %s for account %s / %s"}`, grepPassA, grepPassB, grepAcctA, grepAcctB))}})
		if code, body := invoke(devAPIKey, "int_evt_brandA"); code < 400 {
			t.Fatalf("vendor %d: invoke answered %d %s", status, code, body)
		}
	}
	if code, body := invoke(devAPIKey, "int_evt_brandB"); code != http.StatusAccepted {
		t.Fatalf("async invoke against the failing vendor: %d %s", code, body)
	}
	waitFor(t, 15*time.Second, "the request.failed callback", func() bool {
		cbMu.Lock()
		defer cbMu.Unlock()
		return len(callbackSigs) > 1
	})

	// A caller error after authorization: the gateway's request-path log line (positive
	// control that request-path lines reach the capture).
	if code, body := send(t, http.MethodPost, gateway+"/v1/integrations/int_evt_brandA/invoke", `{"user": {}}`,
		map[string]string{"Authorization": "Bearer " + devAPIKey, "Content-Type": "application/json"}); code != http.StatusBadRequest {
		t.Fatalf("invalid body answered %d %s", code, body)
	}

	// 4. Admin: connection test against the echoing vendor, then a rotation that the gateway uses.
	code, body := send(t, http.MethodPost, admin+"/oidc/dev-token", `{"sub":"secops","email":"secops@example.com","roles":["admin"]}`, nil)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || code != 200 || tok.AccessToken == "" {
		t.Fatalf("dev token: %d %s", code, body)
	}
	forbid("admin access token", tok.AccessToken)
	auth := map[string]string{"Authorization": "Bearer " + tok.AccessToken, "Content-Type": "application/json"}
	if code, body := send(t, http.MethodPost, admin+"/admin/v1/accounts/ct-brandA-in/test-connection", `{"slot":"active"}`, auth); code != 200 {
		t.Fatalf("test-connection: %d %s", code, body)
	}
	vendorOK()
	if code, body := send(t, http.MethodPost, admin+"/admin/v1/accounts/ct-nope/credentials/next",
		`{"credentials":{"passcode":"`+grepStrayPass+`"},"reason":"secret grep: unknown account"}`, auth); code < 400 {
		t.Fatalf("credentials for an unknown account: %d %s", code, body)
	}
	if code, body := send(t, http.MethodPost, admin+"/admin/v1/accounts/ct-brandA-in/credentials/next",
		`{"credentials":{"account_id":"`+grepAcctA2+`","passcode":"`+grepPassA2+`"},"reason":"secret grep rotation"}`, auth); code != 200 {
		t.Fatalf("add next: %d %s", code, body)
	}
	if code, body := send(t, http.MethodPost, admin+"/admin/v1/accounts/ct-brandA-in/test-connection", `{"slot":"next"}`, auth); code != 200 {
		t.Fatalf("test-connection next: %d %s", code, body)
	}
	if code, body := send(t, http.MethodPost, admin+"/admin/v1/accounts/ct-brandA-in/credentials/promote",
		`{"expected_next_version":2,"reason":"secret grep rotation"}`, auth); code != 200 {
		t.Fatalf("promote: %d %s", code, body)
	}
	waitFor(t, 10*time.Second, "the gateway to use the rotated credential", func() bool {
		mv.Reset(t)
		vendorOK()
		if code, _ := invoke(devAPIKey, "int_evt_brandA"); code != http.StatusOK {
			return false
		}
		got := passcodes()
		return len(got) == 1 && got[0] == grepPassA2
	})

	// 5. An unknown API key is refused and never logged.
	if code, _ := invoke(grepWrongKey, "int_evt_brandA"); code != http.StatusUnauthorized {
		t.Fatalf("unknown key answered %d", code)
	}

	logs := roleLogs.String()[mark:]
	for _, r := range []Role{Gateway, Worker, Scheduler, Admin} {
		if !strings.Contains(logs, `"role":"`+string(r)+`"`) {
			t.Errorf("no %s log lines captured: the grep would prove nothing for it", r)
		}
	}
	if !strings.Contains(logs, `"msg":"request failed"`) || !strings.Contains(logs, `"msg":"credentials reloaded after rotation"`) {
		t.Error("request-path or rotation log lines missing from the capture")
	}
	for _, l := range scanLogs(logs) {
		t.Errorf("secret in logs (%s): %.400s", l.label, l.line)
	}
}

// waitFor polls cond every 50 ms until it holds or d elapses.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
