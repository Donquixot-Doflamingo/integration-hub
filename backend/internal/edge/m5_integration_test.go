//go:build integration

package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// accountLoader gives each account its own credential values, so mockvendor routes can tell
// accounts apart by the X-CleverTap-Account-Id header.
type accountLoader struct{}

func (accountLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: map[string]string{"account_id": "acct-" + id, "passcode": "fake-pass-M5M5"}}}
	}
	return out, nil
}

const (
	acctB = "ct-brandB-us" // shared by int_evt_brandB and int_evt_brandB_min
	acctA = "ct-brandA-in"
)

// m5 is two gateway nodes (A, B: own breakers, one Redis), a worker on node A and a scheduler
// node (probes, drain), all against one mockvendor. Breaker timings are shortened.
type m5 struct {
	t        *testing.T
	db       *pgxpool.Pool
	rdb      *redis.Client
	mv       *mockvendor.Instance
	snap     *registry.Snapshot
	a, b     http.Handler
	worker   *delivery.Worker
	res      *delivery.Resilience
	sched    *delivery.Scheduler
	sink     *recordingSink
	spec     *contract.Spec
	breakers *upstream.Breakers // node A
}

type m5Tune func(id string, in *registry.Integration, v *registry.IntegrationVersion)

func newM5(t *testing.T, tune m5Tune, snapTune ...func(*registry.Snapshot)) *m5 {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, _ := sql.Open("pgx", dsn)
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	db, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	_ = (audit.PGStore{Pool: db}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -1), 3)
	_, _ = db.Exec(t.Context(), `DELETE FROM hub.outbox WHERE tenant_id = 'orders-service'`)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr()})
	t.Cleanup(func() { _ = rdb.Close() })
	for _, pattern := range []string{"hub:breaker:*", "hub:rl:acct:*"} {
		keys, _ := rdb.Keys(t.Context(), pattern).Result()
		if len(keys) > 0 {
			rdb.Del(t.Context(), keys...)
		}
	}

	mv := mockvendor.Start(t)
	snap := queuedSnapshot(t, e2eSnapshot, mv.VendorURL, tune)
	for name, v := range snap.Vendors {
		vc := *v
		vc.Defaults.Breaker = registry.BreakerConfig{WindowS: 10, MinCalls: 4, FailureRatio: 0.5, ConsecutiveTimeouts: 5, OpenS: 5, OpenMaxS: 40, HalfOpenProbes: 2}
		// 5 s: long enough that a slow CI runner parks the whole backlog before half-open;
		// recovery does not wait for it (probes close the breaker).
		eps := map[string]registry.Endpoint{}
		for n, ep := range v.Endpoints {
			ep.TimeoutMS = 400
			eps[n] = ep
		}
		vc.Endpoints = eps
		snap.Vendors[name] = &vc
	}
	accts := map[string]*registry.Account{}
	for id, a := range snap.Accounts {
		ac := *a
		ac.Quota.RPS = 50
		accts[id] = &ac
	}
	snap.Accounts = accts
	for _, f := range snapTune {
		f(snap)
	}

	m := telemetry.NewMetrics("itest", "m5")
	log := slog.New(slog.DiscardHandler)
	cache, _ := creds.NewCache(accountLoader{}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	run := func(bs *upstream.Breakers) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
		done := make(chan struct{})
		go func() { bs.Run(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	newBreakers := func() *upstream.Breakers {
		bs := upstream.NewBreakers(rdb, telemetry.NewMetrics("itest", "node"), nil)
		run(bs)
		return bs
	}
	ba, bb, bsched := newBreakers(), newBreakers(), newBreakers()
	limiter := upstream.NewAccountLimiter(rdb, m, nil)
	q := &queue.Outbox{}
	tr := &delivery.Transitions{DB: db, Queue: q}
	settler := delivery.Settler{Transitions: tr, Budget: delivery.NewBudget(nil)}
	sink := &recordingSink{}
	snaps := staticSnapshots{snap}
	node := func(bs *upstream.Breakers) http.Handler {
		return New(Deps{Snapshots: snaps, Creds: cache, Upstream: client, Breakers: bs, Limiter: limiter, Metrics: telemetry.NewMetrics("itest", "gw"),
			Log: log, Audit: sink, Queueing: &Queueing{DB: db, Queue: q, Settler: settler, Name: "gw-" + core.NewULID(time.Now())}}).Handler()
	}
	w := &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: db, Queue: q, Transitions: tr, Snapshots: snaps,
		Attempter: &delivery.Attempter{Creds: cache, Upstream: client, Breakers: ba, Limiter: limiter, Log: log},
		Audit:     audit.Discard{}, Budget: delivery.NewBudget(nil), Log: log, Idle: 20 * time.Millisecond, Batch: 64, Concurrency: 32}
	res := &delivery.Resilience{Breakers: bsched, Upstream: client, Snapshots: snaps, Queue: q, Log: log, RampStep: time.Second, ProbeEvery: 200 * time.Millisecond}
	res.RegisterMetrics(prometheus.NewRegistry())
	sched := &delivery.Scheduler{DB: db, Queue: q, Transitions: tr, Log: log, Resilience: res, Tick: 100 * time.Millisecond, Standby: 100 * time.Millisecond}
	return &m5{t: t, db: db, rdb: rdb, mv: mv, snap: snap, a: node(ba), b: node(bb), worker: w, res: res, sched: sched, sink: sink,
		spec: contract.Load(t, "hub-public.v1.yaml"), breakers: ba}
}

func (r *m5) vendor(account string, b mockvendor.Behaviour) {
	r.mv.SetRoute(r.t, account, mockvendor.RouteSpec{Priority: 10,
		Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload", Headers: map[string]string{"X-Clevertap-Account-Id": "acct-" + account}}, Behaviour: b})
}

func down() mockvendor.Behaviour {
	return mockvendor.Behaviour{Status: 503, Body: json.RawMessage(`{"status":"fail","error":"service unavailable"}`)}
}

func up() mockvendor.Behaviour { return mockvendor.Behaviour{Status: 200, Body: okVendor} }

func (r *m5) call(h http.Handler, integration string, hdr ...string) (int, map[string]any, http.Header) {
	r.t.Helper()
	req := httptest.NewRequestWithContext(r.t.Context(), http.MethodPost, "/v1/integrations/"+integration+"/invoke", strings.NewReader(trackBody))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := r.spec.Serve(r.t, h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func circuit(body map[string]any) bool {
	e, _ := body["error"].(map[string]any)
	return e != nil && e["circuit"] == "open"
}

func (r *m5) vendorCalls(account string) int {
	n := 0
	for _, rq := range r.mv.Requests(r.t, nil) {
		if len(rq.Headers["X-Clevertap-Account-Id"]) > 0 && rq.Headers["X-Clevertap-Account-Id"][0] == "acct-"+account && len(rq.Headers["X-Hub-Probe"]) == 0 {
			n++
		}
	}
	return n
}

func (r *m5) probes() int {
	n := 0
	for _, rq := range r.mv.Requests(r.t, nil) {
		if len(rq.Headers["X-Hub-Probe"]) > 0 {
			n++
		}
	}
	return n
}

func (r *m5) statusOf(id string) string {
	var s string
	_ = r.db.QueryRow(r.t.Context(), `SELECT status FROM hub.outbox WHERE request_id = $1`, id).Scan(&s)
	if s != "" {
		return s
	}
	from, _ := core.RequestIDTime(id)
	_ = r.db.QueryRow(r.t.Context(), `SELECT status FROM hub.requests WHERE request_id = $1 AND created_at BETWEEN $2 AND $3`,
		id, from.Add(-time.Minute), from.Add(time.Minute)).Scan(&s)
	return s
}

func (r *m5) startScheduler() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.t.Context()))
	done := make(chan struct{})
	go func() { r.sched.Run(ctx); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
}

func (r *m5) startWorker() {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.t.Context()))
	done := make(chan struct{})
	go func() { r.worker.Run(ctx); close(done) }()
	r.t.Cleanup(func() { cancel(); <-done })
}

// tripB opens the shared breaker of acctB through node A's sync calls on int_evt_brandA
// (retargeted to acctB) and returns how many calls it took.
func (r *m5) tripB() int {
	r.t.Helper()
	for i := 1; i <= 10; i++ {
		if _, body, _ := r.call(r.a, "int_evt_brandA"); circuit(body) {
			return i
		}
	}
	r.t.Fatal("breaker never opened")
	return 0
}

// brandAOnB retargets int_evt_brandA to acctB in sync mode (so one account carries sync, wait
// and async traffic).
func brandAOnB(policy registry.OnCircuitOpen) m5Tune {
	return func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandA" {
			t := v.Targets[0]
			t.Account = acctB
			v.Targets = []registry.Target{t}
			v.Delivery.Mode, v.Delivery.WaitMS, v.Delivery.OnCircuitOpen = registry.ModeSync, 0, policy
		}
	}
}

