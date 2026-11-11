package adminapi

import (
	"context"
	"sync/atomic"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TenantResolverFromSnapshot maps an ISFC tenant UUID to the Hub tenant id through the synced
// copy (Tenant.ISFC.TenantID) of the admin's current snapshot. The index is rebuilt once per
// snapshot, so a request costs one map lookup. A nil snapshot knows no tenant.
func TenantResolverFromSnapshot(snapshot func() *registry.Snapshot) func(context.Context, string) (string, bool) {
	type index struct {
		snap *registry.Snapshot
		byID map[string]string
	}
	var cur atomic.Pointer[index]
	return func(_ context.Context, isfcTenantID string) (string, bool) {
		snap := snapshot()
		if snap == nil {
			return "", false
		}
		ix := cur.Load()
		if ix == nil || ix.snap != snap {
			byID := make(map[string]string, len(snap.Tenants))
			for id, t := range snap.Tenants {
				if t != nil && t.ISFC != nil && t.ISFC.TenantID != "" {
					byID[t.ISFC.TenantID] = id
				}
			}
			ix = &index{snap: snap, byID: byID}
			cur.Store(ix)
		}
		hub, ok := ix.byID[isfcTenantID]
		return hub, ok
	}
}
