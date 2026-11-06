package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// devSources is backend/config plus the repo-root contracts/ directory.
func devSources(t testing.TB) Sources {
	t.Helper()
	return Sources{Config: os.DirFS("../../config"), Contracts: os.DirFS("../../../contracts")}
}

// TestDevConfigBuilds is the HUB-046 acceptance: the dev registry loads and validates with zero
// problems. Run it after editing backend/config.
func TestDevConfigBuilds(t *testing.T) {
	s, err := Build(devSources(t), time.Now())
	if err != nil {
		t.Fatalf("dev config: %v", err)
	}
	for _, id := range []string{"int_evt_brandA", "int_evt_brandB", "int_evt_brandB_min"} {
		if in, ok := s.Integrations[id]; !ok || in.Active() == nil {
			t.Errorf("integration %s missing or without active version", id)
		}
	}
	for _, id := range []string{"ct-brandA-in", "ct-brandB-us"} {
		a := s.Accounts[id]
		if a == nil || !strings.HasPrefix(a.CredentialsRef, "localfile://") {
			t.Errorf("account %s must reference the local encrypted credential file, got %+v", id, a)
		}
	}
	// The documented dev key (config/README.md) resolves to orders-service.
	sum := sha256.Sum256([]byte("hk_test_dev_orders_service_local_only"))
	if k, ok := s.KeyByHash("sha256:" + hex.EncodeToString(sum[:])); !ok || k.Tenant.Tenant != "orders-service" {
		t.Error("documented dev key does not resolve to orders-service")
	}
}

// Capability schemas live only in contracts/ (ADR-0004, TECH_SPEC §8.2).
func TestNoSchemaCopiesUnderBackend(t *testing.T) {
	err := filepath.WalkDir("../../config", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".json") && !strings.Contains(p, "/fixtures/") {
			t.Errorf("%s: capability schemas must stay in contracts/, not backend/config", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestLoadOverlayConfigBuilds checks the M8 load-test registry (HUB-235): deploy/load/run.sh
// copies backend/config and then deploy/load/config on top of it, so the overlay must build
// with zero problems and carry the load integrations in the modes the scenarios rely on.
func TestLoadOverlayConfigBuilds(t *testing.T) {
	dir := t.TempDir()
	for _, src := range []string{"../../config", "../../../deploy/load/config"} {
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			dst := filepath.Join(dir, rel)
			if d.IsDir() {
				return os.MkdirAll(dst, 0o750)
			}
			b, err := os.ReadFile(p) //nolint:gosec // G304: repo config files
			if err != nil {
				return err
			}
			return os.WriteFile(dst, b, 0o600)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Build(Sources{Config: os.DirFS(dir), Contracts: os.DirFS("../../../contracts")}, time.Now())
	if err != nil {
		t.Fatalf("load overlay config: %v", err)
	}
	for id, mode := range map[string]Mode{"int_load_sync": ModeSync, "int_load_async": ModeAsync, "int_load_cb": ModeAsyncCallback} {
		in, ok := s.Integrations[id]
		if !ok || in.Active() == nil {
			t.Fatalf("integration %s missing or without active version", id)
		}
		if got := in.Active().Delivery.Mode; got != mode {
			t.Errorf("%s: mode %s, want %s", id, got, mode)
		}
	}
	sum := sha256.Sum256([]byte("hk_test_load_tenant_local_only"))
	if k, ok := s.KeyByHash("sha256:" + hex.EncodeToString(sum[:])); !ok || k.Tenant.Tenant != "load-tenant" {
		t.Error("documented load key does not resolve to load-tenant")
	}
}
