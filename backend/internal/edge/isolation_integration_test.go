//go:build integration

package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// Noisy-neighbour chaos tests (HUB-412, docs/tech/ISOLATION.md). Account A (ct-brandA-in, region
// "in") and account B (ct-brandB-us, region "us") are served by two separate mockvendor
// instances, so A's vendor can refuse connections without touching B's. Callers keep calling A
// at 20/s while it misbehaves; B carries 20 async/s and 10 sync/s throughout. B's async latency
// (accept → succeeded, from hub.requests) and sync latency (gateway round trip) are compared
// with B's own baseline taken while A was healthy, and nothing of A or B may be lost.
//
// The worker runs with the pre-HUB-412 defaults (16 in flight, claims of 32), so the scenarios
// also measure the code they replaced: a batch barrier and shared slots.

const (
	isoTimeoutMS = 1500 // A's and B's endpoint timeout
	isoSlowMS    = 1300 // scenario (a): A answers 200 OK just under its timeout
	isoVendorMS  = 20   // healthy vendor latency
	isoBaseline  = 3 * time.Second
	isoChaos     = 6 * time.Second
)

type isoRig struct {
	t        *testing.T
	gwDB     *pgxpool.Pool
	mvA, mvB *mockvendor.Instance
	h        http.Handler
	worker   *delivery.Worker
	sched    *delivery.Scheduler
	reg      *prometheus.Registry // the worker's isolation metrics
}

func isoPool(t *testing.T, dsn string, conns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = conns
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

// newIsoRig wires a gateway, a worker and a scheduler the way cmd/hub does, each with its own
// Postgres pool (separate processes in production), one Redis and two vendors.
func newIsoRig(t *testing.T) *isoRig {
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
	gwDB, wDB, schedDB := isoPool(t, dsn, 8), isoPool(t, dsn, 8), isoPool(t, dsn, 4)
	_ = (audit.PGStore{Pool: gwDB}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -1), 3)
	_, _ = gwDB.Exec(t.Context(), `DELETE FROM hub.outbox WHERE tenant_id = 'orders-service'`)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr()})
	t.Cleanup(func() { _ = rdb.Close() })
	for _, pattern := range []string{"hub:breaker:*", "hub:rl:acct:*"} {
		keys, _ := rdb.Keys(t.Context(), pattern).Result()
		if len(keys) > 0 {
			rdb.Del(t.Context(), keys...)
		}
	}

	mvA, mvB := mockvendor.Start(t), mockvendor.Start(t)
	snap := queuedSnapshot(t, e2eSnapshot, mvB.VendorURL, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		switch id {
		case "int_evt_brandA": // account A, async
			v.Delivery.Mode, v.Delivery.WaitMS = registry.ModeAsync, 0
		case "int_evt_brandB": // account B, async (async_callback in the dev config; no subscription needed here)
			v.Delivery.Mode, v.Delivery.WaitMS = registry.ModeAsync, 0
		case "int_evt_brandB_min": // account B, sync
			v.Delivery.Mode, v.Delivery.WaitMS, v.Delivery.OnCircuitOpen = registry.ModeSync, 0, registry.FailFast
			v.Breaker.Override.OpenAfterConsecutiveTimeouts = 0
		}
	})
	for name, v := range snap.Vendors {
		vc := *v
		vc.Regions = map[string]string{"in": mvA.VendorURL, "us": mvB.VendorURL}
		// The M5 chaos breaker (shortened windows), so a dead vendor parks within the test.
		vc.Defaults.Breaker = registry.BreakerConfig{WindowS: 10, MinCalls: 4, FailureRatio: 0.5, ConsecutiveTimeouts: 5, OpenS: 5, OpenMaxS: 40, HalfOpenProbes: 2}
		eps := map[string]registry.Endpoint{}
		for n, ep := range v.Endpoints {
			ep.TimeoutMS, ep.ConnectTimeoutMS = isoTimeoutMS, 200
			eps[n] = ep
		}
		vc.Endpoints = eps
		snap.Vendors[name] = &vc
	}

	m := telemetry.NewMetrics("itest", "isolation")
	log := slog.New(slog.DiscardHandler)
	cache, _ := creds.NewCache(accountLoader{}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	run := func(f func(context.Context)) {
		ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
		done := make(chan struct{})
		go func() { f(ctx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	newBreakers := func() *upstream.Breakers {
		bs := upstream.NewBreakers(rdb, telemetry.NewMetrics("itest", "node"), nil)
		run(bs.Run)
		return bs
	}
	gwBreakers, wBreakers, schedBreakers := newBreakers(), newBreakers(), newBreakers()
	limiter := upstream.NewAccountLimiter(rdb, m, nil)
	q := &queue.Outbox{}
	snaps := staticSnapshots{snap}
	gwTr := &delivery.Transitions{DB: gwDB, Queue: q}
	h := New(Deps{Snapshots: snaps, Creds: cache, Upstream: client, Breakers: gwBreakers, Limiter: limiter, Metrics: m, Log: log,
		Audit: &recordingSink{}, Queueing: &Queueing{DB: gwDB, Queue: q, Name: "gw-" + core.NewULID(time.Now()),
			Settler: delivery.Settler{Transitions: gwTr, Budget: delivery.NewBudget(nil)}}}).Handler()
	// A's recovery must finish within the test: retries on a short schedule and an unbounded
	// retry budget (the 30 s budget deferral has its own tests). Neither touches B.
	budget := delivery.NewBudget(nil)
	budget.Ratio, budget.Burst = 1, 1e6
	wTr := &delivery.Transitions{DB: wDB, Queue: q}
	w := &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: wDB, Queue: q, Transitions: wTr, Snapshots: snaps,
		Attempter: &delivery.Attempter{Creds: cache, Upstream: client, Breakers: wBreakers, Limiter: limiter, Log: log},
		Audit:     audit.Discard{}, Budget: budget, Log: log, Idle: 20 * time.Millisecond,
		Backoff: delivery.Backoff{Schedule: []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second, time.Second,
			time.Second, time.Second, time.Second, time.Second, time.Second, time.Second}},
		Batch: 32, Concurrency: 16, // the pre-HUB-412 defaults
	}
	res := &delivery.Resilience{Breakers: schedBreakers, Upstream: client, Snapshots: snaps, Queue: q, Log: log, RampStep: time.Second, ProbeEvery: 200 * time.Millisecond}
	res.RegisterMetrics(prometheus.NewRegistry())
	sched := &delivery.Scheduler{DB: schedDB, Queue: q, Transitions: &delivery.Transitions{DB: schedDB, Queue: q}, Log: log, Resilience: res,
		Tick: 100 * time.Millisecond, Standby: 100 * time.Millisecond}
	reg := prometheus.NewRegistry()
	w.RegisterMetrics(reg)
	r := &isoRig{t: t, gwDB: gwDB, mvA: mvA, mvB: mvB, h: h, worker: w, sched: sched, reg: reg}
	r.vendorB(healthy())
	r.vendorA(healthy())
	run(w.Run)
	run(sched.Run)
	return r
}

func healthy() mockvendor.Behaviour {
	return mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: isoVendorMS}}
}

