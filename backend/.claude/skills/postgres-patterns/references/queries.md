# SQL reference

Column lists follow DESIGN §18.2; adjust to the real schema once migrations exist.

## sqlc.yaml (backend/)
```yaml
version: "2"
sql:
  - engine: postgresql
    schema: migrations          # sqlc reads goose Up sections
    queries: internal/queue/postgres/queries
    gen:
      go:
        package: db
        out: internal/queue/postgres/db
        sql_package: pgx/v5
        overrides:
          - db_type: jsonb
            go_type: encoding/json.RawMessage
          - db_type: timestamptz
            go_type: time.Time
  # one entry per adapter package (audit, control, sessions, callbacks)
```

## Partitioned table migration
```sql
-- +goose Up
CREATE TABLE requests (
  request_id      text        NOT NULL,
  created_at      timestamptz NOT NULL,
  tenant_id       text        NOT NULL,
  integration_id  text        NOT NULL,
  status          text        NOT NULL,
  final_version   int,
  timings         jsonb,
  policy          text        NOT NULL,
  PRIMARY KEY (request_id, created_at)
) PARTITION BY RANGE (created_at);

CREATE TABLE request_events (
  request_id          text        NOT NULL,
  request_created_at  timestamptz NOT NULL,   -- partition key = parent request's day
  seq                 int         NOT NULL,
  type                text        NOT NULL,
  at                  timestamptz NOT NULL,
  payload             bytea,
  payload_ref         text,
  sha256              bytea       NOT NULL,
  bytes               int         NOT NULL,
  PRIMARY KEY (request_id, request_created_at, seq)
) PARTITION BY RANGE (request_created_at);

-- +goose Down
DROP TABLE request_events;
DROP TABLE requests;
```

## Partition maintenance (scheduler job, idempotent)
```sql
-- +goose StatementBegin  (only if placed in a migration; as a job, run via Exec)
CREATE OR REPLACE FUNCTION hub_ensure_day_partition(parent regclass, day date) RETURNS void AS $$
DECLARE
  name text := format('%s_%s', parent::text, to_char(day, 'YYYYMMDD'));
BEGIN
  EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF %s FOR VALUES FROM (%L) TO (%L)',
                 name, parent, day::timestamptz, (day + 1)::timestamptz);
END $$ LANGUAGE plpgsql;
-- +goose StatementEnd
```
Partition bounds are `timestamptz`; make sure the session `TimeZone` is UTC (`RuntimeParams["timezone"] = "UTC"`) so "day" means UTC day.
Retention: `ALTER TABLE requests DETACH PARTITION requests_20260801 CONCURRENTLY;` → export → `DROP TABLE`.

## Enqueue (same transaction as the request row)
```sql
-- name: InsertOutbox :exec
INSERT INTO outbox (request_id, status, account_id, endpoint_id, next_attempt_at, expires_at,
                    ordering_key, attempt, claim_count, envelope, created_at)
VALUES (sqlc.arg(request_id), 'accepted', sqlc.narg(account_id), sqlc.narg(endpoint_id), now(),
        sqlc.arg(expires_at), sqlc.narg(ordering_key), 0, 0, sqlc.arg(envelope), now());
```

## Nack (schedule retry)
```sql
-- name: ScheduleRetry :execrows
UPDATE outbox
SET status = 'retry_scheduled', attempt = attempt + 1, next_attempt_at = sqlc.arg(retry_at),
    claimed_by = NULL, claimed_until = NULL
WHERE request_id = sqlc.arg(request_id) AND status = 'in_flight' AND claimed_by = sqlc.arg(worker);
```

## Park and release (drain)
```sql
-- name: Park :execrows
UPDATE outbox
SET status = 'parked', account_id = sqlc.arg(account_id), endpoint_id = sqlc.arg(endpoint_id),
    claimed_by = NULL, claimed_until = NULL
WHERE request_id = sqlc.arg(request_id) AND status = 'in_flight' AND claimed_by = sqlc.arg(worker);

-- name: ReleaseParked :execrows
WITH batch AS (
  SELECT request_id FROM outbox
  WHERE status = 'parked' AND account_id = sqlc.arg(account_id) AND endpoint_id = sqlc.arg(endpoint_id)
  ORDER BY created_at
  LIMIT sqlc.arg(max_rows)
  FOR UPDATE SKIP LOCKED
)
UPDATE outbox o SET status = 'retry_scheduled', next_attempt_at = now()
FROM batch WHERE o.request_id = batch.request_id;
```

## Worker heartbeat (long vendor calls, e.g. LLM)
```sql
-- name: ExtendClaim :execrows
UPDATE outbox SET claimed_until = now() + sqlc.arg(visibility)::interval
WHERE request_id = sqlc.arg(request_id) AND status = 'in_flight' AND claimed_by = sqlc.arg(worker);
```
0 rows → someone reclaimed it; abandon the attempt's side effects.

## Release on shutdown
```sql
-- name: ReleaseClaim :execrows
UPDATE outbox SET status = 'retry_scheduled', next_attempt_at = now(), claimed_by = NULL, claimed_until = NULL
WHERE request_id = sqlc.arg(request_id) AND status = 'in_flight' AND claimed_by = sqlc.arg(worker);
```

## TTL sweep (scheduler)
```sql
-- name: ExpireDue :many
WITH gone AS (
  SELECT request_id FROM outbox
  WHERE status IN ('retry_scheduled', 'parked') AND expires_at <= now()
  LIMIT sqlc.arg(batch)
  FOR UPDATE SKIP LOCKED
)
UPDATE outbox o SET status = 'expired'
FROM gone WHERE o.request_id = gone.request_id
RETURNING o.request_id, o.envelope;
```
Then, in the same transaction, update `requests`, write the terminal audit record and enqueue `request.expired` callbacks for the returned rows.

## Ordering key (opt-in)
Only claim the oldest pending row per `ordering_key` and skip keys with an in-flight row:
```sql
AND (ordering_key IS NULL OR NOT EXISTS (
  SELECT 1 FROM outbox x WHERE x.ordering_key = outbox.ordering_key AND x.status = 'in_flight'))
```
plus `DISTINCT ON (ordering_key)` in the candidate CTE. Index `(ordering_key, created_at) WHERE ordering_key IS NOT NULL`. Test it under concurrency; it is the easiest place to get a double-claim.

## Diagnostics
```sql
EXPLAIN (ANALYZE, BUFFERS) SELECT ...;               -- wrap writes in BEGIN; ... ROLLBACK;
SELECT relname, n_live_tup, n_dead_tup, last_autovacuum FROM pg_stat_user_tables WHERE relname = 'outbox';
SELECT * FROM pg_locks WHERE locktype = 'advisory';
```
