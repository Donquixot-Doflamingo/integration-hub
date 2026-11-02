-- Integration drafts and created-but-unpublished integrations (HUB-205, hub-admin
-- createIntegration / createIntegrationDraft). The config tree (snapshot archive, ADR-0010) holds
-- only what nodes run: a draft, and an integration without a published version, stay here until
-- their first publish moves them into the tree. Schema-qualified.

-- +goose Up
CREATE TABLE hub.integration_drafts (
    integration_id      text PRIMARY KEY,                -- at most one draft per integration
    spec                text NOT NULL,                   -- registry version document (YAML), defaults filled
    based_on            integer CHECK (based_on > 0),
    copied_from_version integer CHECK (copied_from_version > 0),
    author              text NOT NULL,
    reason              text NOT NULL CHECK (length(reason) > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE hub.integration_pending (
    integration_id text PRIMARY KEY,
    header         text NOT NULL,                        -- integration.yaml published with version 1
    author         text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Snapshot → change lookups (listSnapshots, getSnapshot).
CREATE INDEX change_log_snapshot_idx ON hub.change_log (snapshot_version) WHERE snapshot_version IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS hub.change_log_snapshot_idx;
DROP TABLE IF EXISTS hub.integration_pending;
DROP TABLE IF EXISTS hub.integration_drafts;
