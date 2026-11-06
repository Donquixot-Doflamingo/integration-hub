package queue

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

// DB is what the queue runs statements on: a pool, or the caller's transaction, so an outbox
// change commits together with the requests row and callback rows (TECH_SPEC §4.3).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrLost means a guarded transition matched no row: the envelope is not in the expected state
// or not claimed by this worker any more (another actor won, or the claim expired). The caller
// drops its result: no double completion, no double callback (DESIGN §10.9).
var ErrLost = errors.New("queue: envelope not in the expected state or claimed by another worker")

// ErrDuplicate means an envelope with this request id is already queued.
var ErrDuplicate = errors.New("queue: request already queued")

// Pools (DESIGN §17): LLM capabilities run in their own worker pool.
const (
	PoolMessaging = "messaging"
	PoolLLM       = "llm"
)

// PoolFor returns the worker pool of a capability key (Replay derives it the same way in SQL).
func PoolFor(capability string) string {
	if len(capability) >= 4 && capability[:4] == "llm." {
		return PoolLLM
	}
	return PoolMessaging
}

// Claimed is an envelope handed to a worker, with its queue bookkeeping.
type Claimed struct {
	core.Envelope
	Status       core.State // state before the claim (accepted or retry_scheduled; in_flight when reclaimed)
	ClaimedUntil time.Time
	Crashes      int // times a claim on it expired without an outcome (poison detector)
}

// Outbox is the Postgres Queue (DESIGN §11.2) on hub.outbox and hub.dead_letters. Every method
// takes the DB to run on, so callers can compose it in their own transaction.
type Outbox struct {
	// Visibility is how long a claim is exclusive (default 30 s).
	Visibility time.Duration
	Now        func() time.Time
}

func (o *Outbox) visibility() time.Duration {
	if o.Visibility <= 0 {
		return 30 * time.Second
	}
	return o.Visibility
}

func (o *Outbox) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Enqueue inserts env as accepted. It is durable when db commits; "accepted" means committed.
func (o *Outbox) Enqueue(ctx context.Context, db DB, env core.Envelope) error {
	return o.enqueue(ctx, db, env, "", 0)
}

// EnqueueClaimed inserts env as in_flight, claimed by worker for claimFor: the wait-mode
// gateway attempt (TECH_SPEC: claimed_until = now + wait + timeout + 5 s). If the gateway dies,
// the claim expires and a worker reclaims the envelope.
func (o *Outbox) EnqueueClaimed(ctx context.Context, db DB, env core.Envelope, worker string, claimFor time.Duration) error {
	return o.enqueue(ctx, db, env, worker, claimFor)
}

