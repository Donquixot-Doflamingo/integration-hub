//go:build integration

package adminapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

func init() {
	mutationCases["addNextCredential"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/accounts/ct-brandA-in/credentials/next",
		body:  `{"credentials":{"account_id":"DEV-PROJECT-A","passcode":"next-passcode-0004"},"reason":"quarterly rotation"}`,
		setup: func(a *adminRig) { withRotator(a) }}
	mutationCases["promoteNextCredential"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/accounts/ct-brandA-in/credentials/promote",
		body: `{"expected_next_version":2,"reason":"connection test passed"}`, setup: func(a *adminRig) {
			withRotator(a)
			if _, err := a.s.d.Rotator.AddNext(a.t.Context(), "ct-brandA-in", map[string]string{"passcode": accessSecrets[3]}, nil, "setup@example.com", "setup"); err != nil {
				a.t.Fatal(err)
			}
		}}
	mutationCases["deleteOldCredential"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/accounts/ct-brandA-in/credentials/delete-old",
		body: `{"expected_previous_version":1,"reason":"no auth failures for 15 min"}`, setup: func(a *adminRig) {
			withRotator(a)
			ctx := a.t.Context()
			if _, err := a.s.d.Rotator.AddNext(ctx, "ct-brandA-in", map[string]string{"passcode": accessSecrets[3]}, nil, "setup@example.com", "setup"); err != nil {
				a.t.Fatal(err)
			}
			if _, err := a.s.d.Rotator.Promote(ctx, "ct-brandA-in", 2, "setup@example.com", "setup"); err != nil {
				a.t.Fatal(err)
			}
		}}
}

// pointVendorAt publishes the clevertap vendor with both regions on url (mockvendor).
func pointVendorAt(a *adminRig, url string) {
	a.t.Helper()
	if _, err := a.pub.Edit(a.t.Context(), func(t control.Tree, _ *registry.Snapshot) (control.Change, error) {
		return control.Change{Actor: "test@example.com", Action: "updateVendor", ObjectType: "vendor", ObjectID: "clevertap", Reason: "point at mockvendor"},
			accessSetKeys(t, "vendors/clevertap.yaml", map[string]any{"regions": map[string]string{"in": url, "us": url}})
	}); err != nil {
		a.t.Fatal(err)
	}
}

