//go:build integration

package queue_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	p, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// cleanup removes a test's own rows (the tables are shared with other packages' tests).
func cleanup(t *testing.T, p *pgxpool.Pool, ids ...string) {
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = p.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = ANY($1)`, ids)
		_, _ = p.Exec(ctx, `DELETE FROM hub.dead_letters WHERE request_id = ANY($1)`, ids)
	})
}

// env is an envelope queued in pool ("llm" or anything else for messaging).
func env(pool string, created time.Time) core.Envelope {
	if pool != queue.PoolLLM {
		pool = ""
	}
	return core.Envelope{Pool: pool,
		RequestID: core.NewRequestID(), Tenant: "orders-service", IntegrationID: "int_evt_brandA",
		Operation: "default", Canonical: json.RawMessage(`{"user":{"id":"u_1"},"event":{"name":"e"}}`), Mode: "async",
		NextAttemptAt: created, ExpiresAt: created.Add(6 * time.Hour), CreatedAt: created,
	}
}

// drainOthers makes every pre-existing due envelope of the pool not due, so a test's claims see
// only its own rows (the table is shared with other packages' tests).
func isolate(t *testing.T, p *pgxpool.Pool, pl string) {
	t.Helper()
	if _, err := p.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 day', claimed_until = now() + interval '1 day' WHERE pool = $1`, pl); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClaimersNeverShare(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	q := &queue.Outbox{}
	now := time.Now().Add(-time.Second)
	var ids []string
	for range 200 {
		e := env(queue.PoolLLM, now)
		ids = append(ids, e.RequestID)
		if err := q.Enqueue(t.Context(), p, e); err != nil {
			t.Fatal(err)
		}
	}
	cleanup(t, p, ids...)
	var mu sync.Mutex
	seen := map[string]string{}
	var wg sync.WaitGroup
	for w := range 8 {
		worker := "w" + string(rune('a'+w))
		wg.Go(func() {
			for {
				got, err := q.Claim(t.Context(), p, worker, queue.PoolLLM, 7)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, c := range got {
					if prev, dup := seen[c.RequestID]; dup {
						t.Errorf("%s claimed by %s and %s", c.RequestID, prev, worker)
					}
					seen[c.RequestID] = worker
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != 200 {
		t.Fatalf("claimed %d of 200", len(seen))
	}
}

func TestExpiredVisibilityIsClaimableAgain(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	clock := time.Now()
	q := &queue.Outbox{Visibility: 30 * time.Second, Now: func() time.Time { return clock }}
	e := env(queue.PoolLLM, clock.Add(-time.Second))
	cleanup(t, p, e.RequestID)
	if err := q.Enqueue(t.Context(), p, e); err != nil {
		t.Fatal(err)
	}
	first, err := q.Claim(t.Context(), p, "crashy", queue.PoolLLM, 10)
	if err != nil || len(first) != 1 || first[0].Attempt != 1 || first[0].Status != core.StateAccepted {
		t.Fatalf("%+v %v", first, err)
	}
	if again, _ := q.Claim(t.Context(), p, "other", queue.PoolLLM, 10); len(again) != 0 {
		t.Fatal("claimed while the visibility window is open")
	}
	clock = clock.Add(31 * time.Second) // the worker crashed: its claim expires
	again, err := q.Claim(t.Context(), p, "other", queue.PoolLLM, 10)
	if err != nil || len(again) != 1 || again[0].Crashes != 1 || again[0].Attempt != 2 || again[0].Status != core.StateInFlight {
		t.Fatalf("reclaim %+v %v", again, err)
	}
	// The crashed worker's late outcome is refused; the new owner's is accepted.
	if err := q.Ack(t.Context(), p, e.RequestID, "crashy"); !errors.Is(err, queue.ErrLost) {
		t.Fatalf("late ack: %v", err)
	}
	if err := q.Ack(t.Context(), p, e.RequestID, "other"); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseInArrivalOrder(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	q := &queue.Outbox{}
	base := time.Now().Add(-time.Hour)
	var ids []string
	for i := range 5 {
		e := env(queue.PoolLLM, base.Add(time.Duration(i)*time.Minute))
		e.NextAttemptAt = time.Now().Add(-time.Second)
		ids = append(ids, e.RequestID)
		if err := q.Enqueue(t.Context(), p, e); err != nil {
			t.Fatal(err)
		}
	}
	cleanup(t, p, ids...)
	got, err := q.Claim(t.Context(), p, "w", queue.PoolLLM, 10)
	if err != nil || len(got) != 5 {
		t.Fatalf("%d %v", len(got), err)
	}
	for i := range got { // park in reverse to prove order comes from arrival, not parking
		if err := q.Park(t.Context(), p, got[len(got)-1-i].RequestID, "w", "acct-order", "upload@1"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := q.Release(t.Context(), p, "acct-order", "upload@1", 2)
	if err != nil || n != 2 {
		t.Fatalf("released %d %v", n, err)
	}
	rows, _ := p.Query(t.Context(), `SELECT request_id FROM hub.outbox WHERE request_id = ANY($1) AND status = 'retry_scheduled' ORDER BY request_id`, ids)
	released, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	if !slices.Equal(released, ids[:2]) { // ULIDs sort by creation time
		t.Fatalf("released %v, want the two oldest %v", released, ids[:2])
	}
	if n, _ := q.Release(t.Context(), p, "acct-order", "upload@1", 10); n != 3 {
		t.Fatalf("second release %d", n)
	}
}

func TestGuardedTransitionsAndDeadLetter(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	q := &queue.Outbox{}
	e := env(queue.PoolLLM, time.Now().Add(-time.Second))
	cleanup(t, p, e.RequestID)
	ctx := t.Context()
	if err := q.Enqueue(ctx, p, e); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, p, e); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	// Not claimed yet: every worker transition is refused.
	for name, f := range map[string]func() error{
		"ack":  func() error { return q.Ack(ctx, p, e.RequestID, "w") },
		"nack": func() error { return q.Nack(ctx, p, e.RequestID, "w", time.Now(), "x") },
		"park": func() error { return q.Park(ctx, p, e.RequestID, "w", "a", "e") },
		"dl":   func() error { return q.DeadLetter(ctx, p, e.RequestID, "w", queue.DeadLetter{Reason: "x"}) },
	} {
		if err := f(); !errors.Is(err, queue.ErrLost) {
			t.Errorf("%s before claim: %v", name, err)
		}
	}
	if got, _ := q.Claim(ctx, p, "w", queue.PoolLLM, 1); len(got) != 1 {
		t.Fatal("claim")
	}
	if err := q.Nack(ctx, p, e.RequestID, "w", time.Now().Add(-time.Millisecond), "UPSTREAM_TIMEOUT"); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Claim(ctx, p, "w2", queue.PoolLLM, 1); len(got) != 1 || got[0].Attempt != 2 || got[0].Status != core.StateRetryScheduled {
		t.Fatalf("retry claim %+v", got)
	}
	dl := queue.DeadLetter{Reason: "UPSTREAM_REJECTED", LastError: "event name invalid", AccountID: "ct-brandA-in",
		LastVendorResponse: json.RawMessage(`{"status":400}`)}
	if err := q.DeadLetter(ctx, p, e.RequestID, "w2", dl); err != nil {
		t.Fatal(err)
	}
	var reason string
	var attempts int
	if err := p.QueryRow(ctx, `SELECT reason, attempts FROM hub.dead_letters WHERE request_id = $1`, e.RequestID).Scan(&reason, &attempts); err != nil || reason != "UPSTREAM_REJECTED" || attempts != 2 {
		t.Fatalf("dead letter %s %d %v", reason, attempts, err)
	}
	if err := q.Replay(ctx, p, e.RequestID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := q.Replay(ctx, p, e.RequestID, time.Now().Add(time.Hour)); !errors.Is(err, queue.ErrLost) {
		t.Fatalf("second replay of the same dead letter: %v", err)
	}
	got, _ := q.Claim(ctx, p, "w3", queue.PoolLLM, 1)
	if len(got) != 1 || got[0].Attempt != 1 {
		t.Fatalf("replayed envelope %+v", got)
	}
}

// TestDoubleCompleteOnlyOnce: two actors racing to finish the same envelope (the claimer and a
// reclaimer after an expired claim) produce exactly one terminal transition (HUB-111).
func TestDoubleCompleteOnlyOnce(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	ctx := t.Context()
	for range 20 {
		clock := time.Now()
		q := &queue.Outbox{Now: func() time.Time { return clock }}
		e := env(queue.PoolLLM, clock.Add(-time.Second))
		cleanup(t, p, e.RequestID)
		_ = q.Enqueue(ctx, p, e)
		_, _ = q.Claim(ctx, p, "first", queue.PoolLLM, 1)
		late := &queue.Outbox{Now: func() time.Time { return clock.Add(time.Minute) }}
		_, _ = late.Claim(ctx, p, "second", queue.PoolLLM, 1)
		var wins sync.WaitGroup
		results := make(chan error, 2)
		for _, w := range []string{"first", "second"} {
			wins.Go(func() { results <- q.Ack(ctx, p, e.RequestID, w) })
		}
		wins.Wait()
		close(results)
		ok := 0
		for err := range results {
			if err == nil {
				ok++
			} else if !errors.Is(err, queue.ErrLost) {
				t.Fatal(err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d terminal transitions", ok)
		}
	}
}

func TestExpireOnlyWaitingEnvelopes(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	ctx := t.Context()
	clock := time.Now()
	q := &queue.Outbox{Now: func() time.Time { return clock }}
	waiting, claimed := env(queue.PoolLLM, clock.Add(-2*time.Hour)), env(queue.PoolLLM, clock.Add(-2*time.Hour))
	waiting.ExpiresAt, claimed.ExpiresAt = clock.Add(-time.Minute), clock.Add(-time.Minute)
	waiting.NextAttemptAt = clock.Add(time.Hour)
	cleanup(t, p, waiting.RequestID, claimed.RequestID)
	_ = q.Enqueue(ctx, p, waiting)
	_ = q.EnqueueClaimed(ctx, p, claimed, "worker", 30*time.Second) // wait-mode immediate attempt: in_flight at insert
	due, err := q.DueExpiries(ctx, p, 100)
	ids := make([]string, 0, len(due))
	for _, e := range due {
		ids = append(ids, e.RequestID)
	}
	if err != nil || !slices.Contains(ids, waiting.RequestID) || slices.Contains(ids, claimed.RequestID) {
		t.Fatalf("due %v %v", due, err)
	}
	if err := q.Expire(ctx, p, waiting.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := q.Expire(ctx, p, claimed.RequestID); !errors.Is(err, queue.ErrLost) {
		t.Fatalf("expired a claimed envelope: %v", err)
	}
}

// TestPoolsAreSeparate: an LLM backlog never fills a messaging worker's claims (HUB-148: LLM pool
// exhaustion does not starve messaging workers).
func TestPoolsAreSeparate(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	isolate(t, p, queue.PoolMessaging)
	q := &queue.Outbox{}
	now := time.Now().Add(-time.Second)
	var ids []string
	for range 50 {
		e := env(queue.PoolLLM, now)
		ids = append(ids, e.RequestID)
		_ = q.Enqueue(t.Context(), p, e)
	}
	msg := env(queue.PoolMessaging, now)
	ids = append(ids, msg.RequestID)
	_ = q.Enqueue(t.Context(), p, msg)
	cleanup(t, p, ids...)
	got, err := q.Claim(t.Context(), p, "messaging-worker", queue.PoolMessaging, 1)
	if err != nil || len(got) != 1 || got[0].RequestID != msg.RequestID {
		t.Fatalf("messaging claim behind an LLM backlog: %+v %v", got, err)
	}
}

// TestDeferTarget is HUB-412: an envelope put back over its target's cap is not an attempt,
// keeps the target as its hint, and the put-back is guarded by the claim.
func TestDeferTarget(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolMessaging)
	q := &queue.Outbox{}
	now := time.Now().Add(-time.Second)
	a := env(queue.PoolMessaging, now)
	cleanup(t, p, a.RequestID)
	if err := q.Enqueue(t.Context(), p, a); err != nil {
		t.Fatal(err)
	}
	got, err := q.Claim(t.Context(), p, "w1", queue.PoolMessaging, 1)
	if err != nil || len(got) != 1 || got[0].RequestID != a.RequestID {
		t.Fatalf("claim %+v %v", got, err)
	}
	full := queue.Target{Account: "acct-a", Endpoint: "vendor.send@1"}
	if err := q.DeferTarget(t.Context(), p, a.RequestID, "w1", now, "target at cap", full); err != nil {
		t.Fatal(err)
	}
	var status, account, endpoint string
	var attempt, crashes int
	if err := p.QueryRow(t.Context(), `SELECT status, attempt, crashes, account_id, endpoint_id FROM hub.outbox WHERE request_id = $1`, a.RequestID).
		Scan(&status, &attempt, &crashes, &account, &endpoint); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" || attempt != 0 || crashes != 0 || account != full.Account || endpoint != full.Endpoint {
		t.Fatalf("after DeferTarget: %s attempt %d crashes %d hint %s/%s", status, attempt, crashes, account, endpoint)
	}
	if err := q.DeferTarget(t.Context(), p, a.RequestID, "w1", now, "again", full); !errors.Is(err, queue.ErrLost) {
		t.Fatalf("second DeferTarget: %v, want ErrLost (guarded by the claim)", err)
	}
	got, err = q.Claim(t.Context(), p, "w1", queue.PoolMessaging, 1)
	if err != nil || len(got) != 1 || got[0].Attempt != 1 {
		t.Fatalf("claim after the put-back: %+v %v, want the envelope on its first attempt", got, err)
	}
}

// TestClaimWithinTargetRoom is HUB-412: a claim takes no more of a hinted target's backlog than
// the worker's free slots for it (none at its cap, the rows are left alone), no more than
// perTarget of a target it has nothing in flight for, and unhinted envelopes as usual. A slow
// target at its cap is left out before the LIMIT, so its older backlog does not keep other
// targets' envelopes out of the claim.
func TestClaimWithinTargetRoom(t *testing.T) {
	slow := queue.Target{Account: "acct-slow", Endpoint: "vendor.send@1"}
	idle := queue.Target{Account: "acct-idle", Endpoint: "vendor.send@1"}
	tests := []struct {
		name         string
		n, perTarget int
		rooms        []queue.TargetRoom
		slow         int // slow's envelopes claimed (of 5, all older than the rest)
		idle         int // idle's envelopes claimed (of 3)
		unhinted     int // of 2, the newest
	}{
		{"no limits: oldest first", 10, 0, nil, 5, 3, 2},
		{"no limits, short claim: the oldest backlog wins", 3, 0, nil, 3, 0, 0},
		{"slow at its cap", 10, 2, []queue.TargetRoom{{Target: slow, Slots: 0}}, 0, 2, 2},
		{"slow at its cap, short claim: its backlog is skipped", 3, 0, []queue.TargetRoom{{Target: slow, Slots: 0}}, 0, 3, 0},
		{"slow has one free slot", 10, 2, []queue.TargetRoom{{Target: slow, Slots: 1}}, 1, 2, 2},
		{"room beats perTarget", 10, 1, []queue.TargetRoom{{Target: slow, Slots: 3}, {Target: idle, Slots: 2}}, 3, 2, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := pool(t)
			isolate(t, p, queue.PoolMessaging)
			q := &queue.Outbox{}
			base := time.Now().Add(-time.Minute)
			hinted := map[string]queue.Target{}
			var ids, plain []string
			add := func(target *queue.Target) {
				e := env(queue.PoolMessaging, base.Add(time.Duration(len(ids))*time.Millisecond))
				ids = append(ids, e.RequestID)
				if err := q.Enqueue(t.Context(), p, e); err != nil {
					t.Fatal(err)
				}
				if target == nil {
					plain = append(plain, e.RequestID)
					return
				}
				hinted[e.RequestID] = *target
				if _, err := p.Exec(t.Context(), `UPDATE hub.outbox SET account_id = $2, endpoint_id = $3 WHERE request_id = $1`,
					e.RequestID, target.Account, target.Endpoint); err != nil {
					t.Fatal(err)
				}
			}
			for range 5 {
				add(&slow)
			}
			for range 3 {
				add(&idle)
			}
			add(nil)
			add(nil)
			cleanup(t, p, ids...)
			got, err := q.ClaimWithin(t.Context(), p, "w1", queue.PoolMessaging, tt.n, tt.perTarget, tt.rooms)
			if err != nil {
				t.Fatal(err)
			}
			count := map[queue.Target]int{}
			var unhinted int
			for _, c := range got {
				if target, ok := hinted[c.RequestID]; ok {
					count[target]++
				} else if slices.Contains(plain, c.RequestID) {
					unhinted++
				}
			}
			if count[slow] != tt.slow || count[idle] != tt.idle || unhinted != tt.unhinted {
				t.Fatalf("claimed slow %d idle %d unhinted %d, want %d %d %d", count[slow], count[idle], unhinted, tt.slow, tt.idle, tt.unhinted)
			}
			var left int
			if err := p.QueryRow(t.Context(), `SELECT count(*) FROM hub.outbox WHERE request_id = ANY($1) AND status = 'accepted' AND attempt = 0`, ids).
				Scan(&left); err != nil {
				t.Fatal(err)
			}
			if left != len(ids)-len(got) {
				t.Fatalf("%d rows left untouched, want %d (rows over the room are not claimed)", left, len(ids)-len(got))
			}
		})
	}
}
