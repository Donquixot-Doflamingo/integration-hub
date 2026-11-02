package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// fakeLive serves /snapshots/latest and its blob from the given config tree (the shapes of
// getLatestSnapshot and getSnapshotBlob) behind a fixed token.
func fakeLive(t *testing.T, dir string) string {
	t.Helper()
	src := registry.Sources{Config: os.DirFS(dir), Contracts: os.DirFS("../../../contracts")}
	snap, err := registry.Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := registry.Pack(src, snap)
	if err != nil {
		t.Fatal(err)
	}
	return fakeAdmin(t, map[string]any{
		"GET /admin/v1/snapshots/latest": map[string]any{"version": 7, "checksum": snap.Checksum, "author": "t", "created_at": "2026-11-02T00:00:00Z", "blob_url": "/snapshots/7/blob"},
		"GET /admin/v1/snapshots/7/blob": blob, // raw gzip: hubctl also accepts it undecoded
	})
}

func copyDir(t *testing.T, from string) string {
	t.Helper()
	files, err := readTree(from)
	if err != nil {
		t.Fatal(err)
	}
	to := t.TempDir()
	if err := writeTree(to, files, false); err != nil {
		t.Fatal(err)
	}
	return to
}

func TestExportThenPlanShowsNoChanges(t *testing.T) {
	url := fakeLive(t, "../../config")
	t.Setenv("HUB_ADMIN_TOKEN", "tok")
	out := filepath.Join(t.TempDir(), "exported")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export", "-admin-url", url, "-out", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("export %d: %s", code, stderr.String())
	}
	if code := run([]string{"export", "-admin-url", url, "-out", out}, &stdout, &stderr); code != 2 && code != 1 {
		t.Fatalf("export into a non-empty dir without -force: %d", code)
	}
	stdout.Reset()
	if code := run([]string{"plan", "-admin-url", url, "-config-dir", out}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "No changes") {
		t.Fatalf("plan after export %d: %s %s", code, stdout.String(), stderr.String())
	}
	// The tree holds credential references only: no key file, no secrets file, no value.
	files, _ := readTree(out)
	for p, b := range files {
		if strings.HasSuffix(p, ".enc") || strings.HasSuffix(p, ".key") || strings.Contains(string(b), "fake-pass") {
			t.Fatalf("export wrote secret material: %s", p)
		}
	}
}

func TestPlanShowsPathLevelDiffs(t *testing.T) {
	url := fakeLive(t, "../../config")
	t.Setenv("HUB_ADMIN_TOKEN", "tok")
	dir := copyDir(t, "../../config")
	edit := func(p, old, repl string) {
		b, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil || !strings.Contains(string(b), old) {
			t.Fatalf("%s has no %q", p, old)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(strings.Replace(string(b), old, repl, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit("accounts/ct-brandA-in.yaml", "rps: 50", "rps: 80")
	edit("integrations/int_evt_brandA/integration.yaml", "active_version: 2", "active_version: 1")
	if err := os.WriteFile(filepath.Join(dir, "tenants/billing.yaml"), []byte("tenant: billing\napi_keys: []\nrate_limit: { rps: 5, burst: 10 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "registrar/reg_orders_cb.yaml")); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"plan", "-admin-url", url, "-config-dir", dir}, &stdout, &stderr)
	got := stdout.String()
	for _, want := range []string{
		"~ account ct-brandA-in (accounts/ct-brandA-in.yaml)\n    ~ quota.rps: 50 → 80",
		"~ integration int_evt_brandA (integrations/int_evt_brandA/integration.yaml)\n    ~ active_version: 2 → 1",
		"+ tenant billing (tenants/billing.yaml)",
		"- registrar endpoint reg_orders_cb (registrar/reg_orders_cb.yaml)",
		"1 to add, 2 to change, 1 to remove",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan lacks %q:\n%s", want, got)
		}
	}
	if code != 3 {
		t.Fatalf("plan with changes exit %d: %s", code, stderr.String())
	}
}
