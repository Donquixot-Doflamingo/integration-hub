-- name: UpsertTenant :exec
INSERT INTO hub.tenants (tenant_id, status, rate_limit_rps, rate_limit_burst)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id) DO UPDATE
SET status = EXCLUDED.status, rate_limit_rps = EXCLUDED.rate_limit_rps,
    rate_limit_burst = EXCLUDED.rate_limit_burst, updated_at = now();

-- name: InsertIntegration :exec
INSERT INTO hub.integrations (integration_id, owner_tenant, capability, active_version, paused, description, moves_money_or_quota)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: InsertIntegrationVersion :exec
INSERT INTO hub.integration_versions (integration_id, version, spec, author, reason)
VALUES ($1, $2, $3, $4, $5);

-- name: ListIntegrationVersions :many
SELECT integration_id, version, spec, author, reason, published_at
FROM hub.integration_versions
WHERE integration_id = $1
ORDER BY version;

-- name: SetActiveVersion :execrows
-- Rollback and cutover move the pointer; versions themselves never change (DESIGN §6.4).
UPDATE hub.integrations
SET active_version = $2, updated_at = now()
WHERE integration_id = $1;

-- name: InsertTransformer :exec
INSERT INTO hub.transformers (transformer_id, version, kind, capability, endpoint_id, yaml, fixtures)
VALUES ($1, $2, $3, $4, $5, $6, $7);
