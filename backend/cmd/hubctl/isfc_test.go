package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
)

const (
	ciClientID = "integration-hub-ci"
	ciSecret   = "ci-secret-canary-41c7"
	testTenant = "3f1c2a4e-0000-4000-8000-000000000001"
)

// isfcAdmin is a fake Hub admin listener that accepts one ISFC credential (machine mode) or one
// bearer token, and records what it received.
type isfcAdmin struct {
	srv      *httptest.Server
	mu       sync.Mutex
	key      string // accepted x-isfc-api-key
	bearer   string // accepted bearer token
	headers  []http.Header
	requests atomic.Int32
}

func newISFCAdmin(t *testing.T) *isfcAdmin {
	t.Helper()
	a := &isfcAdmin{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		a.mu.Lock()
		a.headers = append(a.headers, r.Header.Clone())
		okISFC := a.key != "" && r.Header.Get("x-isfc-client-id") == ciClientID && r.Header.Get("x-isfc-api-key") == a.key && r.Header.Get("Authorization") == ""
		okBearer := a.bearer != "" && r.Header.Get("Authorization") == "Bearer "+a.bearer
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/admin/v1/me" || (!okISFC && !okBearer) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"UNAUTHORIZED","message":"no","request_id":"req_x"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"subject":"client:integration-hub-ci","roles":["admin"],"tenants":[],"token_expires_at":null}`)
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *isfcAdmin) setKey(k string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.key = k
}

func (a *isfcAdmin) last() http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.headers[len(a.headers)-1]
}

// machineEnv points hubctl at the fake admin and a mockisfc auth-service holding a key for the CI client.
func machineEnv(t *testing.T, admin *isfcAdmin) (m *mockisfc.Server, keyID, key string) {
	t.Helper()
	m = mockisfc.New(mockisfc.Options{})
	m.Seed("integration-hub", "hub-secret", "integration-hub-console")
	m.AddClient(ciClientID, ciSecret, "backend_services")
	keyID, key = m.IssueKey("integration-hub", ciClientID, "HUB_INTEGRATION:WRITE")
	auth := httptest.NewServer(m.Handler())
	t.Cleanup(auth.Close)
	t.Setenv("HUBCTL_ISFC_CLIENT_ID", ciClientID)
	t.Setenv("HUBCTL_ISFC_CLIENT_SECRET", ciSecret)
	t.Setenv("HUBCTL_ISFC_AUTH_URL", auth.URL)
	t.Setenv("HUB_ADMIN_TOKEN", "")
	t.Setenv("HUB_TENANT_ID", "")
	t.Setenv("HUBCTL_TOKEN_FILE", filepath.Join(t.TempDir(), "none"))
	admin.setKey(key)
	return m, keyID, key
}

func TestMachineModeCallsTheAdminDirectlyWithTheFetchedKey(t *testing.T) {
	admin := newISFCAdmin(t)
	m, _, key := machineEnv(t, admin)
	t.Setenv("HUB_TENANT_ID", testTenant)
	var out, errb bytes.Buffer
	for range 2 {
		out.Reset()
		if code := run([]string{"whoami", "-admin-url", admin.srv.URL}, &out, &errb); code != 0 || !strings.Contains(out.String(), "client:integration-hub-ci roles=admin") {
			t.Fatalf("whoami %d: %s %s", code, out.String(), errb.String())
		}
	}
	h := admin.last()
	if h.Get("x-isfc-client-id") != ciClientID || h.Get("x-isfc-api-key") != key {
		t.Errorf("credential headers %q / %q", h.Get("x-isfc-client-id"), h.Get("x-isfc-api-key"))
	}
	if h.Get("Authorization") != "" {
		t.Error("an Authorization header was sent in machine mode")
	}
	if h.Get("X-Tenant-Id") != testTenant {
		t.Errorf("X-Tenant-Id %q", h.Get("X-Tenant-Id"))
	}
	if n := m.Calls("GET /api/v1/client/api-key"); n != 2 { // one fetch per hubctl run, not per request
		t.Errorf("key fetches %d, want 2 (one per run)", n)
	}
	if strings.Contains(out.String()+errb.String(), key) || strings.Contains(out.String()+errb.String(), ciSecret) {
		t.Error("a secret reached the output")
	}
}

