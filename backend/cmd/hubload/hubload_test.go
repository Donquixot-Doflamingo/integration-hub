package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// Minimal assertion helpers (the module uses the standard library only).

func eq[T any](t *testing.T, want, got T, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Errorf("%s: want %#v, got %#v", fmtMsg(msg), want, got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func near(t *testing.T, want, got, delta float64, msg ...any) {
	t.Helper()
	if math.Abs(want-got) > delta {
		t.Errorf("%s: want %v ± %v, got %v", fmtMsg(msg), want, delta, got)
	}
}

func within(t *testing.T, want, got, rel float64, msg ...any) {
	t.Helper()
	near(t, want, got, math.Abs(want)*rel, msg...)
}

func truth(t *testing.T, ok bool, msg ...any) {
	t.Helper()
	if !ok {
		t.Errorf("%s", fmtMsg(msg))
	}
}

func fmtMsg(msg []any) string {
	if len(msg) == 0 {
		return "check failed"
	}
	if f, ok := msg[0].(string); ok {
		return fmt.Sprintf(f, msg[1:]...)
	}
	return fmt.Sprint(msg...)
}

func closeIdle(t *testing.T, c *http.Client) {
	t.Helper()
	t.Cleanup(c.CloseIdleConnections)
}

// A single-threaded vendor that stalls once: an open-loop generator keeps sending during the
// stall and charges the wait to every arrival behind it. A closed-loop client would record
// one slow request and silently send fewer (coordinated omission).
func TestGeneratorIsOpenLoop(t *testing.T) {
	var mu sync.Mutex
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if n.Add(1) == 10 {
			time.Sleep(300 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newHTTPClient(500, 5*time.Second)
	closeIdle(t, c)
	g := &generator{client: c, target: target{method: http.MethodPost, url: srv.URL, body: []byte(`{}`)}, rate: 100, maxInFlight: 500}
	res := g.run(t.Context(), time.Second)

	eq(t, 100, len(res.samples), "every scheduled arrival is sent, stall or not")
	slow := 0
	var lats []time.Duration
	for _, s := range res.samples {
		eq(t, "200", s.outcome)
		lats = append(lats, s.latency)
		if s.latency > 100*time.Millisecond {
			slow++
		}
	}
	truth(t, slow >= 15, "arrivals during the 300 ms stall all wait for it: %d slow", slow)
	truth(t, quantilesOf(lats).P99 >= 200, "p99 %v includes the stall", quantilesOf(lats).P99)
	truth(t, res.elapsed < 2*time.Second, "elapsed %s", res.elapsed)
}

func TestGeneratorDropsBeyondMaxInFlight(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := newHTTPClient(5, 5*time.Second)
	closeIdle(t, c)
	g := &generator{client: c, target: target{method: http.MethodPost, url: srv.URL}, rate: 200, maxInFlight: 5}
	done := make(chan genResult)
	go func() { done <- g.run(t.Context(), 100*time.Millisecond) }()
	time.Sleep(300 * time.Millisecond)
	close(release)
	res := <-done
	out := map[string]int{}
	for _, s := range res.samples {
		out[s.outcome]++
	}
	eq(t, map[string]int{"202": 5, "dropped": 15}, out)
}

func TestOutcomeOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"success has no code", 200, `{"status":"ok"}`, "200"},
		{"accepted", 202, `{"request_id":"req_x"}`, "202"},
		{"canonical error envelope", 503, `{"error":{"code":"UPSTREAM_UNAVAILABLE","message":"x"}}`, "503 UPSTREAM_UNAVAILABLE"},
		{"error without envelope", 502, `bad gateway`, "502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			eq(t, tt.want, outcomeOf(tt.status, []byte(tt.body)))
		})
	}
}

