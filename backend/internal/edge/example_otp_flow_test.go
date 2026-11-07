package edge

import (
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// HUB-342: examples/otp-account-flow is a registry overlay kept out of backend/config
// (production config). The overlay is merged over it in memory, the way sessionOverlay builds its
// test registry. The examples still use the pre-ADR-0023 layout (capability schemas on a
// contracts side) until HUB-435 rewrites them: the merged tree goes through
// registry.UpgradeLegacy, which leaves an already-upgraded tree as it is.

const exampleDir = "../../../examples/otp-account-flow"

// exampleKey is the documented test-only key of the example tenant loan-onboarding
// (examples/otp-account-flow/config/tenants/loan-onboarding.yaml).
const exampleKey = "hk_test_example_loan_onboarding_local_only"

// mergeInto copies every file of src into m under prefix.
func mergeInto(t testing.TB, m fstest.MapFS, src fs.FS) {
	t.Helper()
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(src, p)
		m[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// exampleSources is backend/config + the example config (upgraded, see above). activeOTP picks
// the active version of int_demo_otp (the publish-equivalent).
func exampleSources(t testing.TB, activeOTP int) registry.Sources {
	t.Helper()
	cfg := exampleTree(t, exampleDir)
	const file = "integrations/int_demo_otp/integration.yaml"
	in := string(cfg[file].Data)
	if !strings.Contains(in, "active_version: 1\n") {
		t.Fatalf("%s: want active_version: 1", file)
	}
	cfg[file] = &fstest.MapFile{Data: []byte(strings.Replace(in, "active_version: 1\n", "active_version: "+strconv.Itoa(activeOTP)+"\n", 1))}
	return registry.Sources{Config: cfg}
}

// exampleTree merges the example dir's config over backend/config and upgrades the result when
// the example still holds capabilities (its schemas in dir/contracts).
func exampleTree(t testing.TB, dir string) fstest.MapFS {
	t.Helper()
	cfg, contracts := fstest.MapFS{}, fstest.MapFS{}
	mergeInto(t, cfg, os.DirFS("../../config"))
	mergeOverlay(t, cfg, os.DirFS(dir+"/config"))
	if _, err := os.Stat(dir + "/contracts"); err == nil {
		mergeInto(t, contracts, os.DirFS(dir+"/contracts"))
	}
	up, _, err := registry.UpgradeLegacy(cfg, contracts)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	return up
}

// mergeOverlay merges an example overlay and refuses to overwrite a backend/config or contracts/
// file: the example only adds objects.
func mergeOverlay(t testing.TB, base fstest.MapFS, overlay fs.FS) {
	t.Helper()
	m := fstest.MapFS{}
	mergeInto(t, m, overlay)
	for p, f := range m {
		if _, clash := base[p]; clash {
			t.Fatalf("example overlay would overwrite %s", p)
		}
		base[p] = f
	}
}

// exampleSnapshot builds the example registry; its goldens run as part of the build.
func exampleSnapshot(t testing.TB, activeOTP int) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(exampleSources(t, activeOTP), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestExampleOTPConfigBuilds: the example overlay loads, validates and passes every golden of
// its four transformers (a failing golden refuses the snapshot), in both int_demo_otp versions.
func TestExampleOTPConfigBuilds(t *testing.T) {
	for _, active := range []int{1, 2} {
		snap := exampleSnapshot(t, active)
		for _, id := range []string{"int_demo_otp", "int_demo_bank_verify"} {
			in := snap.Integrations[id]
			if in == nil {
				t.Fatalf("v%d: %s missing", active, id)
			}
			if in.Active().Delivery.Mode != registry.ModeSync || !in.MovesMoneyOrQuota {
				t.Errorf("%s: mode %s, moves_money_or_quota %v", id, in.Active().Delivery.Mode, in.MovesMoneyOrQuota)
			}
		}
		for _, id := range []string{"int_demo_otp", "int_demo_bank_verify"} {
			if !snap.Integrations[id].Interactive {
				t.Errorf("%s is not interactive", id)
			}
		}
		target := snap.Integrations["int_demo_otp"].Active().Targets[0]
		if acct := target.Account; acct != "demo-kyc-"+map[int]string{1: "a", 2: "b"}[active] {
			t.Errorf("v%d targets %s", active, acct)
		}
		// HUB-343: each operation has its own pair, so its own vendor endpoint.
		for op, endpoint := range map[string]string{"send_otp": "demo-kyc.otp_send@1", "verify_otp": "demo-kyc.otp_verify@1"} {
			if tf := snap.Transformers[target.Pair(op).Request]; tf == nil || tf.VendorEndpoint != endpoint {
				t.Errorf("v%d %s: request transformer %+v, want endpoint %s", active, op, tf, endpoint)
			}
		}
	}
}

// TestExampleOTPRefusesOnePairForBothOperations is HUB-345 on the example: without its
// operations entry, verify_otp would run send_otp's transformers, whose samples do not fit
// verify_otp's request schema (ADR-0023 input_schema), and the build refuses that.
func TestExampleOTPRefusesOnePairForBothOperations(t *testing.T) {
	src := exampleSources(t, 1)
	cfg := src.Config.(fstest.MapFS)
	const file = "integrations/int_demo_otp/versions/1.yaml"
	// The first target's per-operation pair, whatever the layout of the file.
	ops := regexp.MustCompile(`(?m)^ +operations:\n +verify_otp: \{ ?request: DEMO-OTP-VERIFY-REQ@1, response: DEMO-OTP-VERIFY-RES@1 ?\}\n`)
	loc := ops.FindIndex(cfg[file].Data)
	if loc == nil {
		t.Fatalf("%s has no verify_otp pair", file)
	}
	doc := string(cfg[file].Data)
	cfg[file] = &fstest.MapFile{Data: []byte(doc[:loc[0]] + doc[loc[1]:])}
	_, err := registry.Build(src, time.Now())
	var ps registry.ConfigErrors
	if !errors.As(err, &ps) || !slices.ContainsFunc(ps, func(p registry.Problem) bool {
		return p.File == file && p.Path == "targets[0].transformer.request" && p.Check == registry.CheckInputSchema && strings.Contains(p.Message, "operation verify_otp")
	}) {
		t.Fatalf("want a targets[0].transformer.request input_schema problem for verify_otp, got %v", err)
	}
}
