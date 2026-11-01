package resolve

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func snapshot(targets ...registry.Target) *registry.Snapshot {
	v1 := &registry.IntegrationVersion{Version: 1, Targets: []registry.Target{{Account: "acct-a", Weight: 100}}, Delivery: registry.Delivery{Mode: registry.ModeSync}}
	v2 := &registry.IntegrationVersion{Version: 2, Targets: targets, Delivery: registry.Delivery{Mode: registry.ModeWait, WaitMS: 3000},
		Failover: []registry.Target{{Account: "acct-fo"}}}
	in := &registry.Integration{
		IntegrationFile: registry.IntegrationFile{Integration: "int_x", OwnerTenant: "orders-service", Grants: []string{"billing"}, ActiveVersion: 2},
		Versions:        map[int]*registry.IntegrationVersion{1: v1, 2: v2},
	}
	return &registry.Snapshot{Version: 9, Integrations: map[string]*registry.Integration{"int_x": in}}
}

func TestResolveVersionsAndGrants(t *testing.T) {
	snap := snapshot(registry.Target{Account: "acct-a", Weight: 100})
	r, err := Resolve(snap, "orders-service", "int_x", 0, "k")
	if err != nil || r.VersionNumber != 2 || r.SnapshotVersion != 9 || r.Delivery.WaitMS != 3000 || len(r.Failover) != 1 {
		t.Fatalf("active: %+v %v", r, err)
	}
	if r, err := Resolve(snap, "billing", "int_x", 1, "k"); err != nil || r.VersionNumber != 1 {
		t.Fatalf("grantee pinned v1: %+v %v", r, err)
	}
	tests := []struct {
		tenant, id string
		version    int
		want       error
	}{
		{"orders-service", "int_missing", 0, ErrIntegrationNotFound},
		{"marketing", "int_x", 0, ErrNotGranted},
		{"orders-service", "int_x", 7, ErrVersionNotFound},
	}
	for _, tt := range tests {
		if _, err := Resolve(snap, tt.tenant, tt.id, tt.version, "k"); !errors.Is(err, tt.want) {
			t.Errorf("%s/%s/v%d: %v, want %v", tt.tenant, tt.id, tt.version, err, tt.want)
		}
	}
}

func TestWeightedChoiceIsStickyAndProportional(t *testing.T) {
	snap := snapshot(registry.Target{Account: "acct-a", Weight: 90}, registry.Target{Account: "acct-b", Weight: 10})
	first, _ := Resolve(snap, "orders-service", "int_x", 0, "order-8821-created")
	for range 100 {
		r, _ := Resolve(snap, "orders-service", "int_x", 0, "order-8821-created")
		if r.TargetIndex != first.TargetIndex {
			t.Fatal("same route key picked a different target")
		}
	}
	const n = 100_000
	counts := [2]int{}
	for i := range n {
		r, err := Resolve(snap, "orders-service", "int_x", 0, fmt.Sprintf("req_%08d", i))
		if err != nil {
			t.Fatal(err)
		}
		counts[r.TargetIndex]++
	}
	if share := float64(counts[1]) / n; math.Abs(share-0.10) > 0.02 {
		t.Fatalf("10%% target got %.3f of %d keys", share, n)
	}
}

// TestWeightChangesOnlyMoveTheShiftedBuckets is HUB-173: across a 90/10 → 50/50 → 0/100 cutover
// a key only ever moves from A to B, never back, so callers see one switch at most.
func TestWeightChangesOnlyMoveTheShiftedBuckets(t *testing.T) {
	steps := []*registry.Snapshot{
		snapshot(registry.Target{Account: "acct-a", Weight: 90}, registry.Target{Account: "acct-b", Weight: 10}),
		snapshot(registry.Target{Account: "acct-a", Weight: 50}, registry.Target{Account: "acct-b", Weight: 50}),
		snapshot(registry.Target{Account: "acct-a", Weight: 0}, registry.Target{Account: "acct-b", Weight: 100}),
	}
	const n = 20_000
	moved := 0
	for i := range n {
		key := fmt.Sprintf("req_%08d", i)
		prev := ""
		for s, snap := range steps {
			r, err := Resolve(snap, "orders-service", "int_x", 0, key)
			if err != nil {
				t.Fatal(err)
			}
			acct := r.Target.Account
			if prev == "acct-b" && acct == "acct-a" {
				t.Fatalf("key %s moved back from B to A at step %d", key, s)
			}
			if prev == "acct-a" && acct == "acct-b" && s == 1 {
				moved++
			}
			prev = acct
		}
		if prev != "acct-b" {
			t.Fatalf("key %s not on B after the cutover", key)
		}
	}
	// 90/10 → 50/50 moves the 40 buckets in between, and only those.
	if share := float64(moved) / n; math.Abs(share-0.40) > 0.02 {
		t.Fatalf("90/10 → 50/50 moved %.3f of keys, want 0.40", share)
	}
}

func TestZeroWeightTargetIsNeverPicked(t *testing.T) {
	snap := snapshot(registry.Target{Account: "acct-a", Weight: 100}, registry.Target{Account: "acct-b", Weight: 0})
	for i := range 10_000 {
		if r, _ := Resolve(snap, "orders-service", "int_x", 0, fmt.Sprint(i)); r.TargetIndex != 0 {
			t.Fatal("zero-weight target picked")
		}
	}
}

// BenchmarkResolve: budget 0.05 ms p99 (DESIGN §18.6).
func BenchmarkResolve(b *testing.B) {
	snap := snapshot(registry.Target{Account: "acct-a", Weight: 90}, registry.Target{Account: "acct-b", Weight: 10})
	durations := make([]time.Duration, 0, 1<<16)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		start := time.Now()
		if _, err := Resolve(snap, "orders-service", "int_x", 0, fmt.Sprint(i)); err != nil {
			b.Fatal(err)
		}
		durations = append(durations, time.Since(start))
		i++
	}
	slices.Sort(durations)
	p99 := durations[len(durations)*99/100]
	b.ReportMetric(float64(p99.Nanoseconds()), "p99-ns")
	if p99 > 50*time.Microsecond {
		b.Errorf("p99 %s exceeds 0.05 ms", p99)
	}
}
