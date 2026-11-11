package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

const (
	uOrders  = "6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f"
	uBilling = "7a2d1c3f-4b5e-4d6f-9a71-2b3c4d5e6f70"
	uGone    = "8b3e2d4a-5c6f-4e70-8b82-3c4d5e6f7081"
	uPending = "9c4f3e5b-6d70-4f81-9c93-4d5e6f708192"
	uSusp    = "ad504f6c-7e81-4092-ad04-5e6f708192a3"
	uOff     = "be615a7d-8f92-41a3-be15-6f708192a3b4"
	uInact   = "cf726b8e-90a3-42b4-8f26-708192a3b4c5"
)

func isfcTenant(id, uuid string, mutate func(*registry.Tenant)) *registry.Tenant {
	t := &registry.Tenant{Tenant: id, Status: "active", APIKeys: []registry.APIKey{},
		ISFC: &registry.TenantISFC{TenantID: uuid, Key: strings.ToUpper(id), Name: id, Status: registry.ISFCStatusActive, Linked: true}}
	if mutate != nil {
		mutate(t)
	}
	return t
}

// isfcRig is the edge with ISFC credentials on: the dev snapshot, tenants linked to ISFC (the
// orders-service tenant owns the dev integrations) and a key map.
func isfcRig(t *testing.T) (http.Handler, *Server, *telemetry.Metrics) {
	t.Helper()
	snap := devSnapshot(t)
	orders := *snap.Tenants["orders-service"]
	orders.ISFC = &registry.TenantISFC{TenantID: uOrders, Key: "ORDERS", Name: "Orders", Status: registry.ISFCStatusActive, Linked: true}
	snap = withTenants(t, snap, &orders,
		isfcTenant("billing-t", uBilling, nil),
		isfcTenant("gone-t", uGone, func(x *registry.Tenant) { x.Disabled = true }),
		isfcTenant("pending-t", uPending, func(x *registry.Tenant) { x.Setup = registry.SetupPending }),
		isfcTenant("susp-t", uSusp, func(x *registry.Tenant) { x.Status = "suspended" }),
		isfcTenant("inact-t", uInact, func(x *registry.Tenant) { x.ISFC.Status = registry.ISFCStatusInactive }),
	)
	var km isfc.KeyMap
	km.Replace([]isfc.CallerEntry{
		{KeyID: "ik_orders", Hash: HashKey("isfc_orders"), ClientID: "orders-backend", TenantIDs: []string{uOrders}, Scopes: []string{"invoke", "dry_run"}},
		{KeyID: "ik_multi", Hash: HashKey("isfc_multi"), ClientID: "multi-backend", TenantIDs: []string{uOrders, uBilling}, Scopes: []string{"invoke"}},
		{KeyID: "ik_none", Hash: HashKey("isfc_none"), ClientID: "none-backend", Scopes: []string{"invoke"}},
		{KeyID: "ik_ghost", Hash: HashKey("isfc_ghost"), ClientID: "ghost-backend", TenantIDs: []string{"deadbeef-0000-4000-8000-000000000000"}, Scopes: []string{"invoke"}},
		{KeyID: "ik_gone", Hash: HashKey("isfc_gone"), ClientID: "gone-backend", TenantIDs: []string{uGone}, Scopes: []string{"invoke"}},
		{KeyID: "ik_pending", Hash: HashKey("isfc_pending"), ClientID: "pending-backend", TenantIDs: []string{uPending}, Scopes: []string{"invoke"}},
		{KeyID: "ik_susp", Hash: HashKey("isfc_susp"), ClientID: "susp-backend", TenantIDs: []string{uSusp}, Scopes: []string{"invoke"}},
		{KeyID: "ik_inact", Hash: HashKey("isfc_inact"), ClientID: "inact-backend", TenantIDs: []string{uInact}, Scopes: []string{"invoke"}},
		{KeyID: "ik_ro", Hash: HashKey("isfc_ro"), ClientID: "ro-backend", TenantIDs: []string{uOrders}, Scopes: []string{"requests_read"}},
	})
	m := telemetry.NewMetrics("gateway", "test")
	s := New(Deps{Snapshots: staticSnapshots{snap}, ISFCKeys: &km, Metrics: m})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/integrations/{integration_id}/invoke", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := CallerFrom(r.Context())
		if _, ce := authorizeIntegration(c, r.PathValue("integration_id"), ScopeInvoke); ce != nil {
			WriteError(w, r, ce)
			return
		}
		w.Header().Set("X-Test-Tenant", c.Tenant.Tenant+"/"+c.Method+"/"+c.ClientID+"/"+c.Key.KeyID)
		w.WriteHeader(http.StatusNoContent)
	})))
	return withRequestID(mux), s, m
}

