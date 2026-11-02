-- Request explorer and DLQ list indexes (DESIGN §16.1, HUB-207, HUB-209). Owned by
-- internal/control/adminapi (requests.go, dlq.go). Every explorer query bounds created_at, so
-- the day partitions are pruned first; these indexes serve the filters inside a partition,
-- newest first. Indexes on the partitioned parents cascade to every partition, existing and
-- future (audit.PGStore.EnsurePartitions). Always schema-qualified.

-- +goose Up
-- tenant_viewer and per-tenant searches without an integration filter.
CREATE INDEX requests_tenant_created_idx ON hub.requests (tenant_id, created_at DESC);
-- Status filter across every state (requests_open_idx covers only the open ones).
CREATE INDEX requests_status_created_idx ON hub.requests (status, created_at DESC);
-- Final canonical code filter (most rows have none).
CREATE INDEX requests_code_created_idx ON hub.requests (error_code, created_at DESC) WHERE error_code IS NOT NULL;
-- Exact idempotency key lookup.
CREATE INDEX requests_idempotency_idx ON hub.requests (idempotency_key, created_at DESC) WHERE idempotency_key IS NOT NULL;
-- Attempts of one request in order (last attempt: vendor and account filters, summaries).
CREATE INDEX request_attempts_request_no_idx ON hub.request_attempts (request_id, attempt_no DESC);
-- Unfiltered DLQ list, newest first (the other filters have their own indexes in 00004).
CREATE INDEX dead_letters_created_idx ON hub.dead_letters (created_at DESC, request_id DESC);

-- +goose Down
DROP INDEX IF EXISTS hub.dead_letters_created_idx;
DROP INDEX IF EXISTS hub.request_attempts_request_no_idx;
DROP INDEX IF EXISTS hub.requests_idempotency_idx;
DROP INDEX IF EXISTS hub.requests_code_created_idx;
DROP INDEX IF EXISTS hub.requests_status_created_idx;
DROP INDEX IF EXISTS hub.requests_tenant_created_idx;
