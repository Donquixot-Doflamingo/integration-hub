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

// Pools (DESIGN §17). An envelope names its pool (Envelope.Pool, default messaging). The llm
// pool used to be chosen from llm.* capability names, which ADR-0023 removed; until an explicit
// integration setting exists (HUB-439, ADR-0024) the gateway queues every request as messaging.
const (
	PoolMessaging = "messaging"
	PoolLLM       = "llm"
)

// PoolOf is the worker pool of env (Replay derives it the same way in SQL).
func PoolOf(env core.Envelope) string {
	if env.Pool == PoolLLM {
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
	// Target is the row's (account, endpoint) hint when it has one (ClaimWithin only). The worker
	// reserves one of that target's slots from the claim until the attempt starts (HUB-444).
	Target *Target
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
	var account, endpoint any // the claim hint, when the gateway resolved a target (HUB-444)
	if env.TargetAccount != "" && env.TargetEndpoint != "" {
		account, endpoint = env.TargetAccount, env.TargetEndpoint
	}
	_, err = db.Exec(ctx, `INSERT INTO hub.outbox (request_id, status, pool, tenant_id, integration_id, attempt, next_attempt_at,
			expires_at, ordering_key, claimed_by, claimed_until, envelope, created_at, account_id, endpoint_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		env.RequestID, string(status), PoolOf(env), env.Tenant, env.IntegrationID, env.Attempt, env.NextAttemptAt,
		env.ExpiresAt, env.OrderingKey, claimedBy, claimedUntil, body, env.CreatedAt, account, endpoint)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicate
	}
	if err != nil {
		return fmt.Errorf("queue: enqueue %s: %w", env.RequestID, err)
	}
	return nil
}

// Target is a breaker key, (account, endpoint): the unit the worker isolates (DESIGN §10.4,
// HUB-412). Endpoint is the full ref (vendor.endpoint@version).
type Target struct {
	Account, Endpoint string
}

// Claim takes up to n due envelopes of pool for worker: accepted or retry_scheduled with
// next_attempt_at ≤ now, plus in_flight ones whose claim expired (a crashed worker; their
// crash count goes up). Rows are locked with SKIP LOCKED, so concurrent claimers never get the
// same envelope; each claim is exclusive for Visibility.
func (o *Outbox) Claim(ctx context.Context, db DB, worker, pool string, n int) ([]Claimed, error) {
	return o.ClaimWithin(ctx, db, worker, pool, n, 0, nil)
}

// TargetRoom is how many more attempts a worker will start for one target right now.
type TargetRoom struct {
	Target
	Slots int // 0: the target is at its cap
}

// ClaimWithin is Claim that takes no more of one target's backlog than the worker can start
// (HUB-412). An envelope's target is its account_id / endpoint_id hint, written by Park and
// DeferTarget (the target it was last seen on). Envelopes hinted with a target in rooms are taken
// at most Slots at a time (a target at its cap: none, they are not even locked), envelopes of any
// other hinted target at most perTarget (0: no limit), envelopes without a hint as usual. A target
// at its cap is left out before the LIMIT, so its backlog never fills a claim ahead of other
// targets' envelopes; one with free slots may fill the rest of a claim's window only that once
// (then it is at its cap). Nothing over the room is claimed only to be put back. The hint is only
// a filter: a claimed envelope is still resolved at send time (D10).
func (o *Outbox) ClaimWithin(ctx context.Context, db DB, worker, pool string, n, perTarget int, rooms []TargetRoom) ([]Claimed, error) {
	now := o.now()
	accounts, endpoints, slots := make([]string, 0, len(rooms)), make([]string, 0, len(rooms)), make([]int, 0, len(rooms))
	for _, r := range rooms {
		accounts, endpoints, slots = append(accounts, r.Account), append(endpoints, r.Endpoint), append(slots, max(r.Slots, 0))
	}
	rows, err := db.Query(ctx, `
		WITH room AS (SELECT * FROM unnest($6::text[], $7::text[], $8::int[]) AS r(account, endpoint, slots)),
		due AS MATERIALIZED (
			SELECT q.request_id, q.status AS prev, q.next_attempt_at, q.account_id, q.endpoint_id FROM hub.outbox q
			WHERE q.pool = $2 AND (
				(q.status IN ('accepted', 'retry_scheduled') AND q.next_attempt_at <= $4)
				OR (q.status = 'in_flight' AND q.claimed_until < $4))
			AND NOT EXISTS (SELECT 1 FROM room WHERE room.slots <= 0 AND room.account = q.account_id AND room.endpoint = q.endpoint_id)
			ORDER BY q.next_attempt_at
			LIMIT $3
			FOR UPDATE OF q SKIP LOCKED),
		ranked AS (
			SELECT d.request_id, d.prev, d.account_id,
				row_number() OVER (PARTITION BY d.account_id, d.endpoint_id ORDER BY d.next_attempt_at) AS rn,
				COALESCE((SELECT room.slots FROM room WHERE room.account = d.account_id AND room.endpoint = d.endpoint_id), NULLIF($9::int, 0)) AS slots
			FROM due d),
		pick AS (SELECT request_id, prev FROM ranked WHERE account_id IS NULL OR slots IS NULL OR rn <= slots)
		UPDATE hub.outbox o SET status = 'in_flight', claimed_by = $1, claimed_until = $5,
			attempt = o.attempt + 1, crashes = o.crashes + CASE WHEN pick.prev = 'in_flight' THEN 1 ELSE 0 END, updated_at = $4
		FROM pick WHERE o.request_id = pick.request_id
		RETURNING o.request_id, pick.prev, o.attempt, o.claimed_until, o.crashes, o.envelope, o.account_id, o.endpoint_id`,
		worker, pool, n, now, now.Add(o.visibility()), accounts, endpoints, slots, max(perTarget, 0))
	if err != nil {
		return nil, fmt.Errorf("queue: claim: %w", err)
	}
	defer rows.Close()
	var out []Claimed
	for rows.Next() {
		var c Claimed
		var id, prev string
		var body []byte
		var account, endpoint *string
		if err := rows.Scan(&id, &prev, &c.Attempt, &c.ClaimedUntil, &c.Crashes, &body, &account, &endpoint); err != nil {
			return nil, fmt.Errorf("queue: claim scan: %w", err)
		}
		attempt := c.Attempt
		if err := json.Unmarshal(body, &c.Envelope); err != nil {
			return nil, fmt.Errorf("queue: decode envelope %s: %w", id, err)
		}
		c.Attempt, c.Status = attempt, core.State(prev) // the row's attempt, not the stored envelope's
		if account != nil && endpoint != nil {
			c.Target = &Target{Account: *account, Endpoint: *endpoint}
		}
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

// DeferTarget is Defer for an envelope its worker would not attempt because the worker's
// in-flight cap for target was reached (HUB-412): nothing was sent, so the claim is not an
// attempt, and the row keeps target as its account_id / endpoint_id hint so ClaimWithin takes it
// only when that target has room again.
func (o *Outbox) DeferTarget(ctx context.Context, db DB, id, worker string, retryAt time.Time, reason string, target Target) error {
	tag, err := db.Exec(ctx, `UPDATE hub.outbox SET status = CASE WHEN attempt <= 1 THEN 'accepted' ELSE 'retry_scheduled' END,
			attempt = GREATEST(attempt - 1, 0), next_attempt_at = $3, last_error = $4, claimed_by = NULL, claimed_until = NULL, updated_at = $5,
			account_id = $6, endpoint_id = $7
		WHERE request_id = $1 AND status = 'in_flight' AND claimed_by = $2`, id, worker, retryAt, reason, o.now(), target.Account, target.Endpoint)
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
// replay, DESIGN §10.6). The worker re-resolves and re-transforms. A dead letter whose body was
// erased (EraseDeadLetters) has nothing to deliver: ErrLost. The new expiry is written to the
// column and into the envelope JSON: the worker checks the JSON's, so a stale one would expire
// a late replay unattempted (HUB-449).
func (o *Outbox) Replay(ctx context.Context, db DB, id string, expiresAt time.Time) error {
	now := o.now()
	tag, err := db.Exec(ctx, `
		WITH dl AS (
			UPDATE hub.dead_letters SET replayed_at = $2 WHERE request_id = $1 AND replayed_at IS NULL
				AND envelope->'canonical' IS DISTINCT FROM 'null'::jsonb
			RETURNING request_id, tenant_id, integration_id, envelope)
		INSERT INTO hub.outbox (request_id, status, pool, tenant_id, integration_id, attempt, next_attempt_at, expires_at, envelope, created_at)
		SELECT request_id, 'accepted', CASE WHEN envelope->>'pool' = 'llm' THEN 'llm' ELSE 'messaging' END,
			tenant_id, integration_id, 0, $2, $3, 
			jsonb_set(jsonb_set(envelope, '{attempt}', '0'), '{expires_at}', to_jsonb($4::text)), $2 FROM dl`,
		id, now, expiresAt, expiresAt.UTC().Format(time.RFC3339Nano))
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