func isfcCall(t *testing.T, h http.Handler, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader("{}"))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func id(client, key string, extra ...string) map[string]string {
	h := map[string]string{"x-isfc-client-id": client, "x-isfc-api-key": key}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

// TestAuthenticateISFC is the table of AUTH_PLAN §8.2 (HUB-486).
func TestAuthenticateISFC(t *testing.T) {
	h, _, _ := isfcRig(t)
	tests := []struct {
		name   string
		hdr    map[string]string
		status int
		code   string
		reason string
		tenant string // X-Test-Tenant on success
	}{
		{"valid key, one tenant", id("orders-backend", "isfc_orders"), 204, "", "", "orders-service/isfc/orders-backend/ik_orders"},
		{"X-Tenant-Id naming the one tenant is fine", id("orders-backend", "isfc_orders", "X-Tenant-Id", uOrders), 204, "", "", "orders-service/isfc/orders-backend/ik_orders"},
		{"X-Tenant-Id is case-insensitive", id("orders-backend", "isfc_orders", "X-Tenant-Id", strings.ToUpper(uOrders)), 204, "", "", "orders-service/isfc/orders-backend/ik_orders"},
		{"unknown key", id("orders-backend", "isfc_nope"), 401, "UNAUTHORIZED", "", ""},
		{"wrong client id", id("billing-backend", "isfc_orders"), 401, "UNAUTHORIZED", "", ""},
		{"missing client id", map[string]string{"x-isfc-api-key": "isfc_orders"}, 401, "UNAUTHORIZED", "", ""},
		{"revoked key (not in the map)", id("orders-backend", "isfc_revoked"), 401, "UNAUTHORIZED", "", ""},
		{"multi-tenant key without X-Tenant-Id", id("multi-backend", "isfc_multi"), 400, "VALIDATION_FAILED", "", ""},
		{"multi-tenant key with X-Tenant-Id", id("multi-backend", "isfc_multi", "X-Tenant-Id", uOrders), 204, "", "", "orders-service/isfc/multi-backend/ik_multi"},
		{"multi-tenant key, tenant it does not cover", id("multi-backend", "isfc_multi", "X-Tenant-Id", uSusp), 403, "FORBIDDEN", "tenant_not_covered", ""},
		{"single-tenant key, other tenant named", id("orders-backend", "isfc_orders", "X-Tenant-Id", uBilling), 403, "FORBIDDEN", "tenant_not_covered", ""},
		{"X-Tenant-Id is not a UUID", id("orders-backend", "isfc_orders", "X-Tenant-Id", "orders-service"), 400, "VALIDATION_FAILED", "", ""},
		{"key covering no tenant", id("none-backend", "isfc_none"), 403, "FORBIDDEN", "tenant_not_covered", ""},
		{"key covering a tenant this Hub does not know", id("ghost-backend", "isfc_ghost"), 403, "FORBIDDEN", "tenant_not_covered", ""},
		{"pending tenant", id("pending-backend", "isfc_pending"), 401, "UNAUTHORIZED", "", ""},
		{"disabled tenant", id("gone-backend", "isfc_gone"), 401, "UNAUTHORIZED", "", ""},
		{"tenant INACTIVE in ISFC", id("inact-backend", "isfc_inact"), 401, "UNAUTHORIZED", "", ""},
		{"suspended tenant", id("susp-backend", "isfc_susp"), 403, "FORBIDDEN", "tenant_suspended", ""},
		{"both credential kinds", id("orders-backend", "isfc_orders", "Authorization", "Bearer "+devKey), 400, "VALIDATION_FAILED", "", ""},
		{"hk_ key alone still works", map[string]string{"Authorization": "Bearer " + devKey}, 204, "", "", "orders-service/hub_key//key_01JDEV0RDERS0000000000000A"},
		{"a Hub key in the ISFC header is just an unknown ISFC key", id("orders-backend", devKey), 401, "UNAUTHORIZED", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := isfcCall(t, h, tt.hdr)
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.code) || !strings.Contains(rec.Body.String(), tt.reason) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("X-Test-Tenant"); got != tt.tenant {
				t.Fatalf("caller %q, want %q", got, tt.tenant)
			}
			if body := rec.Body.String(); strings.Contains(body, "isfc_") || strings.Contains(body, devKey) {
				t.Fatalf("a credential is echoed: %s", body)
			}
		})
	}
}

