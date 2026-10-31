//go:build integration

package delivery

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

func newScheduler(t *testing.T, tr *Transitions) (*Scheduler, func()) {
	t.Helper()
	db, err := pgxpool.New(t.Context(), os.Getenv("HUB_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{DB: db, Queue: tr.Queue, Transitions: &Transitions{DB: db, Queue: tr.Queue, OnTerminal: tr.OnTerminal},
		Log: slog.New(slog.DiscardHandler), Tick: 50 * time.Millisecond, Standby: 200 * time.Millisecond}
	s.RegisterMetrics(prometheus.NewRegistry())
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	stop := func() { cancel(); <-done; db.Close() }
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			stop()
		}
	})
	return s, stop
}

func leading(s *Scheduler) bool { return testutil.ToFloat64(s.leader) == 1 }

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSchedulerLeaderElection: one leader; the standby takes over within 15 s of the leader's
// death (HUB-114), here within its 200 ms poll.
func TestSchedulerLeaderElection(t *testing.T) {
	r := newRig(t)
	a, stopA := newScheduler(t, r.w.Transitions)
	waitFor(t, "a leader", 5*time.Second, func() bool { return leading(a) })
	b, _ := newScheduler(t, r.w.Transitions)
	time.Sleep(500 * time.Millisecond)
	if leading(b) {
		t.Fatal("two schedulers lead at once")
	}
	died := time.Now()
	stopA() // the leader's process ends: its session and lock go with it
	waitFor(t, "the standby to take over", 15*time.Second, func() bool { return leading(b) })
	t.Logf("takeover in %s", time.Since(died).Round(time.Millisecond))
}

func TestSchedulerExpiresWaitingEnvelopes(t *testing.T) {
	r := newRig(t)
	env := r.enqueue("int_evt_brandA", time.Minute)
	// Not due before its TTL: push its next attempt out, so only the scheduler can touch it.
	if _, err := r.db.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 hour', expires_at = now() - interval '1 second' WHERE request_id = $1`, env.RequestID); err != nil {
		t.Fatal(err)
	}
	_, _ = newScheduler(t, &Transitions{DB: r.db, Queue: &queue.Outbox{}, OnTerminal: func(_ context.Context, _ pgx.Tx, ev TerminalEvent) error {
		r.mu.Lock()
		r.ended = append(r.ended, ev)
		r.mu.Unlock()
		return nil
	}})
	waitFor(t, "expiry", 5*time.Second, func() bool { s, _ := r.status(env.RequestID); return s == "expired" })
	r.mu.Lock()
	defer r.mu.Unlock()
	found := false
	for _, ev := range r.ended {
		if ev.Envelope.RequestID == env.RequestID {
			found = ev.State == core.StateExpired && ev.Err.Code == core.CodeExpired
		}
	}
	if !found {
		t.Fatalf("no request.expired terminal event: %+v", r.ended)
	}
}
