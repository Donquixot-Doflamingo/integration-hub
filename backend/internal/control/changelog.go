package control

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// Change is one hub.change_log row (append-only; hub-admin ChangeLogEntry).
type Change struct {
	ChangeID        string
	At              time.Time
	Actor           string
	ActorRoles      []string
	Action          string // operationId-style name: publishIntegrationVersion, rollbackIntegration, ...
	ObjectType      string // contract ObjectType
	ObjectID        string
	Reason          string
	Before, After   any // JSON-encodable, may be nil
	SnapshotVersion int64
	RequestID       string
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RecordChange appends c (minting change_id and at when empty) and returns its id.
func RecordChange(ctx context.Context, db execer, c Change) (string, error) {
	if c.ChangeID == "" {
		c.ChangeID = "chg_" + core.NewULID(time.Now())
	}
	if c.At.IsZero() {
		c.At = time.Now().UTC()
	}
	if c.ActorRoles == nil {
		c.ActorRoles = []string{}
	}
	enc := func(v any) (any, error) {
		if v == nil {
			return nil, nil
		}
		return json.Marshal(v)
	}
	before, err := enc(c.Before)
	if err != nil {
		return "", fmt.Errorf("change log: before: %w", err)
	}
	after, err := enc(c.After)
	if err != nil {
		return "", fmt.Errorf("change log: after: %w", err)
	}
	var snap, rid any
	if c.SnapshotVersion > 0 {
		snap = c.SnapshotVersion
	}
	if c.RequestID != "" {
		rid = c.RequestID
	}
	_, err = db.Exec(ctx, `INSERT INTO hub.change_log (change_id, at, actor, actor_roles, action, object_type, object_id, reason, before, after,
			snapshot_version, request_id) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.ChangeID, c.At, c.Actor, c.ActorRoles, c.Action, c.ObjectType, c.ObjectID, c.Reason, before, after, snap, rid)
	if err != nil {
		return "", fmt.Errorf("change log: %w", err)
	}
	return c.ChangeID, nil
}
