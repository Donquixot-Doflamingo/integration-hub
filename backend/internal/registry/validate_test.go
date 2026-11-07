package registry

import (
	"errors"
	"strings"
	"testing"
)

const v1 = "integrations/int_evt_brandA/versions/1.yaml"

// version returns versions/1.yaml with old replaced by new (exactly once).
func version(t *testing.T, old, repl string) string {
	t.Helper()
	s := validFiles[v1]
	if !strings.Contains(s, old) {
		t.Fatalf("fixture has no %q", old)
	}
	return strings.Replace(s, old, repl, 1)
}

func validateFiles(t *testing.T, src Sources) (*Config, error) {
	t.Helper()
	c, err := Load(src)
	if err != nil {
		t.Fatalf("Load must succeed for a validation case: %v", err)
	}
	return c, Validate(c)
}

func TestValidConfigHasNoProblems(t *testing.T) {
	c, err := validateFiles(t, sources(t, validFiles))
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if ttl := c.Integrations["int_evt_brandA"].Versions[1].Delivery.TTL.Std(); ttl.Hours() != 6 {
		t.Fatalf("ttl %s", ttl)
	}
}

func TestValidateRejectsInvalidConfigs(t *testing.T) {
	const twoTargets = `targets:
  - vendor: clevertap
    account: ct-brandA-in
    transformer: { request: T1@7, response: R2@1 }
    weight: 60
  - vendor: clevertap
    account: ct-brandB-us
    transformer: { request: T1@7, response: R2@1 }
    weight: 30`
	tests := []struct {
		name      string
		overrides func(t *testing.T) map[string]string
		want      prob
	}{
		{"unknown account", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "account: ct-brandA-in", "account: ct-nope")}
		}, prob{v1, "targets[0].account", `unknown account "ct-nope"`}},
		{"account of another vendor", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "- vendor: clevertap", "- vendor: moengage")}
		}, prob{v1, "targets[0].vendor", `account "ct-brandA-in" belongs to vendor "clevertap", not "moengage"`}},
		{"unknown transformer", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "request: T1@7", "request: T9@1")}
		}, prob{v1, "targets[0].transformer.request", "unknown transformer T9@1"}},
		{"response transformer in request slot", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "request: T1@7", "request: R2@1")}
		}, prob{v1, "targets[0].transformer.request", "R2@1 is a response transformer, want request"}},
		{"weights do not total 100", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "targets:\n  - vendor: clevertap\n    account: ct-brandA-in\n    transformer: { request: T1@7, response: R2@1 }\n    weight: 100", twoTargets)}
		}, prob{v1, "targets", "weights must total 100, got 90"}},
		{"failover without chain", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "mode: async", "mode: sync\n  on_circuit_open: failover")}
		}, prob{v1, "delivery.on_circuit_open", "failover needs a failover chain"}},
		{"wait window too long", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "mode: async", "mode: wait\n  wait_ms: 45000")}
		}, prob{v1, "delivery.wait_ms", "must be 1..30000"}},
		{"async without polling or webhook", func(t *testing.T) map[string]string {
			s := version(t, "webhooks:\n  - event: delivery.status\n    endpoint: reg_orders_cb\n", "")
			return map[string]string{v1: s}
		}, prob{v1, "delivery", "mode async needs polling: true or a webhook subscription (DESIGN §5.2)"}},
		{"async_callback without webhook", func(t *testing.T) map[string]string {
			s := version(t, "webhooks:\n  - event: delivery.status\n    endpoint: reg_orders_cb\n", "")
			return map[string]string{v1: strings.Replace(s, "mode: async", "mode: async_callback", 1)}
		}, prob{v1, "webhooks", "mode async_callback needs at least one webhook subscription"}},
		{"unknown registrar endpoint", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "endpoint: reg_orders_cb", "endpoint: reg_missing")}
		}, prob{v1, "webhooks[0].endpoint", `unknown registrar endpoint "reg_missing"`}},
		{"registrar endpoint of another tenant", func(t *testing.T) map[string]string {
			return map[string]string{
				"tenants/billing.yaml":    "tenant: billing\napi_keys: []\nrate_limit: { rps: 1, burst: 1 }\n",
				"registrar/reg_bill.yaml": "endpoint_id: reg_bill\ntenant: billing\nurl: https://billing.example/cb\nsecret_ref: localfile://reg_bill\n",
				v1:                        version(t, "endpoint: reg_orders_cb", "endpoint: reg_bill"),
			}
		}, prob{v1, "webhooks[0].endpoint", `registrar endpoint "reg_bill" belongs to tenant "billing", not the owner "orders-service"`}},
		{"unknown webhook event", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "event: delivery.status", "event: delivery.statsu")}
		}, prob{v1, "webhooks[0].event", `unknown event "delivery.statsu" (want one of request.completed, request.failed, request.expired, delivery.status)`}},
		{"active version missing", func(t *testing.T) map[string]string {
			return map[string]string{"integrations/int_evt_brandA/integration.yaml": strings.Replace(validFiles["integrations/int_evt_brandA/integration.yaml"], "active_version: 1", "active_version: 4", 1)}
		}, prob{"integrations/int_evt_brandA/integration.yaml", "active_version", "version 4 does not exist (have [1])"}},
		{"unknown owner tenant", func(t *testing.T) map[string]string {
			return map[string]string{"integrations/int_evt_brandA/integration.yaml": strings.Replace(validFiles["integrations/int_evt_brandA/integration.yaml"], "owner_tenant: orders-service", "owner_tenant: ghosts", 1)}
		}, prob{"integrations/int_evt_brandA/integration.yaml", "owner_tenant", `unknown tenant "ghosts"`}},
		{"request transformer missing", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "transformer: { request: T1@7, response: R2@1 }", "transformer: { response: R2@1 }")}
		}, prob{v1, "targets[0].transformer.request", "required"}},
		{"request transformer in response slot", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "response: R2@1", "response: T1@7")}
		}, prob{v1, "targets[0].transformer.response", "T1@7 is a request transformer, want response"}},
		{"retired account in active version", func(t *testing.T) map[string]string {
			return map[string]string{"accounts/ct-brandA-in.yaml": strings.Replace(validFiles["accounts/ct-brandA-in.yaml"], "state: active", "state: retired", 1)}
		}, prob{v1, "targets[0].account", `account "ct-brandA-in" is retired and cannot be targeted by the active version (DESIGN §6.5)`}},
		{"account region missing on vendor", func(t *testing.T) map[string]string {
			return map[string]string{"accounts/ct-brandB-us.yaml": strings.Replace(validFiles["accounts/ct-brandB-us.yaml"], "region: us", "region: eu", 1)}
		}, prob{"accounts/ct-brandB-us.yaml", "region", `vendor "clevertap" has no region "eu" (has in, us)`}},
		{"transformer endpoint unknown", func(t *testing.T) map[string]string {
			return map[string]string{"transformers/T1/7.yaml": strings.Replace(validFiles["transformers/T1/7.yaml"], "clevertap.upload@1", "clevertap.upload@2", 1)}
		}, prob{"transformers/T1/7.yaml", "vendor_endpoint", `vendor "clevertap" has no endpoint "upload@2"`}},
		{"publish metadata missing", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "publish: { author: platform-team, reason: initial }", "publish: { author: platform-team }")}
		}, prob{v1, "publish", "author and reason are required"}},
		{"shadow sends to a side-effect endpoint", func(t *testing.T) map[string]string {
			return map[string]string{
				v1:                       version(t, "publish: {", shadowYAML("send", "")+"publish: {"),
				"vendors/clevertap.yaml": strings.Replace(validFiles["vendors/clevertap.yaml"], "idempotent: true", "idempotent: false", 1),
			}
		}, prob{v1, "shadow.mode", "clevertap.upload@1 is not idempotent (it has side effects): its shadow must be dry_run"}},
		{"shadow mode unknown", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "publish: {", shadowYAML("mirror", "")+"publish: {")}
		}, prob{v1, "shadow.mode", "must be send or dry_run"}},
		{"shadow sample ratio out of range", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "publish: {", shadowYAML("dry_run", "\n  sample_ratio: 1.5")+"publish: {")}
		}, prob{v1, "shadow.sample_ratio", "must be 0..1"}},
		{"shadow target unknown account", func(t *testing.T) map[string]string {
			return map[string]string{v1: version(t, "publish: {", strings.Replace(shadowYAML("dry_run", ""), "ct-brandB-us", "ct-nope", 1)+"publish: {")}
		}, prob{v1, "shadow.target.account", `unknown account "ct-nope"`}},
		{"registrar url not absolute", func(t *testing.T) map[string]string {
			return map[string]string{"registrar/reg_orders_cb.yaml": strings.Replace(validFiles["registrar/reg_orders_cb.yaml"], "https://orders.internal.example/hub/callbacks", "/hub/callbacks", 1)}
		}, prob{"registrar/reg_orders_cb.yaml", "url", "must be an absolute http(s) URL"}},
	}
	if len(tests) < 15 {
		t.Fatalf("HUB-043 asks for >= 15 invalid configs, have %d", len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateFiles(t, sources(t, files(tt.overrides(t))))
			var ps ConfigErrors
			if !errors.As(err, &ps) {
				t.Fatalf("want ConfigErrors, got %v", err)
			}
			if !hasProblem(ps, tt.want.file, tt.want.path, "", tt.want.msg) {
				t.Fatalf("missing problem\n  %s\ngot:\n  %s", tt.want, strings.ReplaceAll(err.Error(), "\n", "\n  "))
			}
		})
	}
}

