//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/adminapi"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// liveAdmin runs the real admin API on the shared dev database, starting from an empty archive
// (the publisher bootstraps from backend/config), and logs hubctl in as an admin.
func liveAdmin(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, _ := sql.Open("pgx", dsn)
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, q := range []string{`TRUNCATE hub.config_snapshots`, `TRUNCATE hub.integration_drafts`, `TRUNCATE hub.integration_pending`} {
		if _, err := pool.Exec(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	archive := control.NewArchive(pool, control.DirBlobStore{Root: t.TempDir()})
	pub := &control.Publisher{Pool: pool, Archive: archive, Guard: control.RetireGuard(pool),
		Bootstrap: registry.Sources{Config: os.DirFS("../../config"), Contracts: os.DirFS("../../../contracts")}}
	// Archive the bootstrap tree, as `hub admin` does at its first start.
	_, snap, err := pub.Current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := archive.Publish(t.Context(), pub.Bootstrap, snap, control.ConfigAuthorPrefix+"test", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	iss, _ := adminapi.NewDevIssuer(srv.URL)
	api := adminapi.New(adminapi.Deps{Pool: pool, Verifier: iss.Verifier(), Publisher: pub, Archive: archive,
		Snapshot: func() *registry.Snapshot { _, s, _ := pub.Current(context.Background()); return s }})
	mux.Handle("/admin/v1/", api.Handler())
	tok, err := iss.Mint(adminapi.Principal{Subject: "gitops", Email: "gitops@example.com", Roles: []adminapi.Role{adminapi.RoleAdmin}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_ADMIN_TOKEN", tok)
	return srv.URL, pool
}

func hubctl(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func editFile(t *testing.T, p, old, repl string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), old) {
		t.Fatalf("%s has no %q", p, old)
	}
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, repl, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplyIsIdempotent is HUB-211 and the M7 acceptance: export → edit → apply publishes
// through the admin API (one change_log row per change), and a second plan and apply find
// nothing to do.
func TestApplyIsIdempotent(t *testing.T) {
	url, pool := liveAdmin(t)
	dir := filepath.Join(t.TempDir(), "config")
	if code, _, errs := hubctl(t, "export", "-admin-url", url, "-out", dir); code != 0 {
		t.Fatalf("export: %s", errs)
	}
	if code, out, errs := hubctl(t, "plan", "-admin-url", url, "-config-dir", dir, "-contracts-dir", "../../../contracts"); code != 0 {
		t.Fatalf("plan after export %d: %s %s", code, out, errs)
	}
	// Three edits: account quota, integration description, a new active version.
	editFile(t, filepath.Join(dir, "accounts/ct-brandA-in.yaml"), "rps: 50", "rps: 80")
	editFile(t, filepath.Join(dir, "integrations/int_evt_brandA/integration.yaml"), "description: Brand A engagement events", "description: Brand A engagement events (GitOps)")
	v2, _ := os.ReadFile(filepath.Join(dir, "integrations/int_evt_brandA/versions/2.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "integrations/int_evt_brandA/versions/3.yaml"), []byte(strings.Replace(string(v2), "version: 2", "version: 3", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	editFile(t, filepath.Join(dir, "integrations/int_evt_brandA/integration.yaml"), "active_version: 2", "active_version: 3")

	args := []string{"apply", "-admin-url", url, "-config-dir", dir, "-contracts-dir", "../../../contracts", "-reason", "GitOps change 42"}
	code, out, errs := hubctl(t, args...)
	if code != 3 || !strings.Contains(out, "run again with -yes") {
		t.Fatalf("apply without -yes: %d %s %s", code, out, errs)
	}
	before := changeRows(t, pool)
	code, out, errs = hubctl(t, append(args, "-yes")...)
	if code != 0 {
		t.Fatalf("apply: %d %s %s", code, out, errs)
	}
	for _, want := range []string{"update account ct-brandA-in", "update integration int_evt_brandA", "draft int_evt_brandA v3", "publish int_evt_brandA v3", "4 change(s) applied"} {
		if !strings.Contains(out, want) {
			t.Errorf("apply output lacks %q:\n%s", want, out)
		}
	}
	if n := changeRows(t, pool) - before; n != 4 {
		t.Fatalf("%d change_log rows for 4 changes", n)
	}
	code, out, errs = hubctl(t, "plan", "-admin-url", url, "-config-dir", dir, "-contracts-dir", "../../../contracts")
	if code != 0 || !strings.Contains(out, "No changes") {
		t.Fatalf("plan after apply %d:\n%s%s", code, out, errs)
	}
	if code, out, _ := hubctl(t, append(args, "-yes")...); code != 0 || !strings.Contains(out, "No changes") {
		t.Fatalf("second apply %d: %s", code, out)
	}
	if n := changeRows(t, pool) - before; n != 4 {
		t.Fatalf("the second apply wrote %d more change rows", n-4)
	}
}

// TestApplyRefusesAndStopsOnValidation: changes apply cannot express are refused before any
// call; a version that fails publish validation stops with the path-qualified error.
func TestApplyRefusesAndStopsOnValidation(t *testing.T) {
	url, pool := liveAdmin(t)
	dir := filepath.Join(t.TempDir(), "config")
	if code, _, errs := hubctl(t, "export", "-admin-url", url, "-out", dir); code != 0 {
		t.Fatalf("export: %s", errs)
	}
	args := []string{"apply", "-admin-url", url, "-config-dir", dir, "-contracts-dir", "../../../contracts", "-reason", "GitOps change 43", "-yes"}
	before := changeRows(t, pool)

	editFile(t, filepath.Join(dir, "integrations/int_evt_brandA/versions/1.yaml"), "weight: 100", "weight: 90")
	editFile(t, filepath.Join(dir, "accounts/ct-brandA-in.yaml"), "rps: 50", "rps: 70")
	code, _, errs := hubctl(t, args...)
	if code != 1 || !strings.Contains(errs, "nothing applied") || !strings.Contains(errs, "published versions are immutable") {
		t.Fatalf("immutable version edit: %d %s", code, errs)
	}
	if n := changeRows(t, pool) - before; n != 0 {
		t.Fatalf("a refused apply wrote %d change(s)", n)
	}
	editFile(t, filepath.Join(dir, "integrations/int_evt_brandA/versions/1.yaml"), "weight: 90", "weight: 100")

	// A new active version whose weights do not total 100: the draft is refused with its pointer.
	v2, _ := os.ReadFile(filepath.Join(dir, "integrations/int_evt_brandA/versions/2.yaml"))
	bad := strings.Replace(strings.Replace(string(v2), "version: 2", "version: 3", 1), "weight: 100", "weight: 60", 1)
	if err := os.WriteFile(filepath.Join(dir, "integrations/int_evt_brandA/versions/3.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	editFile(t, filepath.Join(dir, "integrations/int_evt_brandA/integration.yaml"), "active_version: 2", "active_version: 3")
	code, out, errs := hubctl(t, args...)
	if code != 1 || !strings.Contains(errs, "failed after") || !strings.Contains(errs, "/targets") {
		t.Fatalf("invalid version: %d %s %s", code, out, errs)
	}
}

func changeRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.change_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
