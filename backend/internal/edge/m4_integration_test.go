//go:build integration

package edge

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

func redisAddr() string {
	if a := os.Getenv("HUB_REDIS_ADDR"); a != "" {
		return a
	}
	return "localhost:6379"
}

// terminalCounter counts OnTerminal calls per request (what callbacks would send).
type terminalCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *terminalCounter) hook(_ context.Context, _ pgx.Tx, ev delivery.TerminalEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[ev.Envelope.RequestID]++
	return nil
}

func (c *terminalCounter) count(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[id]
}

// TestAsyncAuditTimeline is HUB-117: the stored timeline of an async request runs from accept
// to the terminal state, the requests row carries queue_wait_ms, and the terminal record,
// status and callbacks commit atomically.
func TestAsyncAuditTimeline(t *testing.T) {
	var writer *audit.Writer
	q := newQueuedRig(t, "", nil, func(d *Deps, w *delivery.Worker) {
		writer = audit.NewWriter(audit.PGStore{Pool: w.DB}, telemetry.NewMetrics("itest", "audit"), audit.WriterOptions{FlushEvery: 10 * time.Millisecond})
		d.Audit, w.Audit = writer, writer
	})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { writer.Run(ctx); close(done) }()
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	_, body := q.invoke(t, "int_evt_brandB", trackBody)
	id := body["request_id"].(string)
	time.Sleep(20 * time.Millisecond) // some queue wait
	if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	cancel()
	<-done // drain

	rows, err := q.db.Query(t.Context(), `SELECT type FROM hub.request_events WHERE request_id = $1 ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	types, _ := pgx.CollectRows(rows, pgx.RowTo[string])
	got := strings.Join(types, ",")
	want := "request.received,request.validated,integration.resolved,request.persisted,policy.applied,request.accepted," +
		"integration.resolved,request.transformed,vendor.sent,vendor.responded,response.transformed,request.completed"
	if got != want {
		t.Fatalf("timeline\n got %s\nwant %s", got, want)
	}
	var status string
	var wait *float64
	from, _ := core.RequestIDTime(id)
	if err := q.db.QueryRow(t.Context(), `SELECT status, queue_wait_ms FROM hub.requests WHERE request_id = $1 AND created_at BETWEEN $2 AND $3`,
		id, from.Add(-time.Minute), from.Add(time.Minute)).Scan(&status, &wait); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || wait == nil || *wait < 20 {
		t.Fatalf("record status %s queue_wait_ms %v", status, wait)
	}
}

// TestTerminalCommitIsAtomic: if anything in the terminal transaction fails (here the callback
// hook), neither the outbox ack nor the status change nor the result is committed.
func TestTerminalCommitIsAtomic(t *testing.T) {
	failHook := func(context.Context, pgx.Tx, delivery.TerminalEvent) error { return errors.New("callback store down") }
	q := newQueuedRig(t, "", nil, func(_ *Deps, w *delivery.Worker) { w.Transitions.OnTerminal = failHook })
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	_, body := q.invoke(t, "int_evt_brandB", trackBody)
	id := body["request_id"].(string)
	_, _ = q.worker.ProcessOnce(t.Context())
	if _, st := q.status(t, devKey, id); st["status"] != "in_flight" || st["result"] != nil {
		t.Fatalf("a failed terminal transaction leaked: %v", st)
	}
	var results int
	_ = q.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.request_results WHERE request_id = $1`, id).Scan(&results)
	if results != 0 {
		t.Fatal("result committed without the status change")
	}
}