func (o *Outbox) enqueue(ctx context.Context, db DB, env core.Envelope, claimBy string, claimFor time.Duration) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("queue: encode envelope: %w", err)
	}
	status, claimedBy, claimedUntil := core.StateAccepted, any(nil), any(nil)
	if claimBy != "" {
		status, claimedBy, claimedUntil = core.StateInFlight, claimBy, o.now().Add(claimFor)
		env.Attempt = 1 // the gateway's immediate attempt
	}
	_, err = db.Exec(ctx, `INSERT INTO hub.outbox (request_id, status, pool, tenant_id, integration_id, attempt, next_attempt_at,
			expires_at, ordering_key, claimed_by, claimed_until, envelope, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		env.RequestID, string(status), PoolFor(env.Capability), env.Tenant, env.IntegrationID, env.Attempt, env.NextAttemptAt,
		env.ExpiresAt, env.OrderingKey, claimedBy, claimedUntil, body, env.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("queue: enqueue %s: %w", env.RequestID, err)
	}
	return nil
}

// Claim takes up to n due envelopes of pool for worker: accepted or retry_scheduled with
// next_attempt_at ≤ now, plus in_flight ones whose claim expired (a crashed worker; their
// crash count goes up). Rows are locked with SKIP LOCKED, so concurrent claimers never get the
// same envelope; each claim is exclusive for Visibility.
func (o *Outbox) Claim(ctx context.Context, db DB, worker, pool string, n int) ([]Claimed, error) {
	now := o.now()
	rows, err := db.Query(ctx, `
		WITH due AS MATERIALIZED (
			SELECT request_id, status AS prev FROM hub.outbox
			WHERE pool = $2 AND (
				(status IN ('accepted', 'retry_scheduled') AND next_attempt_at <= $4)
				OR (status = 'in_flight' AND claimed_until < $4))
			ORDER BY next_attempt_at
			LIMIT $3
			FOR UPDATE SKIP LOCKED)
		UPDATE hub.outbox o SET status = 'in_flight', claimed_by = $1, claimed_until = $5,
			attempt = o.attempt + 1, crashes = o.crashes + CASE WHEN due.prev = 'in_flight' THEN 1 ELSE 0 END, updated_at = $4
		FROM due WHERE o.request_id = due.request_id
		RETURNING o.request_id, due.prev, o.attempt, o.claimed_until, o.crashes, o.envelope`,
		worker, pool, n, now, now.Add(o.visibility()))
	if err != nil {
		return nil, fmt.Errorf("queue: claim: %w", err)
	}
	defer rows.Close()
	var out []Claimed
	for rows.Next() {
		var c Claimed
		var id, prev string
		var body []byte
		if err := rows.Scan(&id, &prev, &c.Attempt, &c.ClaimedUntil, &c.Crashes, &body); err != nil {
			return nil, fmt.Errorf("queue: claim scan: %w", err)
		}
		attempt := c.Attempt
		if err := json.Unmarshal(body, &c.Envelope); err != nil {
			return nil, fmt.Errorf("queue: decode envelope %s: %w", id, err)
		}
		c.Attempt, c.Status = attempt, core.State(prev) // the row's attempt, not the stored envelope's
		out = append(out, c)
	}
	return out, rows.Err()
}

func guarded(tag pgconn.CommandTag, err error, op, id string) error {
	if err != nil {
		return fmt.Errorf("queue: %s %s: %w", op, id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLost
	}
	return nil
}

// Ack removes a delivered envelope (in_flight → succeeded). Guarded by status and claim.
func (o *Outbox) Ack(ctx context.Context, db DB, id, worker string) error {
	tag, err := db.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker)
	return guarded(tag, err, "ack", id)
}

// Nack schedules the next attempt (in_flight → retry_scheduled).
func (o *Outbox) Nack(ctx context.Context, db DB, id, worker string, retryAt time.Time, reason string) error {
	tag, err := db.Exec(ctx, `UPDATE hub.outbox SET status = 'retry_scheduled', next_attempt_at = $3, last_error = $4,
			claimed_by = NULL, claimed_until = NULL, updated_at = $5
		WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker, retryAt, reason, o.now())
	return guarded(tag, err, "nack", id)
}

// Defer puts a claimed envelope back without counting the claim as an attempt (the integration
// is paused, or the retry budget is spent before anything was sent).
func (o *Outbox) Defer(ctx context.Context, db DB, id, worker string, retryAt time.Time, reason string) error {
	tag, err := db.Exec(ctx, `UPDATE hub.outbox SET status = CASE WHEN attempt <= 1 THEN 'accepted' ELSE 'retry_scheduled' END,
			attempt = GREATEST(attempt - 1, 0), next_attempt_at = $3, last_error = $4, claimed_by = NULL, claimed_until = NULL, updated_at = $5
		WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker, retryAt, reason, o.now())
	return guarded(tag, err, "defer", id)
}

// Park holds an envelope for its account + endpoint while the circuit is open
// (in_flight → parked). Nothing was sent, so the claim does not count as an attempt. Release
// drains parked envelopes in arrival order.
func (o *Outbox) Park(ctx context.Context, db DB, id, worker, account, endpoint string) error {
	tag, err := db.Exec(ctx, `UPDATE hub.outbox SET status = 'parked', account_id = $3, endpoint_id = $4,
			attempt = GREATEST(attempt - 1, 0), claimed_by = NULL, claimed_until = NULL, updated_at = $5
		WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker, account, endpoint, o.now())
	return guarded(tag, err, "park", id)
}

// Release makes up to limit parked envelopes of account + endpoint due now, oldest first
// (parked → retry_scheduled; DESIGN §10.5 drain). It returns how many were released.
func (o *Outbox) Release(ctx context.Context, db DB, account, endpoint string, limit int) (int, error) {
	now := o.now()
	// MATERIALIZED: an inlined "IN (SELECT ... LIMIT n FOR UPDATE SKIP LOCKED)" may be re-run
	// per row and release more than n (seen in CI).
	tag, err := db.Exec(ctx, `
		WITH picked AS MATERIALIZED (
			SELECT request_id FROM hub.outbox WHERE status = 'parked' AND account_id = $1 AND endpoint_id = $2
			ORDER BY created_at, request_id LIMIT $3 FOR UPDATE SKIP LOCKED)
		UPDATE hub.outbox o SET status = 'retry_scheduled', next_attempt_at = $4, updated_at = $4
		FROM picked WHERE o.request_id = picked.request_id`, account, endpoint, limit, now)
	if err != nil {
		return 0, fmt.Errorf("queue: release %s/%s: %w", account, endpoint, err)
	}
	return int(tag.RowsAffected()), nil
}

