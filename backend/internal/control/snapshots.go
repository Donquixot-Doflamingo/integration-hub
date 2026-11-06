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

// Publish records snap (built from src) as the next archive version. If the latest archived
// snapshot already has the same checksum nothing is written and published is false.
func (a *Archive) Publish(ctx context.Context, src registry.Sources, snap *registry.Snapshot, author, reason string) (version int64, published bool, err error) {
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
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return 0, false, fmt.Errorf("archive: latest: %w", err)
	case latest.Checksum == snap.Checksum:
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
	if _, err := q.InsertSnapshot(ctx, db.InsertSnapshotParams{
		Version: version, Checksum: snap.Checksum, BlobRef: ref, Author: author, Reason: reason,
	}); err != nil {
		return 0, false, fmt.Errorf("archive: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("archive: commit: %w", err)
	}
	return version, true, nil
}

// ErrNoSnapshot means the archive is empty.
var ErrNoSnapshot = errors.New("no archived snapshot")

// Latest loads, rebuilds and verifies the newest archived snapshot. A blob that does not
// rebuild to its recorded checksum is refused (registry.ErrCorruptBlob).
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
		return nil, registry.Sources{}, 0, fmt.Errorf("archive: read blob v%d: %w", row.Version, err)
	}
	snap, src, err := registry.Unpack(data, row.Checksum, now)
	if err != nil {
		return nil, registry.Sources{}, 0, fmt.Errorf("archive: snapshot v%d: %w", row.Version, err)
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
