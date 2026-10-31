-- Baseline (HUB-023). Real tables arrive with the tickets that own them (TECH_SPEC §4.3).

-- +goose Up
CREATE SCHEMA IF NOT EXISTS hub;
COMMENT ON SCHEMA hub IS 'Integration Hub registry, ledger, outbox and audit tables';

-- +goose Down
DROP SCHEMA IF EXISTS hub;
