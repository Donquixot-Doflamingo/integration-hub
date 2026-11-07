package roles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func readyDetails(t *testing.T, opsAddr string) (int, []string, map[string]string) {
	t.Helper()
	code, body := get(t, "http://"+opsAddr+"/readyz", nil)
	var env struct {
		Error struct {
			Details []struct{ Message string } `json:"details"`
		} `json:"error"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("readyz body %q: %v", body, err)
	}
	var msgs []string
	for _, d := range env.Error.Details {
		msgs = append(msgs, d.Message)
	}
	return code, msgs, env.Checks
}

func TestRoleLoadsDevConfigSnapshot(t *testing.T) {
	addrs := startRole(t, Gateway, Options{})
	_, details, _ := readyDetails(t, addrs["ops"]) // postgres/redis are down in unit tests
	for _, d := range details {
		if strings.HasPrefix(d, "snapshot") {
			t.Fatalf("dev config snapshot not loaded: %v", details)
		}
	}
	_, metrics := get(t, "http://"+addrs["ops"]+"/metrics", nil)
	// No database here, so the content is not known to be archived: version 0 (HUB-401).
	if !strings.Contains(metrics, "snapshot_version 0") || strings.Contains(metrics, "snapshot_apply_failures_total{") {
		t.Fatal("snapshot metrics not exported")
	}
}

func TestRoleWithInvalidConfigIsNotReady(t *testing.T) {
	cfg := testConfig(t, Worker)
	cfg.ConfigDir = t.TempDir()
	if err := writeFile(cfg.ConfigDir+"/accounts/x-bad.yaml", "account: x-bad\nvendor: nowhere\nregion: in\nstate: active\n"); err != nil {
		t.Fatal(err)
	}
	addrs := startRoleWith(t, cfg, Options{})
	code, details, _ := readyDetails(t, addrs["ops"])
	if code != 503 || !contains(details, "snapshot not loaded") {
		t.Fatalf("readyz %d %v, want 503 with snapshot not loaded", code, details)
	}
	_, metrics := get(t, "http://"+addrs["ops"]+"/metrics", nil)
	if !strings.Contains(metrics, `snapshot_apply_failures_total{reason="invalid_config"} 1`) {
		t.Fatal("build failure not counted")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// writeDevCache packs the dev config into dir/snapshot.json.gz, as a role would after a load.
func writeDevCache(t *testing.T, dir string) {
	t.Helper()
	cfg := testConfig(t, Gateway)
	src := registry.Sources{Config: os.DirFS(cfg.ConfigDir), Contracts: os.DirFS(cfg.ContractsDir)}
	snap, err := registry.Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := registry.Pack(src, snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, "snapshot.json.gz"), string(data)); err != nil {
		t.Fatal(err)
	}
}

func brokenConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeFile(dir+"/accounts/x-bad.yaml", "account: x-bad\nvendor: nowhere\nregion: in\nstate: active\n"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDataPlaneBootsFromLocalCacheWhenConfigIsBroken(t *testing.T) {
	cfg := testConfig(t, Gateway)
	cfg.ConfigDir = brokenConfigDir(t)
	writeDevCache(t, cfg.DataDir)
	addrs := startRoleWith(t, cfg, Options{})
	_, details, _ := readyDetails(t, addrs["ops"]) // postgres/redis are down in unit tests
	if contains(details, "snapshot not loaded") {
		t.Fatalf("gateway did not boot from the cache: %v", details)
	}
	_, metrics := get(t, "http://"+addrs["ops"]+"/metrics", nil)
	if !strings.Contains(metrics, `snapshot_origin{origin="cache"} 1`) {
		t.Fatal("snapshot origin not reported as cache")
	}
}

func TestCorruptCacheIsRefused(t *testing.T) {
	cfg := testConfig(t, Worker)
	cfg.ConfigDir = brokenConfigDir(t)
	if err := writeFile(filepath.Join(cfg.DataDir, "snapshot.json.gz"), "not a snapshot"); err != nil {
		t.Fatal(err)
	}
	addrs := startRoleWith(t, cfg, Options{})
	_, details, _ := readyDetails(t, addrs["ops"])
	if !contains(details, "snapshot not loaded") {
		t.Fatalf("corrupt cache was accepted: %v", details)
	}
	_, metrics := get(t, "http://"+addrs["ops"]+"/metrics", nil)
	if !strings.Contains(metrics, `snapshot_apply_failures_total{reason="cache_corrupt"} 1`) {
		t.Fatal("corrupt cache not counted")
	}
}

func TestAdminNeverBootsFromACopy(t *testing.T) {
	cfg := testConfig(t, Admin)
	cfg.ConfigDir = brokenConfigDir(t)
	writeDevCache(t, cfg.DataDir)
	addrs := startRoleWith(t, cfg, Options{})
	_, details, _ := readyDetails(t, addrs["ops"])
	if !contains(details, "snapshot not loaded") {
		t.Fatalf("admin booted from a cached copy: %v", details)
	}
}
