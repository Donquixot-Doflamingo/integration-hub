package isfcsync

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// KeyRow is one row of hub.isfc_caller_keys.
type KeyRow struct {
	ISFCKeyID, KeyHash, CallerClientID string
	TenantIDs                          []string // ISFC tenant UUIDs
	Permissions                        []string
	Status                             string // ACTIVE | INACTIVE | REVOKED
}

// Key statuses in hub.isfc_caller_keys.
const (
	KeyActive   = "ACTIVE"
	KeyInactive = "INACTIVE"
	KeyRevoked  = "REVOKED"
)

// KeyStore persists the caller keys.
type KeyStore interface {
	// ReplaceCallerKeys makes the table equal rows: new or changed rows are upserted, rows that
	// were ACTIVE and are no longer present are marked REVOKED (kept for audit). It reports whether
	// anything changed and the number of keys per status afterwards.
	ReplaceCallerKeys(ctx context.Context, rows []KeyRow) (changed bool, counts map[string]int, err error)
}

// CallerTenants resolves the interim caller-to-tenant mapping (AUTH_PLAN G1, D5): the ISFC tenant
// UUIDs of every tenant that lists a caller client in isfc_caller_clients, by client id. It goes
// away when ISFC-CR-1 puts tenants on the keys themselves.
func CallerTenants(snap *registry.Snapshot) map[string][]string {
	out := map[string][]string{}
	for _, t := range snap.Tenants {
		if t.ISFC == nil {
			continue
		}
		for _, c := range t.ISFCCallerClients {
			out[c] = append(out[c], strings.ToLower(t.ISFC.TenantID))
		}
	}
	for c := range out {
		slices.Sort(out[c])
		out[c] = slices.Compact(out[c])
	}
	return out
}

// KeySync is the caller-key step of a reconcile (HUB-483): it reads the keys the Hub backend
// client issued, hashes them on read (the isfc client does it while decoding, so the plaintext
// never gets here), stores them and tells the gateways. A key that disappears from ISFC's
// configuration (revoked, deactivated) becomes REVOKED, and gateways drop it on their next load.
type KeySync struct {
	Source          Source
	Store           KeyStore
	BackendClientID string
	// Notify tells gateway nodes the keys changed (Redis PUBLISH hub:isfc:keys); may be nil.
	Notify  func(ctx context.Context)
	Metrics *Metrics
	Log     *slog.Logger
}

// SyncKeys implements KeySyncer. snap is the tenant snapshot after this run's tenant changes.
func (k *KeySync) SyncKeys(ctx context.Context, snap *registry.Snapshot) error {
	cfg, err := k.Source.ClientConfiguration(ctx, k.BackendClientID)
	if err != nil {
		return fmt.Errorf("client configuration: %w", err)
	}
	tenants := CallerTenants(snap)
	rows := make([]KeyRow, 0, len(cfg.Keys))
	for _, key := range cfg.Keys {
		status := strings.ToUpper(key.Status)
		if status != KeyActive {
			status = KeyInactive
		}
		rows = append(rows, KeyRow{ISFCKeyID: key.ID, KeyHash: key.KeyHash, CallerClientID: key.ClientID,
			TenantIDs: tenants[key.ClientID], Permissions: key.Permissions, Status: status})
	}
	changed, counts, err := k.Store.ReplaceCallerKeys(ctx, rows)
	if err != nil {
		return fmt.Errorf("store caller keys: %w", err)
	}
	if k.Metrics != nil {
		for _, s := range []string{KeyActive, KeyInactive, KeyRevoked} {
			k.Metrics.CallerKeys.WithLabelValues(s).Set(float64(counts[s]))
		}
	}
	if changed {
		if k.Log != nil {
			k.Log.InfoContext(ctx, "isfc.sync caller keys changed", slog.Int("active", counts[KeyActive]), slog.Int("revoked", counts[KeyRevoked]))
		}
		if k.Notify != nil {
			k.Notify(ctx)
		}
	}
	return nil
}

