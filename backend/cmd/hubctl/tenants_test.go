package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

const (
	linkUUID1 = "3f1c2a4e-0000-4000-8000-000000000001"
	linkUUID2 = "3f1c2a4e-0000-4000-8000-000000000002"
)

const legacyTenant = `# Legacy tenant, created before ISFC.
tenant: orders-service
display_name: Orders Service
api_keys: []
rate_limit: { rps: 100, burst: 200 } # keep this comment
`

func linkedTenant(uuid string) string {
	return legacyTenant + "isfc:\n  tenant_id: " + uuid + "\n  key: orders\n  name: Orders\n  status: INACTIVE\n  linked: true\n"
}

func tree(files map[string]string) map[string][]byte {
	m := map[string][]byte{}
	for p, s := range files {
		m[p] = []byte(s)
	}
	return m
}

func TestLinkTenant(t *testing.T) {
	other := "tenant: billing\napi_keys: []\nrate_limit: { rps: 1, burst: 1 }\nisfc:\n  tenant_id: " + linkUUID2 + "\n  key: billing\n  name: Billing\n  status: ACTIVE\n  linked: true\n"
	tests := []struct {
		name    string
		files   map[string]string
		hub     string
		uuid    string
		opt     linkOptions
		wantErr string
		check   func(t *testing.T, out string, cfg *registry.Config)
	}{
		{
			name: "links a legacy tenant, keeping comments", hub: "orders-service", uuid: strings.ToUpper(linkUUID1),
			files: map[string]string{"tenants/orders-service.yaml": legacyTenant},
			check: func(t *testing.T, out string, cfg *registry.Config) {
				l := cfg.Tenants["orders-service"].ISFC
				if l == nil || l.TenantID != linkUUID1 || l.Key != "orders-service" || l.Name != "Orders Service" || l.Status != "ACTIVE" || l.Linked {
					t.Fatalf("isfc block = %+v", l)
				}
				if !strings.Contains(out, "# Legacy tenant, created before ISFC.") || !strings.Contains(out, "# keep this comment") {
					t.Fatalf("comments lost:\n%s", out)
				}
			},
		},
		{
			name: "key and name flags seed the copy", hub: "orders-service", uuid: linkUUID1, opt: linkOptions{Key: "orders", Name: "Orders Ltd"},
			files: map[string]string{"tenants/orders-service.yaml": legacyTenant},
			check: func(t *testing.T, _ string, cfg *registry.Config) {
				if l := cfg.Tenants["orders-service"].ISFC; l.Key != "orders" || l.Name != "Orders Ltd" {
					t.Fatalf("isfc block = %+v", l)
				}
			},
		},
		{
			name: "same UUID again is a no-op", hub: "orders-service", uuid: linkUUID1,
			files: map[string]string{"tenants/orders-service.yaml": linkedTenant(linkUUID1)},
			check: func(t *testing.T, out string, _ *registry.Config) {
				if out != linkedTenant(linkUUID1) {
					t.Fatalf("file changed:\n%s", out)
				}
			},
		},
		{
			name: "relink needs force", hub: "orders-service", uuid: linkUUID1,
			files:   map[string]string{"tenants/orders-service.yaml": linkedTenant(linkUUID2)},
			wantErr: "already linked to ISFC tenant",
		},
		{
			name: "force relinks and resets the copy", hub: "orders-service", uuid: linkUUID1, opt: linkOptions{Force: true},
			files: map[string]string{"tenants/orders-service.yaml": linkedTenant(linkUUID2)},
			check: func(t *testing.T, _ string, cfg *registry.Config) {
				if l := cfg.Tenants["orders-service"].ISFC; l.TenantID != linkUUID1 || l.Status != "ACTIVE" || l.Linked {
					t.Fatalf("isfc block = %+v", l)
				}
			},
		},
		{
			name: "UUID linked to another tenant", hub: "orders-service", uuid: linkUUID2,
			files:   map[string]string{"tenants/orders-service.yaml": legacyTenant, "tenants/billing.yaml": other},
			wantErr: `already linked to Hub tenant "billing"`,
		},
		{name: "not a UUID", hub: "orders-service", uuid: "orders", files: map[string]string{"tenants/orders-service.yaml": legacyTenant}, wantErr: "not an ISFC tenant UUID"},
		{name: "unknown Hub tenant", hub: "nope-team", uuid: linkUUID1, files: map[string]string{"tenants/orders-service.yaml": legacyTenant}, wantErr: `no Hub tenant "nope-team"`},
		{name: "path traversal id", hub: "../x", uuid: linkUUID1, files: map[string]string{"tenants/orders-service.yaml": legacyTenant}, wantErr: "not a Hub tenant id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := tree(tc.files)
			p, data, err := linkTenant(files, tc.hub, tc.uuid, tc.opt)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p != "tenants/"+tc.hub+".yaml" {
				t.Fatalf("path = %s", p)
			}
			files[p] = data
			cfg := loadTree(files)
			if cfg == nil {
				t.Fatalf("result does not load:\n%s", data)
			}
			tc.check(t, string(data), cfg)
		})
	}
}

