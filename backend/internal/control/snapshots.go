package control

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/db"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// SnapshotRetention is how long published snapshots are kept (DESIGN §8.2).
const SnapshotRetention = 90 * 24 * time.Hour

// BlobStore keeps snapshot blobs. Phase 1 uses a local directory; object storage implements
// the same interface later (TECH_SPEC §7).
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte) (ref string, err error)
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
}

// OpenBlobStore opens a blob store from a URL. Only file:// is supported in phase 1.
func OpenBlobStore(rawURL string) (BlobStore, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "file" {
		return nil, fmt.Errorf("blob store %q: only file:// URLs are supported", rawURL)
	}
	root := u.Host + u.Path // file://./.data/blobs → ./.data/blobs; file:///var/hub → /var/hub
	if root == "" {
		return nil, fmt.Errorf("blob store %q: empty path", rawURL)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return DirBlobStore{Root: abs}, nil
}

// DirBlobStore stores blobs as files under Root. Refs are file:// URLs of absolute paths.
type DirBlobStore struct{ Root string }

func (d DirBlobStore) path(ref string) (string, error) {
	p, ok := strings.CutPrefix(ref, "file://")
	clean := filepath.Clean(p)
	if !ok || !strings.HasPrefix(clean, d.Root+string(filepath.Separator)) {
		return "", fmt.Errorf("blob ref %q is outside %s", ref, d.Root)
	}
	return clean, nil
}

// Put writes data under key atomically (temp file + rename).
func (d DirBlobStore) Put(_ context.Context, key string, data []byte) (string, error) {
	target := filepath.Join(d.Root, filepath.Clean("/"+key))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return "file://" + target, nil
}

// Get reads a blob.
func (d DirBlobStore) Get(_ context.Context, ref string) ([]byte, error) {
	p, err := d.path(ref)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p) // #nosec G304 -- path is confined to Root above
}

