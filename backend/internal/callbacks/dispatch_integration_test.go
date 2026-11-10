//go:build integration

package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// tenantReceiver is a tenant endpoint that verifies every callback and counts deliveries.
type tenantReceiver struct {
	srv  *httptest.Server
	fail atomic.Int64 // answer 500 to this many requests first
	// delay (ns) slows every answer; inflight/maxInflight count concurrent requests.
	delay, inflight, maxInflight atomic.Int64
	mu                           sync.Mutex
	byID                         map[string]int
	bodies                       [][]byte
	verified                     atomic.Int64
	rejected                     atomic.Int64
}

func newTenantReceiver(t *testing.T, secret string) *tenantReceiver {
	t.Helper()
	r := &tenantReceiver{byID: map[string]int{}}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cur := r.inflight.Add(1)
		defer r.inflight.Add(-1)
		for {
			m := r.maxInflight.Load()
			if cur <= m || r.maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(time.Duration(r.delay.Load()))
		body, _ := io.ReadAll(req.Body)
		if err := Verify(req.Header.Get(SignatureHeader), body, [][]byte{[]byte(secret)}, time.Now(), 0); err != nil {
			r.rejected.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.verified.Add(1)
		r.mu.Lock()
		r.byID[req.Header.Get(DeliveryIDHeader)]++
		r.bodies = append(r.bodies, body)
		r.mu.Unlock()
		if r.fail.Add(-1) >= 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *tenantReceiver) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.byID))
	for k, v := range r.byID {
		out[k] = v
	}
	return out
}

type itest struct {
	pool    *pgxpool.Pool
	enq     *Enqueuer
	rcv     *tenantReceiver
	snaps   staticSnapshots
	secrets fakeSecrets
}

func newITest(t *testing.T) *itest {
	t.Helper()
	p := pgPool(t)
	rcv := newTenantReceiver(t, secretActive)
	snaps := staticSnapshots{testSnapshot(rcv.srv.URL + "/hub/callbacks")}
	enq, err := NewEnqueuer(snaps, telemetry.NewMetrics("test", "dev"), EnqueueOptions{Validator: validator(t)})
	if err != nil {
		t.Fatal(err)
	}
	return &itest{pool: p, enq: enq, rcv: rcv, snaps: snaps, secrets: fakeSecrets{
		"localfile://reg_orders_cb":    {[]byte(secretActive)},
		"localfile://reg_orders_audit": {[]byte(secretActive)},
	}}
}

