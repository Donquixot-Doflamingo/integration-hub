-- Vendor sessions for multi-step operations (DESIGN §3.1, §6.3 row 6, §14, §18.2; TECH_SPEC §6.3,
-- HUB-175). Owned by internal/sessions. A session pins later steps to the integration version and
-- account that opened it. data holds only what later steps need (a vendor txn id); it never holds
-- the raw identifier of the person (DESIGN §14: the Aadhaar number is never stored), so there is no
-- column for one and both data and vendor_txn_id refuse a 12-digit (4-4-4) number. Always
-- schema-qualified (00001_baseline.sql).

-- +goose Up
CREATE TABLE hub.vendor_sessions (
    session_id     text PRIMARY KEY CHECK (session_id ~ '^ses_[0-9A-HJKMNP-TV-Z]{26}$'),
    tenant_id      text NOT NULL,
    integration_id text NOT NULL,
    version        integer NOT NULL CHECK (version > 0),
    account_id     text NOT NULL,
    vendor_txn_id  text CHECK (vendor_txn_id !~ '(^|[^0-9])[0-9]{4}[ -]?[0-9]{4}[ -]?[0-9]{4}([^0-9]|$)'),
    data           jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(data) = 'object')
        CHECK (data::text !~ '(^|[^0-9])[0-9]{4}[ -]?[0-9]{4}[ -]?[0-9]{4}([^0-9]|$)'),
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    CHECK (expires_at > created_at)
) WITH (autovacuum_vacuum_scale_factor = 0.05);

-- Scheduler sweep (Expire) and the open-session lookups.
CREATE INDEX vendor_sessions_expires_idx ON hub.vendor_sessions (expires_at);
-- Retire guard: open sessions per account (CountOpen).
CREATE INDEX vendor_sessions_account_idx ON hub.vendor_sessions (account_id, expires_at);

-- +goose Down
DROP TABLE IF EXISTS hub.vendor_sessions;