// TestChaos1VendorDown (HUB-150): breaker opens within the window; sync 503 circuit open with
// retry_after_ms and no vendor call; wait 202 queued; async parks.
func TestChaos1VendorDown(t *testing.T) {
	r := newM5(t, brandAOnB(registry.FailFast))
	r.vendor(acctB, down())
	calls := r.tripB()
	if calls > 5 {
		t.Fatalf("opened after %d calls; policy is 4 calls over 50 %%", calls)
	}
	before := r.vendorCalls(acctB)
	for range 3 {
		code, body, _ := r.call(r.a, "int_evt_brandA")
		e := body["error"].(map[string]any)
		ra, _ := e["retry_after_ms"].(float64)
		if code != 503 || !circuit(body) || ra <= 0 || ra > 5000 {
			t.Fatalf("sync while open: %d %v", code, body)
		}
	}
	if n := r.vendorCalls(acctB); n != before {
		t.Fatalf("vendor got %d calls while open", n-before)
	}
	code, body, _ := r.call(r.a, "int_evt_brandB_min") // wait
	if code != 202 || body["status"] != "queued" || body["reason"] != "circuit_open" {
		t.Fatalf("wait while open: %d %v", code, body)
	}
	waitID := body["request_id"].(string)
	code, body, _ = r.call(r.a, "int_evt_brandB") // async_callback
	if code != 202 {
		t.Fatalf("async: %d %v", code, body)
	}
	asyncID := body["request_id"].(string)
	if _, err := r.worker.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if s := r.statusOf(asyncID); s != "parked" {
		t.Fatalf("async envelope %s, want parked", s)
	}
	if s := r.statusOf(waitID); s != "parked" {
		t.Fatalf("wait envelope %s, want parked", s)
	}
	if n := r.vendorCalls(acctB); n != before {
		t.Fatalf("vendor got %d calls from parking", n-before)
	}
}

