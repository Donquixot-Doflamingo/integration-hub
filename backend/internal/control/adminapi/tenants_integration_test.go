//go:build integration

package adminapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() {
	mutationCases["createTenant"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/tenants",
		body: `{"tenant_id":"mut-tenant","display_name":"Mutation tenant","reason":"pilot tenant"}`}
	mutationCases["updateTenant"] = mutationCase{role: RoleAdmin, method: http.MethodPatch, path: "/tenants/orders-service",
		body: `{"rate_limit":{"rps":300,"burst":600},"reason":"festive season"}`}
}

func TestTenants(t *testing.T) {
	a := newAdminRig(t)
	resp, body := a.do(RoleAdmin, http.MethodPost, "/tenants", `{"tenant_id":"billing-svc","display_name":"Billing","description":"Invoices",
		"contact":"billing@example.com","rate_limit":{"rps":20,"burst":40},"reason":"new tenant"}`)
	tn, _ := body["tenant"].(map[string]any)
	if resp.Code != 201 || tn["display_name"] != "Billing" || tn["active_api_keys"].(float64) != 0 || resp.Header().Get("ETag") == "" {
		t.Fatalf("create: %d %s", resp.Code, resp.Body)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/tenants", `{"tenant_id":"billing-svc","display_name":"Again","reason":"duplicate"}`); resp.Code != 409 {
		t.Fatalf("duplicate: %d", resp.Code)
	}
	_, snap, _ := a.pub.Current(t.Context())
	if got := snap.Tenants["billing-svc"]; got == nil || got.RateLimit.RPS != 20 || got.Contact != "billing@example.com" {
		t.Fatalf("snapshot tenant %+v", got)
	}

	resp, _ = a.do(RoleViewer, http.MethodGet, "/tenants/billing-svc", "")
	etag := resp.Header().Get("ETag")
	stale := httpReq(t, a, http.MethodPatch, "/tenants/billing-svc", `{"disabled":true,"reason":"stale"}`)
	stale.Header.Set("If-Match", `"nope"`)
	if rec := a.spec.Serve(t, a.h, stale); rec.Code != 412 {
		t.Fatalf("stale: %d %s", rec.Code, rec.Body)
	}
	upd := httpReq(t, a, http.MethodPatch, "/tenants/billing-svc", `{"disabled":true,"contact":null,"rate_limit":{"rps":30},"reason":"pause billing"}`)
	upd.Header.Set("If-Match", etag)
	rec := a.spec.Serve(t, a.h, upd)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"disabled":true`) || !strings.Contains(rec.Body.String(), `"contact":null`) ||
		!strings.Contains(rec.Body.String(), `"rps":30,"burst":40`) && !strings.Contains(rec.Body.String(), `"burst":40,"rps":30`) {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	_, snap, _ = a.pub.Current(t.Context())
	if !snap.Tenants["billing-svc"].Disabled {
		t.Fatal("the snapshot tenant is not disabled (the edge would keep its keys)")
	}

	resp, body = a.do(RoleViewer, http.MethodGet, "/tenants?q=bill", "")
	if items, _ := body["items"].([]any); resp.Code != 200 || len(items) != 1 {
		t.Fatalf("list q: %d %s", resp.Code, resp.Body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/tenants/orders-service", "")
	if resp.Code != 200 || body["integrations_owned"].(float64) < 1 || body["active_api_keys"].(float64) != 1 {
		t.Fatalf("orders-service: %d %s", resp.Code, resp.Body)
	}
	// tenant_viewer (scoped to orders-service by the rig) sees its own tenant only.
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/tenants/orders-service", ""); resp.Code != 200 {
		t.Fatalf("tenant_viewer own: %d", resp.Code)
	}
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/tenants/billing-svc", ""); resp.Code != 404 {
		t.Fatalf("tenant_viewer other: %d", resp.Code)
	}
	for _, bad := range []string{`{"tenant_id":"x","display_name":"X","reason":"short id"}`, `{"tenant_id":"ok-tenant","display_name":"","reason":"no name"}`} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, Base+"/tenants", strings.NewReader(bad))
		req.Header.Set("Authorization", "Bearer "+token(t, a.iss, RoleAdmin))
		req.Header.Set("Content-Type", "application/json")
		if rec := a.spec.ServeInvalid(t, a.h, req); rec.Code != 400 {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
}
