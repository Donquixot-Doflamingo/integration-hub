package edge

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TestRequestStatusNeedsRequestsRead is HUB-415 and HUB-487: GET /v1/requests/{id} requires the
// requests_read scope for Hub keys and ISFC keys alike. Without queueing the answer after the scope
// check is 404, so a 404 here means "scope accepted".
func TestRequestStatusNeedsRequestsRead(t *testing.T) {
	snap := devSnapshot(t)
	tn := &registry.Tenant{Tenant: "scoped-t", APIKeys: []registry.APIKey{
		{KeyID: "k_inv", Hash: HashKey("hk_inv"), Scopes: []string{"invoke", "dry_run"}, State: "active"},
		{KeyID: "k_rd", Hash: HashKey("hk_rd"), Scopes: []string{"requests_read"}, State: "active"},
		{KeyID: "k_one", Hash: HashKey("hk_one"), Scopes: []string{"requests_read:int_evt_brandA"}, State: "active"},
	}}
	snap = withTenants(t, snap, tn)
	var km isfc.KeyMap
	km.Replace([]isfc.CallerEntry{
		{KeyID: "ik_rd", Hash: HashKey("isfc_rd"), ClientID: "c", TenantIDs: []string{uOrders}, Scopes: isfc.ScopesFromPermissions([]string{"HUB:REQUESTS_READ"})},
		{KeyID: "ik_inv", Hash: HashKey("isfc_inv"), ClientID: "c", TenantIDs: []string{uOrders}, Scopes: isfc.ScopesFromPermissions([]string{"HUB:INVOKE", "HUB:DRY_RUN"})},
	})
	orders := *snap.Tenants["orders-service"]
	orders.ISFC = &registry.TenantISFC{TenantID: uOrders, Key: "ORDERS", Name: "Orders", Status: registry.ISFCStatusActive, Linked: true}
	snap = withTenants(t, snap, &orders)
	h := New(Deps{Snapshots: staticSnapshots{snap}, ISFCKeys: &km}).Handler()

	tests := []struct {
		name string
		hdr  map[string]string
		want int
		code string
	}{
		{"hub key with invoke and dry_run only", map[string]string{"Authorization": "Bearer hk_inv"}, 403, "missing_scope"},
		{"hub key with requests_read", map[string]string{"Authorization": "Bearer hk_rd"}, 404, "REQUEST_NOT_FOUND"},
		{"hub key narrowed to one integration passes the first check", map[string]string{"Authorization": "Bearer hk_one"}, 404, "REQUEST_NOT_FOUND"},
		{"dev key (invoke, dry_run, requests_read)", map[string]string{"Authorization": "Bearer " + devKey}, 404, "REQUEST_NOT_FOUND"},
		{"isfc key with HUB:INVOKE only", map[string]string{"x-isfc-client-id": "c", "x-isfc-api-key": "isfc_inv"}, 403, "missing_scope"},
		{"isfc key with HUB:REQUESTS_READ", map[string]string{"x-isfc-client-id": "c", "x-isfc-api-key": "isfc_rd"}, 404, "REQUEST_NOT_FOUND"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6", nil)
			for k, v := range tt.hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.code) {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestHasAnyScope(t *testing.T) {
	for _, tt := range []struct {
		scopes []string
		want   bool
	}{
		{nil, false}, {[]string{"invoke"}, false}, {[]string{"requests_read"}, true},
		{[]string{"requests_read:int_evt_brandA"}, true}, {[]string{"requests_readonly"}, false},
	} {
		if got := hasAnyScope(registry.APIKey{Scopes: tt.scopes}, ScopeRequestsRead); got != tt.want {
			t.Errorf("%v: %v", tt.scopes, got)
		}
	}
}