// parkBacklog accepts n async envelopes, then opens the circuit and lets the worker park them
// (accepting first keeps the parking well inside the open window on slow runners).
func (r *m5) parkBacklog(n int) []string {
	r.t.Helper()
	r.vendor(acctB, down())
	ids := make([]string, 0, n)
	for range n {
		code, body, _ := r.call(r.a, "int_evt_brandB")
		if code != 202 {
			r.t.Fatalf("accept: %d %v", code, body)
		}
		ids = append(ids, body["request_id"].(string))
	}
	r.tripB()
	for {
		if k, _ := r.worker.ProcessOnce(r.t.Context()); k == 0 {
			break
		}
	}
	for _, id := range ids {
		if s := r.statusOf(id); s != "parked" {
			var reason, lastErr string
			_ = r.db.QueryRow(r.t.Context(), `SELECT reason, coalesce(last_error, '') FROM hub.dead_letters WHERE request_id = $1`, id).Scan(&reason, &lastErr)
			r.t.Fatalf("%s is %s, want parked (dead letter reason %q %q)", id, s, reason, lastErr)
		}
	}
	return ids
}

// releaseSamples records cumulative drain releases every 100 ms.
type sample struct {
	at time.Time
	n  float64
}

// sampleReleases samples until stop is closed; wait for done before reading the samples.
func (r *m5) sampleReleases(stop <-chan struct{}) (*[]sample, <-chan struct{}) {
	out := &[]sample{}
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		take := func() {
			v := testutil.ToFloat64(r.res.ReleasedCounter().WithLabelValues(acctB, "clevertap.upload@1"))
			mu.Lock()
			*out = append(*out, sample{time.Now(), v})
			mu.Unlock()
		}
		for {
			select {
			case <-stop:
				// A last sample: the worker can deliver the final releases between two ticks and the
				// caller stops as soon as they succeed (faster since HUB-412's continuous claims, and
				// the limiter no longer adds a Redis round trip per attempt, HUB-408).
				take()
				return
			case <-t.C:
				take()
			}
		}
	}()
	return out, done
}

func releasedBetween(s []sample, from, to time.Time) float64 {
	var a, b float64
	for _, x := range s {
		if !x.at.After(from) {
			a = x.n
		}
		if !x.at.After(to) {
			b = x.n
		}
	}
	return b - a
}

