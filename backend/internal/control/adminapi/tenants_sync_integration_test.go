//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
)

const (
	syncHub     = "integration-hub"
	syncConsole = "integration-hub-console"
)

// testSyncer is the cmd/hub adapter (roles.adminSyncer) over the real reconciler.
type testSyncer struct{ rec *isfcsync.Reconciler }

func (s testSyncer) run(ctx context.Context, opt isfcsync.RunOptions) (SyncResult, error) {
	res, err := s.rec.Run(ctx, isfcsync.TriggerManual, opt)
	switch {
	case errors.Is(err, isfcsync.ErrBusy):
		return SyncResult{}, ErrSyncBusy
	case errors.Is(err, isfcsync.ErrNotLinked):
		return SyncResult{}, ErrSyncNotLinked
	case err != nil:
		return SyncResult{}, err
	}
	return SyncResult{ChangeID: res.ChangeID, Outcome: res.Outcome, Error: res.Error, Started: res.Started, Finished: res.Finished,
		Created: res.Created, Updated: res.Updated + res.Enabled, Disabled: res.Disabled, Unchanged: res.Unchanged}, nil
}

func (s testSyncer) SyncAll(ctx context.Context, actor, reason string) (SyncResult, error) {
	return s.run(ctx, isfcsync.RunOptions{Actor: actor, Reason: reason})
}

func (s testSyncer) SyncOne(ctx context.Context, actor, tenant, reason string) (SyncResult, error) {
	return s.run(ctx, isfcsync.RunOptions{Actor: actor, Reason: reason, Tenant: tenant})
}

// syncFixture is mockisfc plus a real reconciler on the rig's publisher and database.
type syncFixture struct {
	mock *mockisfc.Server
	rec  *isfcsync.Reconciler
	hl   string // ISFC uuid of tenant HL
}

var fixtures sync.Map // *adminRig -> *syncFixture

func syncFor(a *adminRig) *syncFixture {
	if f, ok := fixtures.Load(a); ok {
		return f.(*syncFixture)
	}
	mock := mockisfc.New(mockisfc.Options{})
	mock.Seed(syncHub, "hub-secret", syncConsole)
	ts := httptest.NewServer(mock.Handler())
	a.t.Cleanup(ts.Close)
	client, err := isfc.New(isfc.Config{BaseURL: ts.URL, ClientID: syncHub, ClientSecret: "hub-secret"})
	if err != nil {
		a.t.Fatal(err)
	}
	store := isfcsync.PGStore{Pool: a.s.d.Pool}
	f := &syncFixture{mock: mock, hl: mock.AddTenant("HL", "Home Loans")}
	mock.SetClientTenants(syncConsole, f.hl)
	f.rec = &isfcsync.Reconciler{Source: client, Editor: a.pub, Store: store, Config: isfcsync.Config{ConsoleClientID: syncConsole, BackendClientID: syncHub}}
	a.s.d.Sync = testSyncer{rec: f.rec}
	fixtures.Store(a, f)
	return f
}

func init() {
	mutationCases["syncTenants"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/tenants/sync", body: `{"reason":"new tenant linked"}`,
		setup: func(a *adminRig) { syncFor(a) }}
	mutationCases["syncTenant"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/tenants/hl-t/sync", body: `{"reason":"name changed in ISFC"}`,
		setup: func(a *adminRig) {
			f := syncFor(a)
			if _, err := f.rec.Run(a.t.Context(), isfcsync.TriggerTick, isfcsync.RunOptions{}); err != nil { // creates hl-t
				a.t.Fatal(err)
			}
		}}
}

func decodeBody(rec *httptest.ResponseRecorder) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func asTenantAdmin(a *adminRig, method, path, body string, tenants ...string) (*httptest.ResponseRecorder, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), method, Base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token(a.t, a.iss, RoleTenantAdmin, tenants...))
	req.Header.Set("Content-Type", "application/json")
	rec := a.spec.ServeInvalid(a.t, a.h, req) // the cases below include refusals the contract does not list
	return rec, decodeBody(rec)
}