// EnqueueParked inserts env directly as parked (wait mode or queue_if_allowed with an open
// circuit: nothing is attempted until the drain releases it).
func (o *Outbox) EnqueueParked(ctx context.Context, db DB, env core.Envelope, account, endpoint string) error {
	if err := o.enqueue(ctx, db, env, "", 0); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `UPDATE hub.outbox SET status = 'parked', account_id = $2, endpoint_id = $3 WHERE request_id = $1`,
		env.RequestID, account, endpoint)
	if err != nil {
		return fmt.Errorf("queue: park %s: %w", env.RequestID, err)
	}
	return nil
}

// ParkedGroup is the parked backlog of one account + endpoint (drain, metrics).
type ParkedGroup struct {
	Account, Endpoint string
	Count             int
	Oldest            time.Time
}

// Parked lists the parked backlog per account + endpoint.
func (o *Outbox) Parked(ctx context.Context, db DB) ([]ParkedGroup, error) {
	rows, err := db.Query(ctx, `SELECT account_id, endpoint_id, count(*), min(created_at) FROM hub.outbox
		WHERE status = 'parked' GROUP BY account_id, endpoint_id`)
	if err != nil {
		return nil, fmt.Errorf("queue: parked: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ParkedGroup, error) {
		var g ParkedGroup
		err := r.Scan(&g.Account, &g.Endpoint, &g.Count, &g.Oldest)
		return g, err
	})
}

// DepthRow is the number of envelopes in one pool and status (queue_depth).
type DepthRow struct {
	Pool, Status string
	Count        int
}

// Depth counts the accepted, retry_scheduled and in_flight envelopes per pool and status.
// Parked envelopes are reported per breaker by Parked.
func (o *Outbox) Depth(ctx context.Context, db DB) ([]DepthRow, error) {
	rows, err := db.Query(ctx, `SELECT pool, status, count(*) FROM hub.outbox
		WHERE status IN ('accepted', 'retry_scheduled', 'in_flight') GROUP BY pool, status`)
	if err != nil {
		return nil, fmt.Errorf("queue: depth: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DepthRow, error) {
		var d DepthRow
		err := r.Scan(&d.Pool, &d.Status, &d.Count)
		return d, err
	})
}

// OldestDue returns, per pool, the earliest next_attempt_at among envelopes that are due now
// and not yet claimed (queue_oldest_age_s). Envelopes waiting out a backoff are not due, so a
// long retry schedule never looks like a stuck queue; only work the workers have not picked up
// does. Pools with nothing due are absent.
func (o *Outbox) OldestDue(ctx context.Context, db DB, now time.Time) (map[string]time.Time, error) {
	rows, err := db.Query(ctx, `SELECT pool, min(next_attempt_at) FROM hub.outbox
		WHERE status IN ('accepted', 'retry_scheduled') AND next_attempt_at <= $1 GROUP BY pool`, now)
	if err != nil {
		return nil, fmt.Errorf("queue: oldest due: %w", err)
	}
	out := map[string]time.Time{}
	var pool string
	var at time.Time
	_, err = pgx.ForEachRow(rows, []any{&pool, &at}, func() error {
		out[pool] = at
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("queue: oldest due: %w", err)
	}
	return out, nil
}

// DeadLetterGroup is the unreplayed dead letters of one tenant, integration, account and reason
// (dlq_count).
type DeadLetterGroup struct {
	Tenant, Integration, Account, Reason string
	Count                                int
}

// DeadLetterCounts groups the dead letters not yet replayed, largest groups first, at most
// limit groups (the caller folds the rest into an overflow group with DeadLetterTotal).
func (o *Outbox) DeadLetterCounts(ctx context.Context, db DB, limit int) ([]DeadLetterGroup, error) {
	rows, err := db.Query(ctx, `SELECT tenant_id, integration_id, COALESCE(account_id, ''), reason, count(*) AS n
		FROM hub.dead_letters WHERE replayed_at IS NULL
		GROUP BY tenant_id, integration_id, account_id, reason ORDER BY n DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("queue: dead-letter counts: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeadLetterGroup, error) {
		var g DeadLetterGroup
		err := r.Scan(&g.Tenant, &g.Integration, &g.Account, &g.Reason, &g.Count)
		return g, err
	})
}

// DeadLetterTotal counts the dead letters not yet replayed.
func (o *Outbox) DeadLetterTotal(ctx context.Context, db DB) (int, error) {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM hub.dead_letters WHERE replayed_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("queue: dead-letter total: %w", err)
	}
	return n, nil
}

// DeadLetter is the information kept for a dead letter (DESIGN §10.6).
type DeadLetter struct {
	Reason             string // canonical code, or "poison"
	LastError          string
	AccountID          string
	LastVendorRequest  json.RawMessage // policy-applied, credential-free (the caller's audit view)
	LastVendorResponse json.RawMessage
}

// DeadLetter moves a claimed envelope to hub.dead_letters (in_flight → dead_lettered).
func (o *Outbox) DeadLetter(ctx context.Context, db DB, id, worker string, d DeadLetter) error {
	tag, err := db.Exec(ctx, `
		WITH gone AS (
			DELETE FROM hub.outbox WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2
			RETURNING request_id, tenant_id, integration_id, attempt, envelope)
		INSERT INTO hub.dead_letters (request_id, created_at, tenant_id, integration_id, account_id, reason, last_error, attempts,
			envelope, last_vendor_request, last_vendor_response)
		SELECT request_id, $3, tenant_id, integration_id, $4, $5, $6, attempt, envelope, $7, $8 FROM gone
		ON CONFLICT (request_id) DO UPDATE SET created_at = EXCLUDED.created_at, account_id = EXCLUDED.account_id,
			reason = EXCLUDED.reason, last_error = EXCLUDED.last_error, attempts = EXCLUDED.attempts,
			envelope = EXCLUDED.envelope, last_vendor_request = EXCLUDED.last_vendor_request,
			last_vendor_response = EXCLUDED.last_vendor_response, replayed_at = NULL`,
		id, worker, o.now(), nullIfEmpty(d.AccountID), d.Reason, nullIfEmpty(d.LastError), nullJSON(d.LastVendorRequest), nullJSON(d.LastVendorResponse))
	return guarded(tag, err, "dead-letter", id)
}

// Expire removes an envelope whose TTL passed while it waited (accepted, retry_scheduled or
// parked → expired; DESIGN §10.7). A claimed (in_flight) envelope is never expired here: the
// worker checks expires_at before each attempt.
func (o *Outbox) Expire(ctx context.Context, db DB, id string) error {
	tag, err := db.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = $1 AND status IN ('accepted', 'retry_scheduled', 'parked')
		AND expires_at <= $2`, id, o.now())
	return guarded(tag, err, "expire", id)
}

// ExpireClaimed removes an envelope its worker found past its TTL before attempting it
// (in_flight → expired, guarded by the claim).
func (o *Outbox) ExpireClaimed(ctx context.Context, db DB, id, worker string) error {
	tag, err := db.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker)
	return guarded(tag, err, "expire", id)
}