// prob is the file, path and message of an expected problem.
type prob struct{ file, path, msg string }

func (p prob) String() string { return p.file + ": " + p.path + ": " + p.msg }

func TestInteractiveIntegrationOnlyAllowsSync(t *testing.T) {
	m := files(map[string]string{
		"integrations/int_evt_brandA/integration.yaml": validFiles["integrations/int_evt_brandA/integration.yaml"] + "interactive: true\n",
	})
	_, err := validateFiles(t, sources(t, m))
	var ps ConfigErrors
	if !errors.As(err, &ps) || !hasProblem(ps, v1, "delivery.mode", "", "integration int_evt_brandA is interactive: only sync is allowed (DESIGN §5.4)") {
		t.Fatalf("missing the interactive problem in %v", err)
	}
}

func TestPassThroughPairIsValid(t *testing.T) {
	if _, err := validateFiles(t, sources(t, files(map[string]string{v1: version(t, "response: R2@1 }", "response: null }")}))); err != nil {
		t.Fatalf("a pair without a response transformer (pass-through) must be valid: %v", err)
	}
	if _, err := validateFiles(t, sources(t, files(map[string]string{v1: version(t, ", response: R2@1 }", " }")}))); err != nil {
		t.Fatalf("an omitted response transformer must be valid: %v", err)
	}
}

