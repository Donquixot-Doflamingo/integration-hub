package delivery

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// TestWorkerLimits: TECH_SPEC §8.1/§12.1 defaults, and the per-target cap derived from the
// concurrency (a quarter, never above the 64-connection bulkhead, never above concurrency).
func TestWorkerLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                   string
		w                      *Worker
		batch, conc, targetCap int
	}{
		{"messaging defaults", &Worker{Pool: queue.PoolMessaging}, 32, 256, 64},
		{"llm defaults", &Worker{Pool: queue.PoolLLM}, 32, 64, 16},
		{"explicit concurrency derives the cap", &Worker{Pool: queue.PoolMessaging, Concurrency: 16, Batch: 8}, 8, 16, 4},
		{"tiny concurrency still allows one", &Worker{Concurrency: 2}, 32, 2, 1},
		{"large concurrency capped at the bulkhead", &Worker{Concurrency: 1024}, 32, 1024, 64},
		{"explicit cap", &Worker{Concurrency: 16, TargetCap: 10}, 32, 16, 10},
		{"cap never above concurrency", &Worker{Concurrency: 4, TargetCap: 10}, 32, 4, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.w.batch(); got != tt.batch {
				t.Errorf("batch %d, want %d", got, tt.batch)
			}
			if got := tt.w.concurrency(); got != tt.conc {
				t.Errorf("concurrency %d, want %d", got, tt.conc)
			}
			if got := tt.w.targetCap(); got != tt.targetCap {
				t.Errorf("target cap %d, want %d", got, tt.targetCap)
			}
		})
	}
}

// TestWorkerTargetSlots: acquire refuses past the cap per target, other targets are
// independent, rooms lists each busy target's free slots (what the next claim may take), and the
// gauges follow.
func TestWorkerTargetSlots(t *testing.T) {
	t.Parallel()
	w := &Worker{Pool: queue.PoolMessaging, Concurrency: 8, TargetCap: 2}
	w.RegisterMetrics(prometheus.NewRegistry())
	a := queue.Target{Account: "acct-a", Endpoint: "v.send@1"}
	b := queue.Target{Account: "acct-b", Endpoint: "v.send@1"}
	for i := range 2 {
		if !w.acquire(a) {
			t.Fatalf("acquire %d of a refused", i+1)
		}
	}
	if w.acquire(a) {
		t.Fatal("third acquire of a admitted past the cap")
	}
	if !w.acquire(b) {
		t.Fatal("b refused because a is full")
	}
	rooms := w.rooms()
	slices.SortFunc(rooms, func(x, y queue.TargetRoom) int { return strings.Compare(x.Account, y.Account) })
	if want := []queue.TargetRoom{{Target: a, Slots: 0}, {Target: b, Slots: 1}}; !slices.Equal(rooms, want) {
		t.Fatalf("rooms = %v, want %v", rooms, want)
	}
	if got := testutil.ToFloat64(w.metrics.targetInflight.WithLabelValues(a.Account, a.Endpoint)); got != 2 {
		t.Fatalf("worker_target_inflight{a} = %v", got)
	}
	if got := testutil.ToFloat64(w.metrics.inflight.WithLabelValues(queue.PoolMessaging)); got != 3 {
		t.Fatalf("worker_inflight = %v", got)
	}
	w.release(a)
	if rooms := w.rooms(); len(rooms) != 2 || w.InFlight(a.Account, a.Endpoint) != 1 {
		t.Fatalf("after a release: rooms %v, in flight %d", rooms, w.InFlight(a.Account, a.Endpoint))
	}
	w.release(a)
	w.release(b)
	if rooms := w.rooms(); len(rooms) != 0 {
		t.Fatalf("idle targets still listed: %v", rooms)
	}
	if got := testutil.ToFloat64(w.metrics.inflight.WithLabelValues(queue.PoolMessaging)); got != 0 {
		t.Fatalf("worker_inflight = %v after all releases", got)
	}
}

// TestWorkerTargetSlotsConcurrent: under concurrent acquires the cap is never exceeded.
func TestWorkerTargetSlotsConcurrent(t *testing.T) {
	t.Parallel()
	w := &Worker{Concurrency: 64, TargetCap: 5}
	a := queue.Target{Account: "acct-a", Endpoint: "v.send@1"}
	var mu sync.Mutex
	admitted := 0
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if w.acquire(a) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if admitted != 5 || w.InFlight(a.Account, a.Endpoint) != 5 {
		t.Fatalf("admitted %d (in flight %d), want exactly the cap of 5", admitted, w.InFlight(a.Account, a.Endpoint))
	}
}
