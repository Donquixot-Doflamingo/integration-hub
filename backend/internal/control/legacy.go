package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LegacyUpgradeReason is the reason recorded on the archive version UpgradeLegacyArchive writes.
const LegacyUpgradeReason = "ADR-0023: capabilities folded into integration operations (HUB-434)"

// UpgradeLegacyArchive brings the stored registry past ADR-0023 once, on the admin role at boot
// (migration 00011 handles the relational side):
//
//   - drafts and pending integrations written before the change are upgraded in place: a draft
//     without operations gets them from its integration's capability, every capability field is
//     removed and integration headers gain interactive (registry.Legacy);
//   - the latest archived snapshot, when it is a legacy blob (registry.Unpack upgrades it in
//     memory on every load), is archived again in the current layout under the same author, so
//     nodes and the config-directory rule (ADR-0010) see the same history.
//
// Drafts go first: a crash in between leaves a legacy archive, and the next boot redoes both
// (an upgraded draft is left alone). It reports the archive version written, if any, and the
// drafts it could not upgrade (no operations and no known capability: they stay as they are and
// fail validation until edited).
func UpgradeLegacyArchive(ctx context.Context, pool *pgxpool.Pool, a *Archive, now time.Time) (version int64, skipped []string, err error) {
	snap, src, _, err := a.Latest(ctx, now)
	if errors.Is(err, ErrNoSnapshot) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, err
	}
	if snap.Legacy == nil {
		return 0, nil, nil
	}
	meta, err := a.LatestMeta(ctx)
	if err != nil {
		return 0, nil, err
	}
	if len(snap.Legacy.Capabilities) > 0 {
		if skipped, err = upgradeLegacyRows(ctx, pool, snap.Legacy.IntegrationCapability, snap.Legacy); err != nil {
			return 0, nil, err
		}
	}
	v, _, err := a.Publish(ctx, src, snap, meta.Author, LegacyUpgradeReason)
	if err != nil {
		return 0, skipped, fmt.Errorf("re-archive the upgraded snapshot: %w", err)
	}
	return v, skipped, nil
}

// legacyDocs is what upgradeLegacyRows needs from registry.Legacy.
type legacyDocs interface {
	UpgradeIntegrationDoc(doc []byte) ([]byte, string, error)
	UpgradeVersionDoc(doc []byte, capability string) ([]byte, error)
}

func upgradeLegacyRows(ctx context.Context, pool *pgxpool.Pool, capabilityOf map[string]string, lg legacyDocs) ([]string, error) {
	var skipped []string
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		skipped = nil
		known := map[string]string{}
		for id, c := range capabilityOf {
			known[id] = c
		}
		pending, err := ListPending(ctx, tx)
		if err != nil {
			return err
		}
		for id, p := range pending {
			header, key, err := lg.UpgradeIntegrationDoc(p.Header)
			if err != nil {
				return fmt.Errorf("pending integration %s: %w", id, err)
			}
			if key == "" {
				continue // already upgraded
			}
			known[id] = key
			if _, err := tx.Exec(ctx, `UPDATE hub.integration_pending SET header = $2 WHERE integration_id = $1`, id, string(header)); err != nil {
				return fmt.Errorf("pending integration %s: %w", id, err)
			}
		}
		drafts, err := ListDrafts(ctx, tx)
		if err != nil {
			return err
		}
		for id, d := range drafts {
			spec, err := lg.UpgradeVersionDoc(d.Spec, known[id])
			if err != nil {
				skipped = append(skipped, id)
				continue
			}
			if string(spec) == string(d.Spec) {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE hub.integration_drafts SET spec = $2 WHERE integration_id = $1`, id, string(spec)); err != nil {
				return fmt.Errorf("draft of %s: %w", id, err)
			}
		}
		return nil
	})
	return skipped, err
}
