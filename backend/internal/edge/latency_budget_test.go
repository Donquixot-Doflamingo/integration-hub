//go:build integration && latency

package edge

// The sync latency budget gate (HUB-413, docs/tech/LATENCY.md): a vendor that answers in V ms
// must stay at p99 ≤ V + 10 ms through the Hub (the user's requirement: 100 ms → ≤ 110 ms),
// measured by a caller over HTTP, and the Hub may add at most 2 ms at p50 (3 ms with an
// Idempotency-Key: two Redis round trips by design) and 10 ms at p99 over the same calls made
// directly to the vendor in the same run. p50 is judged against the direct calls, not against
// V: the mock vendor itself answers a direct caller ~1 ms after V. Run it without -race (`make latency-budget`): the race detector
// multiplies CPU time and would measure itself.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// The budget, per DESIGN §1 and the user's requirement (100 ms vendor → ≤ 110 ms).
const (
	budgetP50      = 2 * time.Millisecond  // added p50 over direct calls ≤ 2 ms
	budgetP50Keyed = 3 * time.Millisecond  // with an Idempotency-Key (two Redis round trips)
	budgetP99      = 10 * time.Millisecond // hub p99 ≤ vendor + 10 ms; added p99 ≤ 10 ms
	latencySyncInt = "int_evt_brandA"      // tuned to sync mode
	latencyWaitInt = "int_evt_brandB_min"  // wait mode (D3 default), reported
)

// Sample sizes: sequential pairs, concurrent callers × pairs each, keyed pairs, wait pairs.
const (
	warmupPairs  = 40
	seqPairs     = 300
	concCallers  = 8
	concPairs    = 50
	keyedPairs   = 300
	waitModePair = 200
)

type latencyRig struct {
	mv     *mockvendor.Instance
	hubURL string
	client *http.Client
	body   []byte
	seq    int
	mu     sync.Mutex
	ledger *ledgerSink
}

// ledgerSink forwards to the audit writer and keeps the Hub's own overhead of every sync
// request (the audit ledger's hub_overhead_ms), to tell Hub time from time the caller lost
// elsewhere (diagnostics only).
type ledgerSink struct {
	next audit.Sink
	mu   sync.Mutex
	sync []time.Duration
}

func (l *ledgerSink) Emit(b audit.Batch) {
	l.mu.Lock()
	for _, r := range b.Records {
		if r.Mode == string(registry.ModeSync) {
			l.sync = append(l.sync, time.Duration(r.HubOverheadMS*float64(time.Millisecond)))
		}
	}
	l.mu.Unlock()
	l.next.Emit(b)
}

func (l *ledgerSink) take() []time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.sync
	l.sync = nil
	return out
}

// oversleep measures how late a 1 ms sleep wakes up while the gate runs: when the process does
// not get a CPU (another process, a GC), this grows, for direct and Hub calls alike.
func oversleep(stop <-chan struct{}) <-chan []time.Duration {
	out := make(chan []time.Duration, 1)
	go func() {
		var s []time.Duration
		for {
			select {
			case <-stop:
				out <- s
				return
			default:
			}
			start := time.Now()
			time.Sleep(time.Millisecond)
			s = append(s, time.Since(start)-time.Millisecond)
		}
	}()
	return out
}

