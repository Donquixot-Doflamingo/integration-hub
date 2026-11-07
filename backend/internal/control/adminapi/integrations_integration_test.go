//go:build integration

package adminapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/trackops"
)

// brandASpec is a version document of int_evt_brandA: weight on ct-brandA-in, the rest on
// ct-brandB-us.
func brandASpec(weightA int) string {
	doc := trackops.Block + "targets:\n  - { vendor: clevertap, account: ct-brandA-in, transformer: { request: T1@7, response: R1@2 }, weight: " + fmt.Sprint(weightA) + " }\n"
	if weightA < 100 {
		doc += "  - { vendor: clevertap, account: ct-brandB-us, transformer: { request: T1@7, response: R1@2 }, weight: " + fmt.Sprint(100-weightA) + " }\n"
	}
	return doc + "delivery: { mode: wait, wait_ms: 3000, ttl: 6h, on_circuit_open: fail_fast, pending: re_resolve, polling: true }\naudit: { payloads: masked, retention: 30d }\n"
}

// publishUpTo publishes int_evt_brandA versions until version last exists (the dev config has
// v1, v2); version last splits 90/10, the others are 100 on ct-brandA-in.
func publishUpTo(t *testing.T, a *adminRig, last int) {
	t.Helper()
	for n := 3; n <= last; n++ {
		w := 100
		if n == last {
			w = 90
		}
		if _, err := a.pub.PublishVersion(t.Context(), "int_evt_brandA", []byte(brandASpec(w)), true, "priya@example.com", fmt.Sprintf("v%d", n)); err != nil {
			t.Fatal(err)
		}
	}
}

// TestIntegrationReads is HUB-204: list (filters, tenant scope, pages), get with the active
// version and an ETag, versions newest first, one version in full; every answer validates
// against the contract.
func TestIntegrationReads(t *testing.T) {
	a := newDraftRig(t)
	publishUpTo(t, a, 8)

	resp, body := a.do(RoleViewer, http.MethodGet, "/integrations", "")
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 3 {
		t.Fatalf("list: %d %v", resp.Code, body)
	}
	first := items[0].(map[string]any)
	if first["integration_id"] != "int_evt_brandA" || first["active_version"] != 8.0 || first["latest_version"] != 8.0 ||
		len(first["targets"].([]any)) != 2 || first["delivery_mode"] != "wait" {
		t.Fatalf("brand A row %v", first)
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"?account_id=ct-brandB-us", []string{"int_evt_brandA", "int_evt_brandB", "int_evt_brandB_min"}},
		{"?account_id=ct-brandA-in&vendor_id=clevertap", []string{"int_evt_brandA"}},
		{"?q=CALLBACK", []string{"int_evt_brandB"}},
		{"?paused=true", nil},
		{"?has_active_version=false", nil},
		{"?active_version_behind=false", []string{"int_evt_brandA", "int_evt_brandB", "int_evt_brandB_min"}},
		{"?limit=2", []string{"int_evt_brandA", "int_evt_brandB"}},
		{"?tenant_id=orders-service&limit=1", []string{"int_evt_brandA"}},
	} {
		resp, body := a.do(RoleTenantViewer, http.MethodGet, "/integrations"+tc.query, "")
		var got []string
		for _, it := range body["items"].([]any) {
			got = append(got, it.(map[string]any)["integration_id"].(string))
		}
		if resp.Code != 200 || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %d %v, want %v", tc.query, resp.Code, got, tc.want)
		}
	}
	// Cursor pages cover the list exactly once.
	_, p1 := a.do(RoleViewer, http.MethodGet, "/integrations?limit=2", "")
	cur, _ := p1["next_cursor"].(string)
	_, p2 := a.do(RoleViewer, http.MethodGet, "/integrations?limit=2&cursor="+cur, "")
	if cur == "" || len(p2["items"].([]any)) != 1 || p2["next_cursor"] != nil {
		t.Fatalf("pages %v / %v", p1, p2)
	}
	// A tenant viewer outside the owner tenant sees nothing and gets 404 on the object.
	req := a.request(RoleTenantViewer, http.MethodGet, "/integrations/int_evt_brandA", "", "billing")
	if rec := a.spec.Serve(t, a.h, req); rec.Code != 404 {
		t.Fatalf("out of scope: %d", rec.Code)
	}
	req = a.request(RoleTenantViewer, http.MethodGet, "/integrations", "", "billing")
	if rec := a.spec.Serve(t, a.h, req); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("out of scope list: %d %s", rec.Code, rec.Body)
	}
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/integrations?tenant_id=billing", ""); resp.Code != 403 {
		t.Fatalf("foreign tenant filter: %d", resp.Code)
	}

	resp, body = a.do(RoleTenantViewer, http.MethodGet, "/integrations/int_evt_brandA", "")
	active, _ := body["active"].(map[string]any)
	if resp.Code != 200 || resp.Header().Get("ETag") == "" || active["version"] != 8.0 || active["state"] != "published" ||
		active["published_by"] != "priya@example.com" || active["snapshot_version"] == nil {
		t.Fatalf("get: %d %v", resp.Code, body)
	}

	resp, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions?limit=3", "")
	vs := body["items"].([]any)
	if resp.Code != 200 || len(vs) != 3 || vs[0].(map[string]any)["version"] != 8.0 || vs[0].(map[string]any)["is_active"] != true ||
		vs[1].(map[string]any)["is_active"] != false || body["next_cursor"] == nil {
		t.Fatalf("versions: %d %v", resp.Code, body)
	}
	_, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions?limit=200&cursor="+body["next_cursor"].(string), "")
	if n := len(body["items"].([]any)); n != 5 {
		t.Fatalf("second page has %d versions, want 5", n)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions/1", "")
	spec, _ := body["spec"].(map[string]any)
	if resp.Code != 200 || spec["delivery"].(map[string]any)["ttl_ms"] == nil || body["author"] != "platform-team" {
		t.Fatalf("v1: %d %v", resp.Code, body)
	}
	for _, path := range []string{"/integrations/int_nope", "/integrations/int_evt_brandA/versions/99", "/integrations/int_nope/versions"} {
		if resp, _ := a.do(RoleViewer, http.MethodGet, path, ""); resp.Code != 404 {
			t.Errorf("%s: %d, want 404", path, resp.Code)
		}
	}
}

