//go:build integration

package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Worker and scheduler startup before the first registry snapshot (HUB-406).

func (r *rig) outboxRow(id string) (status string, attempt, crashes int, account string) {
	r.t.Helper()
	var acct *string
	if err := r.db.QueryRow(r.t.Context(), `SELECT status, attempt, crashes, account_id FROM hub.outbox WHERE request_id = $1`, id).
		Scan(&status, &attempt, &crashes, &acct); err != nil {
		r.t.Fatal(err)
	}
	if acct != nil {
		account = *acct
	}
	return status, attempt, crashes, account
}

// start runs the worker loop until the test ends (or stop is called) and returns stop, which
// waits for Run to return.
func (r *rig) start() (stop func()) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.t.Context()))
	done := make(chan struct{})
	go func() { r.w.Run(ctx); close(done) }()
	stop = func() { cancel(); <-done }
	r.t.Cleanup(stop)
	return stop
}

// emptyRig is a rig whose worker has no snapshot yet (it restarted before its first snapshot
// load); it returns the snapshot to store later.
func emptyRig(t *testing.T) (*rig, *registry.Snapshot) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	snap := r.snaps.Snapshot()
	r.snaps.s.Store(nil)
	return r, snap
}

// TestWorkerWaitsForSnapshot is HUB-406: a worker started with a backlog before its first
// snapshot claims nothing (no panic, no crash counted), then delivers everything once the
// snapshot arrives, each on its first attempt.
func TestWorkerWaitsForSnapshot(t *testing.T) {
	r, snap := emptyRig(t)
	r.w.Idle = 10 * time.Millisecond
	var ids []string
	for range 3 {
		ids = append(ids, r.enqueue("int_evt_brandA", 6*time.Hour).RequestID)
	}
	r.start()
	time.Sleep(200 * time.Millisecond) // many claim cycles
	for _, id := range ids {
		if status, attempt, crashes, _ := r.outboxRow(id); status != "accepted" || attempt != 0 || crashes != 0 {
			t.Fatalf("before the snapshot: %s attempt %d crashes %d, want untouched", status, attempt, crashes)
		}
	}
	r.snaps.s.Store(snap)
	waitFor(t, "the backlog to be delivered", 5*time.Second, func() bool {
		for _, id := range ids {
			if s, _ := r.status(id); s != "succeeded" {
				return false
			}
		}
		return true
	})
	for _, id := range ids {
		if _, attempts := r.status(id); attempts != 1 {
			t.Fatalf("%s took %d attempts, want 1", id, attempts)
		}
	}
}

// TestWorkerReleasesClaimWithoutSnapshot is HUB-406's second guard: an envelope claimed while no
// snapshot is loaded is put back (not an attempt, not a crash) instead of panicking.
func TestWorkerReleasesClaimWithoutSnapshot(t *testing.T) {
	r, _ := emptyRig(t)
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	claimed, err := r.w.Queue.Claim(t.Context(), r.db, r.w.Name, queue.PoolMessaging, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim %v %v", claimed, err)
	}
	r.w.handle(t.Context(), claimed[0])
	if status, attempt, crashes, _ := r.outboxRow(env.RequestID); status != "accepted" || attempt != 0 || crashes != 0 {
		t.Fatalf("after handle without a snapshot: %s attempt %d crashes %d", status, attempt, crashes)
	}
	if n := len(r.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("vendor got %d calls", n)
	}
}

// TestDrainWaitsForSnapshot is HUB-406 on the scheduler: before its first snapshot the drain
// knows no account quota, so it releases nothing (it used to release at a 10/s guess); with the
// snapshot it drains as usual.
func TestDrainWaitsForSnapshot(t *testing.T) {
	r, snap := emptyRig(t)
	env := r.enqueueWith("int_evt_brandA", 6*time.Hour, nil, true)
	res := &Resilience{Breakers: upstream.NewBreakers(nil, telemetry.NewMetrics("scheduler", "iso"), r.w.Log), Snapshots: r.snaps,
		Queue: r.w.Queue, Log: r.w.Log, Now: r.now}
	res.RegisterMetrics(prometheus.NewRegistry())
	res.Tick(t.Context(), r.db, time.Second)
	if status, _, _, _ := r.outboxRow(env.RequestID); status != "parked" {
		t.Fatalf("released without a snapshot: %s", status)
	}
	r.snaps.s.Store(snap)
	r.advance(time.Second)
	res.Tick(t.Context(), r.db, time.Second)
	if status, _, _, _ := r.outboxRow(env.RequestID); status != "retry_scheduled" {
		t.Fatalf("with the snapshot: %s, want released", status)
	}
}
