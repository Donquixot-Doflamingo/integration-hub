package edge

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// HUB-435: examples/agify-age onboards a public API (agify.io, auth scheme none) the ADR-0023
// way: the integration owns its request schema, the endpoint declares the query it accepts, and
// both transformers are proven by their samples while the snapshot builds. The integration test
// runs it through the gateway against mockvendor with the scenario files run.sh uses.

const agifyExampleDir = "../../../examples/agify-age"

// agifyScenarios are the files of examples/agify-age/mockvendor (run.sh's scenario argument).
var agifyScenarios = []string{"found", "unknown-name", "rate-limited"}

func TestExampleAgifyConfigBuilds(t *testing.T) {
	snap := overlaySnapshot(t, agifyExampleDir)
	in := snap.Integrations["int_age_by_name"]
	if in == nil {
		t.Fatal("int_age_by_name missing")
	}
	v := in.Active()
	if v.Delivery.Mode != registry.ModeSync || v.Delivery.OnCircuitOpen != "fail_fast" || in.MovesMoneyOrQuota || in.Interactive {
		t.Errorf("delivery %+v, moves_money_or_quota %v, interactive %v", v.Delivery, in.MovesMoneyOrQuota, in.Interactive)
	}
	op := v.Operations["default"]
	if op == nil || op.RequestSchema.Body == nil {
		t.Fatalf("operation default %+v", op)
	}
	if req, _ := op.RequestSchema.Body.Doc["required"].([]any); !slices.Equal(req, []any{"firstName", "lastName"}) {
		t.Errorf("required %v, want firstName and lastName", req)
	}
	vendor := snap.Vendors["agify"]
	if vendor == nil || vendor.Auth != creds.SchemeNone || vendor.Regions["public"] != "https://api.agify.io" || vendor.Regions["mock"] != "http://127.0.0.1:8099" {
		t.Fatalf("vendor %+v", vendor)
	}
	ep, ok := vendor.Endpoints["estimate@1"]
	if !ok || ep.Method != "GET" || ep.Path != "/" || ep.RequestSchema == nil || ep.RequestSchema.Query == nil {
		t.Fatalf("endpoint estimate@1 %+v", ep)
	}
	if a := snap.Accounts["agify-public"]; a == nil || a.Vendor != "agify" || a.CredentialsRef != "" {
		t.Fatalf("account %+v", a)
	}
	if slices.Contains(creds.AccountIDs(snap), "agify-public") {
		t.Error("agify-public is listed as needing credentials")
	}
	pair := v.Targets[0].Pair("default")
	if tf := snap.Transformers[pair.Request]; tf == nil || tf.VendorEndpoint != "agify.estimate@1" {
		t.Errorf("request transformer %+v", tf)
	}
	if pair.PassThrough() || snap.Transformers[pair.Response] == nil {
		t.Errorf("response transformer missing: %+v", pair)
	}
	// Every scenario run.sh can load answers on the endpoint's method and path.
	for _, name := range agifyScenarios {
		b, err := os.ReadFile(agifyExampleDir + "/mockvendor/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var s kycScenario
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if m := s.Routes["agify"].Match; m.Method != ep.Method || m.Path != ep.Path {
			t.Errorf("%s: route %+v does not match %s %s", name, m, ep.Method, ep.Path)
		}
	}
}
