package isfcsync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
)

// RunRecord is one hub.isfc_sync_runs row.
type RunRecord struct {
	ID, Trigger string
	DryRun      bool
	Started     time.Time
	Finished    time.Time
	Result      string // ok | partial | failed
	Error       string
	ChangeID    string
	Changes     map[string]any
}

// Store is the sync's database: the run log, the change log for runs that change no config, the
// the run lock and the caller keys. PGStore is the real one.
type Store interface {
	// TryRunLock takes the cluster-wide lock that makes two reconciles (the leader's loop and an
	// admin "Sync now") run one at a time. ok false: another run holds it.
	TryRunLock(ctx context.Context) (unlock func(), ok bool, err error)
	RecordRun(ctx context.Context, r RunRecord) error
	RecordChange(ctx context.Context, c control.Change) (string, error)
	KeyStore
}

// Advisory lock ids (hub.isfc_*): leader election and the run lock. Arbitrary but fixed.
const (
	lockLeader int64 = 0x48554249_53464301 // "HUBISFC" 01
	lockRun    int64 = 0x48554249_53464302
)

// PGStore implements Store on Postgres.
type PGStore struct{ Pool *pgxpool.Pool }

// TryLock takes a session-level advisory lock on a dedicated connection. The lock is released by
// unlock, or by the database when the connection dies, so a crashed leader frees it.
func (s PGStore) TryLock(ctx context.Context, id int64) (conn *pgxpool.Conn, ok bool, err error) {
	conn, err = s.Pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, id).Scan(&ok); err != nil || !ok {
		conn.Release()
		return nil, false, err
	}
	return conn, true, nil
}

func unlockConn(conn *pgxpool.Conn, id int64) {
	// A fresh context: the caller's may be cancelled (shutdown), and the lock must still go.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, id).Scan(&ok); err != nil || !ok {
		_ = conn.Conn().Close(ctx) // closing the session releases every lock it holds
	}
	conn.Release()
}

// TryRunLock implements Store.
func (s PGStore) TryRunLock(ctx context.Context) (func(), bool, error) {
	conn, ok, err := s.TryLock(ctx, lockRun)
	if err != nil || !ok {
		return nil, false, err
	}
	return func() { unlockConn(conn, lockRun) }, true, nil //nolint:contextcheck // release must work after the caller's context ended
}

// RecordRun implements Store.
func (s PGStore) RecordRun(ctx context.Context, r RunRecord) error {
	changes, err := json.Marshal(r.Changes)
	if err != nil {
		return fmt.Errorf("isfc run: changes: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO hub.isfc_sync_runs (id, trigger, dry_run, started_at, finished_at, result, error, change_id, changes)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9)`,
		r.ID, r.Trigger, r.DryRun, r.Started, r.Finished, r.Result, r.Error, r.ChangeID, changes)
	return err
}

// RecordChange implements Store.
func (s PGStore) RecordChange(ctx context.Context, c control.Change) (string, error) {
	return control.RecordChange(ctx, s.Pool, c)
}
