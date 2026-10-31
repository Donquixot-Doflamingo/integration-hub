package registry

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// validFiles is a small, valid registry used as the base for loader and validation tests.
// Tests copy it, then break one thing.
var validFiles = map[string]string{
	"capabilities/events.track.v1.yaml": `
capability: events.track@v1
schema_ref: schemas/capabilities/events.track.v1.request.json
default_ttl: 6h
`,
	"vendors/clevertap.yaml": `
vendor: clevertap
auth: static_headers
regions:
  in: http://127.0.0.1:8099
  us: http://127.0.0.1:8099
endpoints:
  upload@1: { method: POST, path: /1/upload, timeout_ms: 3000, connect_timeout_ms: 500, idempotent: true, batch_max: 1000 }
defaults:
  rate_limit: { per_account_rps: 50 }
  breaker: { window_s: 30, min_calls: 20, failure_ratio: 0.5, consecutive_timeouts: 5, open_s: 30, open_max_s: 600, half_open_probes: 3 }
  health_probe: { endpoint: upload@1, dry: true, every_s: 30 }
`,
	"accounts/ct-brandA-in.yaml": `
account: ct-brandA-in
vendor: clevertap
region: in
state: active
credentials_ref: localfile://ct-brandA-in
config: { project_id: "TEST-A", brand_code: "A" }
quota: { rps: 50, daily: 5000000 }
`,
	"accounts/ct-brandB-us.yaml": `
account: ct-brandB-us
vendor: clevertap
region: us
state: active
credentials_ref: localfile://ct-brandB-us
config: { project_id: "TEST-B", brand_code: "B" }
quota: { rps: 50, daily: 5000000 }
`,
	"transformers/T1/7.yaml": `
transformer: T1
version: 7
kind: request
capability: events.track@v1
vendor_endpoint: clevertap.upload@1
request:
  method: POST
  path: /1/upload
`,
	"transformers/R2/1.yaml": `
transformer: R2
version: 1
kind: response
capability: events.track@v1
response:
  status: '"ok"'
`,
	"transformers/T1/fixtures/basic.input.json":            `{"user": {"id": "u_1"}, "event": {"name": "e"}}`,
	"transformers/T1/fixtures/basic.expected_request.json": `{"method": "POST", "path": "/1/upload"}`,
	"transformers/R2/fixtures/ok.vendor_response.json":     `{"status": 200, "body": {}}`,
	"transformers/R2/fixtures/ok.expected_response.json":   `{"response": {"status": "ok"}}`,
	"integrations/int_evt_brandA/integration.yaml": `
integration: int_evt_brandA
owner_tenant: orders-service
capability: events.track@v1
grants: []
active_version: 1
`,
	"integrations/int_evt_brandA/versions/1.yaml": `
integration: int_evt_brandA
version: 1
targets:
  - vendor: clevertap
    account: ct-brandA-in
    transformer: { request: T1@7, response: R2@1 }
    weight: 100
delivery:
  mode: async
  ttl: 6h
webhooks:
  - event: delivery.status
    endpoint: reg_orders_cb
publish: { author: platform-team, reason: initial }
`,
	"tenants/orders-service.yaml": `
tenant: orders-service
api_keys:
  - key_id: key_01J9Z3K8X2V7Q4M6N8P0R2S4T6
    hash: sha256:0000000000000000000000000000000000000000000000000000000000000001
    scopes: [invoke, dry_run]
    state: active
rate_limit: { rps: 100, burst: 200 }
`,
	"registrar/reg_orders_cb.yaml": `
endpoint_id: reg_orders_cb
tenant: orders-service
url: https://orders.internal.example/hub/callbacks
secret_ref: localfile://reg_orders_cb
`,
}

// files returns a copy of validFiles with overrides applied; an empty value deletes the file.
func files(overrides map[string]string) map[string]string {
	m := maps.Clone(validFiles)
	for k, v := range overrides {
		if v == "" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func mapFS(m map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for k, v := range m {
		fsys[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fsys
}

// contractsFS is the real repo-root contracts/ directory (capability schemas are never copied).
func contractsFS(t testing.TB) fstest.MapFS {
	t.Helper()
	wd, _ := os.Getwd()
	for d := wd; ; d = filepath.Dir(d) {
		dir := filepath.Join(d, "contracts")
		if st, err := os.Stat(filepath.Join(dir, "schemas", "capabilities")); err == nil && st.IsDir() {
			out := fstest.MapFS{}
			ents, _ := os.ReadDir(filepath.Join(dir, "schemas", "capabilities"))
			for _, e := range ents {
				b, err := os.ReadFile(filepath.Join(dir, "schemas", "capabilities", e.Name()))
				if err == nil {
					out["schemas/capabilities/"+e.Name()] = &fstest.MapFile{Data: b}
				}
			}
			return out
		}
		if filepath.Dir(d) == d {
			t.Fatal("contracts/ not found")
		}
	}
}

func sources(t testing.TB, m map[string]string) Sources {
	return Sources{Config: mapFS(m), Contracts: contractsFS(t)}
}
