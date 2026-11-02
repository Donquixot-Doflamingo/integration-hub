package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SnapshotRow is one archived snapshot with the change that produced it (if any).
type SnapshotRow struct {
	Version   int64
	Checksum  string
	BlobRef   string
	Author    string
	Reason    string
	CreatedAt time.Time
	// ChangeID, Action and ObjectID name the change-log row of the edit that archived this
	// snapshot; empty for snapshots archived from the config directory.
	ChangeID, Action, ObjectID string
}

// snapshotSelect joins each snapshot to the newest change-log row recorded for its version
// (an archive rebuilt from scratch reuses version numbers; the newest row is this archive's).
const snapshotSelect = `SELECT s.version, s.checksum, s.blob_ref, s.author, s.reason, s.created_at,
	coalesce(c.change_id, ''), coalesce(c.action, ''), coalesce(c.object_id, '')
FROM hub.config_snapshots s
LEFT JOIN LATERAL (SELECT change_id, action, object_id FROM hub.change_log c
	WHERE c.snapshot_version = s.version ORDER BY c.at DESC LIMIT 1) c ON true`

func scanSnapshot(row pgx.Row) (SnapshotRow, error) {
	var r SnapshotRow
	err := row.Scan(&r.Version, &r.Checksum, &r.BlobRef, &r.Author, &r.Reason, &r.CreatedAt, &r.ChangeID, &r.Action, &r.ObjectID)
	r.CreatedAt = r.CreatedAt.UTC()
	return r, err
}

// List returns up to limit snapshots older than before (0: from the newest), newest first.
func (a *Archive) List(ctx context.Context, before int64, limit int) ([]SnapshotRow, error) {
	rows, err := a.pool.Query(ctx, snapshotSelect+` WHERE ($1 = 0 OR s.version < $1) ORDER BY s.version DESC LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("archive: list: %w", err)
	}
	defer rows.Close()
	var out []SnapshotRow
	for rows.Next() {
		r, err := scanSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("archive: list: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns snapshot version (ErrNotFound when it is not archived or was pruned).
func (a *Archive) Get(ctx context.Context, version int64) (SnapshotRow, error) {
	r, err := scanSnapshot(a.pool.QueryRow(ctx, snapshotSelect+` WHERE s.version = $1`, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("%w: snapshot %d", ErrNotFound, version)
	}
	if err != nil {
		return r, fmt.Errorf("archive: get: %w", err)
	}
	return r, nil
}

// Newest returns the latest archived snapshot (ErrNoSnapshot when the archive is empty).
func (a *Archive) Newest(ctx context.Context) (SnapshotRow, error) {
	r, err := scanSnapshot(a.pool.QueryRow(ctx, snapshotSelect+` ORDER BY s.version DESC LIMIT 1`))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNoSnapshot
	}
	if err != nil {
		return r, fmt.Errorf("archive: latest: %w", err)
	}
	return r, nil
}

// Previous returns the newest snapshot older than version (ErrNotFound when none).
func (a *Archive) Previous(ctx context.Context, version int64) (SnapshotRow, error) {
	r, err := scanSnapshot(a.pool.QueryRow(ctx, snapshotSelect+` WHERE s.version < $1 ORDER BY s.version DESC LIMIT 1`, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("%w: no snapshot before %d", ErrNotFound, version)
	}
	if err != nil {
		return r, fmt.Errorf("archive: previous: %w", err)
	}
	return r, nil
}

// Blob returns the stored (gzip) blob of a snapshot row.
func (a *Archive) Blob(ctx context.Context, r SnapshotRow) ([]byte, error) {
	b, err := a.blobs.Get(ctx, r.BlobRef)
	if err != nil {
		return nil, fmt.Errorf("archive: read blob v%d: %w", r.Version, err)
	}
	return b, nil
}
