-- Outbox and dead letters (DESIGN §11, §10.6, TECH_SPEC §6.3, HUB-110). Owned by internal/queue.
-- The outbox is not partitioned: a row is deleted on its terminal transition, in the same
-- transaction as the requests status change, so it stays small. Always schema-qualified.

-- +goose Up
CREATE TABLE hub.outbox (
    request_id      text PRIMARY KEY CHECK (request_id ~ '^req_[0-9A-HJKMNP-TV-Z]{26}$'),
    status          text NOT NULL CHECK (status IN ('accepted', 'in_flight', 'retry_scheduled', 'parked')),
    pool            text NOT NULL DEFAULT 'messaging' CHECK (pool IN ('messaging', 'llm')),
    tenant_id       text NOT NULL,
    integration_id  text NOT NULL,
    account_id      text,          -- set when parked (release is per account + endpoint)
    endpoint_id     text,
    attempt         integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    next_attempt_at timestamptz NOT NULL,
    expires_at      timestamptz NOT NULL,
    ordering_key    text,
    claimed_by      text,
    claimed_until   timestamptz,
    crashes         integer NOT NULL DEFAULT 0 CHECK (crashes >= 0), -- reclaims after an expired claim (poison detector)
    last_error      text,
    envelope        jsonb NOT NULL,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
) WITH (fillfactor = 70, autovacuum_vacuum_scale_factor = 0.01);

CREATE INDEX outbox_claim_idx ON hub.outbox (pool, next_attempt_at) WHERE status IN ('accepted', 'retry_scheduled');
CREATE INDEX outbox_parked_idx ON hub.outbox (account_id, endpoint_id, next_attempt_at) WHERE status = 'parked';
CREATE INDEX outbox_reclaim_idx ON hub.outbox (claimed_until) WHERE status = 'in_flight';
CREATE INDEX outbox_ttl_idx ON hub.outbox (expires_at) WHERE status IN ('accepted', 'retry_scheduled', 'parked');
CREATE INDEX outbox_ordering_idx ON hub.outbox (ordering_key, created_at) WHERE ordering_key IS NOT NULL;

-- DESIGN §10.6: canonical payload (envelope), last vendor exchange, reason; attempts live in
-- hub.request_attempts. Kept 14 days; replay re-resolves and re-transforms.
CREATE TABLE hub.dead_letters (
    request_id           text PRIMARY KEY,
    created_at           timestamptz NOT NULL DEFAULT now(),
    tenant_id            text NOT NULL,
    integration_id       text NOT NULL,
    account_id           text,
    reason               text NOT NULL,
    last_error           text,
    attempts             integer NOT NULL DEFAULT 0,
    envelope             jsonb NOT NULL,
    last_vendor_request  jsonb,
    last_vendor_response jsonb,
    replayed_at          timestamptz
);
CREATE INDEX dead_letters_reason_idx ON hub.dead_letters (reason, created_at);
CREATE INDEX dead_letters_account_idx ON hub.dead_letters (account_id, created_at);
CREATE INDEX dead_letters_integration_idx ON hub.dead_letters (tenant_id, integration_id, created_at);

-- Ledger additions for queued requests (owned by internal/audit; 00003 is already applied):
-- the version each attempt used, when the request row last changed, and the final result kept
-- for status polling.
ALTER TABLE hub.request_attempts ADD COLUMN integration_version integer;
ALTER TABLE hub.requests ADD COLUMN updated_at timestamptz;

-- Final canonical result or error of a queued request, for GET /v1/requests/{id}. Kept 7 days
-- (scheduler prunes), separate from the audit payloads so the audit policy stays authoritative
-- for history while polling still gets the result it is owed.
CREATE TABLE hub.request_results (
    request_id text PRIMARY KEY,
    tenant_id  text NOT NULL,
    result     jsonb,
    error      jsonb,
    expires_at timestamptz NOT NULL
);
CREATE INDEX request_results_expiry_idx ON hub.request_results (expires_at);

-- +goose Down
DROP TABLE IF EXISTS hub.request_results;
ALTER TABLE hub.requests DROP COLUMN IF EXISTS updated_at;
ALTER TABLE hub.request_attempts DROP COLUMN IF EXISTS integration_version;
DROP TABLE IF EXISTS hub.dead_letters;
DROP TABLE IF EXISTS hub.outbox;
