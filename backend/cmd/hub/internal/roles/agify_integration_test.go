//go:build integration

package roles

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// The agify-like API of ADR-0023 (https://agify.io/documentation: GET /?name=<name> answers
// {"count", "name", "age"}). It is public, so its vendor uses auth scheme none. Never the real
// vendor: mockvendor answers.
const (
	ageVendor      = "agelookup"
	ageIntegration = "int_age_estimate"
	ageDocsURL     = "https://agify.io/documentation"
	ageVendorBody  = `{"count":1744,"name":"harsh","age":29}`
)

// ageRequestYAML is the request transformer; query.name is what the endpoint requires. With
// validate_input off a missing lastName must still reach the transformer (and the vendor).
func ageRequestYAML(queryKey string) string {
	return "request:\n  method: GET\n  path: /\n  query:\n    " + queryKey + ": 'req.firstName + (req.lastName ?? \"\")'\n"
}

const ageResponseYAML = "response:\n  age: res.body.age\n"

var ageResponseSample = map[string]any{"name": "harsh", "vendor_response": map[string]any{"status": 200, "body": json.RawMessage(ageVendorBody)},
	"expected_response": map[string]any{"age": 29}}

// ageSpec is a version document with one operation (validate_input as given) and a target whose
// response transformer is response (empty: none, pass-through).
func ageSpec(validateInput bool, response string) map[string]any {
	pair := map[string]any{"request": "AGE-REQ@2", "response": nil}
	if response != "" {
		pair["response"] = response
	}
	return map[string]any{
		"operations": map[string]any{"default": map[string]any{
			"description": "Estimate the age for a first and last name.", "idempotent": true, "validate_input": validateInput,
			"request_schema": map[string]any{"body": map[string]any{
				"type": "object", "required": []any{"firstName", "lastName"},
				"properties": map[string]any{
					"firstName": map[string]any{"type": "string", "minLength": 1},
					"lastName":  map[string]any{"type": "string", "minLength": 1},
				},
			}},
		}},
		"targets":  []any{map[string]any{"vendor_id": ageVendor, "account_id": "age-global", "transformer": pair, "weight": 100}},
		"failover": []any{},
		"delivery": map[string]any{"mode": "sync", "on_circuit_open": "fail_fast", "pending": "re_resolve", "polling": false},
		"webhooks": []any{},
		"audit":    map[string]any{"payloads": "masked", "retention_ms": 2592000000},
	}
}

