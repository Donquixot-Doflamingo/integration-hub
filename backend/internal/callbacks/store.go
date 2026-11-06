package callbacks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// delivery is one claimed callback_deliveries row.
type delivery struct {
	ID                 string
	EventID            string
	EventType          string
	RequestID          string
	TenantID           string
	IntegrationID      string
	IntegrationVersion int
	EndpointID         string
	Subscription       int
	Payload            []byte
	Attempts           int    // including the attempt this claim is for
	Traceparent        string // the terminal transition's span ("" without one)
}

// outcome is the recorded result of one attempt.
type outcome struct {
	Status     string // delivered | retry_scheduled | dead_lettered
	HTTPStatus int    // 0 = no response
	Error      string // error class, "" when delivered
	RetryIn    time.Duration
}

// store is the dispatcher's view of hub.callback_deliveries (pgStore in production).
type store interface {
	// claim marks up to n due (or abandoned in-flight) rows in_flight for worker, counting the
	// attempt, with a visibility timeout.
	claim(ctx context.Context, worker string, n int, visibility time.Duration) ([]delivery, error)
	// record applies o if the row is still in_flight and claimed by worker (guarded
	// transition). false: another dispatcher reclaimed it; nothing was changed.
	record(ctx context.Context, id, worker string, o outcome) (bool, error)
	// sample returns the age of the oldest due row and the DLQ size.
	sample(ctx context.Context) (oldest time.Duration, dlq int64, err error)
}

type pgStore struct{ pool *pgxpool.Pool }

// claimDue takes due rows and rows whose claim expired (dispatcher died mid-attempt), each with
// its own partial index, skipping rows other dispatchers hold. The CTEs are MATERIALIZED so the
// LIMIT holds: an inlined LIMIT ... SKIP LOCKED subquery may be re-run and update more rows.
const claimDue = `
WITH due AS MATERIALIZED (
    SELECT delivery_id FROM hub.callback_deliveries
    WHERE status IN ('pending', 'retry_scheduled') AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT $2
    FOR UPDATE SKIP LOCKED
), abandoned AS MATERIALIZED (
    SELECT delivery_id FROM hub.callback_deliveries
    WHERE status = 'in_flight' AND claimed_until < now()
    ORDER BY claimed_until
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE hub.callback_deliveries d
SET status = 'in_flight', claimed_by = $1, claimed_until = now() + $3 * interval '1 millisecond',
    attempts = d.attempts + 1, updated_at = now()
WHERE d.delivery_id IN (SELECT delivery_id FROM due UNION SELECT delivery_id FROM abandoned)
RETURNING d.delivery_id, d.event_id, d.event_type, d.request_id, d.tenant_id, d.integration_id,
    d.integration_version, d.endpoint_id, d.subscription, d.payload, d.attempts, coalesce(d.traceparent, '')`

func (s pgStore) claim(ctx context.Context, worker string, n int, visibility time.Duration) ([]delivery, error) {
	rows, err := s.pool.Query(ctx, claimDue, worker, n, visibility.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("callbacks: claim: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (delivery, error) {
		var d delivery
		err := r.Scan(&d.ID, &d.EventID, &d.EventType, &d.RequestID, &d.TenantID, &d.IntegrationID,
			&d.IntegrationVersion, &d.EndpointID, &d.Subscription, &d.Payload, &d.Attempts, &d.Traceparent)
		return d, err
	})
	if err != nil {
		return nil, fmt.Errorf("callbacks: claim: %w", err)
	}
	return out, nil
}

const recordOutcome = `
UPDATE hub.callback_deliveries
SET status = $3,
    next_attempt_at = CASE WHEN $3 = 'retry_scheduled' THEN now() + $4 * interval '1 millisecond' ELSE next_attempt_at END,
    delivered_at = CASE WHEN $3 = 'delivered' THEN now() END,
    last_http_status = $5, last_error = $6,
    claimed_by = NULL, claimed_until = NULL, updated_at = now()
WHERE delivery_id = $1 AND status = 'in_flight' AND claimed_by = $2`

func (s pgStore) record(ctx context.Context, id, worker string, o outcome) (bool, error) {
	tag, err := s.pool.Exec(ctx, recordOutcome, id, worker, o.Status, o.RetryIn.Milliseconds(), nullable(o.HTTPStatus), nullable(o.Error))
	if err != nil {
		return false, fmt.Errorf("callbacks: record %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

const sampleQueue = `
SELECT
    coalesce(extract(epoch FROM now() - (SELECT min(next_attempt_at) FROM hub.callback_deliveries
        WHERE status IN ('pending', 'retry_scheduled') AND next_attempt_at <= now())), 0)::float8,
    (SELECT count(*) FROM hub.callback_deliveries WHERE status = 'dead_lettered')`

func (s pgStore) sample(ctx context.Context) (time.Duration, int64, error) {
	var secs float64
	var dlq int64
	if err := s.pool.QueryRow(ctx, sampleQueue).Scan(&secs, &dlq); err != nil {
		return 0, 0, fmt.Errorf("callbacks: sample: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), dlq, nil
}

func nullable[T comparable](v T) any {
	var zero T
	if v == zero {
		return nil
	}
	return v
}

var errNoPool = errors.New("callbacks: NewDispatcher needs a Postgres pool")
