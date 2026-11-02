//go:build integration

package adminapi

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Secret values the access tests write. No response body may ever contain one of them.
var accessSecrets = []string{"dev-passcode-A-0001", "dev-passcode-B-0002", "created-passcode-0003", "next-passcode-0004", "dev-signing-secret-0005"}

// withRotator gives the rig a credential store in a temp dir, seeded like the dev config:
// the two dev accounts and the dev registrar secret.
func withRotator(a *adminRig) *creds.LocalFile {
	a.t.Helper()
	key, err := creds.GenerateKey()
	if err != nil {
		a.t.Fatal(err)
	}
	store, err := creds.NewLocalFile(filepath.Join(a.t.TempDir(), "secrets.enc"), key)
	if err != nil {
		a.t.Fatal(err)
	}
	if err := store.WriteAll(map[string]creds.AccountSecrets{
		"ct-brandA-in":  {Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": "DEV-PROJECT-A", "passcode": accessSecrets[0]}}},
		"ct-brandB-us":  {Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": "DEV-PROJECT-B", "passcode": accessSecrets[1]}}},
		"reg_orders_cb": {Active: creds.Slot{Version: 1, Values: map[string]string{creds.SigningSecretKey: accessSecrets[4]}}},
	}); err != nil {
		a.t.Fatal(err)
	}
	a.s.d.Rotator = &control.Rotator{Pool: a.s.d.Pool, Store: store}
	return store
}

// noSecrets fails when body carries any secret value the tests wrote.
func noSecrets(t *testing.T, body string, extra ...string) {
	t.Helper()
	for _, s := range append(append([]string{}, accessSecrets...), extra...) {
		if s != "" && strings.Contains(body, s) {
			t.Fatalf("response leaks secret material %q: %s", s, body)
		}
	}
}

// createTestAccount creates account id through the API (it drains at once: no version targets it).
func createTestAccount(a *adminRig, id string) {
	a.t.Helper()
	resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts", `{"account_id":"`+id+`","vendor_id":"clevertap","region":"in","config":{"project_id":"P"},
		"credentials":{"account_id":"P","passcode":"`+accessSecrets[2]+`"},"reason":"test account"}`)
	if resp.Code != http.StatusCreated {
		a.t.Fatalf("create %s: %d %s", id, resp.Code, resp.Body)
	}
}

func init() {
	mutationCases["createAccount"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/accounts",
		body:  `{"account_id":"ct-mut-create","vendor_id":"clevertap","region":"in","config":{},"credentials":{"passcode":"created-passcode-0003"},"reason":"new workspace"}`,
		setup: func(a *adminRig) { withRotator(a) }}
	mutationCases["updateAccount"] = mutationCase{role: RoleAdmin, method: http.MethodPatch, path: "/accounts/ct-brandA-in",
		body: `{"quota":{"rps":80},"reason":"vendor raised the quota"}`, setup: func(a *adminRig) { withRotator(a) }}
	mutationCases["transitionAccountLifecycle"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/accounts/ct-mut-life/lifecycle",
		body: `{"target_state":"retired","reason":"brand moved away"}`, setup: func(a *adminRig) {
			withRotator(a)
			createTestAccount(a, "ct-mut-life")
		}}
}

func TestAccountsCRUD(t *testing.T) {
	a := newAdminRig(t)
	store := withRotator(a)
	createTestAccount(a, "ct-new-acct")

	resp, body := a.do(RoleViewer, http.MethodGet, "/accounts/ct-new-acct", "")
	noSecrets(t, resp.Body.String())
	slots, _ := body["credentials"].([]any)
	if resp.Code != 200 || len(slots) != 1 || slots[0].(map[string]any)["cred_version"].(float64) != 1 || resp.Header().Get("ETag") == "" {
		t.Fatalf("get: %d %s", resp.Code, resp.Body)
	}
	all, _ := store.ReadAll()
	if all["ct-new-acct"].Active.Values["passcode"] != accessSecrets[2] {
		t.Fatal("the credential store does not hold the created credential")
	}
	_, snap, _ := a.pub.Current(t.Context())
	if got := snap.Accounts["ct-new-acct"]; got == nil || got.CredentialsRef != "localfile://ct-new-acct" || got.Config["project_id"] != "P" {
		t.Fatalf("snapshot account %+v", got)
	}

	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts", `{"account_id":"ct-new-acct","vendor_id":"clevertap","region":"in","config":{},"credentials":{"p":"x"},"reason":"again"}`); resp.Code != 409 {
		t.Fatalf("duplicate: %d %s", resp.Code, resp.Body)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts", `{"account_id":"ct-bad-vendor","vendor_id":"nope-vendor","region":"in","config":{},"credentials":{"p":"x"},"reason":"bad"}`); resp.Code != 400 {
		t.Fatalf("unknown vendor: %d %s", resp.Code, resp.Body)
	}
	if all, _ := store.ReadAll(); len(all) != 4 {
		t.Fatalf("refused creates touched the store: %d entries", len(all))
	}

	etag := resp.Header().Get("ETag")
	stale := httpReq(t, a, http.MethodPatch, "/accounts/ct-new-acct", `{"quota":{"rps":7},"reason":"stale write"}`)
	stale.Header.Set("If-Match", `"0000"`)
	if rec := a.spec.Serve(t, a.h, stale); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %s", rec.Code, rec.Body)
	}
	ok := httpReq(t, a, http.MethodPatch, "/accounts/ct-new-acct", `{"config":{"project_id":null,"brand_code":"Z"},"quota":{"rps":7,"daily":null},"reason":"tune"}`)
	ok.Header.Set("If-Match", etag)
	rec := a.spec.Serve(t, a.h, ok)
	noSecrets(t, rec.Body.String())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"brand_code":"Z"`) || strings.Contains(rec.Body.String(), "project_id") || !strings.Contains(rec.Body.String(), `"rps":7`) {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}

	resp, body = a.do(RoleViewer, http.MethodGet, "/accounts?vendor_id=clevertap&limit=2", "")
	noSecrets(t, resp.Body.String())
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 2 || body["next_cursor"] == nil {
		t.Fatalf("list page 1: %d %s", resp.Code, resp.Body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/accounts?limit=2&cursor="+body["next_cursor"].(string), "")
	if items, _ := body["items"].([]any); resp.Code != 200 || len(items) != 1 || body["next_cursor"] != nil {
		t.Fatalf("list page 2: %d %s", resp.Code, resp.Body)
	}
	if resp, body := a.do(RoleViewer, http.MethodGet, "/accounts?q=brandb", ""); resp.Code != 200 || len(body["items"].([]any)) != 1 {
		t.Fatalf("q filter: %d %s", resp.Code, resp.Body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/accounts/ct-nope-acct", ""); resp.Code != 404 {
		t.Fatalf("unknown: %d", resp.Code)
	}
}

func TestAccountLifecycle(t *testing.T) {
	a := newAdminRig(t)
	store := withRotator(a)
	createTestAccount(a, "ct-life-acct")

	cases := []struct {
		name, account, target string
		guard                 func(ctx context.Context, snap *registry.Snapshot, account string) (int, int, error)
		status                int
		code                  string
	}{
		{"targeted account cannot drain", "ct-brandA-in", "draining", nil, 409, CodeConflict},
		{"active cannot retire", "ct-brandA-in", "retired", nil, 409, CodeConflict},
		{"unreferenced draining cannot re-activate", "ct-life-acct", "active", nil, 409, CodeConflict},
		{"retire blocked by pinned requests and sessions", "ct-life-acct", "retired",
			func(context.Context, *registry.Snapshot, string) (int, int, error) { return 12, 3, nil }, 409, CodeRetireBlocked},
		{"retire", "ct-life-acct", "retired", nil, 200, ""},
		{"retired is final", "ct-life-acct", "draining", nil, 409, CodeConflict},
		{"unknown account", "ct-nope-acct", "retired", nil, 404, CodeNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a.t = t
			a.pub.Guard = control.RetireGuard(a.s.d.Pool)
			if tc.guard != nil {
				a.pub.Guard = tc.guard
			}
			resp, body := a.do(RoleAdmin, http.MethodPost, "/accounts/"+tc.account+"/lifecycle", `{"target_state":"`+tc.target+`","reason":"lifecycle test"}`)
			noSecrets(t, resp.Body.String())
			if resp.Code != tc.status {
				t.Fatalf("%d %s", resp.Code, resp.Body)
			}
			if tc.code != "" {
				e, _ := body["error"].(map[string]any)
				if e["code"] != tc.code {
					t.Fatalf("code %v: %s", e["code"], resp.Body)
				}
				if tc.code == CodeRetireBlocked {
					meta, _ := e["meta"].(map[string]any)
					if meta["pinned_requests"].(float64) != 12 || meta["open_sessions"].(float64) != 3 {
						t.Fatalf("blocking counts %v", meta)
					}
				}
				return
			}
			acct, _ := body["account"].(map[string]any)
			if acct["state"] != "retired" || len(acct["credentials"].([]any)) != 0 {
				t.Fatalf("retired account %v", acct)
			}
			if all, _ := store.ReadAll(); all["ct-life-acct"].Active.Version != 0 {
				t.Fatal("retire kept the credentials")
			}
		})
	}
}

func httpReq(t *testing.T, a *adminRig, method, path, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, Base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token(t, a.iss, RoleAdmin))
	req.Header.Set("Content-Type", "application/json")
	return req
}
