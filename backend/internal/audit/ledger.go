package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// DB runs statements on a pool or the caller's transaction (TECH_SPEC §4.3: the delivery
// transaction spans requests, outbox and callback rows).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ResultTTL is how long the final result of a queued request stays available for polling.
const ResultTTL = 7 * 24 * time.Hour

// Ledger writes the requests row of queued requests synchronously, inside the delivery
// transaction (DESIGN §13.4: terminal records are not buffered on async paths).
type Ledger struct{}

// partitionWindow bounds created_at so a lookup by request id hits one day partition.
func partitionWindow(requestID string) (time.Time, time.Time, error) {
	t, ok := core.RequestIDTime(requestID)
	if !ok {
		return time.Time{}, time.Time{}, fmt.Errorf("audit: %q is not a request id", requestID)
	}
	return t.Add(-time.Minute), t.Add(time.Minute), nil
}

// Insert writes the requests row of a request that is being queued (accepted, or in_flight for
// a wait-mode immediate attempt).
func (Ledger) Insert(ctx context.Context, db DB, r Record) error {
	_, err := db.Exec(ctx, `INSERT INTO hub.requests (request_id, created_at, tenant_id, integration_id, integration_version,
			capability, operation, mode, status, payload_policy, snapshot_version, idempotency_key, trace_id, attempts, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 0, $2)`,
		r.RequestID, r.CreatedAt, r.TenantID, r.IntegrationID, nullable(r.IntegrationVersion), r.Capability, r.Operation, r.Mode,
		r.Status, r.PayloadPolicy, nullable(int64(r.SnapshotVersion)), nullable(r.IdempotencyKey), nullable(r.TraceID)) //nolint:gosec // snapshot versions stay far below 2^63
	if err != nil {
		return fmt.Errorf("audit: insert request %s: %w", r.RequestID, err)
	}
	return nil
}

// Final is the terminal update of a queued request.
type Final struct {
	Status             core.State // succeeded, dead_lettered or expired
	HTTPStatus         int
	ErrorCode          string
	Attempts           int
	IntegrationVersion int
	HubOverheadMS      float64
	VendorMS           float64
	QueueWaitMS        float64
	At                 time.Time
}

// ErrNotPending means the request row is already terminal (or missing): another actor
// finished it. With the guarded queue transition in the same transaction this cannot happen
// for a consistent pair; it is reported, not ignored.
var ErrNotPending = errors.New("audit: request is not pending")

// Finish moves a pending request (accepted, in_flight, retry_scheduled, parked) to its
// terminal state (compare-and-set on status).
func (Ledger) Finish(ctx context.Context, db DB, requestID string, f Final) error {
	from, to, err := partitionWindow(requestID)
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE hub.requests SET status = $4, http_status = $5, error_code = $6, attempts = $7,
			integration_version = COALESCE($8, integration_version), hub_overhead_ms = $9, vendor_ms = $10, queue_wait_ms = $11,
			completed_at = $12, updated_at = $12
		WHERE created_at BETWEEN $2 AND $3 AND request_id = $1 AND status IN ('accepted', 'in_flight', 'retry_scheduled', 'parked')`,
		requestID, from, to, string(f.Status), nullable(f.HTTPStatus), nullable(f.ErrorCode), f.Attempts, nullable(f.IntegrationVersion),
		f.HubOverheadMS, f.VendorMS, f.QueueWaitMS, f.At)
	if err != nil {
		return fmt.Errorf("audit: finish %s: %w", requestID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotPending
	}
	return nil
}

// Progress records a non-terminal change (retry scheduled, parked, back to accepted on
// replay) with the attempt count so far.
func (Ledger) Progress(ctx context.Context, db DB, requestID string, status core.State, attempts int, at time.Time) error {
	from, to, err := partitionWindow(requestID)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE hub.requests SET status = $4, attempts = $5, updated_at = $6, completed_at = NULL
		WHERE created_at BETWEEN $2 AND $3 AND request_id = $1`, requestID, from, to, string(status), attempts, at)
	if err != nil {
		return fmt.Errorf("audit: progress %s: %w", requestID, err)
	}
	return nil
}

