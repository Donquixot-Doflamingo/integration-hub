package control

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// RetireGuard counts what still holds an account (DESIGN §6.5): queued envelopes that will be
// sent on a version targeting it without re-resolving (pinned, or bound to a vendor session),
// and open vendor sessions on it. It is the Publisher's Guard.
func RetireGuard(pool *pgxpool.Pool) func(ctx context.Context, snap *registry.Snapshot, account string) (int, int, error) {
	return func(ctx context.Context, snap *registry.Snapshot, account string) (int, int, error) {
		var ids []string
		var versions []int32
		for id, in := range snap.Integrations {
			for n, v := range in.Versions {
				if slices.ContainsFunc(append(slices.Clone(v.Targets), v.Failover...), func(t registry.Target) bool { return t.Account == account }) {
					ids, versions = append(ids, id), append(versions, int32(n)) // #nosec G115 -- integration versions are small
				}
			}
		}
		var pinned int
		if len(ids) > 0 {
			if err := pool.QueryRow(ctx, `
SELECT count(*) FROM hub.outbox o
JOIN unnest($1::text[], $2::int[]) AS v(integration_id, version)
  ON o.integration_id = v.integration_id AND (o.envelope->>'resolved_version')::int = v.version
WHERE (o.envelope->>'pinned')::boolean OR o.envelope->>'session_id' IS NOT NULL`, ids, versions).Scan(&pinned); err != nil {
				return 0, 0, fmt.Errorf("retire guard: count pinned requests: %w", err)
			}
		}
		open, err := sessions.CountOpen(ctx, pool, account)
		if err != nil {
			return 0, 0, fmt.Errorf("retire guard: %w", err)
		}
		return pinned, open, nil
	}
}