// newLatencyRig wires the gateway as the gateway role does (dataplane.go): warm credential
// cache, upstream client, Redis-shared breakers and account limiter, idempotency store, audit
// writer on Postgres, metrics, outbox queueing for wait mode, and the tracing HTTP handler, all
// served over a real loopback HTTP server in front of an in-process mockvendor.
func newLatencyRig(t *testing.T) *latencyRig {
	t.Helper()
	dsn, raddr := os.Getenv("HUB_PG_DSN"), os.Getenv("HUB_REDIS_ADDR")
	if dsn == "" || raddr == "" {
		t.Fatal("HUB_PG_DSN and HUB_REDIS_ADDR must be set (make latency-budget)")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := (audit.PGStore{Pool: pool}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -1), 3); err != nil {
		t.Fatal(err)
	}

	mv := mockvendor.Start(t)
	snap := queuedSnapshot(t, e2eSnapshot, mv.VendorURL, func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == latencySyncInt {
			v.Delivery.Mode = registry.ModeSync
		}
	})
	accts := map[string]*registry.Account{}
	for id, a := range snap.Accounts {
		ac := *a
		ac.Quota.RPS = 1000 // the limiter stays on the measured path but never has to wait
		accts[id] = &ac
	}
	snap.Accounts = accts
	snap = withTenantRate(snap)

	rdb := redis.NewClient(&redis.Options{Addr: raddr})
	t.Cleanup(func() { _ = rdb.Close() })
	if keys, _ := rdb.Keys(t.Context(), "hub:rl:acct:*").Result(); len(keys) > 0 {
		rdb.Del(t.Context(), keys...)
	}
	m := telemetry.NewMetrics("gateway", "latency")
	log := slog.New(slog.DiscardHandler)
	cache, err := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	breakers := upstream.NewBreakers(rdb, m, log)
	limiter := upstream.NewAccountLimiter(rdb, m, log)
	writer := audit.NewWriter(audit.PGStore{Pool: pool}, m, audit.WriterOptions{Log: log})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	var bg sync.WaitGroup
	bg.Go(func() { cache.Run(ctx) }) // background reloads, as the data plane runs them
	bg.Go(func() { breakers.Run(ctx) })
	bg.Go(func() { writer.Run(ctx) })
	t.Cleanup(func() { cancel(); bg.Wait(); limiter.Close() })
	q := &queue.Outbox{}
	tr := &delivery.Transitions{DB: pool, Queue: q, Metrics: m}
	tp, err := telemetry.NewTracerProvider(t.Context(), telemetry.TraceConfig{Role: "gateway"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(context.WithoutCancel(t.Context())) })
	ledger := &ledgerSink{next: writer}
	h := New(Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Breakers: breakers, Limiter: limiter,
		Audit: ledger, Masker: audit.NewMasker([]byte("latency-mask-key-0123456789abcdef")), Metrics: m, Log: log,
		Idempotency: idempotency.New(rdb, m, idempotency.Options{Prefix: "latency:" + core.NewULID(time.Now()) + ":"}),
		Queueing:    &Queueing{DB: pool, Queue: q, Settler: delivery.Settler{Transitions: tr, Budget: delivery.NewBudget(nil)}, Name: "gw-latency-" + core.NewULID(time.Now())},
	}).Handler()
	srv := httptest.NewServer(telemetry.HTTPHandler(h, tp, "gateway"))
	t.Cleanup(srv.Close)
	tr2 := &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64, IdleConnTimeout: time.Minute, DisableCompression: true}
	t.Cleanup(tr2.CloseIdleConnections)
	return &latencyRig{mv: mv, hubURL: srv.URL, client: &http.Client{Transport: tr2, Timeout: 10 * time.Second}, body: latencyBody(t), ledger: ledger}
}

// latencyBody is an events.track body of about 4 KB (the DESIGN §18.6 budget size).
func latencyBody(t *testing.T) []byte {
	props := map[string]any{}
	for i := range 60 {
		props[fmt.Sprintf("property_%02d", i)] = fmt.Sprintf("value-%02d-abcdefghijklmnopqrstuvwxyz0123456789", i)
	}
	raw, _ := json.Marshal(map[string]any{"user": map[string]any{"id": "u_48213"},
		"event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": props}})
	if len(raw) < 3500 || len(raw) > 4600 {
		t.Fatalf("body is %d bytes", len(raw))
	}
	return raw
}

func (r *latencyRig) vendor(t *testing.T, ms int) {
	r.mv.Reset(t)
	r.mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: ms}}})
}

func (r *latencyRig) post(url string, hdr map[string]string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(r.body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	d := time.Since(start)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %d %s", url, resp.StatusCode, b)
	}
	return d, nil
}

// hub is one call through the Hub; key sends a fresh Idempotency-Key.
func (r *latencyRig) hub(integration string, key bool) (time.Duration, error) {
	hdr := map[string]string{"Authorization": "Bearer " + devKey}
	if key {
		r.mu.Lock()
		r.seq++
		n := r.seq
		r.mu.Unlock()
		hdr["Idempotency-Key"] = "lat-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.Itoa(n)
	}
	return r.post(r.hubURL+"/v1/integrations/"+integration+"/invoke", hdr)
}

