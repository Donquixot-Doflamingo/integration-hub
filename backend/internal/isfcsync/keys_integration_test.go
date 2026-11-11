//go:build integration

package isfcsync_test

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TestCallerKeysFollowISFC covers HUB-483: keys are hashed on read into hub.isfc_caller_keys,
// gateways rebuild their map from the table without a snapshot, and a key revoked in ISFC is gone
// from the map after one reconcile.
func TestCallerKeysFollowISFC(t *testing.T) {
	r := newRig(t)
	hl := r.mock.AddTenant("HL", "Home Loans")
	r.mock.SetClientTenants(console, hl)
	if _, err := r.pool.Exec(t.Context(), `DELETE FROM hub.isfc_caller_keys`); err != nil {
		t.Fatal(err)
	}
	var pings atomic.Int32
	r.rec.Keys = &isfcsync.KeySync{Source: r.rec.Source, Store: isfcsync.PGStore{Pool: r.pool}, BackendClientID: hubClient, Metrics: r.m,
		Notify: func(context.Context) { pings.Add(1) }}
	r.run(isfcsync.TriggerManual, isfcsync.RunOptions{Reason: "create the tenant"})

	// Interim D5: the tenant lists the ISFC caller clients that belong to it.
	if _, err := r.pub.Edit(t.Context(), func(tr control.Tree, _ *registry.Snapshot) (control.Change, error) {
		doc, _ := tr.Get("tenants/hl-t.yaml")
		tr.Put("tenants/hl-t.yaml", append(doc, []byte("isfc_caller_clients: [billing-backend]\n")...))
		return control.Change{Actor: "test", Action: "updateTenant", ObjectType: "tenant", ObjectID: "hl-t", Reason: "list the caller client"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	billingID, billingKey := r.mock.IssueKey(hubClient, "billing-backend", "HUB:INVOKE", "HUB:REQUESTS_READ", "SOMETHING:ELSE")
	_, strayKey := r.mock.IssueKey(hubClient, "stray-backend", "HUB:INVOKE")

	v := r.snapshot().Version
	pings.Store(0)
	if res := r.run(isfcsync.TriggerTick, isfcsync.RunOptions{}); res.Outcome != "ok" {
		t.Fatalf("run %+v", res)
	}
	if r.snapshot().Version != v {
		t.Fatal("a key change published a snapshot")
	}
	if pings.Load() != 1 {
		t.Fatalf("pings %d, want 1", pings.Load())
	}
	// The table holds hashes, never the keys.
	var leaked int
	if err := r.pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.isfc_caller_keys WHERE key_hash IN ($1, $2) OR key_hash NOT LIKE 'sha256:%'`, billingKey, strayKey).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("plaintext in the table: %d %v", leaked, err)
	}

	var km isfc.KeyMap
	if km.Loaded() {
		t.Fatal("loaded before any load")
	}
	if err := isfcsync.LoadKeyMap(t.Context(), r.pool, &km); err != nil {
		t.Fatal(err)
	}
	e, ok := km.Lookup(registry.HashKey(billingKey))
	if !ok || e.KeyID != billingID || e.ClientID != "billing-backend" || !slices.Equal(e.TenantIDs, []string{hl}) ||
		strings.Join(e.Scopes, ",") != "invoke,requests_read" {
		t.Fatalf("billing entry %+v", e)
	}
	if s, ok := km.Lookup(registry.HashKey(strayKey)); !ok || len(s.TenantIDs) != 0 {
		t.Fatalf("a caller no tenant lists covers no tenant: %+v %v", s, ok)
	}

	// An unchanged run pings nobody.
	pings.Store(0)
	r.run(isfcsync.TriggerTick, isfcsync.RunOptions{})
	if pings.Load() != 0 {
		t.Fatalf("idle run pinged %d times", pings.Load())
	}

	// ISFC down: the keys stay.
	r.mock.SetDown(true)
	if res := r.run(isfcsync.TriggerTick, isfcsync.RunOptions{}); res.Outcome != "failed" {
		t.Fatalf("down %+v", res)
	}
	r.mock.SetDown(false)
	if err := isfcsync.LoadKeyMap(t.Context(), r.pool, &km); err != nil {
		t.Fatal(err)
	}
	if _, ok := km.Lookup(registry.HashKey(billingKey)); !ok {
		t.Fatal("a failed run dropped a key")
	}

	// Revoked in ISFC: after one reconcile the key is REVOKED and out of the gateway map.
	r.mock.RevokeKey(billingID)
	if res := r.run(isfcsync.TriggerEvent, isfcsync.RunOptions{}); res.Outcome != "ok" {
		t.Fatalf("run %+v", res)
	}
	if pings.Load() != 1 {
		t.Fatalf("revocation pings %d", pings.Load())
	}
	if err := isfcsync.LoadKeyMap(t.Context(), r.pool, &km); err != nil {
		t.Fatal(err)
	}
	if _, ok := km.Lookup(registry.HashKey(billingKey)); ok {
		t.Fatal("revoked key still in the map after one reconcile")
	}
	var status string
	if err := r.pool.QueryRow(t.Context(), `SELECT status FROM hub.isfc_caller_keys WHERE isfc_key_id = $1`, billingID).Scan(&status); err != nil || status != "REVOKED" {
		t.Fatalf("status %q %v", status, err)
	}
	if _, ok := km.Lookup(registry.HashKey(strayKey)); !ok {
		t.Fatal("an unrelated key was dropped")
	}
}
