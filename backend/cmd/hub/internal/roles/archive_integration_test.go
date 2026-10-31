//go:build integration

package roles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func freshArchive(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), os.Getenv("HUB_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	sdb := mustSQL(t)
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "TRUNCATE hub.config_snapshots"); err != nil {
		t.Fatal(err)
	}
	return pool
}

// TestGatewayBootsFromArchiveWhenControlPlaneConfigIsUnavailable: the admin role archives the
// dev snapshot; a gateway whose config source is broken and whose local cache is empty boots
// from the archive (DESIGN §17, HUB-048).
func TestGatewayBootsFromArchiveWhenControlPlaneConfigIsUnavailable(t *testing.T) {
	pool := freshArchive(t)
	blobURL := "file://" + t.TempDir()

	admin := composeConfig(t, Admin)
	admin.BlobURL = blobURL
	adminAddrs := startRoleWith(t, admin, Options{})
	if code, body := get(t, "http://"+adminAddrs["ops"]+"/readyz", nil); code != 200 {
		t.Fatalf("admin not ready: %s", body)
	}
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM hub.config_snapshots").Scan(&n); err != nil || n != 1 {
		t.Fatalf("archived snapshots %d (%v), want 1", n, err)
	}

	gw := composeConfig(t, Gateway)
	gw.BlobURL = blobURL
	gw.ConfigDir = t.TempDir() // a broken config source; the local cache dir is empty
	if err := writeFile(gw.ConfigDir+"/accounts/x-bad.yaml", "account: x-bad\nvendor: nowhere\nregion: in\nstate: active\n"); err != nil {
		t.Fatal(err)
	}
	gwAddrs := startRoleWith(t, gw, Options{})
	code, body := get(t, "http://"+gwAddrs["ops"]+"/readyz", nil)
	if code != 200 || !strings.Contains(body, `"snapshot":"ok"`) {
		t.Fatalf("gateway readyz %d %s", code, body)
	}
	_, metrics := get(t, "http://"+gwAddrs["ops"]+"/metrics", nil)
	if !strings.Contains(metrics, `snapshot_origin{origin="archive"} 1`) {
		t.Fatal("gateway did not boot from the archive")
	}
}

func TestArchiveRefusesCorruptBlobAndPrunes(t *testing.T) {
	pool := freshArchive(t)
	blobs, err := control.OpenBlobStore("file://" + t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := control.NewArchive(pool, blobs)
	cfg := testConfig(t, Admin)
	src := registry.Sources{Config: os.DirFS(cfg.ConfigDir), Contracts: os.DirFS(cfg.ContractsDir)}
	snap, err := registry.Build(src, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	v, published, err := a.Publish(t.Context(), src, snap, "test", "first")
	if err != nil || !published || v != 1 {
		t.Fatalf("publish %d %v %v", v, published, err)
	}
	if v2, published, err := a.Publish(t.Context(), src, snap, "test", "same"); err != nil || published || v2 != 1 {
		t.Fatalf("identical content must not create a version: %d %v %v", v2, published, err)
	}
	if got, _, _, err := a.Latest(t.Context(), time.Now()); err != nil || got.Checksum != snap.Checksum {
		t.Fatalf("latest %v", err)
	}

	// Corrupt the stored blob: Latest must refuse it.
	var ref string
	if err := pool.QueryRow(t.Context(), "SELECT blob_ref FROM hub.config_snapshots WHERE version = 1").Scan(&ref); err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(ref, "file://")
	good, _ := os.ReadFile(path)
	if err := os.WriteFile(path, good[:len(good)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.Latest(t.Context(), time.Now()); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt blob accepted: %v", err)
	}

	// Publish two more versions, age all rows past 90 days: prune removes rows and blobs except the newest.
	for _, rps := range []string{"61", "62"} {
		dir := copyConfig(t, cfg.ConfigDir, rps)
		s2 := registry.Sources{Config: os.DirFS(dir), Contracts: src.Contracts}
		sn, err := registry.Build(s2, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := a.Publish(t.Context(), s2, sn, "test", rps); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(t.Context(), "UPDATE hub.config_snapshots SET created_at = now() - interval '91 days'"); err != nil {
		t.Fatal(err)
	}
	n, err := a.Prune(context.WithoutCancel(t.Context()), time.Now().Add(-control.SnapshotRetention))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d (%v), want 2", n, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("blob of pruned version 1 still exists: %v", err)
	}
	if latest, _, v, err := a.Latest(t.Context(), time.Now()); err != nil || v != 3 || latest == nil {
		t.Fatalf("newest snapshot not kept: v%d %v", v, err)
	}
}

// copyConfig copies dir and sets ct-brandA-in's rps to make the content differ.
func copyConfig(t *testing.T, dir, rps string) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if rel == filepath.Join("accounts", "ct-brandA-in.yaml") {
			b = []byte(strings.Replace(string(b), "rps: 50", "rps: "+rps, 1))
		}
		return writeFile(filepath.Join(dst, rel), string(b))
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
