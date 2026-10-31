-- Audit ledger (DESIGN §13.4, TECH_SPEC §6.2, HUB-091). Owned by internal/audit.
-- Partitioned by day; audit.Writer creates partitions 7 days ahead (EnsurePartitions), the
-- scheduler will detach and export expired ones. Always schema-qualified (00001_baseline.sql).

-- +goose Up
CREATE TABLE hub.requests (
    request_id          text NOT NULL CHECK (request_id ~ '^req_[0-9A-HJKMNP-TV-Z]{26}$'),
    created_at          timestamptz NOT NULL,
    tenant_id           text NOT NULL,
    integration_id      text NOT NULL,
    integration_version integer,
    capability          text NOT NULL,
    operation           text NOT NULL DEFAULT 'default',
    mode                text NOT NULL,
    dry_run             boolean NOT NULL DEFAULT false,
    status              text NOT NULL,
    payload_policy      text NOT NULL CHECK (payload_policy IN ('full', 'masked', 'sealed', 'metadata_only')),
    snapshot_version    bigint,
    idempotency_key     text,
    trace_id            text,
    http_status         integer,
    error_code          text,
    attempts            integer NOT NULL DEFAULT 0,
    hub_overhead_ms     double precision,
    vendor_ms           double precision,
    queue_wait_ms       double precision,
    completed_at        timestamptz,
    PRIMARY KEY (created_at, request_id)
) PARTITION BY RANGE (created_at);
CREATE INDEX requests_id_idx ON hub.requests (request_id);
CREATE INDEX requests_tenant_idx ON hub.requests (tenant_id, integration_id, created_at DESC);
CREATE INDEX requests_open_idx ON hub.requests (status, created_at)
    WHERE status NOT IN ('succeeded', 'dead_lettered', 'expired');

-- One row per §13.1 event. sha256 and bytes are always kept (§13.3), even when the payload
-- itself is not stored (metadata_only) or masked.
CREATE TABLE hub.request_events (
    at             timestamptz NOT NULL,
    request_id     text NOT NULL,
    seq            integer NOT NULL CHECK (seq >= 1),
    type           text NOT NULL,
    attempt_no     integer,
    payload_sha256 text CHECK (payload_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    payload_bytes  integer CHECK (payload_bytes >= 0),
    payload        bytea,
    payload_state  text NOT NULL DEFAULT 'none'
        CHECK (payload_state IN ('none', 'full', 'masked', 'sealed', 'omitted', 'truncated')),
    payload_ref    text,
    meta           jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (at, request_id, seq)
) PARTITION BY RANGE (at);
CREATE INDEX request_events_request_idx ON hub.request_events (request_id, seq);

CREATE TABLE hub.request_attempts (
    started_at   timestamptz NOT NULL,
    request_id   text NOT NULL,
    attempt_no   integer NOT NULL CHECK (attempt_no >= 1),
    account_id   text NOT NULL,
    endpoint_id  text NOT NULL,
    host         text,
    duration_ms  double precision NOT NULL,
    http_status  integer,
    error_class  text,
    PRIMARY KEY (started_at, request_id, attempt_no)
) PARTITION BY RANGE (started_at);
CREATE INDEX request_attempts_request_idx ON hub.request_attempts (request_id);
CREATE INDEX request_attempts_account_idx ON hub.request_attempts (account_id, started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS hub.request_attempts;
DROP TABLE IF EXISTS hub.request_events;
DROP TABLE IF EXISTS hub.requests;