func TestMachineModeRefreshesTheKeyOnce(t *testing.T) {
	admin := newISFCAdmin(t)
	m, keyID, _ := machineEnv(t, admin)
	// The key ISFC hands out is rotated before the first call: the admin knows only the new one.
	m.RevokeKey(keyID)
	_, newKey := m.IssueKey("integration-hub", ciClientID, "HUB_INTEGRATION:WRITE")
	admin.setKey(newKey)
	var out, errb bytes.Buffer
	if code := run([]string{"whoami", "-admin-url", admin.srv.URL}, &out, &errb); code != 0 {
		t.Fatalf("whoami %d: %s", code, errb.String())
	}
	if n := m.Calls("GET /api/v1/client/api-key"); n != 1 {
		t.Errorf("key fetches %d, want 1 (the mock already returns the active key)", n)
	}

	// A key the admin refuses and ISFC keeps handing out: one retry at most, then the admin's error.
	admin.setKey("not-the-key")
	admin.requests.Store(0)
	out.Reset()
	errb.Reset()
	if code := run([]string{"whoami", "-admin-url", admin.srv.URL}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "UNAUTHORIZED") {
		t.Fatalf("whoami %d: %s", code, errb.String())
	}
	if n := admin.requests.Load(); n != 1 {
		t.Errorf("admin requests %d, want 1 (an unchanged key is not retried)", n)
	}
}

func TestMachineModeConfigurationErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		args    []string
		wantErr string
	}{
		{name: "id without secret", env: map[string]string{"HUBCTL_ISFC_CLIENT_SECRET": ""}, wantErr: "must be set together"},
		{name: "secret without id", env: map[string]string{"HUBCTL_ISFC_CLIENT_ID": ""}, wantErr: "must be set together"},
		{name: "both machine and token", env: map[string]string{"HUB_ADMIN_TOKEN": "a.b.c"}, wantErr: "unset one"},
		{name: "no auth url", env: map[string]string{"HUBCTL_ISFC_AUTH_URL": ""}, wantErr: "HUBCTL_ISFC_AUTH_URL"},
		{name: "wrong secret", env: map[string]string{"HUBCTL_ISFC_CLIENT_SECRET": "wrong-secret-canary"}, wantErr: "AS_VE_09"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin := newISFCAdmin(t)
			machineEnv(t, admin)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			var out, errb bytes.Buffer
			if code := run([]string{"whoami", "-admin-url", admin.srv.URL}, &out, &errb); code != 1 || !strings.Contains(errb.String(), tt.wantErr) {
				t.Fatalf("code %d: %s", code, errb.String())
			}
			if strings.Contains(errb.String(), "wrong-secret-canary") || strings.Contains(errb.String(), ciSecret) {
				t.Errorf("secret in the error: %s", errb.String())
			}
			if admin.requests.Load() != 0 {
				t.Error("the admin was called without a credential")
			}
		})
	}
}

func TestExplicitTokenBeatsMachineCredentialAndTenantHeaderIsSent(t *testing.T) {
	admin := newISFCAdmin(t)
	machineEnv(t, admin)
	admin.mu.Lock()
	admin.bearer = "user.token.value"
	admin.mu.Unlock()
	var out, errb bytes.Buffer
	if code := run([]string{"whoami", "-admin-url", admin.srv.URL, "-token", "user.token.value", "-tenant-id", testTenant}, &out, &errb); code != 0 {
		t.Fatalf("whoami %d: %s", code, errb.String())
	}
	h := admin.last()
	if h.Get("Authorization") != "Bearer user.token.value" || h.Get("x-isfc-api-key") != "" || h.Get("X-Tenant-Id") != testTenant {
		t.Errorf("headers %v", h)
	}
}

