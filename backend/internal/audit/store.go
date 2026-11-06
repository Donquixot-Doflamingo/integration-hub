package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore writes the ledger to Postgres: COPY for events and attempts, an upsert for records
// (async paths update the same request row as it moves through states).
type PGStore struct{ Pool *pgxpool.Pool }

var ledgerTables = []string{"requests", "request_events", "request_attempts"}

// EnsurePartitions creates hub.<table>_pYYYYMMDD for each day in [day, day+days).
func (s PGStore) EnsurePartitions(ctx context.Context, day time.Time, days int) error {
	start := day.UTC().Truncate(24 * time.Hour)
	for i := range days {
		from, to := start.AddDate(0, 0, i), start.AddDate(0, 0, i+1)
		for _, t := range ledgerTables {
			sql := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS hub.%s_p%s PARTITION OF hub.%s FOR VALUES FROM ('%s') TO ('%s')`,
				t, from.Format("20060102"), t, from.Format(time.RFC3339), to.Format(time.RFC3339))
			if _, err := s.Pool.Exec(ctx, sql); err != nil {
				return fmt.Errorf("partition %s %s: %w", t, from.Format(time.DateOnly), err)
			}
		}
	}
	return nil
}

func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

// Write stores b in one transaction.
func (s PGStore) Write(ctx context.Context, b Batch) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if len(b.Events) > 0 {
			rows := make([][]any, len(b.Events))
			for i, e := range b.Events {
				meta := e.Meta
				if meta == nil {
					meta = map[string]any{}
				}
				mj, err := json.Marshal(meta)
				if err != nil {
					return fmt.Errorf("event %s/%d meta: %w", e.RequestID, e.Seq, err)
				}
				state := e.PayloadState
				if state == "" {
					state = PayloadNone
				}
				rows[i] = []any{e.At, e.RequestID, e.Seq, e.Type, nullable(e.AttemptNo), nullable(e.SHA256),
					payloadBytes(e), e.Payload, string(state), nullable(e.PayloadRef), mj}
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"hub", "request_events"},
				[]string{"at", "request_id", "seq", "type", "attempt_no", "payload_sha256", "payload_bytes", "payload", "payload_state", "payload_ref", "meta"},
				pgx.CopyFromRows(rows)); err != nil {
				return fmt.Errorf("copy events: %w", err)
			}
		}
		if len(b.Attempts) > 0 {
			rows := make([][]any, len(b.Attempts))
			for i, a := range b.Attempts {
				rows[i] = []any{a.StartedAt, a.RequestID, a.AttemptNo, a.AccountID, a.EndpointID, nullable(a.IntegrationVersion), nullable(a.Host),
					a.DurationMS, nullable(a.HTTPStatus), nullable(a.ErrorClass)}
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{"hub", "request_attempts"},
				[]string{"started_at", "request_id", "attempt_no", "account_id", "endpoint_id", "integration_version", "host", "duration_ms", "http_status", "error_class"},
				pgx.CopyFromRows(rows)); err != nil {
				return fmt.Errorf("copy attempts: %w", err)
			}
		}
		if len(b.Records) > 0 {
			batch := &pgx.Batch{}
			for _, r := range b.Records {
				batch.Queue(upsertRecord, r.RequestID, r.CreatedAt, r.TenantID, r.IntegrationID, nullable(r.IntegrationVersion),
					r.Capability, r.Operation, r.Mode, r.DryRun, r.Status, r.PayloadPolicy, nullable(int64(r.SnapshotVersion)), // #nosec G115 -- snapshot versions stay far below 2^63
					nullable(r.IdempotencyKey), nullable(r.TraceID), nullable(r.HTTPStatus), nullable(r.ErrorCode), r.Attempts,
					r.HubOverheadMS, r.VendorMS, r.QueueWaitMS, nullable(r.CompletedAt))
			}
			if err := tx.SendBatch(ctx, batch).Close(); err != nil {
				return fmt.Errorf("upsert records: %w", err)
			}
		}
		return nil
	})
}

func payloadBytes(e Event) any {
	if e.SHA256 == "" {
		return nil
	}
	return e.Bytes
}

const upsertRecord = `
INSERT INTO hub.requests (request_id, created_at, tenant_id, integration_id, integration_version, capability,
    operation, mode, dry_run, status, payload_policy, snapshot_version, idempotency_key, trace_id, http_status,
    error_code, attempts, hub_overhead_ms, vendor_ms, queue_wait_ms, completed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
ON CONFLICT (created_at, request_id) DO UPDATE SET
    status = EXCLUDED.status, http_status = EXCLUDED.http_status, error_code = EXCLUDED.error_code,
    attempts = EXCLUDED.attempts, hub_overhead_ms = EXCLUDED.hub_overhead_ms, vendor_ms = EXCLUDED.vendor_ms,
    queue_wait_ms = EXCLUDED.queue_wait_ms, completed_at = EXCLUDED.completed_at`