func TestQuantilesOf(t *testing.T) {
	t.Parallel()
	var ds []time.Duration
	for i := 1; i <= 1000; i++ {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	rand.Shuffle(len(ds), func(i, j int) { ds[i], ds[j] = ds[j], ds[i] })
	q := quantilesOf(ds)
	eq(t, Quantiles{N: 1000, Mean: 500.5, P50: 500, P90: 900, P95: 950, P99: 990, P999: 999, Max: 1000}, q)
	eq(t, Quantiles{}, quantilesOf(nil))
}

func TestBodiesAreValidAndSized(t *testing.T) {
	t.Parallel()
	c := canonicalBody(4)
	truth(t, len(c) >= 4096 && len(c) < 4096+100, "canonical body is %d bytes", len(c))
	var in map[string]any
	must(t, json.Unmarshal(c, &in))
	_, hasUser := in["user"]
	truth(t, hasUser, "body has user")
	var v struct {
		D []map[string]any `json:"d"`
	}
	must(t, json.Unmarshal(vendorBody(4), &v))
	eq(t, 1, len(v.D))
	eq(t, "load_test_event", v.D[0]["evtName"])
}

// newOverheadHist mirrors hub_overhead_ms (telemetry.NewMetrics): classic buckets plus a
// native histogram with bucket factor 1.1.
func newOverheadHist(native bool) *prometheus.HistogramVec {
	opts := prometheus.HistogramOpts{Name: "hub_overhead_ms", Help: "test", Buckets: []float64{0.25, 0.5, 1, 2, 3, 4, 5, 7.5, 10, 25, 50}}
	if native {
		opts.NativeHistogramBucketFactor, opts.NativeHistogramMaxBucketNumber = 1.1, 160
	}
	return prometheus.NewHistogramVec(opts, []string{"integration", "mode"})
}

func TestHistogramDeltaQuantiles(t *testing.T) {
	for _, native := range []bool{true, false} {
		name := map[bool]string{true: "native buckets", false: "classic buckets"}[native]
		t.Run(name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			h := newOverheadHist(native)
			reg.MustRegister(h)
			srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
			defer srv.Close()
			ctl := &http.Client{Timeout: 5 * time.Second}
			closeIdle(t, ctl)
			obs := h.WithLabelValues("int_load_sync", "sync")
			other := h.WithLabelValues("int_other", "sync")
			for range 1000 { // before the window: must not count
				obs.Observe(40)
			}
			before, err := scrapeMetrics(t.Context(), ctl, srv.URL)
			must(t, err)
			for i := range 10000 {
				obs.Observe(1 + 2*float64(i)/10000) // uniform 1..3 ms
				other.Observe(45)                   // other integration: filtered out
			}
			for range 200 {
				obs.Observe(20)
			}
			after, err := scrapeMetrics(t.Context(), ctl, srv.URL)
			must(t, err)
			m := map[string]string{"integration": "int_load_sync"}
			d := after.histOf("hub_overhead_ms", m).minus(before.histOf("hub_overhead_ms", m))
			eq(t, native, d.native)
			near(t, 10200, d.count, 0.1)
			within(t, 2.0, d.quantile(0.5), 0.1)
			p99 := d.quantile(0.99) // rank 10098: inside the 200 outliers at 20 ms
			if native {
				within(t, 20, p99, 0.1)
			} else {
				truth(t, p99 > 10 && p99 <= 25, "classic p99 %v within the (10, 25] bucket", p99)
			}
			near(t, 200.0/10200, d.fractionAbove(5), 1e-9, "5 ms is a classic boundary: exact")
			eq(t, -1.0, d.fractionAbove(6))
			s := summarize(d, 5)
			near(t, 200.0/10200, s.OverBudget, 1e-9)
		})
	}
}

func TestScrapeSumsCountersAndGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hub_requests_total", Help: "t"}, []string{"code"})
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "queue_oldest_age_s", Help: "t"}, []string{"pool"})
	reg.MustRegister(c, g)
	c.WithLabelValues("ok").Add(7)
	c.WithLabelValues("UPSTREAM_UNAVAILABLE").Add(3)
	g.WithLabelValues("messaging").Set(4)
	g.WithLabelValues("llm").Set(9)
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	ctl := &http.Client{Timeout: 5 * time.Second}
	closeIdle(t, ctl)
	s, err := scrapeMetrics(t.Context(), ctl, srv.URL)
	must(t, err)
	near(t, 10, s.sumValue("hub_requests_total", nil), 0)
	near(t, 3, s.sumValue("hub_requests_total", map[string]string{"code": "UPSTREAM_UNAVAILABLE"}), 0)
	near(t, 9, s.maxValue("queue_oldest_age_s", nil), 0)
	eq(t, []string{"UPSTREAM_UNAVAILABLE", "ok"}, labelValues(s, "hub_requests_total", "code"))
}

func postCallback(t *testing.T, h http.Handler, reqID, eventID, typ string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"request_id": reqID, "event_id": eventID, "type": typ})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hub/callbacks", bytes.NewReader(b)))
	eq(t, http.StatusNoContent, rec.Code)
}