func TestValidateReportsAllProblemsInOnePass(t *testing.T) {
	_, err := validateFiles(t, sources(t, files(map[string]string{
		v1:                           strings.NewReplacer("account: ct-brandA-in", "account: ct-nope", "endpoint: reg_orders_cb", "endpoint: reg_missing").Replace(validFiles[v1]),
		"accounts/ct-brandB-us.yaml": strings.Replace(validFiles["accounts/ct-brandB-us.yaml"], "region: us", "region: eu", 1),
	})))
	var ps ConfigErrors
	if !errors.As(err, &ps) || len(ps) != 3 {
		t.Fatalf("want exactly 3 problems, got %v", err)
	}
}

// shadowYAML is a shadow block copying to ct-brandB-us.
func shadowYAML(mode, extra string) string {
	return "shadow:\n  target: { vendor: clevertap, account: ct-brandB-us, transformer: { request: T1@7, response: R2@1 } }\n  mode: " + mode + extra + "\n"
}

func TestShadowDryRunIsValidForSideEffectEndpoints(t *testing.T) {
	_, err := validateFiles(t, sources(t, files(map[string]string{
		v1:                       version(t, "publish: {", shadowYAML("dry_run", "\n  sample_ratio: 0.25")+"publish: {"),
		"vendors/clevertap.yaml": strings.Replace(validFiles["vendors/clevertap.yaml"], "idempotent: true", "idempotent: false", 1),
	})))
	if err != nil {
		t.Fatalf("dry_run shadow on a side-effect endpoint must be valid: %v", err)
	}
}