// TestChaos2RecoveryAndRampedDrain (HUB-151): probes close the breaker without live traffic;
// the drain starts within 60 s; releases ramp 20 % → 40 % → 80 % → 100 % of the account rate.
func TestChaos2RecoveryAndRampedDrain(t *testing.T) {
	r := newM5(t, brandAOnB(registry.FailFast))
	ids := r.parkBacklog(160)
	stop := make(chan struct{})
	samples, sampled := r.sampleReleases(stop)
	recovered := time.Now()
	r.vendor(acctB, up())
	r.startScheduler()
	r.startWorker()
	key := upstream.BreakerKey{Account: acctB, Endpoint: "clevertap.upload@1"}
	var closedAt time.Time
	eventually(t, "probes to close the breaker", func() bool {
		s, ok := r.breakers.SharedState(t.Context(), key)
		if ok && s.State == upstream.Closed {
			closedAt = time.UnixMilli(s.Version)
			return true
		}
		return false
	})
	if r.probes() == 0 {
		t.Fatal("no health probe reached the vendor")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		done := 0
		for _, id := range ids {
			if r.statusOf(id) == "succeeded" {
				done++
			}
		}
		if done == len(ids) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	close(stop)
	<-sampled
	for _, id := range ids {
		if s := r.statusOf(id); s != "succeeded" {
			t.Fatalf("%s ended %s", id, s)
		}
	}
	firstRelease := time.Time{}
	for _, s := range *samples {
		if s.n > 0 {
			firstRelease = s.at
			break
		}
	}
	t.Logf("breaker closed %s after recovery; first release %s after recovery", closedAt.Sub(recovered).Round(10*time.Millisecond), firstRelease.Sub(recovered).Round(10*time.Millisecond))
	if firstRelease.Sub(recovered) > 60*time.Second {
		t.Fatal("drain started more than 60 s after recovery")
	}
	// The ramp: 20 %, 40 %, 80 %, 100 % of 50/s, one ramp step (1 s) each, so by the end of
	// step k the drain owes owed[k] releases. A scheduler pass delayed by the machine (a slow
	// Postgres commit on a CI runner) delays releases but must not lose them: the drain makes up
	// to maxCatchUp (1 s) of quota on its next pass (HUB-155, HUB-156). So the test checks the two
	// properties DESIGN §10.5 needs, per step boundary, instead of exact 1 s windows:
	//   - never ahead of the ramp: released by the boundary <= owed (+10 %), so a recovering
	//     vendor never sees more than the ramped rate;
	//   - nothing lost: by one catch-up horizon after the boundary, at least owed (-10 %) was released.
	step := []float64{10, 20, 40, 50}
	owed := 0.0
	for i, w := range step {
		owed += w
		boundary := closedAt.Add(time.Duration(i+1) * time.Second)
		inStep := releasedBetween(*samples, boundary.Add(-time.Second), boundary)
		byBoundary := releasedBetween(*samples, closedAt, boundary)
		byCatchUp := releasedBetween(*samples, closedAt, boundary.Add(time.Second))
		t.Logf("ramp step %d: %v released in the step (ramp %v); %v by its end, %v one second later (owed %v)", i+1, inStep, w, byBoundary, byCatchUp, owed)
		if byBoundary > owed*1.1+1 {
			t.Fatalf("ramp step %d: %v released by its end, ahead of the ramp (owed %v + 10 %%)", i+1, byBoundary, owed)
		}
		if want := min(owed*0.9-1, float64(len(ids))); byCatchUp < want {
			t.Fatalf("ramp step %d: only %v released one catch-up horizon after its end, want >= %v (quota lost)", i+1, byCatchUp, want)
		}
	}
	// The ramp releases 120 in 4 s, then the last 40 at the full 50/s by 4.8 s. All of them must
	// be out by then plus one catch-up horizon; a drain that loses a quarter of its quota misses it.
	var allOut time.Time
	for _, x := range *samples {
		if x.n >= float64(len(ids)) {
			allOut = x.at
			break
		}
	}
	if allOut.IsZero() || allOut.Sub(closedAt) > 5800*time.Millisecond {
		t.Fatalf("all %d released %s after the close, want <= 5.8 s (4.8 s of ramp + 1 s catch-up)", len(ids), allOut.Sub(closedAt).Round(10*time.Millisecond))
	}
	t.Logf("all %d released %s after the close", len(ids), allOut.Sub(closedAt).Round(10*time.Millisecond))
}

// TestChaos3ReopenMidDrain (HUB-152): the vendor fails again during the drain: the breaker
// re-opens with a doubled window, the rest stay parked (none dead-lettered or lost) and the
// drain resumes after the next recovery.
func TestChaos3ReopenMidDrain(t *testing.T) {
	r := newM5(t, brandAOnB(registry.FailFast))
	ids := r.parkBacklog(120)
	r.vendor(acctB, up())
	r.startScheduler()
	r.startWorker()
	key := upstream.BreakerKey{Account: acctB, Endpoint: "clevertap.upload@1"}
	eventually(t, "the first recovery", func() bool {
		s, ok := r.breakers.SharedState(t.Context(), key)
		return ok && s.State == upstream.Closed
	})
	time.Sleep(1200 * time.Millisecond) // into the 40 % step
	r.vendor(acctB, down())
	var reopened upstream.BreakerSnapshot
	eventually(t, "the re-open", func() bool {
		s, ok := r.breakers.SharedState(t.Context(), key)
		reopened = s
		return ok && s.State == upstream.Open
	})
	if window := reopened.OpenUntil.Sub(time.UnixMilli(reopened.Version)); window < 10*time.Second {
		t.Fatalf("re-open window %s, want doubled (≥ 10 s)", window)
	}
	time.Sleep(500 * time.Millisecond)
	counts := map[string]int{}
	for _, id := range ids {
		counts[r.statusOf(id)]++
	}
	t.Logf("after the re-open: %v", counts)
	if counts["parked"] == 0 || counts["dead_lettered"] != 0 || counts[""] != 0 {
		t.Fatalf("after the re-open: %v (want some still parked, none dead-lettered or lost)", counts)
	}
	r.vendor(acctB, up())
	// Envelopes that failed during the re-open wait their backoff (5 s, 30 s): allow for it.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, id := range ids {
			if r.statusOf(id) != "succeeded" {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	counts = map[string]int{}
	for _, id := range ids {
		counts[r.statusOf(id)]++
	}
	t.Fatalf("drain did not finish after the second recovery: %v", counts)
}

// TestChaos4SharedAccountAndPause (HUB-153): two integrations on one account trip and recover
// together on both nodes; pausing one leaves the other untouched.
func TestChaos4SharedAccountAndPause(t *testing.T) {
	syncBoth := func(id string, in *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandB" || id == "int_evt_brandB_min" {
			v.Delivery.Mode, v.Delivery.WaitMS, v.Delivery.OnCircuitOpen = registry.ModeSync, 0, registry.FailFast
			v.Breaker.Override.OpenAfterConsecutiveTimeouts = 0
		}
	}
	r := newM5(t, syncBoth)
	r.vendor(acctB, down())
	tripped := 0
	for i := 0; i < 10 && tripped == 0; i++ {
		if _, body, _ := r.call(r.a, "int_evt_brandB_min"); circuit(body) {
			tripped = i + 1
		}
	}
	if tripped == 0 {
		t.Fatal("no trip")
	}
	start := time.Now()
	eventually(t, "node B to see the open circuit for the other integration", func() bool {
		_, body, _ := r.call(r.b, "int_evt_brandB")
		return circuit(body)
	})
	if d := time.Since(start); d > time.Second {
		t.Fatalf("node B saw it after %s", d)
	}
	r.vendor(acctB, up())
	r.startScheduler()
	eventually(t, "both integrations to recover on both nodes", func() bool {
		c1, _, _ := r.call(r.a, "int_evt_brandB")
		c2, _, _ := r.call(r.b, "int_evt_brandB_min")
		return c1 == 200 && c2 == 200
	})
	// Pause int_evt_brandB only.
	paused := *r.snap
	paused.Integrations = map[string]*registry.Integration{}
	for id, in := range r.snap.Integrations {
		c := *in
		c.Paused = id == "int_evt_brandB"
		paused.Integrations[id] = &c
	}
	h := New(Deps{Snapshots: staticSnapshots{&paused}, Creds: r.worker.Attempter.Creds, Upstream: r.worker.Attempter.Upstream, Breakers: r.breakers}).Handler()
	if code, body, _ := r.call(h, "int_evt_brandB"); code != 503 || body["error"].(map[string]any)["code"] != "INTEGRATION_PAUSED" {
		t.Fatalf("paused integration: %d %v", code, body)
	}
	if code, _, _ := r.call(h, "int_evt_brandB_min"); code != 200 {
		t.Fatalf("the other integration on the account: %d", code)
	}
}

// TestChaos5Failover (HUB-154): with on_circuit_open failover, the failover target serves,
// X-Hub-Served-By says failover:1, and the audit trail records the failover target.
func TestChaos5Failover(t *testing.T) {
	r := newM5(t, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		brandAOnB(registry.Failover)(id, nil, v)
		if id == "int_evt_brandA" {
			fo := v.Targets[0]
			fo.Account, fo.Weight = acctA, 0
			v.Failover = []registry.Target{fo}
		}
	})
	r.vendor(acctB, down())
	r.vendor(acctA, up())
	for i := range 8 { // before and after the primary's circuit opens
		code, body, hdr := r.call(r.a, "int_evt_brandA", "Idempotency-Key", "fo-"+core.NewULID(time.Now()))
		if code != 200 || hdr.Get("X-Hub-Served-By") != "failover:1" || body["status"] != "ok" {
			t.Fatalf("call %d: %d %v %v", i, code, body, hdr)
		}
	}
	if r.vendorCalls(acctA) != 8 {
		t.Fatalf("failover target got %d calls", r.vendorCalls(acctA))
	}
	found := false
	r.sink.mu.Lock()
	for _, b := range r.sink.batches {
		for _, e := range b.Events {
			if e.Type == audit.IntegrationResolved && e.Meta["failover"] == 1 && e.Meta["account"] == acctA {
				found = true
			}
		}
	}
	r.sink.mu.Unlock()
	if !found {
		t.Fatal("audit has no failover event naming the target")
	}
}