func TestReceiverCountsDuplicatesAndRedeliveries(t *testing.T) {
	t.Parallel()
	r := newReceiver()
	postCallback(t, r, "req_A", "evt_1", "request.completed") // one outcome: fine
	postCallback(t, r, "req_B", "evt_2", "request.completed") // same event twice: a redelivery
	postCallback(t, r, "req_B", "evt_2", "request.completed")
	postCallback(t, r, "req_C", "evt_3", "request.completed") // two outcomes: a duplicate
	postCallback(t, r, "req_C", "evt_4", "request.failed")
	postCallback(t, r, "req_D", "evt_5", "delivery.status")   // non-terminal only
	postCallback(t, r, "req_X", "evt_6", "request.completed") // never accepted
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader("not json")))
	eq(t, http.StatusBadRequest, rec.Code)

	s := r.stats(map[string]bool{"req_A": true, "req_B": true, "req_C": true, "req_D": true, "req_E": true})
	eq(t, 3, s.calledBack)
	eq(t, 1, s.duplicates)
	eq(t, []string{"req_C"}, s.dupExamples)
	eq(t, 1, s.redeliveries)
	eq(t, 1, s.unknown)
	eq(t, map[string]int{"request.completed": 3, "request.failed": 1, "delivery.status": 1}, s.types)
	truth(t, !s.withCallback["req_D"])
	eq(t, 7, r.total(), "every well-formed POST is counted, the malformed one is not")
}

// fakeControl records mockvendor control calls.
type fakeControl struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeControl) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	entry := r.Method + " " + r.URL.Path
	if r.Method == http.MethodPut && strings.Contains(string(b), `"down"`) {
		entry += " down"
	}
	f.calls = append(f.calls, entry)
	f.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func TestChaosLoopAlternatesAndRestoresTheVendor(t *testing.T) {
	fc := &fakeControl{}
	srv := httptest.NewServer(fc)
	defer srv.Close()
	ctl := &http.Client{Timeout: 5 * time.Second}
	closeIdle(t, ctl)
	c := soakConfig{mockControl: srv.URL, kinds: "refuse", minUp: 10 * time.Millisecond, maxUp: 10 * time.Millisecond, minDown: 30 * time.Millisecond, maxDown: 30 * time.Millisecond}
	ctx, cancel := context.WithTimeout(t.Context(), 170*time.Millisecond)
	defer cancel()
	up := newFlag(true)
	var events []ChaosEvent
	c.chaosLoop(ctx, ctl, rand.New(rand.NewPCG(1, 2)), time.Now(), up, func(e ChaosEvent) { events = append(events, e) })

	truth(t, up.get(), "the vendor is healthy when the loop ends")
	if len(events) == 0 {
		t.Fatal("no chaos events")
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if len(fc.calls) < 4 {
		t.Fatalf("want >= 4 control calls, got %v", fc.calls)
	}
	for i, call := range fc.calls {
		want := "PUT /__control/routes/chaos down"
		if i%2 == 1 {
			want = "DELETE /__control/routes/chaos"
		}
		eq(t, want, call, "call %d", i)
	}
	eq(t, "DELETE /__control/routes/chaos", fc.calls[len(fc.calls)-1], "an outage cut short by the end is still undone")
	for _, e := range events {
		eq(t, "refuse", e.Kind)
		eq(t, "", e.Error)
	}
}

func TestRestartTimesStayInTheMiddle(t *testing.T) {
	t.Parallel()
	at := restartTimes(rand.New(rand.NewPCG(3, 4)), 100*time.Second, 50)
	eq(t, 50, len(at))
	for i, d := range at {
		truth(t, d >= 10*time.Second && d < 80*time.Second, "restart at %s", d)
		if i > 0 {
			truth(t, d >= at[i-1], "sorted")
		}
	}
}

func TestFlagParsing(t *testing.T) {
	t.Parallel()
	var u namedURLs
	must(t, u.Set("gateway=http://127.0.0.1:1/metrics,worker=http://127.0.0.1:2/metrics"))
	eq(t, namedURLs{{"gateway", "http://127.0.0.1:1/metrics"}, {"worker", "http://127.0.0.1:2/metrics"}}, u)
	truth(t, u.Set("nope") != nil, "name=url required")
	var c cpuTargets
	must(t, c.Set("gateway=123,postgres=comm:postgres,pg_load=cmd: hub_load "))
	eq(t, cpuTargets{{name: "gateway", pid: 123}, {name: "postgres", comm: "postgres"}, {name: "pg_load", cmd: " hub_load "}}, c)
	truth(t, c.Set("gateway=abc") != nil, "pid must be numeric")
}

func TestRateConfigValidate(t *testing.T) {
	t.Parallel()
	ok := rateConfig{label: "x", mode: "sync", integration: "int", apiKey: "k", rps: 1, duration: time.Second, vendorLatency: -1}
	tests := []struct {
		name    string
		mutate  func(*rateConfig)
		wantErr string
	}{
		{"valid sync", func(*rateConfig) {}, ""},
		{"direct needs no key", func(c *rateConfig) { c.mode, c.apiKey, c.integration = "direct", "", "" }, ""},
		{"label required", func(c *rateConfig) { c.label = "" }, "-label"},
		{"unknown mode", func(c *rateConfig) { c.mode = "wait" }, "-mode"},
		{"key required for the gateway", func(c *rateConfig) { c.apiKey = "" }, "HUBLOAD_API_KEY"},
		{"latency needs the control URL", func(c *rateConfig) { c.vendorLatency = 50 }, "-mock-control"},
		{"positive rate", func(c *rateConfig) { c.rps = 0 }, "-rps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := ok
			tt.mutate(&c)
			err := c.validate()
			if tt.wantErr == "" {
				must(t, err)
				return
			}
			truth(t, err != nil && strings.Contains(err.Error(), tt.wantErr), "want error containing %q, got %v", tt.wantErr, err)
		})
	}
}

