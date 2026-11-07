package edge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// HUB-351, HUB-352: examples/aadhaar-okyc is a registry overlay for two real-provider examples
// (Aadhaar OTP e-KYC and PAN verification on a KYC provider's sandbox), kept out of
// backend/config and contracts/ like examples/otp-account-flow. Its mockvendor/ folder holds the
// route specs that emulate the provider: the integration test and the demo script (run.sh) load
// the same files, so the demo and the tests exercise the same emulation.

const kycExampleDir = "../../../examples/aadhaar-okyc"

// kycKey is the documented test-only key of the example tenant kyc-onboarding
// (examples/aadhaar-okyc/config/tenants/kyc-onboarding.yaml).
const kycKey = "hk_test_example_kyc_onboarding_local_only"

// overlaySources is backend/config with the overlay folder dir merged over it (upgraded while
// the example keeps the pre-ADR-0023 layout, see exampleTree); the overlay may only add files.
func overlaySources(t testing.TB, dir string) registry.Sources {
	t.Helper()
	return registry.Sources{Config: exampleTree(t, dir)}
}

func overlaySnapshot(t testing.TB, dir string) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(overlaySources(t, dir), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// kycScenario is one file of examples/aadhaar-okyc/mockvendor: the routes that make mockvendor
// answer like the provider for one scenario (the same JSON run.sh PUTs to the control API).
type kycScenario struct {
	Description string                          `json:"description"`
	Routes      map[string]mockvendor.RouteSpec `json:"routes"`
}

func loadKYCScenario(t testing.TB, name string) kycScenario {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(kycExampleDir, "mockvendor", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s kycScenario
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(s.Routes) == 0 {
		t.Fatalf("%s: no routes", name)
	}
	return s
}

// TestExampleKYCConfigBuilds: the overlay loads, validates and passes every golden of its
// transformers (a failing golden refuses the snapshot). The Aadhaar integration is sync,
// fail_fast, sealed for 90 days (D6), with one pair per operation on the provider's two OKYC
// endpoints.
func TestExampleKYCConfigBuilds(t *testing.T) {
	snap := overlaySnapshot(t, kycExampleDir)
	in := snap.Integrations["int_aadhaar_okyc"]
	if in == nil {
		t.Fatal("int_aadhaar_okyc missing")
	}
	v := in.Active()
	if v.Delivery.Mode != registry.ModeSync || v.Delivery.OnCircuitOpen != "fail_fast" || !in.MovesMoneyOrQuota {
		t.Errorf("delivery %+v, moves_money_or_quota %v", v.Delivery, in.MovesMoneyOrQuota)
	}
	if v.Audit.Payloads != registry.PayloadSealed || v.Audit.Retention.Std() != 90*24*time.Hour {
		t.Errorf("audit %+v, want sealed for 90 days", v.Audit)
	}
	if !in.Interactive {
		t.Error("int_aadhaar_okyc is not interactive")
	}
	target := v.Targets[0]
	for op, endpoint := range map[string]string{"send_otp": "cashfree-sid.okyc_otp@1", "verify_otp": "cashfree-sid.okyc_verify@1"} {
		if tf := snap.Transformers[target.Pair(op).Request]; tf == nil || tf.VendorEndpoint != endpoint {
			t.Errorf("%s: request transformer %+v, want endpoint %s", op, tf, endpoint)
		}
	}
	// HUB-352: PAN is a single-operation sync lookup on the same vendor and account, also sealed.
	pan := snap.Integrations["int_pan_verify"]
	if pan == nil {
		t.Fatal("int_pan_verify missing")
	}
	pv := pan.Active()
	if pv.Delivery.Mode != registry.ModeSync || !pan.MovesMoneyOrQuota || pv.Audit.Payloads != registry.PayloadSealed || pv.Audit.Retention.Std() != 90*24*time.Hour {
		t.Errorf("int_pan_verify: delivery %+v audit %+v moves_money_or_quota %v", pv.Delivery, pv.Audit, pan.MovesMoneyOrQuota)
	}
	if tf := snap.Transformers[pv.Targets[0].Pair("default").Request]; tf == nil || tf.VendorEndpoint != "cashfree-sid.pan_verify@1" {
		t.Errorf("int_pan_verify request transformer %+v", tf)
	}
	vendor := snap.Vendors["cashfree-sid"]
	if vendor.Regions["sandbox"] != "https://sandbox.cashfree.com/verification" || !strings.HasPrefix(vendor.Regions["mock"], "http://127.0.0.1:") {
		t.Errorf("regions %v", vendor.Regions)
	}
	if vendor.Defaults.HealthProbe == nil || vendor.Defaults.HealthProbe.Endpoint != "okyc_otp@1" {
		t.Errorf("health probe %+v", vendor.Defaults.HealthProbe)
	}
}

// TestExampleKYCScenarioFiles: every mockvendor scenario file parses, and each route matches a
// path the vendor definition declares under the mock region (/verification + endpoint path), so
// the emulation and the config cannot drift apart.
func TestExampleKYCScenarioFiles(t *testing.T) {
	snap := overlaySnapshot(t, kycExampleDir)
	vendor := snap.Vendors["cashfree-sid"]
	var paths []string
	for _, ep := range vendor.Endpoints {
		paths = append(paths, "/verification"+ep.Path)
	}
	files, err := filepath.Glob(filepath.Join(kycExampleDir, "mockvendor", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no scenario files: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		s := loadKYCScenario(t, name)
		if s.Description == "" {
			t.Errorf("%s: no description", name)
		}
		for id, r := range s.Routes {
			if r.Match.Method != "POST" || !slices.Contains(paths, r.Match.Path) {
				t.Errorf("%s/%s: %s %s is not a declared endpoint (%v)", name, id, r.Match.Method, r.Match.Path, paths)
			}
		}
	}
}
