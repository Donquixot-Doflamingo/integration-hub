-- Registry tables (DESIGN §18.2, HUB-047). Owned by internal/control (TECH_SPEC §4.3).
-- Always schema-qualified (hub.<name>): see 00001_baseline.sql.

-- +goose Up
CREATE TABLE hub.tenants (
    tenant_id        text PRIMARY KEY,
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    rate_limit_rps   integer NOT NULL CHECK (rate_limit_rps >= 0),
    rate_limit_burst integer NOT NULL CHECK (rate_limit_burst >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Tenant keys are stored only as hashes (DESIGN §16.5).
CREATE TABLE hub.api_keys (
    key_id     text PRIMARY KEY,
    tenant_id  text NOT NULL REFERENCES hub.tenants (tenant_id),
    key_hash   text NOT NULL UNIQUE CHECK (key_hash ~ '^sha256:[0-9a-f]{64}$'),
    scopes     text[] NOT NULL DEFAULT '{}',
    state      text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'expiring', 'revoked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    revoked_at timestamptz
);
CREATE INDEX api_keys_tenant_idx ON hub.api_keys (tenant_id);

CREATE TABLE hub.vendors (
    vendor_id  text PRIMARY KEY,
    auth       text NOT NULL,
    regions    jsonb NOT NULL,
    defaults   jsonb NOT NULL,
    docs_url   text,
    version    integer NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hub.vendor_endpoints (
    vendor_id          text NOT NULL REFERENCES hub.vendors (vendor_id),
    endpoint_id        text NOT NULL CHECK (endpoint_id ~ '^[a-z][a-z0-9_]*@[1-9][0-9]*$'),
    method             text NOT NULL,
    path               text NOT NULL,
    timeout_ms         integer NOT NULL CHECK (timeout_ms > 0),
    connect_timeout_ms integer NOT NULL CHECK (connect_timeout_ms > 0),
    idempotent         boolean NOT NULL DEFAULT false,
    batch_max          integer,
    PRIMARY KEY (vendor_id, endpoint_id)
);

CREATE TABLE hub.accounts (
    account_id      text PRIMARY KEY,
    vendor_id       text NOT NULL REFERENCES hub.vendors (vendor_id),
    region          text NOT NULL,
    state           text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'draining', 'retired')),
    config          jsonb NOT NULL DEFAULT '{}',
    quota           jsonb NOT NULL DEFAULT '{}',
    credentials_ref text NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Ciphertext only: envelope encryption, per-account data key wrapped by KMS (DESIGN §9).
CREATE TABLE hub.account_credentials (
    account_id   text NOT NULL REFERENCES hub.accounts (account_id),
    cred_version integer NOT NULL CHECK (cred_version > 0),
    slot         text NOT NULL CHECK (slot IN ('active', 'next', 'previous')),
    ciphertext   bytea NOT NULL,
    wrapped_key  bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, cred_version)
);
CREATE UNIQUE INDEX account_credentials_slot_idx ON hub.account_credentials (account_id, slot);

CREATE TABLE hub.capabilities (
    capability      text NOT NULL,
    major           integer NOT NULL CHECK (major > 0),
    minor           integer NOT NULL CHECK (minor >= 0),
    request_schema  jsonb NOT NULL,
    response_schema jsonb NOT NULL,
    operations      jsonb NOT NULL,
    interactive     boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (capability, minor)
);

-- Immutable per version (DESIGN §8.1): UPDATE is rejected by trigger below.
CREATE TABLE hub.transformers (
    transformer_id text NOT NULL,
    version        integer NOT NULL CHECK (version > 0),
    kind           text NOT NULL CHECK (kind IN ('request', 'response')),
    capability     text NOT NULL,
    endpoint_id    text,
    yaml           text NOT NULL,
    fixtures       jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (transformer_id, version)
);

CREATE TABLE hub.integrations (
    integration_id       text PRIMARY KEY,
    owner_tenant         text NOT NULL REFERENCES hub.tenants (tenant_id),
    capability           text NOT NULL,
    active_version       integer NOT NULL CHECK (active_version > 0),
    paused               boolean NOT NULL DEFAULT false,
    description          text NOT NULL DEFAULT '',
    moves_money_or_quota boolean NOT NULL DEFAULT false,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Immutable once published (DESIGN §6.4): UPDATE is rejected by trigger below.
CREATE TABLE hub.integration_versions (
    integration_id text NOT NULL REFERENCES hub.integrations (integration_id),
    version        integer NOT NULL CHECK (version > 0),
    spec           jsonb NOT NULL,
    author         text NOT NULL,
    reason         text NOT NULL,
    published_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, version)
);

CREATE TABLE hub.integration_grants (
    integration_id text NOT NULL REFERENCES hub.integrations (integration_id),
    tenant_id      text NOT NULL REFERENCES hub.tenants (tenant_id),
    PRIMARY KEY (integration_id, tenant_id)
);

CREATE TABLE hub.registrar_endpoints (
    endpoint_id text PRIMARY KEY,
    tenant_id   text NOT NULL REFERENCES hub.tenants (tenant_id),
    url         text NOT NULL,
    secret_ref  text NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per published snapshot (DESIGN §8.2). The blob holds the snapshot sources; rows
-- older than 90 days are pruned by the scheduler.
CREATE TABLE hub.config_snapshots (
    version    bigint PRIMARY KEY CHECK (version > 0),
    checksum   text NOT NULL CHECK (checksum ~ '^sha256:[0-9a-f]{64}$'),
    blob_ref   text NOT NULL,
    author     text NOT NULL,
    reason     text NOT NULL DEFAULT '',
    diff       jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX config_snapshots_created_idx ON hub.config_snapshots (created_at);

-- +goose StatementBegin
CREATE FUNCTION hub.reject_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable; publish a new version instead', TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER integration_versions_immutable BEFORE UPDATE ON hub.integration_versions
    FOR EACH ROW EXECUTE FUNCTION hub.reject_update();
CREATE TRIGGER transformers_immutable BEFORE UPDATE ON hub.transformers
    FOR EACH ROW EXECUTE FUNCTION hub.reject_update();

-- +goose Down
DROP TABLE hub.config_snapshots;
DROP TABLE hub.registrar_endpoints;
DROP TABLE hub.integration_grants;
DROP TABLE hub.integration_versions;
DROP TABLE hub.integrations;
DROP TABLE hub.transformers;
DROP TABLE hub.capabilities;
DROP TABLE hub.account_credentials;
DROP TABLE hub.accounts;
DROP TABLE hub.vendor_endpoints;
DROP TABLE hub.vendors;
DROP TABLE hub.api_keys;
DROP TABLE hub.tenants;
DROP FUNCTION hub.reject_update();