func TestRenderReportComparison(t *testing.T) {
	t.Parallel()
	direct := RateResult{Kind: "rate", Label: "cmp-direct-50", Mode: "direct", VendorLatencyMS: 50, TargetRPS: 100,
		Outcomes: map[string]int{"200": 3000}, LatencyOK: Quantiles{P50: 51, P95: 52, P99: 55}}
	hub := RateResult{Kind: "rate", Label: "cmp-hub-50", Mode: "sync", VendorLatencyMS: 50, TargetRPS: 100,
		Outcomes: map[string]int{"200": 2999, "503 UPSTREAM_UNAVAILABLE": 1}, LatencyOK: Quantiles{P50: 53.5, P95: 55, P99: 54},
		Ledger: &LedgerStats{Rows: 3000, P50: 1.2, P95: 2.1, P99: 3.4, WallP50: 1.5, WallP95: 2.5, WallP99: 4.4}}
	var out bytes.Buffer
	renderReport(&out, []RateResult{hub, direct}, nil)
	truth(t, strings.Contains(out.String(), "| 50 ms | 100 | 51.0 / 52.0 / 55.0 | 53.5 / 55.0 / 54.0 | 2.5 / 3.0 / -1.0 | 1.2 / 2.1 / 3.4 | 1.5 / 2.5 / 4.4 | 0; 503 UPSTREAM_UNAVAILABLE ×1 |"), out.String())
}

func TestSettleReturnsWhenBelowThreshold(t *testing.T) {
	t.Parallel()
	start := time.Now()
	settle(t.Context(), 1e6, time.Hour, time.Hour, io.Discard)
	truth(t, time.Since(start) < time.Second, "settle returned at once")
}

func TestCPUAccountingSeesThisProcess(t *testing.T) {
	t.Parallel()
	targets := cpuTargets{}
	a := targets.snap()
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) { //nolint:revive // burn CPU on purpose
	}
	u := cpuBetween(a, targets.snap())
	truth(t, u.Cores["hubload"] > 0 && u.MachineBusy >= 0, "cpu %+v", u)
}

// hub_overhead_ms can hold negative observations (HUB-405: the gateway computes it with a
// timestamp taken before the vendor call). Native negative buckets must be read, not dropped.
func TestHistogramQuantileWithNegativeObservations(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := newOverheadHist(true)
	reg.MustRegister(h)
	srv := httptest.NewServer(promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	defer srv.Close()
	ctl := &http.Client{Timeout: 5 * time.Second}
	closeIdle(t, ctl)
	obs := h.WithLabelValues("int_load_sync", "sync")
	for range 1000 {
		obs.Observe(-10)
		obs.Observe(2)
	}
	s, err := scrapeMetrics(t.Context(), ctl, srv.URL)
	must(t, err)
	d := s.histOf("hub_overhead_ms", nil)
	truth(t, d.native, "native buckets present")
	within(t, -10, d.quantile(0.25), 0.1, "p25 in the negative half")
	within(t, 2, d.quantile(0.75), 0.1, "p75 in the positive half")
	near(t, 0.5, summarize(d, 5).Negative, 0.001, "negative share")
}