// TestCredentialRotationRunbook walks DESIGN §6.6 through the API: add next → test next (the
// vendor accepts only the new passcode) → promote → delete old, with 409s for steps that do not
// apply, and no secret in any response.
func TestCredentialRotationRunbook(t *testing.T) {
	a := newAdminRig(t)
	store := withRotator(a)
	mv := mockvendor.Start(t)
	pointVendorAt(a, mv.VendorURL)
	mv.SetRoute(t, "deny", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "*", Path: "/**"}, Priority: 1,
		Behaviour: mockvendor.Behaviour{Status: 401, Body: json.RawMessage(`{"status":"fail","error":"Invalid credentials"}`)}})
	mv.SetRoute(t, "allow-next", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload",
		Headers: map[string]string{"X-CleverTap-Passcode": accessSecrets[3], "X-CleverTap-Account-Id": "DEV-PROJECT-A"}}, Priority: 10,
		Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","processed":0,"unprocessed":[]}`)}})

	type step struct {
		name, path, body string
		status           int
		check            func(body map[string]any) error
	}
	connection := func(ok bool, slot string, version float64) func(map[string]any) error {
		return func(b map[string]any) error {
			if b["ok"] != ok || b["slot"] != slot || b["cred_version"] != version || b["endpoint_ref"] != "upload@1" || b["change_id"] != nil {
				return fmt.Errorf("connection test %v", b)
			}
			return nil
		}
	}
	slots := func(want ...string) func(map[string]any) error {
		return func(b map[string]any) error {
			got, _ := b["credentials"].([]any)
			if len(got) != len(want) || b["change_id"] == nil {
				return fmt.Errorf("slots %v, want %v", got, want)
			}
			for i, w := range want {
				s := got[i].(map[string]any)
				if fmt.Sprintf("%s@%v", s["slot"], s["cred_version"]) != w {
					return fmt.Errorf("slots %v, want %v", got, want)
				}
			}
			return nil
		}
	}
	steps := []step{
		{"promote without next", "/credentials/promote", `{"expected_next_version":2,"reason":"too early"}`, 409, nil},
		{"test missing next", "/test-connection", `{"slot":"next"}`, 409, nil},
		{"active credential is refused by the vendor", "/test-connection", `{}`, 200, connection(false, "active", 1)},
		{"add next", "/credentials/next", `{"credentials":{"account_id":"DEV-PROJECT-A","passcode":"` + accessSecrets[3] + `"},"expires_at":"2027-10-31T00:00:00Z","reason":"rotation"}`, 200, slots("active@1", "next@2")},
		{"next credential works", "/test-connection", `{"slot":"next","endpoint_ref":"upload@1"}`, 200, connection(true, "next", 2)},
		{"promote stale version", "/credentials/promote", `{"expected_next_version":5,"reason":"stale"}`, 409, nil},
		{"promote", "/credentials/promote", `{"expected_next_version":2,"reason":"test passed"}`, 200, slots("active@2", "previous@1")},
		{"active now works", "/test-connection", `{"slot":"active"}`, 200, connection(true, "active", 2)},
		{"delete wrong previous", "/credentials/delete-old", `{"expected_previous_version":2,"reason":"wrong"}`, 409, nil},
		{"delete old", "/credentials/delete-old", `{"expected_previous_version":1,"reason":"quiet for 15 min"}`, 200, slots("active@2")},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			a.t = t
			resp, body := a.do(RoleAdmin, http.MethodPost, "/accounts/ct-brandA-in"+st.path, st.body)
			noSecrets(t, resp.Body.String())
			if resp.Code != st.status {
				t.Fatalf("%d %s", resp.Code, resp.Body)
			}
			if st.check != nil {
				if err := st.check(body); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	a.t = t
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts/ct-nope-acct/credentials/next", `{"credentials":{"p":"v"},"reason":"nope"}`); resp.Code != 404 {
		t.Fatalf("unknown account: %d", resp.Code)
	}
	all, _ := store.ReadAll()
	if got := all["ct-brandA-in"]; got.Active.Values["passcode"] != accessSecrets[3] || got.Next != nil || got.Previous != nil {
		t.Fatalf("store after the runbook: active v%d next %v previous %v", got.Active.Version, got.Next != nil, got.Previous != nil)
	}
	// The probe carried the vendor's own auth headers and the dry flag; the test is audited.
	reqs := mv.Requests(t, nil)
	if len(reqs) != 3 || reqs[0].Query["dry"][0] != "1" || reqs[0].Headers["X-Hub-Probe"][0] != "1" {
		t.Fatalf("probe requests %+v", reqs)
	}
	var audited int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.change_log WHERE action = 'testAccountConnection' AND object_id = 'ct-brandA-in'
		AND after->>'ok' IS NOT NULL AND at > now() - interval '1 minute'`).Scan(&audited)
	if audited < 3 {
		t.Fatalf("connection tests audited %d times, want 3", audited)
	}
	var leaked int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.change_log WHERE before::text LIKE '%'||$1||'%' OR after::text LIKE '%'||$1||'%'`, accessSecrets[3]).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("the change log holds secret material")
	}
}

// TestRotationWithoutStore: no credential store configured → 500, never a panic.
func TestRotationWithoutStore(t *testing.T) {
	a := newAdminRig(t)
	for _, p := range []string{"/credentials/next", "/test-connection"} {
		body := `{"credentials":{"p":"v"},"reason":"no store"}`
		if p == "/test-connection" {
			body = `{}`
		}
		if resp, _ := a.do(RoleAdmin, http.MethodPost, "/accounts/ct-brandA-in"+p, body); resp.Code != 500 {
			t.Fatalf("%s: %d %s", p, resp.Code, resp.Body)
		}
	}
}
