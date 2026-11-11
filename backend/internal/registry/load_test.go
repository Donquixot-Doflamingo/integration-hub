package registry

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func mustLoad(t *testing.T, m map[string]string) *Config {
	t.Helper()
	c, err := Load(sources(t, m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadCompilesOperationSchemas(t *testing.T) {
	c := mustLoad(t, validFiles)
	op := c.Integrations["int_evt_brandA"].Versions[1].Op("default")
	if op == nil || !op.Idempotent || !op.Validates() || op.Name != "default" {
		t.Fatalf("operation %+v", op)
	}
	if len(op.PII) == 0 || op.PII[len(op.PII)-1] != (PIIPath{Pointer: "/user/id", Kind: "identifier"}) {
		t.Fatalf("x-pii paths %+v", op.PII)
	}
	body := op.RequestSchema.Body.Compiled()
	ok := map[string]any{"user": map[string]any{"id": "u_1"}, "event": map[string]any{"name": "order_placed"}}
	if err := body.Validate(ok); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := body.Validate(map[string]any{"event": map[string]any{"name": "x"}}); err == nil {
		t.Fatal("request without user accepted")
	}
}

func TestLoadOperationSchemaProblems(t *testing.T) {
	v1 := "integrations/int_evt_brandA/versions/1.yaml"
	tests := []struct {
		name, ops, want string
	}{
		{"no operations", "operations: {}\n", "operations: declare at least one operation"},
		{"body missing", "operations:\n  default: { idempotent: true, request_schema: {} }\n", "operations.default.request_schema.body: required"},
		{"body does not compile", "operations:\n  default: { idempotent: true, request_schema: { body: { type: 12 } } }\n", "operations.default.request_schema.body: does not compile"},
		{"hub header declared", "operations:\n  default: { idempotent: true, request_schema: { body: { type: object }, headers: { type: object, properties: { Idempotency-Key: { type: string } } } } }\n",
			"Idempotency-Key is a Hub header"},
		{"bad operation name", "operations:\n  Send: { idempotent: true, request_schema: { body: { type: object } } }\n", "operation names must match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := validFiles[v1]
			start, end := strings.Index(doc, "operations:"), strings.Index(doc, "targets:")
			doc = doc[:start] + tt.ops + doc[end:]
			_, err := Load(sources(t, files(map[string]string{v1: doc})))
			var ps ConfigErrors
			if !errors.As(err, &ps) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if ps[0].Check != CheckSchemasCompile {
				t.Fatalf("check %q, want schemas_compile", ps[0].Check)
			}
		})
	}
}

func TestLoadEndpointRequestSchema(t *testing.T) {
	vendor := validFiles["vendors/clevertap.yaml"]
	t.Run("required", func(t *testing.T) {
		_, err := Load(sources(t, files(map[string]string{"vendors/clevertap.yaml": strings.Replace(vendor, ", request_schema: {}", "", 1)})))
		if err == nil || !strings.Contains(err.Error(), "endpoints.upload@1.request_schema: required") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("compiled", func(t *testing.T) {
		c := mustLoad(t, files(map[string]string{"vendors/clevertap.yaml": strings.Replace(vendor, "request_schema: {}",
			"request_schema: { query: { type: object, required: [name] } }, response_schema: { type: object }", 1)}))
		ep := c.Vendors["clevertap"].Endpoints["upload@1"]
		if ep.RequestSchema.Query.Compiled() == nil || ep.RequestSchema.Body != nil || ep.ResponseSchema["type"] != "object" {
			t.Fatalf("endpoint %+v", ep)
		}
	})
	t.Run("does not compile", func(t *testing.T) {
		_, err := Load(sources(t, files(map[string]string{"vendors/clevertap.yaml": strings.Replace(vendor, "request_schema: {}", "request_schema: { body: { minLength: x } }", 1)})))
		if err == nil || !strings.Contains(err.Error(), "endpoints.upload@1.request_schema.body: does not compile") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestLoadVendorAccountAndDefaults(t *testing.T) {
	c := mustLoad(t, files(map[string]string{
		"integrations/int_evt_brandA/versions/2.yaml": `
integration: int_evt_brandA
version: 2
operations: { default: { idempotent: true, request_schema: { body: { type: object } } } }
targets: [{ vendor: clevertap, account: ct-brandA-in, transformer: { request: T1@7, response: R2@1 } }]
publish: { author: a, reason: b }
`,
	}))
	ep := c.Vendors["clevertap"].Endpoints["upload@1"]
	if ep.TimeoutMS != 3000 || !ep.Idempotent || ep.BatchMax != 1000 || c.Vendors["clevertap"].Defaults.Breaker.OpenMaxS != 600 {
		t.Fatalf("endpoint %+v", ep)
	}
	if a := c.Accounts["ct-brandB-us"]; a.Region != "us" || a.State != AccountActive || a.Config["brand_code"] != "B" {
		t.Fatalf("account %+v", a)
	}
	v := c.Integrations["int_evt_brandA"].Versions[2]
	want := Delivery{Mode: ModeWait, WaitMS: 3000, OnCircuitOpen: FailFast, Pending: ReResolve, TTL: Duration(DefaultTTL)}
	if v.Delivery != want || v.Audit.Payloads != PayloadMasked || v.Audit.Retention.Std() != 30*24*time.Hour {
		t.Fatalf("defaults not applied (D3, D4, D10): %+v %+v", v.Delivery, v.Audit)
	}
	if c.Transformers[TransformerRef{"T1", 7}].VendorEndpoint != "clevertap.upload@1" {
		t.Fatal("transformer header not loaded")
	}
}

func TestLoadAcceptsDesignVersionVerbatim(t *testing.T) {
	raw, err := os.ReadFile("testdata/design/3.4-integration-version.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, files(map[string]string{
		"integrations/int_evt_brandB_min/integration.yaml": `
integration: int_evt_brandB_min
owner_tenant: orders-service
grants: []
active_version: 3
`,
		"integrations/int_evt_brandB_min/versions/3.yaml": string(raw),
	}))
	if v := c.Integrations["int_evt_brandB_min"].Versions[3]; v.Delivery.OnCircuitOpen != QueueIfAllowed || v.Breaker.Override.OpenAfterConsecutiveTimeouts != 3 {
		t.Fatalf("version %+v", v)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(sources(t, files(map[string]string{
		"vendors/clevertap.yaml":      validFiles["vendors/clevertap.yaml"] + "\ntimeout: 5\n",
		"accounts/ct-brandB-us.yaml":  strings.Replace(validFiles["accounts/ct-brandB-us.yaml"], "account: ct-brandB-us", "account: ct-brandB-eu", 1),
		"tenants/orders-service.yaml": "tenant: orders-service\napi_keys:\n  - key_id: k1\n    key: hk_live_plaintext\n",
		"tenants/billing.yaml": "tenant: billing\napi_keys:\n  - { key_id: k1, hash: md5:abc, scopes: [invoke], state: active }\n" +
			"  - { key_id: k2, hash: \"sha256:0000000000000000000000000000000000000000000000000000000000000000\", scopes: [invoke], state: expiring }\n" +
			"  - { key_id: k3, hash: \"sha256:1111111111111111111111111111111111111111111111111111111111111111\", scopes: [invoke], state: paused }\n",
		"transformers/T1/7.yaml":                      strings.Replace(validFiles["transformers/T1/7.yaml"], "version: 7", "version: 8", 1),
		"integrations/int_evt_brandA/versions/1.yaml": "integration: int_evt_brandA\nversion: 1\ntargets: [\n",
	})))
	var ps ConfigErrors
	if !errors.As(err, &ps) {
		t.Fatalf("want ConfigErrors, got %v", err)
	}
	want := []string{
		"vendors/clevertap.yaml: line 14: field timeout not found",
		`accounts/ct-brandB-us.yaml: account: id "ct-brandB-eu" must equal the file name "ct-brandB-us"`,
		"tenants/orders-service.yaml: line 4: field key not found",
		"tenants/billing.yaml: api_keys[0].hash: must be",
		"tenants/billing.yaml: api_keys[1].expires_at: an expiring key needs expires_at",
		"tenants/billing.yaml: api_keys[2].state: must be active, expiring or revoked",
		"transformers/T1/7.yaml: version: version 8 must equal the file name",
		"integrations/int_evt_brandA/versions/1.yaml: ",
	}
	got := err.Error()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing problem %q in:\n%s", w, got)
		}
	}
	if len(ps) < len(want) {
		t.Errorf("got %d problems, want >= %d (all in one pass)", len(ps), len(want))
	}
}

func TestLoadMissingDirectoriesAreEmpty(t *testing.T) {
	c, err := Load(Sources{Config: mapFS(map[string]string{})})
	if err != nil || len(c.Integrations) != 0 {
		t.Fatalf("empty config: %v %+v", err, c)
	}
}

func TestLoadTenantISFCFields(t *testing.T) {
	const id = "6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f"
	linked := "tenant: hl-t\nsetup: pending\nisfc_caller_clients: [billing-backend]\nisfc:\n  tenant_id: " + id +
		"\n  key: HL\n  name: Home Loans\n  status: ACTIVE\n  linked: true\n  synced_at: 2026-11-11T10:00:00Z\napi_keys: []\n"
	t.Run("old yaml loads unchanged", func(t *testing.T) {
		c := mustLoad(t, validFiles)
		for id, tn := range c.Tenants {
			if tn.ISFC != nil || tn.Setup != "" || tn.SetupState() != SetupComplete || tn.ISFCCallerClients != nil {
				t.Errorf("tenant %s gained ISFC fields: %+v", id, tn)
			}
		}
	})
	t.Run("isfc block loads", func(t *testing.T) {
		c := mustLoad(t, files(map[string]string{"tenants/hl-t.yaml": linked}))
		tn := c.Tenants["hl-t"]
		if tn.SetupState() != SetupPending || tn.ISFC == nil || tn.ISFC.Key != "HL" || !tn.ISFC.Linked ||
			tn.ISFC.SyncedAt == nil || tn.ISFCCallerClients[0] != "billing-backend" {
			t.Fatalf("tenant %+v isfc %+v", tn, tn.ISFC)
		}
	})
	tests := []struct{ name, doc, want string }{
		{"bad setup", "tenant: hl-t\nsetup: done\n", "setup: must be pending or complete"},
		{"bad uuid", "tenant: hl-t\nisfc: { tenant_id: nope, key: HL, name: x, status: ACTIVE, linked: true }\n", "isfc.tenant_id: must be an ISFC tenant UUID"},
		{"no key", "tenant: hl-t\nisfc: { tenant_id: " + id + ", name: x, status: ACTIVE, linked: true }\n", "isfc.key: required"},
		{"bad status", "tenant: hl-t\nisfc: { tenant_id: " + id + ", key: HL, name: x, status: GONE, linked: true }\n", "isfc.status: must be ACTIVE or INACTIVE"},
		{"empty caller client", "tenant: hl-t\nisfc_caller_clients: ['']\n", "isfc_caller_clients[0]: must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(sources(t, files(map[string]string{"tenants/hl-t.yaml": tt.doc})))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
	t.Run("same ISFC tenant on two Hub tenants", func(t *testing.T) {
		other := strings.Replace(linked, "tenant: hl-t", "tenant: hl-t-2", 1)
		_, err := Load(sources(t, files(map[string]string{"tenants/hl-t.yaml": linked, "tenants/hl-t-2.yaml": other})))
		if err == nil || !strings.Contains(err.Error(), "already linked to Hub tenant") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestSnapshotIndexesTenantsByISFCID(t *testing.T) {
	const id = "6F1C0B2E-3A4D-4C5E-8F60-1A2B3C4D5E6F"
	doc := "tenant: hl-t\nisfc: { tenant_id: " + id + ", key: HL, name: Home Loans, status: ACTIVE, linked: true }\n"
	s := build(t, files(map[string]string{"tenants/hl-t.yaml": doc}))
	if tn, ok := s.TenantByISFC(strings.ToLower(id)); !ok || tn.Tenant != "hl-t" {
		t.Fatalf("TenantByISFC: %v %v", tn, ok)
	}
	if _, ok := s.TenantByISFC("00000000-0000-0000-0000-000000000000"); ok {
		t.Fatal("unknown id found")
	}
}
