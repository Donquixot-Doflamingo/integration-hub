package roles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if !strings.Contains(metrics, "snapshot_version 1") || strings.Contains(metrics, "snapshot_apply_failures_total{") {
		t.Fatal("snapshot metrics not exported")
	}
}

func TestRoleWithInvalidConfigIsNotReady(t *testing.T) {
	cfg := testConfig(Worker)
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
