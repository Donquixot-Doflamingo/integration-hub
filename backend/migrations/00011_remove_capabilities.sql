-- ADR-0023 (HUB-434): capabilities are removed. An integration version declares its operations
-- with the caller's request schema; a vendor endpoint declares the request it accepts.
--
-- What runs nodes is the snapshot archive (hub.config_snapshots + blobs, ADR-0010): a blob
-- archived before this change (format 1) is upgraded when it is loaded (registry.UpgradeLegacy),
-- and the admin role re-archives the upgraded tree together with drafts and pending integrations
-- (control.UpgradeLegacyArchive). This migration handles the relational side:
--   * hub.integration_versions: each spec without operations gets them copied from the latest
--     minor of its integration's capability (description, idempotent, request schema as
--     request_schema.body);
--   * hub.integrations gains interactive (from the capability) and loses capability;
--   * hub.vendor_endpoints gains request_schema ({} = nothing checked) and response_schema;
--   * hub.transformers and hub.requests lose capability; queued and dead-lettered envelopes lose
--     their capability key (the worker pool no longer derives from it; llm.* envelopes get
--     pool llm, ADR-0024);
--   * hub.capabilities is dropped.
-- Schema-qualified (00001_baseline.sql).

-- +goose Up
ALTER TABLE hub.integrations ADD COLUMN interactive boolean NOT NULL DEFAULT false;
UPDATE hub.integrations AS i SET interactive = c.interactive
FROM (SELECT DISTINCT ON (capability) capability, interactive FROM hub.capabilities ORDER BY capability, minor DESC) AS c
WHERE c.capability = i.capability;

-- Versions are immutable (trigger integration_versions_immutable); this one-off data move is the
-- only exception, so the trigger is off for the UPDATE alone.
ALTER TABLE hub.integration_versions DISABLE TRIGGER integration_versions_immutable;
UPDATE hub.integration_versions AS v
SET spec = v.spec || jsonb_build_object('operations', ops.operations)
FROM (
    SELECT i.integration_id,
           CASE WHEN jsonb_typeof(c.operations) = 'object' AND c.operations <> '{}'::jsonb THEN (
               SELECT jsonb_object_agg(o.key, jsonb_strip_nulls(jsonb_build_object(
                   'description', o.value->'description',
                   'idempotent', COALESCE(o.value->'idempotent', 'false'::jsonb),
                   'request_schema', jsonb_build_object('body',
                       CASE WHEN jsonb_typeof(o.value->'request_schema') = 'object' THEN o.value->'request_schema'
                            ELSE c.request_schema END))))
               FROM jsonb_each(c.operations) AS o)
           ELSE jsonb_build_object('default', jsonb_build_object('idempotent', false,
               'request_schema', jsonb_build_object('body', c.request_schema)))
           END AS operations
    FROM hub.integrations AS i
    JOIN (SELECT DISTINCT ON (capability) capability, request_schema, operations FROM hub.capabilities ORDER BY capability, minor DESC) AS c
        ON c.capability = i.capability
) AS ops
WHERE ops.integration_id = v.integration_id AND NOT v.spec ? 'operations';
UPDATE hub.integration_versions SET spec = spec - 'capability' WHERE spec ? 'capability';
ALTER TABLE hub.integration_versions ENABLE TRIGGER integration_versions_immutable;

ALTER TABLE hub.integrations DROP COLUMN capability;

ALTER TABLE hub.transformers DROP COLUMN capability;

ALTER TABLE hub.vendor_endpoints
    ADD COLUMN request_schema jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN response_schema jsonb;

ALTER TABLE hub.requests DROP COLUMN capability;

-- An llm.* envelope keeps its pool as the explicit pool key (ADR-0024), so a replay still lands
-- in the llm pool.
UPDATE hub.outbox SET envelope = (envelope - 'capability')
    || CASE WHEN envelope->>'capability' LIKE 'llm.%' THEN '{"pool":"llm"}'::jsonb ELSE '{}'::jsonb END
WHERE envelope ? 'capability';
UPDATE hub.dead_letters SET envelope = (envelope - 'capability')
    || CASE WHEN envelope->>'capability' LIKE 'llm.%' THEN '{"pool":"llm"}'::jsonb ELSE '{}'::jsonb END
WHERE envelope ? 'capability';

DROP TABLE hub.capabilities;

-- +goose Down
-- Restores the columns and the table, not their content: the capability of an integration is
-- gone with the change (production never runs down migrations).
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
ALTER TABLE hub.requests ADD COLUMN capability text NOT NULL DEFAULT '';
ALTER TABLE hub.vendor_endpoints DROP COLUMN response_schema, DROP COLUMN request_schema;
ALTER TABLE hub.transformers ADD COLUMN capability text NOT NULL DEFAULT '';
ALTER TABLE hub.integrations ADD COLUMN capability text NOT NULL DEFAULT '';
ALTER TABLE hub.integration_versions DISABLE TRIGGER integration_versions_immutable;
UPDATE hub.integration_versions SET spec = spec - 'operations' WHERE spec ? 'operations';
ALTER TABLE hub.integration_versions ENABLE TRIGGER integration_versions_immutable;
ALTER TABLE hub.integrations DROP COLUMN interactive;