// SaveResult keeps the final canonical result or error for status polling (ResultTTL).
func (Ledger) SaveResult(ctx context.Context, db DB, requestID, tenant string, result json.RawMessage, cerr *core.CanonicalError, at time.Time) error {
	var errJSON []byte
	if cerr != nil {
		b, err := json.Marshal(cerr)
		if err != nil {
			return fmt.Errorf("audit: encode error of %s: %w", requestID, err)
		}
		errJSON = b
	}
	var res any
	if len(result) > 0 {
		res = []byte(result)
	}
	_, err := db.Exec(ctx, `INSERT INTO hub.request_results (request_id, tenant_id, result, error, expires_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (request_id) DO UPDATE SET result = EXCLUDED.result, error = EXCLUDED.error, expires_at = EXCLUDED.expires_at`,
		requestID, tenant, res, errJSON, at.Add(ResultTTL))
	if err != nil {
		return fmt.Errorf("audit: save result %s: %w", requestID, err)
	}
	return nil
}

// PruneResults deletes kept results past their expiry.
func (Ledger) PruneResults(ctx context.Context, db DB, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM hub.request_results WHERE expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("audit: prune results: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RequestStatus is the contract RequestStatus (getRequestStatus).
type RequestStatus struct {
	RequestID      string               `json:"request_id"`
	IntegrationID  string               `json:"integration_id"`
	Capability     string               `json:"capability"`
	Operation      string               `json:"operation"`
	Mode           string               `json:"mode"`
	Status         string               `json:"status"`
	IdempotencyKey *string              `json:"idempotency_key"`
	Attempts       int                  `json:"attempts"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
	CompletedAt    *time.Time           `json:"completed_at"`
	NextAttemptAt  *time.Time           `json:"next_attempt_at"`
	ExpiresAt      *time.Time           `json:"expires_at"`
	Result         json.RawMessage      `json:"result"`
	Error          *core.CanonicalError `json:"error"`
}

// ErrRequestNotFound means no request with that id belongs to the tenant.
var ErrRequestNotFound = errors.New("audit: request not found")

// Status reads a request of tenant: the requests row, the live outbox state while queued, and
// the kept result. Another tenant's request is reported as not found (no probing).
func (Ledger) Status(ctx context.Context, db DB, tenant, requestID string) (RequestStatus, error) {
	from, to, err := partitionWindow(requestID)
	if err != nil {
		return RequestStatus{}, ErrRequestNotFound
	}
	var s RequestStatus
	var outboxStatus *string
	var outboxAttempt *int
	var result, errJSON []byte
	var updated *time.Time
	err = db.QueryRow(ctx, `
		SELECT r.request_id, r.integration_id, r.capability, r.operation, r.mode, r.status, r.idempotency_key, r.attempts,
			r.created_at, r.updated_at, r.completed_at, o.status, o.attempt, o.next_attempt_at, o.expires_at, res.result, res.error
		FROM hub.requests r
		LEFT JOIN hub.outbox o ON o.request_id = r.request_id
		LEFT JOIN hub.request_results res ON res.request_id = r.request_id AND res.expires_at > now()
		WHERE r.created_at BETWEEN $2 AND $3 AND r.request_id = $1 AND r.tenant_id = $4`,
		requestID, from, to, tenant).Scan(&s.RequestID, &s.IntegrationID, &s.Capability, &s.Operation, &s.Mode, &s.Status,
		&s.IdempotencyKey, &s.Attempts, &s.CreatedAt, &updated, &s.CompletedAt, &outboxStatus, &outboxAttempt, &s.NextAttemptAt,
		&s.ExpiresAt, &result, &errJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestStatus{}, ErrRequestNotFound
	}
	if err != nil {
		return RequestStatus{}, fmt.Errorf("audit: status %s: %w", requestID, err)
	}
	s.CreatedAt, s.UpdatedAt = s.CreatedAt.UTC(), s.CreatedAt.UTC()
	if updated != nil {
		s.UpdatedAt = updated.UTC()
	}
	for _, t := range []**time.Time{&s.CompletedAt, &s.NextAttemptAt, &s.ExpiresAt} {
		if *t != nil {
			u := (*t).UTC()
			*t = &u
		}
	}
	if outboxStatus != nil { // still queued: the outbox is the live state
		s.Status = *outboxStatus
		if outboxAttempt != nil && *outboxAttempt > s.Attempts {
			s.Attempts = *outboxAttempt
		}
		if s.Status == string(core.StateInFlight) {
			s.NextAttemptAt = nil
		}
	} else {
		s.NextAttemptAt = nil
	}
	if len(result) > 0 {
		s.Result = result
	}
	if len(errJSON) > 0 {
		var ce core.CanonicalError
		if err := json.Unmarshal(errJSON, &ce); err == nil {
			s.Error = &ce
		}
	}
	return s, nil
}