func TestUnlinkedTenants(t *testing.T) {
	cfg := loadTree(tree(map[string]string{
		"tenants/orders-service.yaml": legacyTenant,
		"tenants/billing.yaml":        "tenant: billing\napi_keys: []\nrate_limit: { rps: 1, burst: 1 }\nisfc:\n  tenant_id: " + linkUUID2 + "\n  key: billing\n  name: Billing\n  status: ACTIVE\n  linked: true\n",
		"tenants/aaa-team.yaml":       "tenant: aaa-team\napi_keys: []\nrate_limit: { rps: 1, burst: 1 }\n",
	}))
	if cfg == nil {
		t.Fatal("tree does not load")
	}
	got := strings.Join(unlinkedTenants(cfg), ",")
	if got != "aaa-team,orders-service" {
		t.Fatalf("unlinked = %q", got)
	}
}

func TestTenantsCmd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tenants"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tenants", "orders-service.yaml"), []byte(legacyTenant), 0o644); err != nil {
		t.Fatal(err)
	}
	run1 := func(args ...string) (int, string, string) {
		var o, e bytes.Buffer
		c := run(append([]string{"tenants", "link", "-config-dir", dir}, args...), &o, &e)
		return c, o.String(), e.String()
	}
	if c, o, _ := run1("--report"); c != 3 || !strings.Contains(o, "unlinked  orders-service") {
		t.Fatalf("report before: %d %s", c, o)
	}
	if c, _, e := run1("orders-service"); c != 2 {
		t.Fatalf("missing uuid: %d %s", c, e)
	}
	if c, o, e := run1("orders-service", linkUUID1); c != 0 || !strings.Contains(o, "linked Hub tenant orders-service") {
		t.Fatalf("link: %d %s %s", c, o, e)
	}
	if c, o, _ := run1("-report"); c != 0 || !strings.Contains(o, "1 linked to ISFC, 0 without") {
		t.Fatalf("report after: %d %s", c, o)
	}
	if c, _, e := run1("orders-service", linkUUID2); c != 1 || !strings.Contains(e, "-force") {
		t.Fatalf("relink without force: %d %s", c, e)
	}
}

// apply must not pretend to push an ISFC link: updateTenant cannot carry it.
func TestApplyRefusesISFCLinkOnlyChange(t *testing.T) {
	live := loadTree(tree(map[string]string{"tenants/orders-service.yaml": legacyTenant}))
	_, data, err := linkTenant(tree(map[string]string{"tenants/orders-service.yaml": legacyTenant}), "orders-service", linkUUID1, linkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	local := loadTree(tree(map[string]string{"tenants/orders-service.yaml": string(data)}))
	acts, refused := planActions([]fileChange{{Path: "tenants/orders-service.yaml", Object: "tenant orders-service", Kind: "change"}}, live, local, "link")
	if len(acts) != 0 || len(refused) != 1 || !strings.Contains(refused[0], "ISFC link") {
		t.Fatalf("acts=%v refused=%v", acts, refused)
	}
}