func (it *itest) enqueue(t *testing.T, evs ...*Event) {
	t.Helper()
	if err := pgx.BeginFunc(t.Context(), it.pool, func(tx pgx.Tx) error {
		for _, ev := range evs {
			if _, err := it.enq.Enqueue(t.Context(), tx, ev); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (it *itest) dispatcher(t *testing.T, sink audit.Sink, opts DispatcherOptions) *Dispatcher {
	t.Helper()
	opts.Guard.AllowLoopback = true // the test receiver listens on 127.0.0.1
	if opts.PollInterval == 0 {
		opts.PollInterval = 10 * time.Millisecond
	}
	d, err := NewDispatcher(DispatcherDeps{
		Pool: it.pool, Snapshots: it.snaps, Secrets: it.secrets, Audit: sink, Metrics: telemetry.NewMetrics("test", "dev"),
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// run starts d and returns a stop function that cancels it and waits for Run to return.
func run(t *testing.T, d *Dispatcher) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type deliveryRow struct {
	status, lastError string
	attempts          int
	httpStatus        *int
	delivered         bool
}

func readRow(t *testing.T, p *pgxpool.Pool, id string) deliveryRow {
	t.Helper()
	var r deliveryRow
	var lastErr *string
	if err := p.QueryRow(t.Context(), `SELECT status, attempts, last_http_status, last_error, delivered_at IS NOT NULL
		FROM hub.callback_deliveries WHERE delivery_id = $1`, id).Scan(&r.status, &r.attempts, &r.httpStatus, &lastErr, &r.delivered); err != nil {
		t.Fatal(err)
	}
	if lastErr != nil {
		r.lastError = *lastErr
	}
	return r
}

func deliveryIDs(t *testing.T, p *pgxpool.Pool, requestID string) []string {
	t.Helper()
	rows, err := p.Query(t.Context(), `SELECT delivery_id FROM hub.callback_deliveries WHERE request_id = $1 ORDER BY subscription`, requestID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// TestDispatchSignedCallbackEndToEnd: every event type's enqueue becomes one signed POST per
// subscription that the tenant's receiver verifies, with a contract-valid body, recorded as
// delivered and audited.
func TestDispatchSignedCallbackEndToEnd(t *testing.T) {
	it := newITest(t)
	v1 := it.snaps.s.Integrations["int_evt_brandB"].Versions[1]
	v1.Webhooks = append(v1.Webhooks,
		registry.Subscription{Event: "request.expired", Endpoint: "reg_orders_cb"},
		registry.Subscription{Event: "delivery.status", Endpoint: "reg_orders_audit"})
	sink := &captureSink{}
	all := sampleEvents()
	evs := []*Event{all["completed"], all["failed"], all["expired"], all["delivery.status parked"]}
	it.enqueue(t, evs...)
	want := map[string]int{evs[0].RequestID: 2, evs[1].RequestID: 1, evs[2].RequestID: 1, evs[3].RequestID: 1}
	var ids []string
	for _, ev := range evs {
		got := deliveryIDs(t, it.pool, ev.RequestID)
		if len(got) != want[ev.RequestID] {
			t.Fatalf("%s: deliveries = %v, want %d", ev.Data.Type(), got, want[ev.RequestID])
		}
		ids = append(ids, got...)
	}
	d := it.dispatcher(t, sink, DispatcherOptions{})
	stop := run(t, d)
	waitFor(t, "every delivery delivered", func() bool { return countRows(t, it.pool, `status = 'delivered'`) == len(ids) })
	stop()
	for _, id := range ids {
		r := readRow(t, it.pool, id)
		if r.attempts != 1 || r.httpStatus == nil || *r.httpStatus != 204 || !r.delivered || r.lastError != "" {
			t.Fatalf("row %s = %+v", id, r)
		}
	}
	if it.rcv.verified.Load() != int64(len(ids)) || it.rcv.rejected.Load() != 0 {
		t.Fatalf("receiver verified %d rejected %d", it.rcv.verified.Load(), it.rcv.rejected.Load())
	}
	v := validator(t)
	byRequest := map[string]*Event{}
	for _, ev := range evs {
		byRequest[ev.RequestID] = ev
	}
	for _, b := range it.rcv.bodies {
		var env struct {
			Type           EventType `json:"type"`
			RequestID      string    `json:"request_id"`
			IdempotencyKey string    `json:"idempotency_key"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatal(err)
		}
		ev := byRequest[env.RequestID]
		if ev == nil || env.Type != ev.Data.Type() || env.IdempotencyKey != ev.IdempotencyKey {
			t.Fatalf("received body %s", b)
		}
		if err := v.Validate(env.Type, b); err != nil {
			t.Fatal(err)
		}
	}
	if evs := sink.all(); len(evs) != len(ids) || evs[0].Type != audit.CallbackDelivered || evs[0].Meta["outcome"] != statusDelivered {
		t.Fatalf("audit = %+v", evs)
	}
	if got := testutil.ToFloat64(d.deliveries.WithLabelValues("orders-service", statusDelivered)); got != float64(len(ids)) {
		t.Fatalf("callback_deliveries_total{delivered} = %v", got)
	}
}

// TestDispatchRetriesThenDeadLetters: a failing endpoint is retried on the callback schedule,
// independently of vendor delivery, and dead-lettered when the schedule is exhausted; an
// endpoint that recovers mid-schedule is delivered.
func TestDispatchRetriesThenDeadLetters(t *testing.T) {
	it := newITest(t)
	sink := &captureSink{}
	it.rcv.fail.Store(1_000)
	dead := sampleEvents()["failed"] // one subscription: reg_orders_cb
	it.enqueue(t, dead)
	deadID := deliveryIDs(t, it.pool, dead.RequestID)[0]

	d := it.dispatcher(t, sink, DispatcherOptions{Schedule: []time.Duration{30 * time.Millisecond, 30 * time.Millisecond}, SampleEvery: 20 * time.Millisecond})
	stop := run(t, d)
	waitFor(t, "dead-letter", func() bool { return readRow(t, it.pool, deadID).status == statusDeadLettered })
	waitFor(t, "callback_dlq_count", func() bool { return testutil.ToFloat64(d.dlq) == 1 })
	r := readRow(t, it.pool, deadID)
	if r.attempts != 3 || r.lastError != errHTTP5xx || r.httpStatus == nil || *r.httpStatus != 500 {
		t.Fatalf("dead-lettered row = %+v", r)
	}
	if got := it.rcv.counts()[deadID]; got != 3 {
		t.Fatalf("endpoint saw %d attempts, want 3", got)
	}
	var outcomes []any
	for _, e := range sink.all() {
		outcomes = append(outcomes, e.Meta["outcome"])
	}
	if fmt.Sprint(outcomes) != "[retry_scheduled retry_scheduled dead_lettered]" {
		t.Fatalf("audited outcomes %v", outcomes)
	}

	// Recovery: the endpoint fails twice more, then answers 204: both deliveries of a
	// request.completed (two subscriptions) succeed on a retry.
	it.rcv.fail.Store(2)
	ok := sampleEvents()["completed"]
	it.enqueue(t, ok)
	okIDs := deliveryIDs(t, it.pool, ok.RequestID)
	waitFor(t, "delivery after recovery", func() bool {
		return readRow(t, it.pool, okIDs[0]).status == statusDelivered && readRow(t, it.pool, okIDs[1]).status == statusDelivered
	})
	stop()
	for _, id := range okIDs {
		if r := readRow(t, it.pool, id); r.attempts < 2 || r.lastError != "" {
			t.Fatalf("recovered row = %+v", r)
		}
	}
}

// TestConcurrentDispatchersDoNotDoubleSend: three dispatchers claim from the same table with
// FOR UPDATE SKIP LOCKED; every delivery reaches the receiver exactly once.
func TestConcurrentDispatchersDoNotDoubleSend(t *testing.T) {
	it := newITest(t)
	const n = 150
	evs := make([]*Event, n)
	for i := range evs {
		evs[i] = sampleEvents()["failed"]
	}
	it.enqueue(t, evs...)
	if got := countRows(t, it.pool, `status = 'pending'`); got != n {
		t.Fatalf("pending = %d", got)
	}
	var stops []func()
	for i := range 3 {
		d := it.dispatcher(t, audit.Discard{}, DispatcherOptions{ID: fmt.Sprintf("cb-test-%d", i), Senders: 8, ClaimBatch: 5, PollInterval: time.Millisecond})
		stops = append(stops, run(t, d))
	}
	waitFor(t, "all delivered", func() bool { return countRows(t, it.pool, `status = 'delivered'`) == n })
	for _, s := range stops {
		s()
	}
	counts := it.rcv.counts()
	if len(counts) != n {
		t.Fatalf("receiver saw %d distinct deliveries, want %d", len(counts), n)
	}
	for id, c := range counts {
		if c != 1 {
			t.Fatalf("delivery %s sent %d times", id, c)
		}
	}
	if got := countRows(t, it.pool, `attempts <> 1`); got != 0 {
		t.Fatalf("%d rows took more than one attempt", got)
	}
}

// TestAbandonedClaimIsReclaimed: a row left in_flight by a crashed dispatcher is picked up
// after its visibility timeout; the crashed dispatcher's late outcome no longer applies.
func TestAbandonedClaimIsReclaimed(t *testing.T) {
	it := newITest(t)
	ev := sampleEvents()["failed"]
	it.enqueue(t, ev)
	id := deliveryIDs(t, it.pool, ev.RequestID)[0]
	if _, err := it.pool.Exec(t.Context(), `UPDATE hub.callback_deliveries SET status = 'in_flight', claimed_by = 'cb-crashed',
		claimed_until = now() - interval '1 second', attempts = 1 WHERE delivery_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	stop := run(t, it.dispatcher(t, audit.Discard{}, DispatcherOptions{}))
	waitFor(t, "reclaimed delivery", func() bool { return readRow(t, it.pool, id).status == statusDelivered })
	stop()
	if r := readRow(t, it.pool, id); r.attempts != 2 {
		t.Fatalf("row = %+v, want attempt 2 after the reclaim", r)
	}
	won, err := pgStore{pool: it.pool}.record(t.Context(), id, "cb-crashed", outcome{Status: statusDeadLettered, Error: errTimeout})
	if err != nil || won {
		t.Fatalf("crashed dispatcher's late record applied: %v %v", won, err)
	}
	if r := readRow(t, it.pool, id); r.status != statusDelivered {
		t.Fatalf("row changed to %+v", r)
	}
}

// TestClaimNeverReturnsMoreThanAsked (HUB-452): with a due backlog AND expired in-flight rows, a
// claim of n returns exactly n rows (the SQL took n of each and returned up to 2n), taking the
// abandoned rows first; the rest stay claimable.
func TestClaimNeverReturnsMoreThanAsked(t *testing.T) {
	it := newITest(t)
	const due, abandoned = 8, 6
	evs := make([]*Event, due+abandoned)
	for i := range evs {
		evs[i] = sampleEvents()["failed"]
	}
	it.enqueue(t, evs...)
	if _, err := it.pool.Exec(t.Context(), `UPDATE hub.callback_deliveries SET status = 'in_flight', claimed_by = 'cb-crashed',
		claimed_until = now() - interval '1 second', attempts = 1
		WHERE delivery_id IN (SELECT delivery_id FROM hub.callback_deliveries ORDER BY delivery_id LIMIT $1)`, abandoned); err != nil {
		t.Fatal(err)
	}
	st := pgStore{pool: it.pool}
	got, err := st.claim(t.Context(), "cb-a", 4, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("claim(4) returned %d rows", len(got))
	}
	for _, d := range got {
		if d.Attempts != 2 {
			t.Fatalf("claimed %s with attempt %d, want an abandoned row (attempt 2)", d.ID, d.Attempts)
		}
	}
	// 10 left (2 abandoned, 8 due): asking for 7 gives 7, then the remaining 3; none twice.
	seen := map[string]bool{}
	for _, d := range got {
		seen[d.ID] = true
	}
	for _, ask := range []int{7, 20} {
		more, err := st.claim(t.Context(), "cb-a", ask, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if want := min(ask, due+abandoned-len(seen)); len(more) != want {
			t.Fatalf("claim(%d) returned %d rows, want %d", ask, len(more), want)
		}
		for _, d := range more {
			if seen[d.ID] {
				t.Fatalf("row %s claimed twice", d.ID)
			}
			seen[d.ID] = true
		}
	}
	if len(seen) != due+abandoned {
		t.Fatalf("claimed %d distinct rows of %d", len(seen), due+abandoned)
	}
}

// TestRunWithBacklogAndAbandonedRowsStaysWithinSenders: Run over a due backlog plus expired
// in-flight rows delivers all of them, never has more than Senders requests in flight, and stops.
func TestRunWithBacklogAndAbandonedRowsStaysWithinSenders(t *testing.T) {
	it := newITest(t)
	const total = 60
	evs := make([]*Event, total)
	for i := range evs {
		evs[i] = sampleEvents()["failed"]
	}
	it.enqueue(t, evs...)
	if _, err := it.pool.Exec(t.Context(), `UPDATE hub.callback_deliveries SET status = 'in_flight', claimed_by = 'cb-crashed',
		claimed_until = now() - interval '1 second', attempts = 1
		WHERE delivery_id IN (SELECT delivery_id FROM hub.callback_deliveries ORDER BY delivery_id LIMIT 30)`); err != nil {
		t.Fatal(err)
	}
	it.rcv.delay.Store(int64(15 * time.Millisecond))
	stop := run(t, it.dispatcher(t, audit.Discard{}, DispatcherOptions{Senders: 4, ClaimBatch: 4}))
	waitFor(t, "all delivered", func() bool { return countRows(t, it.pool, `status = 'delivered'`) == total })
	stop()
	if m := it.rcv.maxInflight.Load(); m > 4 {
		t.Fatalf("%d concurrent requests with 4 senders", m)
	}
}

// TestDisabledEndpointDoesNotBurnAttempts (HUB-452): a delivery for a disabled endpoint is looked
// at again and again without using up the 11-attempt schedule, never dead-letters, and is
// delivered as attempt 1 once the endpoint is enabled.
func TestDisabledEndpointDoesNotBurnAttempts(t *testing.T) {
	it := newITest(t)
	ev := sampleEvents()["failed"]
	it.enqueue(t, ev)
	id := deliveryIDs(t, it.pool, ev.RequestID)[0]
	d := it.dispatcher(t, audit.Discard{}, DispatcherOptions{})
	ep := it.snaps.s.Registrar["reg_orders_cb"]
	cycle := func() {
		t.Helper()
		if _, err := it.pool.Exec(t.Context(), `UPDATE hub.callback_deliveries SET next_attempt_at = now() WHERE delivery_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		rows, err := d.deps.store.claim(t.Context(), d.ID(), 1, 30*time.Second)
		if err != nil || len(rows) != 1 {
			t.Fatalf("claim = %d rows, %v", len(rows), err)
		}
		d.attempt(t.Context(), rows[0])
	}
	ep.Disabled = true
	for i := range 15 { // more than the 11 attempts of the schedule
		cycle()
		if r := readRow(t, it.pool, id); r.status != statusRetryScheduled || r.attempts != 0 || r.lastError != errEndpointDisabled {
			t.Fatalf("cycle %d: row = %+v, want retry_scheduled, 0 attempts, endpoint_disabled", i, r)
		}
	}
	var wait time.Duration
	if err := it.pool.QueryRow(t.Context(), `SELECT next_attempt_at - now() FROM hub.callback_deliveries WHERE delivery_id = $1`, id).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	if wait < disabledHold-time.Minute || wait > disabledHold {
		t.Fatalf("held for %v, want about %v", wait, disabledHold)
	}
	ep.Disabled = false
	cycle()
	if r := readRow(t, it.pool, id); r.status != statusDelivered || r.attempts != 1 {
		t.Fatalf("after re-enable row = %+v, want delivered on attempt 1", r)
	}
}

// TestClaimUsesPartialIndexes guards the claim query against index regressions.
func TestClaimUsesPartialIndexes(t *testing.T) {
	p := pgPool(t)
	if _, err := p.Exec(t.Context(), `ANALYZE hub.callback_deliveries`); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), `EXPLAIN `+claimDue, "cb-explain", 10, 30000)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprint(lines)
	for _, idx := range []string{"callback_deliveries_due_idx", "callback_deliveries_inflight_idx"} {
		if !strings.Contains(plan, idx) {
			t.Fatalf("claim plan does not use %s:\n%s", idx, plan)
		}
	}
}
