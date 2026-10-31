-- name: InsertSnapshot :one
INSERT INTO hub.config_snapshots (version, checksum, blob_ref, author, reason, diff)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING version, checksum, blob_ref, author, reason, created_at;

-- name: LatestSnapshot :one
SELECT version, checksum, blob_ref, author, reason, created_at
FROM hub.config_snapshots
ORDER BY version DESC
LIMIT 1;

-- name: GetSnapshot :one
SELECT version, checksum, blob_ref, author, reason, created_at
FROM hub.config_snapshots
WHERE version = $1;

-- name: PruneSnapshots :execrows
-- Keeps the newest snapshot even when it is older than the cut-off.
DELETE FROM hub.config_snapshots AS s
WHERE s.created_at < @older_than::timestamptz
  AND s.version < (SELECT max(n.version) FROM hub.config_snapshots AS n);