// fakeAuthService answers the loopback PKCE flow: GET /oauth2/auth checks the request and
// redirects back with a code; POST /oauth2/token verifies code_verifier against the challenge.
type fakeAuthService struct {
	srv *httptest.Server

	mu         sync.Mutex
	challenge  string
	redirect   string
	state      string
	deviceID   string
	tokenCalls int
	override   func(q url.Values) url.Values // changes the redirect query (state mismatch, error)
}

func newFakeAuthService(t *testing.T) *fakeAuthService {
	t.Helper()
	f := &fakeAuthService{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth2/auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("client_id") != "hubctl" || q.Get("code_challenge_method") != "S256" ||
			q.Get("code_challenge") == "" || q.Get("state") == "" || !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") ||
			!strings.HasSuffix(q.Get("redirect_uri"), "/callback") {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.challenge, f.redirect, f.state = q.Get("code_challenge"), q.Get("redirect_uri"), q.Get("state")
		override := f.override
		f.mu.Unlock()
		back := url.Values{"code": {"auth-code-1"}, "state": {q.Get("state")}}
		if override != nil {
			back = override(back)
		}
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
	})
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			GrantType, ClientID, RedirectURI, Code, CodeVerifier string
			SessionInfo                                          map[string]any
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		b.GrantType, _ = m["grant_type"].(string)
		b.ClientID, _ = m["client_id"].(string)
		b.RedirectURI, _ = m["redirect_uri"].(string)
		b.Code, _ = m["code"].(string)
		b.CodeVerifier, _ = m["code_verifier"].(string)
		f.mu.Lock()
		f.tokenCalls++
		f.deviceID = r.Header.Get("x-device-id")
		sum := sha256.Sum256([]byte(b.CodeVerifier))
		ok := b.GrantType == "authorization_code" && b.ClientID == "hubctl" && b.Code == "auth-code-1" && b.RedirectURI == f.redirect &&
			base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge && strings.HasPrefix(f.deviceID, "device-")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error_code":"AS_VE_40","error":"invalid grant"}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"message":"ok","data":{"access_token":"isfc.access.token-canary","refresh_token":"r","session_id":"s","expires_in":900}}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// followInBackground plays the browser: it GETs the authorize URL and follows the redirect to the loopback.
func followInBackground(t *testing.T) func(string) error {
	t.Helper()
	return func(target string) error {
		go func() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoginISFCLoopbackPKCE(t *testing.T) {
	auth := newFakeAuthService(t)
	var out bytes.Buffer
	tok, err := loginISFC(t.Context(), isfcLoginOptions{authURL: auth.srv.URL, clientID: "hubctl", timeout: 10 * time.Second, openBrowser: followInBackground(t)}, &out)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if tok.AccessToken != "isfc.access.token-canary" || tok.ExpiresIn != 15*time.Minute {
		t.Errorf("token %+v", tok)
	}
	if auth.tokenCalls != 1 {
		t.Errorf("token calls %d", auth.tokenCalls)
	}
	if !strings.Contains(out.String(), "/oauth2/auth?") {
		t.Errorf("the URL was not printed: %s", out.String())
	}
}

func TestLoginISFCCommandSavesTheTokenPrivatelyAndWhoamiUsesIt(t *testing.T) {
	auth := newFakeAuthService(t)
	admin := newISFCAdmin(t)
	admin.mu.Lock()
	admin.bearer = "isfc.access.token-canary"
	admin.mu.Unlock()
	file := filepath.Join(t.TempDir(), "cfg", "token")
	t.Setenv("HUBCTL_TOKEN_FILE", file)
	t.Setenv("HUB_ADMIN_TOKEN", "")
	t.Setenv("HUBCTL_ISFC_CLIENT_ID", "")
	t.Setenv("HUBCTL_ISFC_CLIENT_SECRET", "")
	// Without a browser the command prints the URL: play the browser from a goroutine that reads it.
	pr, pw, _ := os.Pipe()
	t.Cleanup(func() { _ = pr.Close() })
	var errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- loginISFCCmdWith(t.Context(), isfcLoginOptions{authURL: auth.srv.URL, clientID: "hubctl", noBrowser: true, timeout: 10 * time.Second}, pw, &errb)
		_ = pw.Close()
	}()
	br := followInBackground(t)
	buf := make([]byte, 4096)
	var seen string
	for !strings.Contains(seen, "Waiting for the sign-in") {
		n, err := pr.Read(buf)
		seen += string(buf[:n])
		if err != nil {
			break
		}
	}
	start := strings.Index(seen, "http")
	end := strings.IndexAny(seen[start:], " \n")
	if err := br(seen[start : start+end]); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("login exit %d: %s", code, errb.String())
	}
	st, err := os.Stat(file)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token file %v %v", st, err)
	}
	if b, _ := os.ReadFile(file); strings.TrimSpace(string(b)) != "isfc.access.token-canary" {
		t.Errorf("token file holds %q", b)
	}
	var out bytes.Buffer
	if code := run([]string{"whoami", "-admin-url", admin.srv.URL}, &out, &errb); code != 0 || !strings.Contains(out.String(), "client:integration-hub-ci") {
		t.Fatalf("whoami %d: %s %s", code, out.String(), errb.String())
	}
}

