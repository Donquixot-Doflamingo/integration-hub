//go:build integration

package adminapi

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/multiop"
)

// HUB-343 (hub-admin 1.2.0, HUB-344): per-operation transformer pairs are accepted, stored,
// rendered and copied by the admin API, and validate/publish report operations_have_transformers
// with contract pointers. Every request and response is validated against hub-admin.v1.yaml.

// overlaid merges overlay over base into one file system.
func overlaid(t *testing.T, base, overlay fs.FS) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	for _, src := range []fs.FS{base, overlay} {
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
	return m
}

// newPerOpRig is an admin rig whose bootstrap tree is the dev config plus the two-operation
// overlay (int_two_step v1: send on the default pair, verify on its own pair).
func newPerOpRig(t *testing.T) *adminRig {
	t.Helper()
	a := newAdminRigFrom(t, registry.Sources{
		Config:    overlaid(t, os.DirFS("../../../config"), multiop.Config()),
		Contracts: overlaid(t, os.DirFS("../../../../contracts"), multiop.Contracts()),
	})
	resetArchive(a)
	return a
}

const (
	sendPairJSON   = `{"request":"TS-SEND-REQ@1","response":"TS-SEND-RES@1"}`
	verifyPairJSON = `{"request":"TS-VERIFY-REQ@1","response":"TS-VERIFY-RES@1"}`
)

// twoStepSpec is a contract IntegrationSpec of int_two_step; operations is the primary target's
// operations member ("" omits it). The failover target always has the correct verify pair (it
// also keeps sv-backup referenced, so no account changes lifecycle state during the test).
func twoStepSpec(operations string) string {
	ops := ""
	if operations != "" {
		ops = `,"operations":` + operations
	}
	return `{"targets":[{"vendor_id":"stepvendor","account_id":"sv-primary","transformer":` + sendPairJSON + ops + `,"weight":100}],` +
		`"failover":[{"vendor_id":"stepvendor","account_id":"sv-backup","transformer":` + sendPairJSON + `,"operations":{"verify":` + verifyPairJSON + `}}],` +
		`"delivery":{"mode":"sync","on_circuit_open":"failover","pending":"re_resolve"},"webhooks":[],"audit":{"payloads":"masked","retention_ms":2592000000}}`
}

// verifyPairOf returns targets[0].operations.verify (and failover[0]'s when present) of a version.
func verifyPairOf(t *testing.T, v map[string]any, member string) map[string]any {
	t.Helper()
	spec, _ := v["spec"].(map[string]any)
	list, _ := spec[member].([]any)
	if len(list) == 0 {
		t.Fatalf("version has no %s: %v", member, v)
	}
	ops, _ := list[0].(map[string]any)["operations"].(map[string]any)
	pair, _ := ops["verify"].(map[string]any)
	if pair["request"] != "TS-VERIFY-REQ@1" || pair["response"] != "TS-VERIFY-RES@1" {
		t.Fatalf("%s[0].operations.verify = %v in %v", member, pair, spec)
	}
	return pair
}

// failedChecks is "check pointer" → message for every failed check of a validation report.
func failedChecks(body map[string]any) map[string]string {
	fails := map[string]string{}
	checks, _ := body["checks"].([]any)
	for _, c := range checks {
		m := c.(map[string]any)
		if m["status"] == "fail" {
			fails[m["check"].(string)+" "+fmt.Sprint(m["pointer"])] = fmt.Sprint(m["message"])
		}
	}
	return fails
}

func TestPerOperationSpecs(t *testing.T) {
	a := newPerOpRig(t)
	const base = "/integrations/int_two_step"

	// The published version renders its per-operation pairs (targets and failover).
	v1 := mustDo(a, RoleViewer, http.MethodGet, base+"/versions/1", "", 200)
	verifyPairOf(t, v1, "targets")
	verifyPairOf(t, v1, "failover")

	// A draft whose single pair would serve verify too (HUB-345) is refused by validate.
	mustDo(a, RoleEditor, http.MethodPost, base+"/versions", `{"spec":`+twoStepSpec("")+`,"reason":"one pair for both"}`, 201)
	for _, tc := range []struct {
		name, operations, want string
	}{
		{"one pair for both operations", "", "operations_have_transformers /targets/0/transformer"},
		{"undeclared operation", `{"verfy":` + verifyPairJSON + `}`, "operations_have_transformers /targets/0/operations/verfy"},
		{"verify pair compiled for send", `{"verify":` + sendPairJSON + `}`, "operations_have_transformers /targets/0/operations/verify"},
	} {
		mustDo(a, RoleEditor, http.MethodPut, base+"/versions/2", `{"spec":`+twoStepSpec(tc.operations)+`,"reason":"`+tc.name+`"}`, 200)
		body := mustDo(a, RoleEditor, http.MethodPost, base+"/versions/2/validate", "", 200)
		if fails := failedChecks(body); body["valid"] != false || fails[tc.want] == "" {
			t.Errorf("%s: want %q, got %v", tc.name, tc.want, fails)
		}
	}
	// Publishing the last broken draft is refused with the same check and pointer.
	resp, _ := a.do(RoleEditor, http.MethodPost, base+"/versions/2/publish", `{"expected_active_version":1,"reason":"broken"}`)
	if resp.Code != 422 || !strings.Contains(resp.Body.String(), `"pointer":"/targets/0/operations/verify"`) ||
		!strings.Contains(resp.Body.String(), `"keyword":"operations_have_transformers"`) {
		t.Fatalf("publish of a broken draft: %d %s", resp.Code, resp.Body)
	}

	// The correct draft validates, publishes and round-trips operations.
	body := mustDo(a, RoleEditor, http.MethodPut, base+"/versions/2", `{"spec":`+twoStepSpec(`{"verify":`+verifyPairJSON+`}`)+`,"reason":"verify on its own endpoint"}`, 200)
	verifyPairOf(t, body["integration_version"].(map[string]any), "targets")
	if body := mustDo(a, RoleEditor, http.MethodPost, base+"/versions/2/validate", "", 200); body["valid"] != true {
		t.Fatalf("valid draft: %v", failedChecks(body))
	}
	mustDo(a, RoleEditor, http.MethodPost, base+"/versions/2/publish", `{"expected_active_version":1,"reason":"verify on its own endpoint"}`, 200)
	verifyPairOf(t, mustDo(a, RoleViewer, http.MethodGet, base+"/versions/2", "", 200), "targets")

	// Copies keep operations: a weight change (v3) and a rollback to v1 (v4).
	mustDo(a, RoleEditor, http.MethodPost, base+"/weights",
		`{"expected_active_version":2,"weights":[{"vendor_id":"stepvendor","account_id":"sv-primary","weight":100}],"reason":"same weights"}`, 200)
	verifyPairOf(t, mustDo(a, RoleViewer, http.MethodGet, base+"/versions/3", "", 200), "targets")
	body = mustDo(a, RoleEditor, http.MethodPost, base+"/rollback", `{"to_version":1,"expected_active_version":3,"reason":"back to v1"}`, 200)
	if body["active_version"] != 4.0 {
		t.Fatalf("rollback: %v", body)
	}
	v4 := mustDo(a, RoleViewer, http.MethodGet, base+"/versions/4", "", 200)
	verifyPairOf(t, v4, "targets")
	verifyPairOf(t, v4, "failover")
	if diff := mustDo(a, RoleViewer, http.MethodGet, base+"/diff?from=1&to=4", "", 200); len(diff["changes"].([]any)) != 0 {
		t.Fatalf("rollback copy differs from v1: %v", diff["changes"])
	}
}
