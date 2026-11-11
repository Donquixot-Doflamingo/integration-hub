package adminapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// policyTree is authz-policies/integration-hub of the repository.
func policyTree(t testing.TB) string {
	t.Helper()
	dir, err := contract.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(filepath.Dir(dir), "authz-policies", "integration-hub")
}

// TestCatalogueMatchesRego: the role and permission catalogue the Go code knows (machine roles in
// isfc.go, the test fixtures) is the one the generator writes to catalogue.json and the seed script
// creates in ISFC (HUB-477). A permission added in one place and not the others fails here.
func TestCatalogueMatchesRego(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(policyTree(t), "catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Roles []struct {
			Name        string   `json:"name"`
			HubRole     string   `json:"hub_role"`
			Permissions []string `json:"permissions"`
		} `json:"roles"`
		HumanOnly []string `json:"human_only_operations"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"admin":         slices.Concat(hubReadPermissions, hubWritePermissions, hubAdminPermissions),
		"editor":        slices.Concat(hubReadPermissions, hubWritePermissions),
		"viewer":        hubReadPermissions,
		"tenant_admin":  hubTenantSetupBundle,
		"tenant_viewer": hubTenantReadBundle,
	}
	if len(cat.Roles) != len(want) {
		t.Fatalf("catalogue has %d roles, want %d", len(cat.Roles), len(want))
	}
	for _, r := range cat.Roles {
		if got, ok := want[r.HubRole]; !ok || !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(r.Permissions))) {
			t.Errorf("role %s (%s): catalogue %v, Go %v", r.Name, r.HubRole, r.Permissions, got)
		}
		if got := isfcRoleOf(Role(r.HubRole)); got.name != r.Name {
			t.Errorf("test fixture names %s as %s, catalogue as %s", r.HubRole, got.name, r.Name)
		}
	}
	var human []string
	for op := range machineRefused {
		human = append(human, op)
	}
	if !slices.Equal(slices.Sorted(slices.Values(human)), slices.Sorted(slices.Values(cat.HumanOnly))) {
		t.Errorf("human-only operations: Go %v, catalogue %v", human, cat.HumanOnly)
	}
}