// TestISFCTenantsThroughTheAdminAPI covers HUB-484: createTenant refuses in ISFC mode, syncTenants
// and syncTenant pull from ISFC and report it, the tenant list and detail show the ISFC copy and
// the setup state, and a tenant admin finishes setup (one way) for its own tenant only.
func TestISFCTenantsThroughTheAdminAPI(t *testing.T) {
	a := newAdminRig(t)
	f := syncFor(a)
	a.s.d.TenantSource = "isfc"
	pl := f.mock.AddTenant("PL", "Personal Loans")
	f.mock.SetClientTenants(syncConsole, f.hl, pl)

	resp, body := a.do(RoleAdmin, http.MethodPost, "/tenants", `{"tenant_id":"by-hand","display_name":"By hand","reason":"try"}`)
	if e, _ := body["error"].(map[string]any); resp.Code != 409 || e["code"] != "tenant_managed_by_isfc" {
		t.Fatalf("createTenant in isfc mode: %d %s", resp.Code, resp.Body)
	}

	resp, body = a.do(RoleAdmin, http.MethodPost, "/tenants/sync", `{"reason":"link HL and PL"}`)
	if resp.Code != 200 || body["result"] != "ok" || body["created"] != 2.0 || body["unchanged"] != 0.0 || body["error"] != nil || body["change_id"] == nil {
		t.Fatalf("syncTenants: %d %s", resp.Code, resp.Body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/tenants/hl-t", "")
	isfcBlock, _ := body["isfc"].(map[string]any)
	if resp.Code != 200 || body["setup"] != "pending" || body["display_name"] != "Home Loans" || isfcBlock["key"] != "HL" ||
		isfcBlock["tenant_id"] != f.hl || isfcBlock["linked"] != true || isfcBlock["status"] != "ACTIVE" || body["disabled"] != false {
		t.Fatalf("getTenant: %d %s", resp.Code, resp.Body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/tenants?q=personal", "")
	if items, _ := body["items"].([]any); resp.Code != 200 || len(items) != 1 || items[0].(map[string]any)["setup"] != "pending" {
		t.Fatalf("listTenants: %d %s", resp.Code, resp.Body)
	}
	if resp, body = a.do(RoleViewer, http.MethodGet, "/tenants/orders-service", ""); body["isfc"] != nil || body["setup"] != "complete" {
		t.Fatalf("a legacy tenant: %d %s", resp.Code, resp.Body)
	}

	// A tenant admin finishes the setup of its own tenant: setup, rate limit; not disabled; not backwards.
	if rec, b := asTenantAdmin(a, http.MethodPatch, "/tenants/hl-t", `{"setup":"complete","rate_limit":{"rps":50,"burst":100},"reason":"setup done"}`, "hl-t"); rec.Code != 200 ||
		b["tenant"].(map[string]any)["setup"] != "complete" {
		t.Fatalf("finish setup: %d %s", rec.Code, rec.Body)
	}
	for name, tt := range map[string]struct {
		path, body string
		tenants    []string
		want       int
	}{
		"back to pending":           {"/tenants/hl-t", `{"setup":"pending","reason":"undo"}`, []string{"hl-t"}, 400},
		"disabled is platform-only": {"/tenants/hl-t", `{"disabled":true,"reason":"stop"}`, []string{"hl-t"}, 403},
		"another tenant":            {"/tenants/pl-t", `{"setup":"complete","reason":"not mine"}`, []string{"hl-t"}, 404},
		"isfc fields are read-only": {"/tenants/hl-t", `{"isfc":{"name":"x"},"reason":"edit"}`, []string{"hl-t"}, 400},
	} {
		if rec, _ := asTenantAdmin(a, http.MethodPatch, tt.path, tt.body, tt.tenants...); rec.Code != tt.want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body, tt.want)
		}
	}
	// A viewer and a tenant_viewer may not.
	if resp, _ := a.doInvalid(RoleViewer, http.MethodPatch, "/tenants/hl-t", `{"setup":"complete","reason":"x"}`); resp.Code != 403 {
		t.Fatalf("viewer: %d", resp.Code)
	}

	// Renamed in ISFC, then one tenant is synced.
	f.mock.RenameTenant(f.hl, "HL", "Housing Loans")
	resp, body = a.do(RoleAdmin, http.MethodPost, "/tenants/hl-t/sync", `{"reason":"name changed"}`)
	tn, _ := body["tenant"].(map[string]any)
	if resp.Code != 200 || body["result"] != "ok" || tn["display_name"] != "Housing Loans" || tn["setup"] != "complete" {
		t.Fatalf("syncTenant: %d %s", resp.Code, resp.Body)
	}
	if resp, _ = a.do(RoleAdmin, http.MethodPost, "/tenants/nope-tenant/sync", `{"reason":"unknown tenant"}`); resp.Code != 404 {
		t.Fatalf("unknown tenant: %d", resp.Code)
	}
	if resp, _ = a.do(RoleAdmin, http.MethodPost, "/tenants/orders-service/sync", `{"reason":"no ISFC link"}`); resp.Code != 409 {
		t.Fatalf("tenant without an ISFC link: %d", resp.Code)
	}

	// Deactivated in ISFC: disabled on the next sync; ISFC down: 200 failed, nothing disabled.
	f.mock.SetTenantStatus(pl, "INACTIVE")
	resp, body = a.do(RoleAdmin, http.MethodPost, "/tenants/sync", `{"reason":"PL deactivated"}`)
	if resp.Code != 200 || body["disabled"] != 1.0 {
		t.Fatalf("deactivate: %d %s", resp.Code, resp.Body)
	}
	f.mock.SetDown(true)
	resp, body = a.do(RoleAdmin, http.MethodPost, "/tenants/sync", `{"reason":"while down"}`)
	if resp.Code != 200 || body["result"] != "failed" || body["error"] == nil || body["change_id"] == nil || body["disabled"] != 0.0 {
		t.Fatalf("ISFC down: %d %s", resp.Code, resp.Body)
	}
	f.mock.SetDown(false)
	if resp, body = a.do(RoleViewer, http.MethodGet, "/tenants/hl-t", ""); body["disabled"] != false {
		t.Fatalf("a failed sync disabled a tenant: %s", resp.Body)
	}

	// Another process holds the run lock: 409.
	unlock, ok, err := isfcsync.PGStore{Pool: a.s.d.Pool}.TryRunLock(t.Context())
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	resp, _ = a.do(RoleAdmin, http.MethodPost, "/tenants/sync", `{"reason":"busy"}`)
	unlock()
	if resp.Code != 409 {
		t.Fatalf("busy: %d", resp.Code)
	}
}

func TestSyncWithoutISFCIsAConflict(t *testing.T) {
	a := newAdminRig(t) // Deps.Sync is nil: HUB_TENANT_SOURCE is local
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/tenants/sync", `{"reason":"no isfc here"}`); resp.Code != 409 {
		t.Fatalf("syncTenants: %d %s", resp.Code, resp.Body)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/tenants/orders-service/sync", `{"reason":"no ISFC link"}`); resp.Code != 409 {
		t.Fatalf("syncTenant: %d %s", resp.Code, resp.Body)
	}
}