// TestIntegrationOverride (HUB-142): int_evt_brandB_min's override (3
// consecutive timeouts) opens its own view while the shared breaker stays closed for
// int_evt_brandB on the same account.
func TestIntegrationOverride(t *testing.T) {
	r := newM5(t, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandB" || id == "int_evt_brandB_min" {
			v.Delivery.Mode, v.Delivery.WaitMS, v.Delivery.OnCircuitOpen = registry.ModeSync, 0, registry.FailFast
		}
	}, func(s *registry.Snapshot) { // one attempt per call (no sync retry); the shared breaker
		// keeps DESIGN's 20-call minimum so only the integration's view can trip here
		for name, v := range s.Vendors {
			for n, ep := range v.Endpoints {
				ep.Idempotent = false
				v.Endpoints[n] = ep
			}
			v.Defaults.Breaker.MinCalls = 20
			s.Vendors[name] = v
		}
	})
	r.vendor(acctB, mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 700}})
	for i := range 3 {
		if code, _, _ := r.call(r.a, "int_evt_brandB_min"); code != 504 {
			t.Fatalf("timeout %d: %d", i, code)
		}
	}
	if _, body, _ := r.call(r.a, "int_evt_brandB_min"); !circuit(body) {
		t.Fatalf("the override did not open the integration's view: %v", body)
	}
	before := r.vendorCalls(acctB)
	if code, body, _ := r.call(r.a, "int_evt_brandB"); circuit(body) || code != 504 {
		t.Fatalf("the shared breaker must stay closed for the other integration: %d %v", code, body)
	}
	if r.vendorCalls(acctB) != before+1 {
		t.Fatal("the other integration did not reach the vendor")
	}
	if s := r.breakers.Get(upstream.BreakerKey{Account: acctB, Endpoint: "clevertap.upload@1"}, upstream.BreakerPolicy{}).Snapshot(); s.State != upstream.Closed {
		t.Fatalf("shared breaker %s", s.State)
	}
}

// TestTenantLimiter (HUB-148): a tenant over its budget gets 429 without touching the vendor.
func TestTenantLimiter(t *testing.T) {
	r := newM5(t, brandAOnB(registry.FailFast), func(s *registry.Snapshot) {
		tenants := map[string]*registry.Tenant{}
		for id, tn := range s.Tenants {
			c := *tn
			c.RateLimit = registry.TenantLimit{RPS: 1, Burst: 2}
			tenants[id] = &c
		}
		s.Tenants = tenants
		registry.ReindexKeys(s)
	})
	r.vendor(acctB, up())
	codes := []int{}
	for range 3 {
		code, body, _ := r.call(r.a, "int_evt_brandA")
		codes = append(codes, code)
		if code == 429 && body["error"].(map[string]any)["reason"] != "tenant_quota" {
			t.Fatalf("429 reason %v", body)
		}
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 || r.vendorCalls(acctB) != 2 {
		t.Fatalf("codes %v, vendor calls %d", codes, r.vendorCalls(acctB))
	}
}
