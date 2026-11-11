//go:build integration

package control_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// smallPool rewires p to a pool of n connections (HUB-451: the edit lock used to hold one
// connection per waiting edit, so n concurrent edits starved the holder).
func smallPool(t *testing.T, p *control.Publisher, n int32) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("HUB_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = n
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	p.Pool = pool
	p.Archive = control.NewArchive(pool, control.DirBlobStore{Root: t.TempDir()})
}

func TestConcurrentEditsBeyondPoolSizeFinish(t *testing.T) {
	p, _ := publisher(t)
	smallPool(t, p, 3)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	const n = 9
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.SetPaused(ctx, "int_evt_brandA", i%2 == 0, "tester", fmt.Sprintf("edit %d", i))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("edit failed (deadlock until the timeout?): %v", err)
		}
	}
	var rows int
	if err := p.Pool.QueryRow(ctx, `SELECT count(*) FROM hub.change_log WHERE actor = 'tester' AND reason LIKE 'edit %'`).Scan(&rows); err != nil || rows < n {
		t.Fatalf("change rows = %d (%v), want at least %d", rows, err, n)
	}
}

// bootstrapBuild is the dev config tree built as is: content different from a paused archive.
func bootstrapBuild(t *testing.T, p *control.Publisher) (registry.Sources, *registry.Snapshot) {
	t.Helper()
	snap, err := registry.Build(p.Bootstrap, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p.Bootstrap, snap
}

func TestSnapshotAndChangeCommitTogether(t *testing.T) {
	p, pool := publisher(t)
	ctx := t.Context()
	if _, err := p.SetPaused(ctx, "int_evt_brandA", true, "alice", "first pause"); err != nil {
		t.Fatal(err)
	}
	meta, err := p.Archive.LatestMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A failing change-log write rolls the snapshot back.
	src, snap := bootstrapBuild(t, p)
	boom := errors.New("change log insert failed")
	_, _, err = p.Archive.PublishWith(ctx, src, snap, "bob", "doomed", control.PublishOptions{
		InTx: func(context.Context, pgx.Tx, int64, bool) error { return boom },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the callback error", err)
	}
	after, _ := p.Archive.LatestMeta(ctx)
	if after.Version != meta.Version {
		t.Fatalf("snapshot v%d survived a failed change-log write (was v%d)", after.Version, meta.Version)
	}

	// A no-op publish (same content) must not attribute the existing snapshot to its author.
	res, err := p.SetPaused(ctx, "int_evt_brandA", true, "alice", "first pause")
	if err != nil {
		t.Fatal(err)
	}
	var snapVersion *int64
	if err := pool.QueryRow(ctx, `SELECT snapshot_version FROM hub.change_log WHERE change_id = $1`, res.ChangeID).Scan(&snapVersion); err != nil {
		t.Fatal(err)
	}
	if snapVersion != nil {
		t.Fatalf("no-op change carries snapshot_version %d, want NULL", *snapVersion)
	}
	if res.ArchiveVersion != meta.Version {
		t.Fatalf("no-op ArchiveVersion = %d, want %d", res.ArchiveVersion, meta.Version)
	}
}

func TestArchiveGuardRunsUnderTheLock(t *testing.T) {
	p, _ := publisher(t)
	ctx := t.Context()
	if _, err := p.SetPaused(ctx, "int_evt_brandA", true, "alice", "ui publish"); err != nil {
		t.Fatal(err)
	}
	src, snap := bootstrapBuild(t, p)
	stale := errors.New("archive moved on")
	_, published, err := p.Archive.PublishWith(ctx, src, snap, "hub-admin:config", "config directory changed", control.PublishOptions{
		Guard: func(latest control.Meta, exists bool) error {
			if exists && latest.Author != "hub-admin:config" {
				return stale
			}
			return nil
		},
	})
	if !errors.Is(err, stale) || published {
		t.Fatalf("published=%v err=%v, want the guard's refusal", published, err)
	}
}

func TestUnloadableSnapshotNamesVersionAndCause(t *testing.T) {
	p, pool := publisher(t)
	ctx := t.Context()
	if _, err := p.SetPaused(ctx, "int_evt_brandA", true, "alice", "pause"); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := pool.QueryRow(ctx, `SELECT blob_ref FROM hub.config_snapshots ORDER BY version DESC LIMIT 1`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(ref[len("file://"):]); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := p.Archive.Latest(ctx, time.Now())
	var le *control.SnapshotLoadError
	if !errors.As(err, &le) || le.Version == 0 {
		t.Fatalf("err = %v, want *SnapshotLoadError with the version", err)
	}
	if got := le.Reason(); got == "" || containsPath(got, ref) {
		t.Fatalf("reason %q must be set and must not leak the blob path", got)
	}
}

func containsPath(s, ref string) bool {
	p := ref[len("file://"):]
	for i := 0; i+len(p) <= len(s); i++ {
		if s[i:i+len(p)] == p {
			return true
		}
	}
	return false
}