// TestDiffShowsExactPaths is HUB-204's acceptance: the diff of v7 (100 % ct-brandA-in) and v8
// (90/10) names exactly the changed paths, and the unified text shows them.
func TestDiffShowsExactPaths(t *testing.T) {
	a := newDraftRig(t)
	publishUpTo(t, a, 8)
	resp, body := a.do(RoleTenantViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=7&to=8", "")
	if resp.Code != 200 {
		t.Fatalf("diff: %d %v", resp.Code, body)
	}
	var got []string
	for _, c := range body["changes"].([]any) {
		m := c.(map[string]any)
		got = append(got, fmt.Sprintf("%s %s %v→%v", m["op"], m["path"], m["before"], m["after"]))
	}
	want := []string{
		"replace /targets/0/weight 100→90",
		"add /targets/1 <nil>→map[account_id:ct-brandB-us transformer:map[request:T1@7 response:R1@2] vendor_id:clevertap weight:10]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes\n%v\nwant\n%v", got, want)
	}
	unified := body["unified"].(string)
	for _, line := range []string{"--- v7\n+++ v8\n", "-      weight: 100\n", "+      weight: 90\n", "+    - account_id: ct-brandB-us\n"} {
		if !strings.Contains(unified, line) {
			t.Errorf("unified diff lacks %q:\n%s", line, unified)
		}
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=7&to=7", ""); len(body["changes"].([]any)) != 0 {
		t.Fatalf("self diff %v", body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=7&to=42", ""); resp.Code != 404 {
		t.Fatalf("unknown version: %d", resp.Code)
	}
	req := a.request(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=7", "")
	if rec := a.spec.ServeInvalid(t, a.h, req); rec.Code != 400 {
		t.Fatalf("missing to: %d", rec.Code)
	}
}

// request builds an admin request as role (scoped to tenants, default orders-service).
func (a *adminRig) request(role Role, method, path, body string, tenants ...string) *http.Request {
	a.t.Helper()
	if len(tenants) == 0 {
		tenants = []string{"orders-service"}
	}
	req := httptest.NewRequestWithContext(a.t.Context(), method, Base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token(a.t, a.iss, role, tenants...))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}
