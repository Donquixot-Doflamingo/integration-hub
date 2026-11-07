//go:build integration

package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const acmeVendor = `{"vendor_id":"acme","display_name":"Acme","auth_scheme":"bearer","regions":{"in":"https://in.acme.test"},
"defaults":{"rate_limit":{"per_account_rps":10},"breaker":{"window_ms":30000,"min_calls":5,"failure_ratio":0.5,"open_ms":10000}},
"webhook_verification":{"scheme":"hmac_sha256","header":"X-Acme-Signature","max_skew_ms":300000},
"docs_url":"https://docs.acme.test/api","reason":"Onboard Acme"}`

func init() {
	mutationCases["createVendor"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/vendors", body: acmeVendor}
	mutationCases["updateVendor"] = mutationCase{role: RoleAdmin, method: http.MethodPut, path: "/vendors/clevertap",
		body: `{"display_name":"CleverTap","auth_scheme":"static_headers","regions":{"in":"http://127.0.0.1:8099","us":"http://127.0.0.1:8099"},"reason":"Rename"}`}
	mutationCases["createVendorEndpoint"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/vendors/clevertap/endpoints",
		body: `{"name":"profile","method":"POST","path":"/1/profile","idempotent":true,"request_schema":{},"reason":"Profile uploads"}`}
}

// doH is do with extra request headers.
func (a *adminRig) doH(role Role, method, path, body string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), method, Base+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token(a.t, a.iss, role, "orders-service"))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := a.spec.Serve(a.t, a.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestVendorsReadAndWrite(t *testing.T) {
	a := newAdminRig(t)

	resp, body := a.do(RoleViewer, http.MethodGet, "/vendors", "")
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 1 || items[0].(map[string]any)["vendor_id"] != "clevertap" || items[0].(map[string]any)["account_count"] != 2.0 {
		t.Fatalf("list: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/vendors/clevertap", "")
	if resp.Code != 200 || resp.Header().Get("ETag") != `"1"` || len(body["endpoints"].([]any)) != 1 {
		t.Fatalf("get: %d %s %v", resp.Code, resp.Header().Get("ETag"), body)
	}
	if b := body["defaults"].(map[string]any)["breaker"].(map[string]any); b["window_ms"] != 30000.0 {
		t.Fatalf("breaker in ms: %v", b)
	}

	resp, body = a.do(RoleAdmin, http.MethodPost, "/vendors", acmeVendor)
	v, _ := body["vendor"].(map[string]any)
	if resp.Code != 201 || resp.Header().Get("ETag") != `"1"` || v["display_name"] != "Acme" || v["revision"] != 1.0 {
		t.Fatalf("create: %d %v", resp.Code, body)
	}
	if _, snap, _ := a.pub.Current(t.Context()); snap.Vendors["acme"] == nil || snap.Vendors["acme"].Defaults.Breaker.WindowS != 30 {
		t.Fatal("the published snapshot has no acme vendor with a 30 s window")
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/vendors", acmeVendor); resp.Code != 409 {
		t.Fatalf("duplicate create: %d", resp.Code)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/vendors?q=acm", ""); !strings.Contains(resp.Body.String(), `"acme"`) || strings.Contains(resp.Body.String(), "clevertap") {
		t.Fatalf("q filter: %s", resp.Body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/vendors?limit=1", "")
	if next, _ := body["next_cursor"].(string); resp.Code != 200 || next == "" {
		t.Fatalf("page 1: %v", body)
	} else if _, body := a.do(RoleViewer, http.MethodGet, "/vendors?limit=1&cursor="+next, ""); body["next_cursor"] != nil || len(body["items"].([]any)) != 1 {
		t.Fatalf("page 2: %v", body)
	}

	refused := []struct{ name, body string }{
		{"unimplemented auth", strings.Replace(acmeVendor, `"bearer"`, `"mtls"`, 1)},
		{"sub-second breaker", strings.Replace(acmeVendor, `"open_ms":10000`, `"open_ms":1500`, 1)},
		{"probe of a missing endpoint", strings.Replace(acmeVendor, `"defaults":{`, `"defaults":{"health_probe":{"endpoint":"upload@1","dry":true,"every_ms":30000},`, 1)},
	}
	for _, c := range refused {
		body := strings.Replace(c.body, `"vendor_id":"acme"`, `"vendor_id":"acme2"`, 1)
		if resp, _ := a.do(RoleAdmin, http.MethodPost, "/vendors", body); resp.Code != 400 {
			t.Errorf("%s: %d %s", c.name, resp.Code, resp.Body)
		}
	}

	update := `{"display_name":"Acme Corp","auth_scheme":"bearer","regions":{"in":"https://in.acme.test","sg":"https://sg.acme.test"},"reason":"Add SG"}`
	resp, body = a.doH(RoleAdmin, http.MethodPut, "/vendors/acme", update, map[string]string{"If-Match": `"1"`})
	if v, _ := body["vendor"].(map[string]any); resp.Code != 200 || resp.Header().Get("ETag") != `"2"` || v["revision"] != 2.0 || v["display_name"] != "Acme Corp" {
		t.Fatalf("update: %d %v", resp.Code, body)
	}
	resp, body = a.doH(RoleAdmin, http.MethodPut, "/vendors/acme", update, map[string]string{"If-Match": `"1"`})
	if e, _ := body["error"].(map[string]any); resp.Code != 412 || e["code"] != CodePreconditionFailed || e["meta"].(map[string]any)["current_etag"] != `"2"` {
		t.Fatalf("stale If-Match: %d %v", resp.Code, body)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPut, "/vendors/nope", update); resp.Code != 404 {
		t.Fatalf("update unknown: %d", resp.Code)
	}

	ep := `{"name":"upload","method":"POST","path":"/1/upload","timeout_ms":2500,"idempotent":true,"batch_max":500,` +
		`"request_schema":{"body":{"type":"object","required":["d"]}},"response_schema":{"type":"object"},"reason":"v2 limits"}`
	resp, body = a.do(RoleAdmin, http.MethodPost, "/vendors/clevertap/endpoints", ep)
	if e, _ := body["endpoint"].(map[string]any); resp.Code != 201 || e["endpoint_ref"] != "upload@2" || e["connect_timeout_ms"] != 500.0 ||
		e["request_schema"].(map[string]any)["body"] == nil || e["response_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("create endpoint: %d %v", resp.Code, body)
	}
	// ADR-0023: request_schema is required, and must compile.
	for _, bad := range []struct{ body, pointer string }{
		{`{"name":"upload","method":"POST","path":"/1/upload","idempotent":true,"reason":"no schema"}`, "/request_schema"},
		{`{"name":"upload","method":"POST","path":"/1/upload","idempotent":true,"request_schema":{"query":{"type":7}},"reason":"bad schema"}`, "/request_schema/query"},
	} {
		resp, body := a.doInvalid(RoleAdmin, http.MethodPost, "/vendors/clevertap/endpoints", bad.body)
		d, _ := body["error"].(map[string]any)["details"].([]any)
		if resp.Code != 400 || len(d) == 0 || d[0].(map[string]any)["pointer"] != bad.pointer {
			t.Errorf("%s: %d %v", bad.pointer, resp.Code, body)
		}
	}
	_, snap, _ := a.pub.Current(t.Context())
	if old := snap.Vendors["clevertap"].Endpoints["upload@1"]; old.TimeoutMS != 3000 || old.BatchMax != 1000 {
		t.Fatalf("upload@1 changed: %+v", old)
	}
	if resp, body := a.do(RoleViewer, http.MethodGet, "/vendors/clevertap/endpoints", ""); resp.Code != 200 || len(body["items"].([]any)) != 2 {
		t.Fatalf("list endpoints: %v", body)
	}
	if resp, body := a.do(RoleViewer, http.MethodGet, "/vendors/clevertap/endpoints/upload@2", ""); resp.Code != 200 || body["batch_max"] != 500.0 {
		t.Fatalf("get endpoint: %v", body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/vendors/clevertap/endpoints/upload@9", ""); resp.Code != 404 {
		t.Fatalf("missing endpoint: %d", resp.Code)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/vendors/nope/endpoints", ep); resp.Code != 404 {
		t.Fatalf("endpoint of unknown vendor: %d", resp.Code)
	}
}
