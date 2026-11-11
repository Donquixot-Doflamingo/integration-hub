-- ISFC tenant sync and caller keys (AUTH_PLAN §6.1, HUB-480). Owned by internal/isfcsync.
-- isfc_sync_runs: one row per reconcile that changed something, failed, or was asked for.
-- isfc_caller_keys: ISFC API keys issued by the Hub backend client, stored only as sha256
-- hashes (registry.HashKey) the moment they are read. The plaintext is never stored.

-- +goose Up
CREATE TABLE hub.isfc_sync_runs (
    id          text PRIMARY KEY CHECK (id ~ '^isr_[0-9A-HJKMNP-TV-Z]{26}$'),
    trigger     text NOT NULL CHECK (trigger IN ('event', 'tick', 'manual')),
    dry_run     boolean NOT NULL DEFAULT false,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    result      text NOT NULL CHECK (result IN ('ok', 'partial', 'failed')),
    error       text,
    change_id   text,
    changes     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX isfc_sync_runs_started_idx ON hub.isfc_sync_runs (started_at DESC);

CREATE TABLE hub.isfc_caller_keys (
    isfc_key_id      text PRIMARY KEY,
    key_hash         text NOT NULL CHECK (key_hash ~ '^sha256:[0-9a-f]{64}$'),
    caller_client_id text NOT NULL,
    tenant_ids       uuid[] NOT NULL DEFAULT '{}',
    permissions      text[] NOT NULL DEFAULT '{}',
    status           text NOT NULL CHECK (status IN ('ACTIVE', 'INACTIVE', 'REVOKED')),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX isfc_caller_keys_hash_idx ON hub.isfc_caller_keys (key_hash);
CREATE INDEX isfc_caller_keys_client_idx ON hub.isfc_caller_keys (caller_client_id);

-- +goose Down
DROP TABLE IF EXISTS hub.isfc_caller_keys;
DROP TABLE IF EXISTS hub.isfc_sync_runs;
