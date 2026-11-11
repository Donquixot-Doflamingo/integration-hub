//go:build integration

package adminapi

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/authz"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/opa"
)

// startPolicyOPA serves the real policies of authz-policies/integration-hub (package placeholder
// replaced by "integrationhub", tests left out) with the bundle data of the matrix: the console
// client and the machine keys. It skips when docker is not available.
func startPolicyOPA(t *testing.T) *authz.Client {
	t.Helper()
	src, dst := policyTree(t), t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".rego") || strings.HasSuffix(path, "_test.rego") {
			return err
		}
		b, err := os.ReadFile(path) //nolint:gosec // repository policy file
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil { //nolint:gosec // test temp dir
			return err
		}
		return os.WriteFile(out, []byte(strings.ReplaceAll(string(b), "{__package_name__}", "integrationhub")), 0o644) //nolint:gosec // test temp dir
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []map[string]any
	for key, k := range machineKeys() {
		keys = append(keys, map[string]any{"key": key, "allowed_client_id": k.client, "status": k.status, "roles": []string{}, "permissions": k.perms})
	}
	data, _ := json.Marshal(map[string]any{"allowed_api_keys": keys, "allowed_client_ids": []map[string]any{{"client_id": testConsoleID, "status": "ACTIVE"}}})
	if err := os.MkdirAll(filepath.Join(dst, "integrationhub"), 0o755); err != nil { //nolint:gosec // test temp dir
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "integrationhub", "data.json"), data, 0o644); err != nil { //nolint:gosec // test temp dir
		t.Fatal(err)
	}
	c, err := authz.New(authz.Config{URL: opa.Start(t, dst)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestRBACMatrixRealPolicies runs the ISFC-mode RBAC matrix and the machine matrix with the real
// generated Rego in a real OPA (HUB-475, HUB-479): the Go role matrix, the catalogue and the
// policies must agree on every operation, role and key.
func TestRBACMatrixRealPolicies(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	opaClient := startPolicyOPA(t)
	m, s := isfcMatrixModeWith(t, opaClient)
	t.Run("roles", func(t *testing.T) { runRBACMatrix(t, spec, m) })
	t.Run("machines", func(t *testing.T) { runMachineMatrix(t, spec, m, s) })
}