// DueExpiries lists up to n waiting envelopes whose TTL passed (scheduler).
func (o *Outbox) DueExpiries(ctx context.Context, db DB, n int) ([]core.Envelope, error) {
	rows, err := db.Query(ctx, `SELECT envelope, attempt FROM hub.outbox WHERE status IN ('accepted', 'retry_scheduled', 'parked')
		AND expires_at <= $1 ORDER BY expires_at LIMIT $2`, o.now(), n)
	if err != nil {
		return nil, fmt.Errorf("queue: due expiries: %w", err)
	}
	envs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (core.Envelope, error) {
		var body []byte
		var e core.Envelope
		var attempt int
		if err := row.Scan(&body, &attempt); err != nil {
			return e, err
		}
		err := json.Unmarshal(body, &e)
		e.Attempt = attempt // the row's count, not the stored envelope's
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("queue: due expiries: %w", err)
	}
	return envs, nil
}

// PruneDeadLetters deletes dead letters older than retention (DESIGN §10.6: 14 days).
func (o *Outbox) PruneDeadLetters(ctx context.Context, db DB, retention time.Duration) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM hub.dead_letters WHERE created_at < $1`, o.now().Add(-retention))
	if err != nil {
		return 0, fmt.Errorf("queue: prune dead letters: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Replay puts a dead letter back as accepted, due now (dead_lettered → accepted; operator
// replay, DESIGN §10.6). The worker re-resolves and re-transforms.
func (o *Outbox) Replay(ctx context.Context, db DB, id string, expiresAt time.Time) error {
	now := o.now()
	tag, err := db.Exec(ctx, `
		WITH dl AS (
			UPDATE hub.dead_letters SET replayed_at = $2 WHERE request_id = $1 AND replayed_at IS NULL
			RETURNING request_id, tenant_id, integration_id, envelope)
		INSERT INTO hub.outbox (request_id, status, pool, tenant_id, integration_id, attempt, next_attempt_at, expires_at, envelope, created_at)
		SELECT request_id, 'accepted', CASE WHEN envelope->>'capability' LIKE 'llm.%' THEN 'llm' ELSE 'messaging' END,
			tenant_id, integration_id, 0, $2, $3, jsonb_set(envelope, '{attempt}', '0'), $2 FROM dl`,
		id, now, expiresAt)
	return guarded(tag, err, "replay", id)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}