func TestLoginISFCFailures(t *testing.T) {
	tests := []struct {
		name     string
		override func(url.Values) url.Values
		opts     func(*isfcLoginOptions)
		wantErr  string
	}{
		{name: "state mismatch", override: func(q url.Values) url.Values { q.Set("state", "forged"); return q }, wantErr: "expected state"},
		{name: "ISFC refuses", override: func(q url.Values) url.Values {
			return url.Values{"state": {q.Get("state")}, "error": {"access_denied"}}
		}, wantErr: "access_denied"},
		{name: "no code", override: func(q url.Values) url.Values { q.Del("code"); return q }, wantErr: "no code"},
		{name: "never comes back", opts: func(o *isfcLoginOptions) {
			o.timeout = 150 * time.Millisecond
			o.openBrowser = func(string) error { return nil }
		}, wantErr: "no sign-in within"},
		{name: "no auth url", opts: func(o *isfcLoginOptions) { o.authURL = "" }, wantErr: "HUBCTL_ISFC_AUTH_URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth := newFakeAuthService(t)
			auth.override = tt.override
			o := isfcLoginOptions{authURL: auth.srv.URL, clientID: "hubctl", timeout: 10 * time.Second, openBrowser: followInBackground(t)}
			if tt.opts != nil {
				tt.opts(&o)
			}
			_, err := loginISFC(t.Context(), o, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err %v, want %q", err, tt.wantErr)
			}
			if auth.tokenCalls != 0 {
				t.Error("the code was exchanged although the sign-in failed")
			}
		})
	}
}

func TestLoginFlagsConflict(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"login", "-dev", "-isfc"}, &out, &errb); code != 2 {
		t.Errorf("login -dev -isfc: %d", code)
	}
	errb.Reset()
	if code := run([]string{"login"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "login -isfc") {
		t.Errorf("login: %d %s", code, errb.String())
	}
	t.Setenv("HUBCTL_ISFC_AUTH_URL", "")
	errb.Reset()
	if code := run([]string{"login", "-isfc", "-no-browser"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "HUBCTL_ISFC_AUTH_URL") {
		t.Errorf("login -isfc without auth url: %d %s", code, errb.String())
	}
}

func TestPKCEIsS256AndDeviceIDHasTheConsoleShape(t *testing.T) {
	v, c, err := newPKCE()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(v))
	if len(v) < 43 || c != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Errorf("verifier %q challenge %q", v, c)
	}
	id, _ := newDeviceID()
	if len(id) != len("device-")+36 || !strings.HasPrefix(id, "device-") {
		t.Errorf("device id %q", id)
	}
}
