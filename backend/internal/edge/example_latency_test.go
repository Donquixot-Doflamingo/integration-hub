package edge

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// HUB-353: examples/latency-demo is a registry overlay for the live latency demo (run.sh calls a
// public echo API directly and through a local Hub). This test keeps its config and goldens green;
// the integration test runs the integration through the gateway against mockvendor.

const latencyExampleDir = "../../../examples/latency-demo"

func TestExampleLatencyConfigBuilds(t *testing.T) {
	snap := overlaySnapshot(t, latencyExampleDir)
	in := snap.Integrations["int_latency_echo"]
	if in == nil {
		t.Fatal("int_latency_echo missing")
	}
	if v := in.Active(); v.Delivery.Mode != registry.ModeSync || in.MovesMoneyOrQuota {
		t.Errorf("delivery %+v, moves_money_or_quota %v", v.Delivery, in.MovesMoneyOrQuota)
	}
	v := snap.Vendors["postman-echo"]
	if v == nil || v.Regions["public"] != "https://postman-echo.com" || v.Regions["mock"] != "http://127.0.0.1:8099" || v.Auth != creds.SchemeNone {
		t.Fatalf("vendor %+v", v)
	}
	if a := snap.Accounts["echo-public"]; a == nil || a.Region != "public" || a.CredentialsRef != "" {
		t.Fatalf("account %+v", a)
	}
	// auth none: the account needs no credentials, so nothing is loaded for it.
	if slices.Contains(creds.AccountIDs(snap), "echo-public") {
		t.Error("echo-public is listed as needing credentials")
	}
	if ep := v.Endpoints["echo@1"]; ep.RequestSchema == nil || ep.RequestSchema.Body == nil {
		t.Errorf("echo@1 has no request schema: %+v", ep)
	}
	// The mock scenario run.sh uses must answer on the vendor's endpoint path.
	b, err := os.ReadFile(latencyExampleDir + "/mockvendor/echo.json")
	if err != nil {
		t.Fatal(err)
	}
	var s kycScenario
	if err := json.Unmarshal(b, &s); err != nil || s.Routes["echo"].Match.Path != v.Endpoints["echo@1"].Path {
		t.Fatalf("mock scenario %+v %v", s, err)
	}
}