func (r *isoRig) vendorA(b mockvendor.Behaviour) {
	r.mvA.SetRoute(r.t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b})
}

func (r *isoRig) vendorB(b mockvendor.Behaviour) {
	r.mvB.SetRoute(r.t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b})
}

// invoke calls the gateway in-process and returns the status, the request id (queued modes) and
// the round-trip time.
func (r *isoRig) invoke(integration string) (int, string, time.Duration) {
	req := httptest.NewRequestWithContext(r.t.Context(), http.MethodPost, "/v1/integrations/"+integration+"/invoke", strings.NewReader(trackBody))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	start := time.Now()
	r.h.ServeHTTP(rec, req)
	took := time.Since(start)
	var out struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.RequestID, took
}

// isoPhase is the traffic of one phase.
type isoPhase struct {
	mu       sync.Mutex
	aIDs     []string
	bIDs     []string
	bSync    []time.Duration
	failures []string
}

func (p *isoPhase) fail(f string, args ...any) {
	p.mu.Lock()
	p.failures = append(p.failures, fmt.Sprintf(f, args...))
	p.mu.Unlock()
}

// drive sends traffic for d: A async 20/s, B async 20/s, B sync 10/s. Every call runs in its
// own goroutine, so a slow call never delays the schedule (open loop).
func (r *isoRig) drive(d time.Duration) *isoPhase {
	p := &isoPhase{}
	var wg sync.WaitGroup
	every := func(period time.Duration, call func()) {
		wg.Go(func() {
			tk := time.NewTicker(period)
			defer tk.Stop()
			end := time.After(d)
			for {
				select {
				case <-end:
					return
				case <-tk.C:
					wg.Go(call)
				}
			}
		})
	}
	every(50*time.Millisecond, func() {
		code, id, _ := r.invoke("int_evt_brandA")
		if code != http.StatusAccepted {
			p.fail("A async accept: %d", code)
			return
		}
		p.mu.Lock()
		p.aIDs = append(p.aIDs, id)
		p.mu.Unlock()
	})
	every(50*time.Millisecond, func() {
		code, id, _ := r.invoke("int_evt_brandB")
		if code != http.StatusAccepted {
			p.fail("B async accept: %d", code)
			return
		}
		p.mu.Lock()
		p.bIDs = append(p.bIDs, id)
		p.mu.Unlock()
	})
	every(100*time.Millisecond, func() {
		code, _, took := r.invoke("int_evt_brandB_min")
		if code != http.StatusOK {
			p.fail("B sync: %d", code)
			return
		}
		p.mu.Lock()
		p.bSync = append(p.bSync, took)
		p.mu.Unlock()
	})
	wg.Wait()
	return p
}

