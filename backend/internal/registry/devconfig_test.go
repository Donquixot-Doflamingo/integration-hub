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