// TestAgifyOnboardedWithoutCapabilities is ADR-0023's proof (HUB-433, HUB-434, HUB-438): an
// operator onboards a public API through the admin API alone (vendor with auth none, endpoint
// request schema, account without credentials, transformers proven by samples, integration
// with its operation schema), and the gateway serves it: response transformer output as it is,
// pass-through without one, validate_input on and off.
func TestAgifyOnboardedWithoutCapabilities(t *testing.T) {
	freshArchive(t)
	logStart := len(roleLogs.String())
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("role logs:\n%s", tailOf(roleLogs.String()[logStart:], 6000))
		}
	})
	blobURL := "file://" + t.TempDir()
	mv := mockvendor.Start(t)
	mv.SetRoute(t, "agify", mockvendor.RouteSpec{
		Match: mockvendor.RouteMatch{Method: http.MethodGet, Path: "/"},
		Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(ageVendorBody),
			Headers: map[string]string{"X-Agify-Server": "agify-eu-1", "X-Rate-Limit-Remaining": "999", "Retry-After": "7"}},
	})

	admin := composeConfig(t, Admin)
	admin.BlobURL = blobURL
	admin.AdminDevOIDC = true
	base := "http://" + startRoleWith(t, admin, Options{})["http"] + "/admin/v1"
	gwCfg := composeConfig(t, Gateway)
	gwCfg.BlobURL = blobURL
	gateway := "http://" + startRoleWith(t, gwCfg, Options{})["http"]

	code, body := send(t, http.MethodPost, strings.TrimSuffix(base, "/admin/v1")+"/oidc/dev-token",
		`{"sub":"onboarding","email":"onboarding@example.com","roles":["admin"]}`, nil)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || code != 200 {
		t.Fatalf("dev token: %d %s", code, body)
	}
	call := func(method, path string, in any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(in)
		code, body := send(t, method, base+path, string(b), map[string]string{"Authorization": "Bearer " + tok.AccessToken, "Content-Type": "application/json"})
		var out map[string]any
		_ = json.Unmarshal([]byte(body), &out)
		if code != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, body)
		}
		return out
	}

	// Vendor, endpoint and account: auth none, query.name required, no credentials.
	call(http.MethodPost, "/vendors", map[string]any{"vendor_id": ageVendor, "display_name": "Age lookup", "auth_scheme": "none",
		"regions": map[string]any{"global": mv.VendorURL}, "docs_url": ageDocsURL, "reason": "Onboard the age estimate API"}, 201)
	call(http.MethodPost, "/vendors/"+ageVendor+"/endpoints", map[string]any{"name": "estimate", "method": "GET", "path": "/", "idempotent": true,
		"request_schema": map[string]any{"query": map[string]any{"type": "object", "required": []any{"name"},
			"properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1}}}},
		"response_schema": map[string]any{"type": "object"}, "reason": "Age estimate"}, 201)
	call(http.MethodPost, "/accounts", map[string]any{"account_id": "age-global", "vendor_id": ageVendor, "region": "global",
		"config": map[string]any{}, "reason": "Public API: no credentials"}, 201)
	if out := call(http.MethodPost, "/accounts/age-global/credentials/next", map[string]any{"credentials": map[string]any{"k": "v"}, "reason": "Rotate the credential"}, 409); out["error"].(map[string]any)["code"] != "CONFLICT" {
		t.Fatalf("credential rotation on an auth none account: %v", out)
	}

	// A request transformer whose sample passes its golden but misses the required query.name
	// is refused with the field (endpoint_schema).
	sample := func(name string, query map[string]any) map[string]any {
		return map[string]any{"name": name, "input": map[string]any{"firstName": "har", "lastName": "sh"},
			"expected_request": map[string]any{"method": "GET", "path": "/", "query": query}}
	}
	out := call(http.MethodPost, "/transformers/AGE-REQ/versions", map[string]any{"kind": "request", "vendor_endpoint": ageVendor + ".estimate@1",
		"yaml": ageRequestYAML("nme"), "fixtures": []any{sample("harsh", map[string]any{"nme": "harsh"})},
		"docs_url": ageDocsURL, "reason": "First mapping"}, 422)
	if !hasDetail(out, "endpoint_schema", "/query/name") {
		t.Fatalf("a sample without query.name: want endpoint_schema at /query/name, got %v", out)
	}
	// A golden mismatch is goldens_pass.
	out = call(http.MethodPost, "/transformers/AGE-REQ/versions", map[string]any{"kind": "request", "vendor_endpoint": ageVendor + ".estimate@1",
		"yaml": ageRequestYAML("name"), "fixtures": []any{sample("harsh", map[string]any{"name": "harsh!"})},
		"docs_url": ageDocsURL, "reason": "First mapping"}, 422)
	if !hasDetail(out, "goldens_pass", "") {
		t.Fatalf("a golden mismatch: want goldens_pass, got %v", out)
	}
	// Refusals store nothing: version 1 is the transformer below. A later version 2 reads lastName
	// with ?? so that the pass-through run can show validate_input off.
	call(http.MethodPost, "/transformers/AGE-REQ/versions", map[string]any{"kind": "request", "vendor_endpoint": ageVendor + ".estimate@1",
		"yaml": strings.Replace(ageRequestYAML("name"), ` + (req.lastName ?? "")`, ` + req.lastName`, 1),
		"fixtures": []any{sample("harsh", map[string]any{"name": "harsh"})}, "docs_url": ageDocsURL, "reason": "First mapping"}, 201)
	call(http.MethodPost, "/transformers/AGE-REQ/versions", map[string]any{"kind": "request", "vendor_endpoint": ageVendor + ".estimate@1",
		"expected_latest_version": 1, "yaml": ageRequestYAML("name"),
		"fixtures": []any{sample("harsh", map[string]any{"name": "harsh"})}, "docs_url": ageDocsURL, "reason": "Tolerate a missing last name"}, 201)
	if out := call(http.MethodPost, "/transformers/AGE-RES/versions", map[string]any{"kind": "response", "vendor_endpoint": ageVendor + ".estimate@1",
		"yaml": ageResponseYAML, "fixtures": []any{ageResponseSample}, "docs_url": ageDocsURL, "reason": "Response with an endpoint"}, 400); !hasDetail(out, "", "/vendor_endpoint") {
		t.Fatalf("response transformer with a vendor_endpoint: %v", out)
	}
	call(http.MethodPost, "/transformers/AGE-RES/versions", map[string]any{"kind": "response", "yaml": ageResponseYAML,
		"fixtures": []any{ageResponseSample}, "docs_url": ageDocsURL, "reason": "Only the age"}, 201)

	// Integration v1: validate_input on, response transformer keeps the age.
	call(http.MethodPost, "/integrations", map[string]any{"integration_id": ageIntegration, "owner_tenant": "orders-service",
		"description": "Age estimate", "reason": "Age estimate for onboarding"}, 201)
	call(http.MethodPost, "/integrations/"+ageIntegration+"/versions", map[string]any{"spec": ageSpec(true, "AGE-RES@1"), "reason": "First version"}, 201)
	call(http.MethodPost, "/integrations/"+ageIntegration+"/versions/1/publish", map[string]any{"expected_active_version": nil, "reason": "Go live"}, 200)

	invoke := func(body string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, gateway+"/v1/integrations/"+ageIntegration+"/invoke", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+devAPIKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	// until polls the gateway (it follows the archive asynchronously) until ok accepts the answer.
	until := func(what, body string, ok func(*http.Response, string) bool) (*http.Response, string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			resp, got := invoke(body)
			if ok(resp, got) {
				return resp, got
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: last answer %d %v %s", what, resp.StatusCode, resp.Header, got)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	full := `{"firstName":"har","lastName":"sh"}`
	resp, got := until("v1 serves", full, func(r *http.Response, _ string) bool { return r.StatusCode == 200 })
	if strings.TrimSpace(got) != `{"age":29}` || resp.Header.Get("X-Hub-Pass-Through") != "" {
		t.Fatalf("response transformer output: %d %v %s", resp.StatusCode, resp.Header, got)
	}
	reqs := mv.Requests(t, url.Values{"route_id": {"agify"}})
	if len(reqs) == 0 || reqs[len(reqs)-1].Query["name"][0] != "harsh" {
		t.Fatalf("vendor request: %+v", reqs)
	}
	resp, got = invoke(`{"firstName":"har"}`)
	if resp.StatusCode != 400 || !strings.Contains(got, "VALIDATION_FAILED") || !strings.Contains(got, "lastName") {
		t.Fatalf("validate_input on, lastName missing: %d %s", resp.StatusCode, got)
	}

	// Integration v2: no response transformer (pass-through) and validate_input off.
	call(http.MethodPost, "/integrations/"+ageIntegration+"/versions", map[string]any{"spec": ageSpec(false, ""), "reason": "Pass-through"}, 201)
	call(http.MethodPost, "/integrations/"+ageIntegration+"/versions/2/publish", map[string]any{"expected_active_version": 1, "reason": "Raw answer"}, 200)
	resp, got = until("v2 passes through", full, func(r *http.Response, _ string) bool { return r.Header.Get("X-Hub-Pass-Through") == "true" })
	if resp.StatusCode != 200 || strings.TrimSpace(got) != ageVendorBody {
		t.Fatalf("pass-through body: %d %s", resp.StatusCode, got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || resp.Header.Get("Retry-After") != "7" {
		t.Fatalf("pass-through Content-Type %q, Retry-After %q", ct, resp.Header.Get("Retry-After"))
	}
	for _, h := range []string{"X-Agify-Server", "X-Rate-Limit-Remaining"} {
		if resp.Header.Get(h) != "" {
			t.Errorf("vendor header %s reached the caller", h)
		}
	}
	if resp.Header.Get("X-Hub-Request-Id") == "" {
		t.Error("Hub headers missing on a pass-through answer")
	}
	resp, got = invoke(`{"firstName":"har"}`)
	if resp.StatusCode != 200 || resp.Header.Get("X-Hub-Pass-Through") != "true" {
		t.Fatalf("validate_input off, lastName missing: %d %s", resp.StatusCode, got)
	}
	reqs = mv.Requests(t, url.Values{"route_id": {"agify"}})
	if q := reqs[len(reqs)-1].Query["name"]; len(q) != 1 || q[0] != "har" {
		t.Fatalf("validate_input off: the transformer did not run on the unchecked body: %+v", reqs[len(reqs)-1])
	}

	// A vendor error passes through with its status and body (non-retryable 4xx: no retry).
	mv.SetRoute(t, "agify-missing", mockvendor.RouteSpec{Priority: 10,
		Match:     mockvendor.RouteMatch{Method: http.MethodGet, Path: "/"},
		Behaviour: mockvendor.Behaviour{Status: 422, Body: json.RawMessage(`{"error":"Missing 'name' parameter"}`), Headers: map[string]string{"X-Agify-Server": "agify-eu-1"}},
	})
	resp, got = invoke(full)
	if resp.StatusCode != 422 || strings.TrimSpace(got) != `{"error":"Missing 'name' parameter"}` ||
		resp.Header.Get("X-Hub-Pass-Through") != "true" || resp.Header.Get("X-Agify-Server") != "" {
		t.Fatalf("vendor error pass-through: %d %v %s", resp.StatusCode, resp.Header, got)
	}
	// Non-JSON keeps its Content-Type.
	raw := "age=29"
	mv.SetRoute(t, "agify-missing", mockvendor.RouteSpec{Priority: 10,
		Match:     mockvendor.RouteMatch{Method: http.MethodGet, Path: "/"},
		Behaviour: mockvendor.Behaviour{Status: 200, BodyRaw: &raw, Headers: map[string]string{"Content-Type": "text/plain; charset=utf-8"}},
	})
	resp, got = invoke(full)
	if resp.StatusCode != 200 || got != raw || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("non-JSON pass-through: %d %q %q", resp.StatusCode, resp.Header.Get("Content-Type"), got)
	}

	// Discovery shows the operation and its request schema, no vendor.
	code, body = get(t, gateway+"/v1/integrations/"+ageIntegration, map[string]string{"Authorization": "Bearer " + devAPIKey})
	if code != 200 || !strings.Contains(body, `"request_schema"`) || strings.Contains(body, ageVendor) || strings.Contains(body, "capabilit") {
		t.Fatalf("discovery: %d %s", code, body)
	}
}

// hasDetail reports whether a VALIDATION_FAILED or PUBLISH_VALIDATION_FAILED body has a detail with keyword check (any when
// empty) whose field or pointer is field (any when empty).
func hasDetail(out map[string]any, check, field string) bool {
	e, _ := out["error"].(map[string]any)
	if e == nil || (e["code"] != "VALIDATION_FAILED" && e["code"] != "PUBLISH_VALIDATION_FAILED") {
		return false
	}
	ds, _ := e["details"].([]any)
	for _, d := range ds {
		m, _ := d.(map[string]any)
		if check != "" && m["keyword"] != check {
			continue
		}
		if field != "" && m["field"] != field && m["pointer"] != field {
			continue
		}
		return true
	}
	return false
}

func tailOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