// direct is the same body sent straight to the vendor (mockvendor), no Hub in between.
func (r *latencyRig) direct() (time.Duration, error) { return r.post(r.mv.VendorURL+"/1/upload", nil) }

type samples struct{ hub, direct []time.Duration }

// pairs runs n interleaved (through the Hub, direct) calls, so slow moments of the machine hit
// both sides alike.
func (r *latencyRig) pairs(n int, integration string, key bool, out *samples, mu *sync.Mutex) error {
	for range n {
		h, err := r.hub(integration, key)
		if err != nil {
			return err
		}
		d, err := r.direct()
		if err != nil {
			return err
		}
		if out != nil {
			mu.Lock()
			out.hub, out.direct = append(out.hub, h), append(out.direct, d)
			mu.Unlock()
		}
	}
	return nil
}

func quantile(s []time.Duration, q float64) time.Duration {
	c := slices.Clone(s)
	slices.Sort(c)
	i := int(q*float64(len(c))+0.5) - 1
	return c[min(max(i, 0), len(c)-1)]
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 1, 64)
}

// check logs one result row and, when assert is set, applies the budget.
func check(t *testing.T, vendorMS int, name string, s samples, assert bool, addedP50 time.Duration) {
	t.Helper()
	v := time.Duration(vendorMS) * time.Millisecond
	q := func(x []time.Duration) [3]time.Duration {
		return [3]time.Duration{quantile(x, 0.50), quantile(x, 0.95), quantile(x, 0.99)}
	}
	h, d := q(s.hub), q(s.direct)
	added := [3]time.Duration{h[0] - d[0], h[1] - d[1], h[2] - d[2]}
	t.Logf("LATENCY vendor=%dms scenario=%s n=%d direct p50/p95/p99=%s/%s/%s hub=%s/%s/%s added=%s/%s/%s",
		vendorMS, name, len(s.hub), ms(d[0]), ms(d[1]), ms(d[2]), ms(h[0]), ms(h[1]), ms(h[2]), ms(added[0]), ms(added[1]), ms(added[2]))
	if !assert {
		return
	}
	if h[2] > v+budgetP99 {
		t.Errorf("%s: p99 through the Hub %s ms > %s ms", name, ms(h[2]), ms(v+budgetP99))
	}
	if added[0] > addedP50 {
		t.Errorf("%s: the Hub adds %s ms at p50 over direct calls (budget %s ms)", name, ms(added[0]), ms(addedP50))
	}
	if added[2] > budgetP99 {
		t.Errorf("%s: the Hub adds %s ms at p99 over direct calls (budget %s ms)", name, ms(added[2]), ms(budgetP99))
	}
}

func vendorLatencies(t *testing.T) []int {
	spec := os.Getenv("LATENCY_VENDOR_MS")
	if spec == "" {
		return []int{100}
	}
	var out []int
	for f := range strings.SplitSeq(spec, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 0 {
			t.Fatalf("LATENCY_VENDOR_MS %q", spec)
		}
		out = append(out, n)
	}
	return out
}

func loadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "n/a"
	}
	return strings.Join(strings.Fields(string(b))[:3], " ")
}

// A run is only a measurement of the Hub when the process could run: a goroutine that sleeps
// 1 ms in a loop must never wake more than maxStall late. A longer freeze means the operating
// system gave the CPUs to another process (or the Hub burnt every CPU itself); every call in
// flight then waits, direct ones included, and a 10 ms budget cannot be measured through it.
// Such a run is repeated after the machine settles, up to attempts times; if no run is clean
// the gate fails. A clean run is always judged against the full budget.
const (
	maxStall = 20 * time.Millisecond
	attempts = 3
)

type run struct {
	plain, keyed, wait samples
	own, late          []time.Duration
	gcCycles           uint32
	gcPause            time.Duration
}