// TestISFCScopesFromPermissions: the key's permissions become scopes, and a missing scope is 403.
func TestISFCScopesFromPermissions(t *testing.T) {
	h, _, _ := isfcRig(t)
	if rec := isfcCall(t, h, id("ro-backend", "isfc_ro")); rec.Code != 403 || !strings.Contains(rec.Body.String(), "missing_scope") {
		t.Fatalf("a key without HUB:INVOKE invoked: %d %s", rec.Code, rec.Body)
	}
	if rec := isfcCall(t, h, id("orders-backend", "isfc_orders")); rec.Code != 204 {
		t.Fatalf("invoke: %d %s", rec.Code, rec.Body)
	}
}

func TestISFCCredentialIgnoredWhenOff(t *testing.T) {
	snap := devSnapshot(t)
	s := New(Deps{Snapshots: staticSnapshots{snap}}) // HUB_EDGE_ISFC_AUTH=off: no key map
	mux := http.NewServeMux()
	mux.Handle("POST /v1/integrations/{integration_id}/invoke", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	h := withRequestID(mux)
	if rec := isfcCall(t, h, id("orders-backend", "isfc_orders")); rec.Code != 401 {
		t.Fatalf("ISFC header without a bearer: %d", rec.Code)
	}
	if rec := isfcCall(t, h, id("orders-backend", "isfc_orders", "Authorization", "Bearer "+devKey)); rec.Code != 204 {
		t.Fatalf("the Hub key must keep working when ISFC auth is off and a stray ISFC header is sent: %d", rec.Code)
	}
}

func TestISFCKeysNotLoadedYetIsNotReady(t *testing.T) {
	snap := devSnapshot(t)
	s := New(Deps{Snapshots: staticSnapshots{snap}, ISFCKeys: &isfc.KeyMap{}})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/integrations/{integration_id}/invoke", s.authenticate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	rec := isfcCall(t, withRequestID(mux), id("orders-backend", "isfc_orders"))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "NOT_READY") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// TestEdgeAuthMetric: hub_edge_auth_total{method,result} tells the ISFC and Hub-key paths apart.
func TestEdgeAuthMetric(t *testing.T) {
	h, s, _ := isfcRig(t)
	isfcCall(t, h, id("orders-backend", "isfc_orders"))
	isfcCall(t, h, id("orders-backend", "isfc_orders"))
	isfcCall(t, h, id("orders-backend", "isfc_nope"))
	isfcCall(t, h, id("multi-backend", "isfc_multi"))
	isfcCall(t, h, id("susp-backend", "isfc_susp"))
	isfcCall(t, h, map[string]string{"Authorization": "Bearer " + devKey})
	isfcCall(t, h, map[string]string{"Authorization": "Bearer hk_nope"})
	want := map[[2]int]float64{{mISFC, resOK}: 2, {mISFC, resUnauthorized}: 1, {mISFC, resBadRequest}: 1, {mISFC, resForbidden}: 1,
		{mHubKey, resOK}: 1, {mHubKey, resUnauthorized}: 1}
	for mi := range s.counters {
		for ri := range s.counters[mi] {
			if got := testutil.ToFloat64(s.counters[mi][ri]); got != want[[2]int{mi, ri}] {
				t.Errorf("method %d result %s: %v, want %v", mi, resultNames[ri], got, want[[2]int{mi, ri}])
			}
		}
	}
}

// TestISFCAuthStageIsTimed: the ISFC path runs inside the auth stage middleware, so
// hub_stage_duration_ms{stage="auth"} covers it.
func TestISFCAuthStageIsTimed(t *testing.T) {
	h, _, m := isfcRig(t)
	isfcCall(t, h, id("orders-backend", "isfc_orders"))
	if n := testutil.CollectAndCount(m.StageDuration); n == 0 {
		t.Fatal("no stage duration recorded for the ISFC auth path")
	}
}