// Delete removes a blob; a missing blob is not an error.
func (d DirBlobStore) Delete(_ context.Context, ref string) error {
	p, err := d.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Archive records published snapshots in hub.config_snapshots with their blobs, and loads the
// latest one back (boot without the control plane, DESIGN §17).
type Archive struct {
	pool  *pgxpool.Pool
	blobs BlobStore
}

// NewArchive returns an archive over pool and blobs.
func NewArchive(pool *pgxpool.Pool, blobs BlobStore) *Archive {
	return &Archive{pool: pool, blobs: blobs}
}

// archiveLockKey serialises publishers (pg_advisory_xact_lock).
const archiveLockKey = 0x4855425f534e4150 // "HUB_SNAP"

// PublishOptions hooks into the archive transaction (HUB-451).
type PublishOptions struct {
	// Guard runs under the archive lock, before anything is written, with the newest archived
	// snapshot (exists false when the archive is empty). An error refuses the publish: callers
	// that decided on an earlier read (config-directory archive, legacy upgrade) re-check here.
	Guard func(latest Meta, exists bool) error
	// InTx runs in the same transaction (published false: nothing was written and version is the
	// unchanged latest). An error rolls the snapshot back, so the change log and the archive
	// never disagree.
	InTx func(ctx context.Context, tx pgx.Tx, version int64, published bool) error
}

// Publish records snap (built from src) as the next archive version. If the latest archived
// snapshot already has the same checksum nothing is written and published is false.
func (a *Archive) Publish(ctx context.Context, src registry.Sources, snap *registry.Snapshot, author, reason string) (version int64, published bool, err error) {
	return a.PublishWith(ctx, src, snap, author, reason, PublishOptions{})
}

// PublishWith is Publish with a Guard and an in-transaction hook.
func (a *Archive) PublishWith(ctx context.Context, src registry.Sources, snap *registry.Snapshot, author, reason string, opt PublishOptions) (version int64, published bool, err error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("archive: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", int64(archiveLockKey)); err != nil {
		return 0, false, fmt.Errorf("archive: lock: %w", err)
	}
	q := db.New(tx)
	latest, err := q.LatestSnapshot(ctx)
	exists := true
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		exists = false
	case err != nil:
		return 0, false, fmt.Errorf("archive: latest: %w", err)
	}
	if opt.Guard != nil {
		if err := opt.Guard(Meta{Version: latest.Version, Checksum: latest.Checksum, Author: latest.Author}, exists); err != nil {
			return 0, false, err
		}
	}
	if exists && latest.Checksum == snap.Checksum {
		if opt.InTx != nil {
			if err := opt.InTx(ctx, tx, latest.Version, false); err != nil {
				return 0, false, err
			}
			if err := tx.Commit(ctx); err != nil {
				return 0, false, fmt.Errorf("archive: commit: %w", err)
			}
		}
		return latest.Version, false, nil
	}
	version = latest.Version + 1
	data, err := registry.Pack(src, snap)
	if err != nil {
		return 0, false, err
	}
	ref, err := a.blobs.Put(ctx, fmt.Sprintf("snapshots/%d.json.gz", version), data)
	if err != nil {
		return 0, false, fmt.Errorf("archive: store blob: %w", err)
	}
	committed := false
	defer func() {
		if !committed { // the blob of a rolled-back version must not outlive it
			_ = a.blobs.Delete(context.WithoutCancel(ctx), ref)
		}
	}()
	if _, err := q.InsertSnapshot(ctx, db.InsertSnapshotParams{
		Version: version, Checksum: snap.Checksum, BlobRef: ref, Author: author, Reason: reason,
	}); err != nil {
		return 0, false, fmt.Errorf("archive: insert: %w", err)
	}
	if opt.InTx != nil {
		if err := opt.InTx(ctx, tx, version, true); err != nil {
			return 0, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("archive: commit: %w", err)
	}
	committed = true
	return version, true, nil
}

// ErrNoSnapshot means the archive is empty.
var ErrNoSnapshot = errors.New("no archived snapshot")

// SnapshotLoadError: the newest archived snapshot cannot be loaded (blob missing or unreadable,
// corrupt, or no longer valid). Every admin read and write starts from it, so the admin answers
// with Reason instead of a bare internal error.
type SnapshotLoadError struct {
	Version int64
	Cause   error
}

func (e *SnapshotLoadError) Error() string {
	return fmt.Sprintf("archive: snapshot v%d cannot be loaded: %v", e.Version, e.Cause)
}

// Unwrap exposes the cause (registry.ErrCorruptBlob, os.ErrNotExist, ...).
func (e *SnapshotLoadError) Unwrap() error { return e.Cause }

// Reason is the cause in words that are safe to show an operator: no secrets and no file paths
// (the blob store's own errors name them).
func (e *SnapshotLoadError) Reason() string {
	switch {
	case errors.Is(e.Cause, os.ErrNotExist):
		return "its blob is missing from the blob store"
	case errors.Is(e.Cause, registry.ErrCorruptBlob):
		msg := e.Cause.Error()
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		return msg
	}
	return "its blob could not be read from the blob store"
}

// Latest loads, rebuilds and verifies the newest archived snapshot. A blob that does not
// rebuild to its recorded checksum is refused (registry.ErrCorruptBlob). A snapshot that cannot
// be loaded is a *SnapshotLoadError naming its version.
func (a *Archive) Latest(ctx context.Context, now time.Time) (*registry.Snapshot, registry.Sources, int64, error) {
	row, err := db.New(a.pool).LatestSnapshot(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, registry.Sources{}, 0, ErrNoSnapshot
	}
	if err != nil {
		return nil, registry.Sources{}, 0, fmt.Errorf("archive: latest: %w", err)
	}
	data, err := a.blobs.Get(ctx, row.BlobRef)
	if err != nil {
		if ctx.Err() != nil {
			return nil, registry.Sources{}, 0, fmt.Errorf("archive: read blob v%d: %w", row.Version, err)
		}
		return nil, registry.Sources{}, 0, &SnapshotLoadError{Version: row.Version, Cause: err}
	}
	snap, src, err := registry.Unpack(data, row.Checksum, now)
	if err != nil {
		return nil, registry.Sources{}, 0, &SnapshotLoadError{Version: row.Version, Cause: err}
	}
	return snap, src, row.Version, nil
}

// Meta is the newest archived snapshot's header (cheap: no blob read).
type Meta struct {
	Version  int64
	Checksum string
	Author   string
}

// LatestMeta returns the newest archived snapshot's version, checksum and author.
func (a *Archive) LatestMeta(ctx context.Context) (Meta, error) {
	row, err := db.New(a.pool).LatestSnapshot(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return Meta{}, ErrNoSnapshot
	}
	if err != nil {
		return Meta{}, fmt.Errorf("archive: latest: %w", err)
	}
	return Meta{Version: row.Version, Checksum: row.Checksum, Author: row.Author}, nil
}

// SnapshotChannel is the Redis channel the publisher pings with each new archive version.
const SnapshotChannel = "hub:snapshot"

// ConfigAuthorPrefix marks snapshots the admin role archived from its config directory.
const ConfigAuthorPrefix = "hub-admin:"

// Prune deletes snapshots older than olderThan (always keeping the newest) and their blobs.
func (a *Archive) Prune(ctx context.Context, olderThan time.Time) (int, error) {
	rows, err := db.New(a.pool).PruneSnapshots(ctx, pgtype.Timestamptz{Time: olderThan, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("archive: prune: %w", err)
	}
	var errs []error
	for _, r := range rows {
		if err := a.blobs.Delete(ctx, r.BlobRef); err != nil {
			errs = append(errs, fmt.Errorf("delete blob v%d: %w", r.Version, err))
		}
	}
	return len(rows), errors.Join(errs...)
}