// outcome is a request's terminal row in hub.requests.
type outcome struct {
	status  string
	latency time.Duration // accept → terminal
}

func (r *isoRig) outcomes(ids []string) map[string]outcome {
	rows, err := r.gwDB.Query(r.t.Context(), `SELECT request_id, status, COALESCE(EXTRACT(EPOCH FROM completed_at - created_at), -1)::float8
		FROM hub.requests WHERE request_id = ANY($1) AND created_at > now() - interval '30 minutes'`, ids)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]outcome{}
	for rows.Next() {
		var id, s string
		var secs float64
		if err := rows.Scan(&id, &s, &secs); err != nil {
			r.t.Fatal(err)
		}
		out[id] = outcome{s, time.Duration(secs * float64(time.Second))}
	}
	return out
}

// settled waits until every id is terminal (or the deadline passes) and returns the outcomes.
func (r *isoRig) settled(ids []string, within time.Duration) map[string]outcome {
	deadline := time.Now().Add(within)
	for {
		out := r.outcomes(ids)
		done := 0
		for _, o := range out {
			if o.status == "succeeded" || o.status == "dead_lettered" || o.status == "expired" {
				done++
			}
		}
		if done == len(ids) || time.Now().After(deadline) {
			return out
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// logSlowest logs where the slowest of ids spent its time (ledger breakdown), so a failed bound
// says whether B waited in the queue, at the vendor or in the Hub.
func (r *isoRig) logSlowest(ids []string) {
	var id string
	var total, queueWait, vendor, overhead float64
	var attempts int
	err := r.gwDB.QueryRow(r.t.Context(), `SELECT request_id, EXTRACT(EPOCH FROM completed_at - created_at)*1000, COALESCE(queue_wait_ms, 0),
			COALESCE(vendor_ms, 0), COALESCE(hub_overhead_ms, 0), attempts
		FROM hub.requests WHERE request_id = ANY($1) AND created_at > now() - interval '30 minutes' AND completed_at IS NOT NULL
		ORDER BY completed_at - created_at DESC LIMIT 1`, ids).Scan(&id, &total, &queueWait, &vendor, &overhead, &attempts)
	if err != nil {
		r.t.Logf("slowest B: %v", err)
		return
	}
	r.t.Logf("slowest B %s: %.0f ms total = queue wait %.0f ms (accept → attempt start), vendor %.0f ms, hub overhead %.0f ms, %d attempt(s)",
		id, total, queueWait, vendor, overhead, attempts)
}

// capDeferred reads worker_target_cap_deferred_total for one account.
func (r *isoRig) capDeferred(account string) float64 {
	mfs, err := r.reg.Gather()
	if err != nil {
		r.t.Fatal(err)
	}
	total := 0.0
	for _, mf := range mfs {
		if mf.GetName() != "worker_target_cap_deferred_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "account" && l.GetValue() == account {
					total += m.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func p99(ds []time.Duration) time.Duration { return quantile(ds, 0.99) }

func quantile(ds []time.Duration, q float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[max(i, 0)]
}

// latencies returns the accept → succeeded latencies of ids and the ids that did not succeed.
func latencies(out map[string]outcome, ids []string) ([]time.Duration, []string) {
	var ds []time.Duration
	var bad []string
	for _, id := range ids {
		o, ok := out[id]
		if !ok || o.status != "succeeded" {
			bad = append(bad, fmt.Sprintf("%s=%q", id, o.status))
			continue
		}
		ds = append(ds, o.latency)
	}
	return ds, bad
}

// Bounds on B while A misbehaves, relative to B's own baseline in the same run (same machine,
// same load, A healthy):
//   - async p99 ≤ baseline p99 + 300 ms. A healthy B delivery is one claim poll (20 ms), one
//     20 ms vendor call and two short transactions (about 40 ms p50 here). 300 ms leaves room for
//     scheduling noise on a shared 2-CPU runner under -race, yet stays far below A's slowest call
//     (1.3 s slow, 1.5 s timeout): a B envelope that waits behind even one of A's calls (batch
//     barrier, or no free slot) breaks it.
//   - sync p99 ≤ baseline p99 + 100 ms. The sync path never touches the worker or the outbox;
//     the only shared things are the process, Postgres and Redis.
//
// A baseline that is itself stalled by the machine (seen: 1.2 s p99 with A healthy) must not
// loosen the bound, so the baseline term is clamped (200 ms async, 100 ms sync): the async bound
// is never above 500 ms, under half of A's slow answer.
const (
	isoAsyncSlack   = 300 * time.Millisecond
	isoAsyncBaseMax = 200 * time.Millisecond
	isoSyncSlack    = 100 * time.Millisecond
	isoSyncBaseMax  = 100 * time.Millisecond
)

// TestNoisyNeighbour is HUB-412: while account A's vendor is (a) slow but successful, (b)
// hanging until the timeout or (c) down (connection refused, or 503), account B's async
// delivery and sync calls stay within a tight bound of B's baseline and B loses nothing. A's
// own requests are not lost either: they retry or park, and all succeed after A recovers.
func TestNoisyNeighbour(t *testing.T) {
	scenarios := []struct {
		name      string
		misbehave mockvendor.Behaviour
	}{
		{"a_slow_but_successful", mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: isoSlowMS}}},
		{"b_hangs_until_timeout", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "hang"}}},
		{"c_refuses_connections", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "refuse"}}},
		{"c_answers_503", down()},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			r := newIsoRig(t)
			base := r.drive(isoBaseline)
			r.vendorA(sc.misbehave)
			chaos := r.drive(isoChaos)
			// A recovers. A refusing listener reopens only once its route is gone.
			r.mvA.DeleteRoute(t, "upload")
			r.vendorA(healthy())

			for _, p := range []*isoPhase{base, chaos} {
				if len(p.failures) > 0 {
					t.Fatalf("caller-visible failures: %v", p.failures)
				}
			}
			bIDs := append(slices.Clone(base.bIDs), chaos.bIDs...)
			bOut := r.settled(bIDs, 30*time.Second)
			bBase, badBase := latencies(bOut, base.bIDs)
			bChaos, badChaos := latencies(bOut, chaos.bIDs)
			aIDs := append(slices.Clone(base.aIDs), chaos.aIDs...)
			aOut := r.settled(aIDs, 60*time.Second)
			_, badA := latencies(aOut, aIDs)
			states := map[string]int{}
			for _, id := range chaos.aIDs {
				states[aOut[id].status]++
			}

			t.Logf("B async accept→succeeded: baseline p50 %v p99 %v (n=%d); while A misbehaves p50 %v p99 %v max %v (n=%d)",
				quantile(bBase, 0.5).Round(time.Millisecond), p99(bBase).Round(time.Millisecond), len(bBase),
				quantile(bChaos, 0.5).Round(time.Millisecond), p99(bChaos).Round(time.Millisecond), quantile(bChaos, 1).Round(time.Millisecond), len(bChaos))
			t.Logf("B sync round trip: baseline p50 %v p99 %v (n=%d); while A misbehaves p50 %v p99 %v (n=%d)",
				quantile(base.bSync, 0.5).Round(time.Millisecond), p99(base.bSync).Round(time.Millisecond), len(base.bSync),
				quantile(chaos.bSync, 0.5).Round(time.Millisecond), p99(chaos.bSync).Round(time.Millisecond), len(chaos.bSync))
			r.logSlowest(chaos.bIDs)
			t.Logf("A: %d accepted while misbehaving, final states %v; worker cap deferrals A %v, B %v", len(chaos.aIDs), states,
				r.capDeferred("ct-brandA-in"), r.capDeferred("ct-brandB-us"))

			if len(badBase)+len(badChaos) > 0 {
				t.Errorf("B lost or failed requests: %v %v", badBase, badChaos)
			}
			if got, want := len(r.mvB.Requests(t, nil)), len(bIDs)+len(base.bSync)+len(chaos.bSync); got < want {
				t.Errorf("B's vendor saw %d calls, want at least %d (every accepted request delivered)", got, want)
			}
			if bound := min(p99(bBase), isoAsyncBaseMax) + isoAsyncSlack; p99(bChaos) > bound {
				t.Errorf("B async p99 %v while A misbehaves, want ≤ %v (baseline p99 %v, at most %v, + %v)", p99(bChaos), bound, p99(bBase), isoAsyncBaseMax, isoAsyncSlack)
			}
			if bound := min(p99(base.bSync), isoSyncBaseMax) + isoSyncSlack; p99(chaos.bSync) > bound {
				t.Errorf("B sync p99 %v while A misbehaves, want ≤ %v (baseline p99 %v, at most %v, + %v)", p99(chaos.bSync), bound, p99(base.bSync), isoSyncBaseMax, isoSyncSlack)
			}
			if len(badA) > 0 {
				t.Errorf("A lost requests after recovery (%d): %v", len(badA), badA[:min(len(badA), 5)])
			}
		})
	}
}