// measure runs the warm-up and the measured phases once at vendor latency v.
func (r *latencyRig) measure(t *testing.T) run {
	t.Helper()
	var mu sync.Mutex
	// Warm-up: connections (caller → Hub, Hub → vendor), credential cache, limiter leases,
	// idempotency scripts and the outbox path, before anything is recorded.
	for _, w := range []struct {
		in  string
		key bool
	}{{latencySyncInt, false}, {latencySyncInt, true}, {latencyWaitInt, false}} {
		if err := r.pairs(warmupPairs/2, w.in, w.key, nil, &mu); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for range concCallers { // open the concurrent connections too
		wg.Go(func() { _ = r.pairs(2, latencySyncInt, false, nil, &mu) })
	}
	wg.Wait()
	runtime.GC()
	r.ledger.take()
	stop := make(chan struct{})
	sleeps := oversleep(stop)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	var out run
	if err := r.pairs(seqPairs, latencySyncInt, false, &out.plain, &mu); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, concCallers)
	for range concCallers {
		wg.Go(func() { errs <- r.pairs(concPairs, latencySyncInt, false, &out.plain, &mu) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := r.pairs(keyedPairs, latencySyncInt, true, &out.keyed, &mu); err != nil {
		t.Fatal(err)
	}
	if err := r.pairs(waitModePair, latencyWaitInt, false, &out.wait, &mu); err != nil {
		t.Fatal(err)
	}
	close(stop)
	out.late = <-sleeps
	runtime.ReadMemStats(&after)
	out.own = r.ledger.take()
	out.gcCycles, out.gcPause = after.NumGC-before.NumGC, time.Duration(after.PauseTotalNs-before.PauseTotalNs)
	return out
}

// settle waits (at most 90 s) until the 1-minute load average is below the CPU count, so a
// retry does not start in the middle of someone else's burst.
func settle(t *testing.T) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return
		}
		if l, err := strconv.ParseFloat(strings.Fields(string(b))[0], 64); err != nil || l < float64(runtime.NumCPU()) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("load average still %s after 90 s", loadAvg())
}

// TestLatencyBudget is the HUB-413 gate. Sync mode, with and without an Idempotency-Key (the one
// Redis call DESIGN §18.5 allows), is asserted; wait mode (the D3 default: two Postgres
// transactions around the vendor call) is measured and reported.
func TestLatencyBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("latency gate: run with make latency-budget")
	}
	r := newLatencyRig(t)
	t.Logf("GOMAXPROCS %d, load average %s", runtime.GOMAXPROCS(0), loadAvg())
	for _, v := range vendorLatencies(t) {
		t.Run(fmt.Sprintf("vendor_%dms", v), func(t *testing.T) {
			r.vendor(t, v)
			for attempt := 1; ; attempt++ {
				m := r.measure(t)
				stall := quantile(m.late, 1)
				t.Logf("DIAG vendor=%dms attempt=%d hub own time (audit ledger, sync) p50/p95/p99/max=%s/%s/%s/%s ms; 1 ms sleep woke late by p50/p99/max=%s/%s/%s ms; GC cycles %d, pause total %s ms; load average %s",
					v, attempt, ms(quantile(m.own, 0.5)), ms(quantile(m.own, 0.95)), ms(quantile(m.own, 0.99)), ms(quantile(m.own, 1)),
					ms(quantile(m.late, 0.5)), ms(quantile(m.late, 0.99)), ms(stall), m.gcCycles, ms(m.gcPause), loadAvg())
				if stall > maxStall {
					for _, x := range []struct {
						name string
						s    samples
					}{{"sync", m.plain}, {"sync+idempotency_key", m.keyed}, {"wait_mode", m.wait}} {
						check(t, v, x.name+"(discarded)", x.s, false, 0)
					}
					if attempt == attempts {
						t.Fatalf("no clean measurement in %d attempts: the process was frozen for up to %s ms (limit %s ms); the machine is too busy to measure a %s ms budget",
							attempts, ms(stall), ms(maxStall), ms(budgetP99))
					}
					t.Logf("attempt %d discarded: the process was frozen for %s ms (another process had the CPUs); retrying", attempt, ms(stall))
					settle(t)
					continue
				}
				check(t, v, "sync", m.plain, true, budgetP50)
				check(t, v, "sync+idempotency_key", m.keyed, true, budgetP50Keyed)
				check(t, v, "wait_mode", m.wait, false, 0)
				return
			}
		})
	}
}
