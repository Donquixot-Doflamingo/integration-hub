//go:build integration

package adminapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// configFile reads a file of the bootstrap config tree.
func configFile(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../../config/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func rawJSON(t testing.TB, s string) json.RawMessage {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(s)
}

// t1Version is a createTransformerVersion body: T1's mapping with its "minimal" golden, the
// expected request optionally edited (to make the golden fail).
func t1Version(t testing.TB, expected any, editWant func(string) string) string {
	t.Helper()
	want := configFile(t, "transformers/T1/fixtures/minimal.expected_request.json")
	if editWant != nil {
		want = editWant(want)
	}
	b, _ := json.Marshal(map[string]any{
		"kind": "request", "vendor_endpoint": "clevertap.upload@1", "expected_latest_version": expected,
		"yaml":     configFile(t, "transformers/T1/7.yaml"),
		"fixtures": []map[string]any{{"name": "minimal", "input": rawJSON(t, configFile(t, "transformers/T1/fixtures/minimal.input.json")), "expected_request": rawJSON(t, want)}},
		"docs_url": "https://developer.clevertap.com/docs/upload-events-api", "reason": "Republish T1 with its minimal golden",
	})
	return string(b)
}

func init() {
	mutationCases["createTransformerVersion"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/transformers/T1/versions",
		body: t1Version(&testing.T{}, 7, nil)}
}

func items(body map[string]any) []map[string]any {
	l, _ := body["items"].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, x := range l {
		out = append(out, x.(map[string]any))
	}
	return out
}

func TestTransformerReads(t *testing.T) {
	a := newAdminRig(t)
	for _, c := range []struct {
		query string
		want  int
	}{{"", 4}, {"?kind=response", 2}, {"?kind=request", 2}, {"?vendor_id=clevertap", 4}, {"?vendor_id=other", 0}, {"?q=t", 2}} {
		if resp, body := a.do(RoleViewer, http.MethodGet, "/transformers"+c.query, ""); resp.Code != 200 || len(items(body)) != c.want {
			t.Errorf("list %q: %d %d items", c.query, resp.Code, len(items(body)))
		}
	}
	resp, body := a.do(RoleViewer, http.MethodGet, "/transformers/T1", "")
	latest, _ := body["latest"].(map[string]any)
	if resp.Code != 200 || body["latest_version"] != 7.0 || latest["ref"] != "T1@7" || len(latest["fixtures"].([]any)) != 2 ||
		latest["goldens"].(map[string]any)["total"] != 2.0 || latest["goldens"].(map[string]any)["passed"] != true ||
		!strings.Contains(latest["yaml"].(string), "transformer: T1") || body["used_by_active_versions"] != 1.0 {
		t.Fatalf("get T1: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/transformers/R1/versions/2", "")
	// A response transformer stands alone (ADR-0023): no vendor endpoint.
	if resp.Code != 200 || body["vendor_endpoint"] != nil || body["kind"] != "response" || body["fixtures"].([]any)[0].(map[string]any)["vendor_response"] == nil {
		t.Fatalf("get R1@2: %d %v", resp.Code, body)
	}
	if resp, body := a.do(RoleViewer, http.MethodGet, "/transformers/T1/versions", ""); resp.Code != 200 || len(items(body)) != 1 || items(body)[0]["goldens_passed"] != true {
		t.Fatalf("versions: %v", body)
	}
	for _, p := range []string{"/transformers/T9", "/transformers/T1/versions/99", "/transformers/T9/versions"} {
		if resp, _ := a.do(RoleViewer, http.MethodGet, p, ""); resp.Code != 404 {
			t.Errorf("%s: %d", p, resp.Code)
		}
	}
}

// TestTransformerCreateRefusesFailingGoldens is the HUB-202 acceptance: a version whose goldens
// fail is refused with 422 and does not exist afterwards; versions are immutable and ordered.
func TestTransformerCreateRefusesFailingGoldens(t *testing.T) {
	a := newAdminRig(t)
	failing := t1Version(t, 7, func(s string) string { return strings.Replace(s, `"app_opened"`, `"app_closed"`, 1) })
	resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/T1/versions", failing)
	if e, _ := body["error"].(map[string]any); resp.Code != 422 || e["code"] != CodePublishValidationFailed ||
		!strings.Contains(resp.Body.String(), "goldens_pass") || !strings.Contains(resp.Body.String(), "evtName") {
		t.Fatalf("failing goldens: %d %v", resp.Code, body)
	}
	if _, snap, _ := a.pub.Current(t.Context()); len(versionsOf(snap, "T1")) != 1 {
		t.Fatal("a transformer version with failing goldens exists")
	}

	refused := []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{"stale expected version", "/transformers/T1/versions", t1Version(t, 6, nil), 409},
		{"new id with an expected version", "/transformers/T9/versions", t1Version(t, 1, nil), 409},
		{"no docs_url", "/transformers/T1/versions", strings.Replace(t1Version(t, 7, nil), `"docs_url":"https://developer.clevertap.com/docs/upload-events-api"`, `"docs_url":null`, 1), 400},
		{"response fixture on a request transformer", "/transformers/T1/versions",
			strings.Replace(t1Version(t, 7, nil), `"expected_request"`, `"expected_response"`, 1), 400},
	}
	for _, c := range refused {
		if resp, body := a.do(RoleEditor, http.MethodPost, c.path, c.body); resp.Code != c.status {
			t.Errorf("%s: %d %v", c.name, resp.Code, body)
		}
	}

	// No fixtures: the contract refuses it too (minItems 1), so the request is sent unvalidated.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, Base+"/transformers/T1/versions", strings.NewReader(setKey(t, t1Version(t, 7, nil), "fixtures", []any{})))
	req.Header.Set("Authorization", "Bearer "+token(t, a.iss, RoleEditor))
	req.Header.Set("Content-Type", "application/json")
	if rec := a.spec.ServeInvalid(t, a.h, req); rec.Code != 400 || !strings.Contains(rec.Body.String(), "golden fixture is required") {
		t.Fatalf("no fixtures: %d %s", rec.Code, rec.Body)
	}

	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/T1/versions", t1Version(t, 7, nil))
	tv, _ := body["transformer_version"].(map[string]any)
	if resp.Code != 201 || tv["ref"] != "T1@8" || tv["author"] != "editor@example.com" || tv["goldens"].(map[string]any)["total"] != 1.0 ||
		!strings.Contains(tv["yaml"].(string), "version: 8") {
		t.Fatalf("create T1@8: %d %v", resp.Code, body)
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/transformers/T1/versions/7", ""); len(body["fixtures"].([]any)) != 2 || !strings.Contains(body["yaml"].(string), "version: 7") {
		t.Fatalf("T1@7 changed: %v", body)
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/transformers/T1/versions", ""); len(items(body)) != 2 || items(body)[0]["version"] != 8.0 {
		t.Fatalf("versions newest first: %v", body)
	}
	if resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/T9/versions", t1Version(t, nil, nil)); resp.Code != 201 ||
		body["transformer_version"].(map[string]any)["ref"] != "T9@1" {
		t.Fatalf("new transformer id: %d %v", resp.Code, body)
	}
}

func setKey(t *testing.T, body, key string, v any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m[key] = v
	b, _ := json.Marshal(m)
	return string(b)
}

func TestRunGoldens(t *testing.T) {
	a := newAdminRig(t)
	resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/run-goldens", `{"ref":"T1@7"}`)
	if resp.Code != 200 || body["passed"] != true || body["total"] != 2.0 || body["failed"] != 0.0 {
		t.Fatalf("stored ref: %d %v", resp.Code, body)
	}
	var create map[string]any
	_ = json.Unmarshal([]byte(t1Version(t, 7, func(s string) string { return strings.Replace(s, `"app_opened"`, `"app_closed"`, 1) })), &create)
	good := create["fixtures"].([]any)[0].(map[string]any)
	pass := map[string]any{"name": "passing", "input": good["input"],
		"expected_request": json.RawMessage(configFile(t, "transformers/T1/fixtures/minimal.expected_request.json"))}
	req, _ := json.Marshal(map[string]any{"draft": map[string]any{"kind": "request", "vendor_endpoint": "clevertap.upload@1",
		"yaml": create["yaml"]}, "fixtures": []any{good, pass}})
	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/run-goldens", string(req))
	if resp.Code != 200 || body["passed"] != false || body["total"] != 2.0 || body["failed"] != 1.0 {
		t.Fatalf("draft: %d %v", resp.Code, body)
	}
	results := body["results"].([]any)
	failed, passed := results[0].(map[string]any), results[1].(map[string]any)
	diff, _ := failed["diff"].([]any)
	if failed["fixture"] != "minimal" || failed["passed"] != false || len(diff) != 1 || passed["passed"] != true || len(passed["diff"].([]any)) != 0 {
		t.Fatalf("per-fixture results: %v", results)
	}
	if d := diff[0].(map[string]any); d["op"] != "replace" || d["path"] != "/body/d/0/evtName" || d["before"] != "app_closed" || d["after"] != "app_opened" {
		t.Fatalf("diff: %v", d)
	}
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"ref and draft", `{"ref":"T1@7","draft":{"kind":"request","vendor_endpoint":"clevertap.upload@1","yaml":"x: 1"}}`, 400},
		{"neither", `{}`, 400},
		{"unknown ref", `{"ref":"T1@99"}`, 404},
		{"draft without fixtures", `{"draft":{"kind":"request","vendor_endpoint":"clevertap.upload@1","yaml":"request: {method: POST, path: /x}"}}`, 400},
	} {
		if resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/run-goldens", c.body); resp.Code != c.status {
			t.Errorf("%s: %d %v", c.name, resp.Code, body)
		}
	}
}

