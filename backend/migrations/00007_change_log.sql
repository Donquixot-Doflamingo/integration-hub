-- Change log (TECH_SPEC §6.1, hub-admin ChangeLogEntry, HUB-170). Owned by internal/control.
-- Append-only: an UPDATE or DELETE raises (audited configuration history). Schema-qualified.

-- +goose Up
CREATE TABLE hub.change_log (
    change_id        text PRIMARY KEY CHECK (change_id ~ '^chg_[0-9A-HJKMNP-TV-Z]{26}$'),
    at               timestamptz NOT NULL DEFAULT now(),
    actor            text NOT NULL,
    actor_roles      text[] NOT NULL DEFAULT '{}',
    action           text NOT NULL,
    object_type      text NOT NULL,
    object_id        text NOT NULL,
    reason           text NOT NULL CHECK (length(reason) > 0),
    before           jsonb,
    after            jsonb,
    snapshot_version bigint,
    request_id       text
);
CREATE INDEX change_log_object_idx ON hub.change_log (object_type, object_id, at DESC);
CREATE INDEX change_log_actor_idx ON hub.change_log (actor, at DESC);
CREATE INDEX change_log_at_idx ON hub.change_log (at DESC);

-- +goose StatementBegin
CREATE FUNCTION hub.change_log_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'hub.change_log is append-only';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER change_log_append_only BEFORE UPDATE OR DELETE ON hub.change_log
    FOR EACH ROW EXECUTE FUNCTION hub.change_log_append_only();

-- +goose Down
DROP TABLE IF EXISTS hub.change_log;
DROP FUNCTION IF EXISTS hub.change_log_append_only();