// ReplaceCallerKeys implements KeyStore in one transaction.
func (s PGStore) ReplaceCallerKeys(ctx context.Context, rows []KeyRow) (bool, map[string]int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	cur := map[string]KeyRow{}
	res, err := tx.Query(ctx, `SELECT isfc_key_id, key_hash, caller_client_id, tenant_ids::text[], permissions, status FROM hub.isfc_caller_keys FOR UPDATE`)
	if err != nil {
		return false, nil, err
	}
	for res.Next() {
		var r KeyRow
		if err := res.Scan(&r.ISFCKeyID, &r.KeyHash, &r.CallerClientID, &r.TenantIDs, &r.Permissions, &r.Status); err != nil {
			res.Close()
			return false, nil, err
		}
		cur[r.ISFCKeyID] = r
	}
	if err := res.Err(); err != nil {
		return false, nil, err
	}
	changed := false
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ISFCKeyID] = true
		r.TenantIDs, r.Permissions = nonNil(r.TenantIDs), nonNil(r.Permissions)
		if old, ok := cur[r.ISFCKeyID]; ok && sameRow(old, r) {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hub.isfc_caller_keys (isfc_key_id, key_hash, caller_client_id, tenant_ids, permissions, status, updated_at)
			VALUES ($1, $2, $3, $4::text[]::uuid[], $5, $6, now())
			ON CONFLICT (isfc_key_id) DO UPDATE SET key_hash = EXCLUDED.key_hash, caller_client_id = EXCLUDED.caller_client_id,
				tenant_ids = EXCLUDED.tenant_ids, permissions = EXCLUDED.permissions, status = EXCLUDED.status, updated_at = now()`,
			r.ISFCKeyID, r.KeyHash, r.CallerClientID, r.TenantIDs, r.Permissions, r.Status); err != nil {
			return false, nil, err
		}
		changed = true
		cur[r.ISFCKeyID] = r
	}
	for id, old := range cur {
		if seen[id] || old.Status == KeyRevoked {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE hub.isfc_caller_keys SET status = 'REVOKED', updated_at = now() WHERE isfc_key_id = $1`, id); err != nil {
			return false, nil, err
		}
		old.Status = KeyRevoked
		cur[id] = old
		changed = true
	}
	counts := map[string]int{}
	for _, r := range cur {
		counts[r.Status]++
	}
	if err := tx.Commit(ctx); err != nil {
		return false, nil, err
	}
	return changed, counts, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func sameRow(a, b KeyRow) bool {
	return a.KeyHash == b.KeyHash && a.CallerClientID == b.CallerClientID && a.Status == b.Status &&
		slices.Equal(a.TenantIDs, b.TenantIDs) && slices.Equal(a.Permissions, b.Permissions)
}

// ActiveCallerKeys reads the ACTIVE keys as gateway entries.
func (s PGStore) ActiveCallerKeys(ctx context.Context) ([]isfc.CallerEntry, error) {
	rows, err := s.Pool.Query(ctx, `SELECT isfc_key_id, key_hash, caller_client_id, tenant_ids::text[], permissions
		FROM hub.isfc_caller_keys WHERE status = 'ACTIVE'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []isfc.CallerEntry
	for rows.Next() {
		var e isfc.CallerEntry
		var perms []string
		if err := rows.Scan(&e.KeyID, &e.Hash, &e.ClientID, &e.TenantIDs, &perms); err != nil {
			return nil, err
		}
		for i := range e.TenantIDs {
			e.TenantIDs[i] = strings.ToLower(e.TenantIDs[i])
		}
		e.Scopes = isfc.ScopesFromPermissions(perms)
		out = append(out, e)
	}
	return out, rows.Err()
}

// LoadKeyMap reloads km from the table. On an error km keeps what it has (the last good map).
func LoadKeyMap(ctx context.Context, pool *pgxpool.Pool, km *isfc.KeyMap) error {
	entries, err := PGStore{Pool: pool}.ActiveCallerKeys(ctx)
	if err != nil {
		return err
	}
	km.Replace(entries)
	return nil
}