// TestWorkerCrashDeliveredOnceToTheCaller (HUB-122): a worker claims, calls the vendor, and dies
// before settling. After the visibility timeout another worker reclaims and delivers. The vendor
// may see the request twice (at-least-once); the caller sees one terminal state and one
// callback.
func TestWorkerCrashDeliveredOnceToTheCaller(t *testing.T) {
	counter := &terminalCounter{}
	q := newQueuedRig(t, "", nil, func(_ *Deps, w *delivery.Worker) { w.Transitions.OnTerminal = counter.hook })
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	_, body := q.invoke(t, "int_evt_brandB", trackBody)
	id := body["request_id"].(string)

	doomedQueue := &queue.Outbox{Visibility: 300 * time.Millisecond}
	claimed, err := doomedQueue.Claim(t.Context(), q.db, "doomed", queue.PoolMessaging, 10)
	if err != nil || len(claimed) != 1 || claimed[0].RequestID != id {
		t.Fatalf("doomed claim %+v %v", claimed, err)
	}
	// The doomed worker sends to the vendor, then crashes before any transition.
	snap := q.worker.Snapshots.Snapshot()
	res := q.worker.Attempter.Run(t.Context(), delivery.Job{RequestID: id, Snap: snap,
		Res: mustResolve(t, snap, "int_evt_brandB"), Body: map[string]any{"user": map[string]any{"id": "u_1"}, "event": map[string]any{"name": "e"}},
		Trail: audit.NewTrail(audit.TrailOptions{RequestID: id}), Timings: &telemetry.Timings{}, FirstAttempt: 1})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if n, _ := q.worker.ProcessOnce(t.Context()); n != 0 {
		t.Fatal("reclaimed before the visibility timeout")
	}
	time.Sleep(400 * time.Millisecond)
	if n, _ := q.worker.ProcessOnce(t.Context()); n != 1 {
		t.Fatal("not reclaimed after the visibility timeout")
	}
	if _, st := q.status(t, devKey, id); st["status"] != "succeeded" {
		t.Fatalf("status %v", st)
	}
	if c := counter.count(id); c != 1 {
		t.Fatalf("%d terminal events (callbacks) for one request", c)
	}
	t.Logf("vendor calls: %d (at-least-once), terminal events: 1", len(q.mv.Requests(t, nil)))
}

func mustResolve(t *testing.T, snap *registry.Snapshot, id string) (r resolveResult) {
	t.Helper()
	res, err := resolveFor(snap, id)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestDuplicateSubmissionsOneVendorCall (HUB-122): concurrent duplicates with one key reach the
// vendor once, in sync, wait and async mode.
func TestDuplicateSubmissionsOneVendorCall(t *testing.T) {
	for _, mode := range []registry.Mode{registry.ModeSync, registry.ModeWait, registry.ModeAsync} {
		t.Run(string(mode), func(t *testing.T) {
			q := newQueuedRig(t, redisAddr(), func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
				if id == "int_evt_brandA" {
					v.Delivery.Mode = mode
					if mode != registry.ModeWait {
						v.Delivery.WaitMS = 0
					}
				}
			})
			q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 150}})
			key := "dup-" + core.NewULID(time.Now())
			var ok, conflict atomic.Int32
			var wg sync.WaitGroup
			for range 6 {
				wg.Go(func() {
					rec, _ := q.invoke(t, "int_evt_brandA", trackBody, "Idempotency-Key", key)
					switch rec.Code {
					case 200, 202:
						ok.Add(1)
					case 409:
						conflict.Add(1)
					default:
						t.Errorf("status %d: %s", rec.Code, rec.Body)
					}
				})
			}
			wg.Wait()
			if mode == registry.ModeAsync {
				for range 3 {
					_, _ = q.worker.ProcessOnce(t.Context())
				}
			}
			if n := len(q.mv.Requests(t, nil)); n != 1 {
				t.Fatalf("%s: %d vendor calls for one key (ok %d, 409 %d)", mode, n, ok.Load(), conflict.Load())
			}
			t.Logf("%s: 6 submissions → %d answered, %d in-progress conflicts, 1 vendor call", mode, ok.Load(), conflict.Load())
		})
	}
}

type resolveResult = resolve.Resolution

func resolveFor(snap *registry.Snapshot, id string) (resolve.Resolution, error) {
	return resolve.Resolve(snap, "orders-service", id, 0, id)
}
