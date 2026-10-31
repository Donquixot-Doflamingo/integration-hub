-- Outbound callback deliveries (DESIGN §12.1, TECH_SPEC §6.3, HUB-120). Owned by
-- internal/callbacks. One row per (event, subscribed registrar endpoint): the row is the
-- callback's own outbox, retry schedule and DLQ (status 'dead_lettered'), independent of the
-- request's vendor delivery. Always schema-qualified (00001_baseline.sql).

-- +goose Up
CREATE TABLE hub.callback_deliveries (
    delivery_id         text PRIMARY KEY CHECK (delivery_id ~ '^dlv_[0-9A-HJKMNP-TV-Z]{26}$'),
    event_id            text NOT NULL CHECK (event_id ~ '^evt_[0-9A-HJKMNP-TV-Z]{26}$'),
    event_type          text NOT NULL
        CHECK (event_type IN ('request.completed', 'request.failed', 'request.expired', 'delivery.status')),
    request_id          text NOT NULL CHECK (request_id ~ '^req_[0-9A-HJKMNP-TV-Z]{26}$'),
    tenant_id           text NOT NULL,
    integration_id      text NOT NULL,
    integration_version integer NOT NULL CHECK (integration_version > 0),
    endpoint_id         text NOT NULL,
    -- Index of the subscription in the version's webhooks list; keys the audit seq.
    subscription        integer NOT NULL CHECK (subscription >= 0),
    -- Exact bytes signed and POSTed on every attempt (stable for receivers that hash them).
    payload             bytea NOT NULL,
    status              text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_flight', 'retry_scheduled', 'delivered', 'dead_lettered')),
    attempts            integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at     timestamptz NOT NULL DEFAULT now(),
    claimed_by          text,
    claimed_until       timestamptz,
    last_http_status    integer,
    -- Short error class of the last failed attempt (never a response body or a header value).
    last_error          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    delivered_at        timestamptz,
    UNIQUE (event_id, endpoint_id)
) WITH (fillfactor = 80, autovacuum_vacuum_scale_factor = 0.01);

-- Claim of due rows (dispatcher).
CREATE INDEX callback_deliveries_due_idx ON hub.callback_deliveries (next_attempt_at)
    WHERE status IN ('pending', 'retry_scheduled');
-- Reclaim of rows whose dispatcher died mid-attempt (visibility timeout).
CREATE INDEX callback_deliveries_inflight_idx ON hub.callback_deliveries (claimed_until)
    WHERE status = 'in_flight';
CREATE INDEX callback_deliveries_request_idx ON hub.callback_deliveries (request_id);
-- The callback DLQ: count, list and replay.
CREATE INDEX callback_deliveries_dlq_idx ON hub.callback_deliveries (created_at)
    WHERE status = 'dead_lettered';

-- +goose Down
DROP TABLE IF EXISTS hub.callback_deliveries;
