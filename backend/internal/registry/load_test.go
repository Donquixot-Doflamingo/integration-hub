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

func TestLoadCapabilityCompilesFromContracts(t *testing.T) {
	c := mustLoad(t, validFiles)
	capability := c.Capabilities["events.track@v1"]
	if capability == nil || capability.Interactive || capability.DefaultTTL != 6*time.Hour {
		t.Fatalf("capability %+v", capability)
	}
	op := capability.Operations["default"]
	if op == nil || !op.Idempotent || op.RequestID != "urn:hub:capability:events.track@v1:default:request" {
		t.Fatalf("operation %+v", op)
	}
	if len(op.PII) == 0 || op.PII[len(op.PII)-1] != (PIIPath{Pointer: "/user/id", Kind: "identifier"}) {
		t.Fatalf("x-pii paths %+v", op.PII)
	}
	ok := map[string]any{"user": map[string]any{"id": "u_1"}, "event": map[string]any{"name": "order_placed"}}
	if err := op.Request.Validate(ok); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := op.Request.Validate(map[string]any{"event": map[string]any{"name": "x"}}); err == nil {
		t.Fatal("request without user accepted")
	}
	if err := op.Response.Validate(map[string]any{"status": "ok"}); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
}

func TestLoadVendorAccountAndDefaults(t *testing.T) {
	c := mustLoad(t, files(map[string]string{
		"integrations/int_evt_brandA/versions/2.yaml": `
integration: int_evt_brandA
version: 2
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
	want := Delivery{Mode: ModeWait, WaitMS: 3000, OnCircuitOpen: FailFast, Pending: ReResolve}
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
capability: events.track@v1
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
		"vendors/clevertap.yaml":                      validFiles["vendors/clevertap.yaml"] + "\ntimeout: 5\n",
		"accounts/ct-brandB-us.yaml":                  strings.Replace(validFiles["accounts/ct-brandB-us.yaml"], "account: ct-brandB-us", "account: ct-brandB-eu", 1),
		"tenants/orders-service.yaml":                 "tenant: orders-service\napi_keys:\n  - key_id: k1\n    key: hk_live_plaintext\n",
		"tenants/billing.yaml":                        "tenant: billing\napi_keys:\n  - { key_id: k1, hash: md5:abc, scopes: [invoke], state: active }\n",
		"transformers/T1/7.yaml":                      strings.Replace(validFiles["transformers/T1/7.yaml"], "version: 7", "version: 8", 1),
		"integrations/int_evt_brandA/versions/1.yaml": "integration: int_evt_brandA\nversion: 1\ntargets: [\n",
		"capabilities/events.track.v1.yaml":           "capability: events.track@v1\nschema_ref: schemas/capabilities/missing.json\n",
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
		"transformers/T1/7.yaml: version: version 8 must equal the file name",
		"integrations/int_evt_brandA/versions/1.yaml: ",
		"capabilities/events.track.v1.yaml: schema_ref: read schemas/capabilities/missing.json",
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
	c, err := Load(Sources{Config: mapFS(map[string]string{}), Contracts: contractsFS(t)})
	if err != nil || len(c.Integrations) != 0 {
		t.Fatalf("empty config: %v %+v", err, c)
	}
}