func TestDryRunTransformer(t *testing.T) {
	a := newAdminRig(t)
	resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/dry-run",
		`{"ref":"T1@7","account_id":"ct-brandA-in","input":{"user":{"id":"u_1"},"event":{"name":"order_placed","timestamp":"2026-10-31T09:15:00Z"}}}`)
	vr, _ := body["vendor_request"].(map[string]any)
	if resp.Code != 200 || body["ok"] != true || vr["url"] != "http://127.0.0.1:8099/1/upload" ||
		vr["headers"].(map[string]any)["X-CleverTap-Passcode"] != "***" || !strings.Contains(resp.Body.String(), `"evtName":"order_placed"`) {
		t.Fatalf("request dry-run: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/dry-run",
		`{"ref":"R1@2","vendor_response":{"status":200,"body":{"status":"success","processed":1,"unprocessed":[]}}}`)
	if resp.Code != 200 || body["ok"] != true || body["canonical_response"].(map[string]any)["status"] != "ok" || body["vendor_request"] != nil {
		t.Fatalf("response dry-run: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/dry-run",
		`{"ref":"R1@2","vendor_response":{"status":503,"body":"<html>down</html>"}}`)
	if resp.Code != 200 || body["canonical_error"] == nil {
		t.Fatalf("response dry-run error: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/dry-run",
		`{"draft":{"kind":"request","vendor_endpoint":"clevertap.upload@1","yaml":"request:\n  method: POST\n  path: /1/upload\n  body:\n    id: 'req.user.id +'\n"},"input":{"user":{"id":"u"},"event":{"name":"x"}}}`)
	if resp.Code != 200 || body["ok"] != false || len(body["compile_errors"].([]any)) == 0 {
		t.Fatalf("draft that does not compile: %d %v", resp.Code, body)
	}
	// ADR-0023: a request transformer knows no caller schema (req.user.nope compiles), and the
	// produced request is checked against the endpoint's request_schema (schema_errors).
	resp, body = a.do(RoleEditor, http.MethodPost, "/transformers/dry-run",
		`{"draft":{"kind":"request","vendor_endpoint":"clevertap.upload@1","yaml":"request:\n  method: POST\n  path: /1/upload\n  body:\n    id: req.user.nope\n"},"input":{"user":{"id":"u"},"event":{"name":"x"}},"input_headers":{"X-Request-Source":"web"}}`)
	fields := map[string]bool{}
	for _, v := range body["schema_errors"].([]any) {
		fields[v.(map[string]any)["field"].(string)] = true
	}
	if resp.Code != 200 || body["ok"] != true || !fields["/body/d"] || !fields["/headers/Content-Type"] {
		t.Fatalf("draft breaking the endpoint schema: %d %v", resp.Code, body)
	}
	for _, c := range []struct {
		name, body string
		status     int
	}{
		{"unknown ref", `{"ref":"T1@99","input":{}}`, 404},
		{"unknown account", `{"ref":"T1@7","account_id":"nope-acct","input":{}}`, 404},
		{"no input", `{"ref":"T1@7"}`, 400},
	} {
		if resp, body := a.do(RoleEditor, http.MethodPost, "/transformers/dry-run", c.body); resp.Code != c.status {
			t.Errorf("%s: %d %v", c.name, resp.Code, body)
		}
	}
}
