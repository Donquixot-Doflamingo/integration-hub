package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// namedURLs is a repeatable name=url flag (ops /metrics endpoints of the Hub processes).
type namedURLs []namedURL

type namedURL struct{ name, url string }

func (n *namedURLs) String() string { return fmt.Sprint(len(*n)) }

func (n *namedURLs) Set(v string) error {
	for part := range strings.SplitSeq(v, ",") {
		name, u, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" || u == "" {
			return fmt.Errorf("want name=url, got %q", part)
		}
		*n = append(*n, namedURL{name, u})
	}
	return nil
}

// RateResult is the JSON written by `hubload rate`.
type RateResult struct {
	Kind            string                 `json:"kind"` // "rate"
	Label           string                 `json:"label"`
	Mode            string                 `json:"mode"`
	Integration     string                 `json:"integration,omitempty"`
	VendorLatencyMS int                    `json:"vendor_latency_ms"`
	TargetRPS       float64                `json:"target_rps"`
	StartedAt       time.Time              `json:"started_at"`
	DurationS       float64                `json:"duration_s"`
	Sent            int                    `json:"sent"`
	OK              int                    `json:"ok"`
	AchievedRPS     float64                `json:"achieved_rps"` // 2xx responses per second of the run
	Outcomes        map[string]int         `json:"outcomes"`
	Latency         Quantiles              `json:"latency"`    // from intended send time, all answered arrivals
	LatencyOK       Quantiles              `json:"latency_ok"` // 2xx only
	Service         Quantiles              `json:"service"`    // from actual send time
	SendLag         Quantiles              `json:"send_lag"`   // generator lateness (CPU starvation of the generator)
	Overhead        map[string]HistSummary `json:"overhead"`   // hub_overhead_ms delta by source/mode
	Stages          map[string]HistSummary `json:"stages"`     // hub_stage_duration_ms delta by source/stage
	VendorLatency   map[string]HistSummary `json:"vendor_ms"`  // vendor_latency_ms delta by source
	Finished        map[string]float64     `json:"finished"`   // hub_requests_total delta by source
	Queue           *QueueStats            `json:"queue,omitempty"`
	Ledger          *LedgerStats           `json:"ledger,omitempty"`           // hub.requests.hub_overhead_ms of the window
	AuditDropped    float64                `json:"audit_dropped,omitempty"`    // audit_events_dropped_total delta
	AuditDroppedBy  map[string]float64     `json:"audit_dropped_by,omitempty"` // same, by source/reason
	CPU             CPUUsage               `json:"cpu"`
	LoadAvgStart    [3]float64             `json:"load_avg_start"`
	LoadAvgEnd      [3]float64             `json:"load_avg_end"`
	Notes           []string               `json:"notes,omitempty"`
}

// QueueStats are the outbox gauges sampled during and after an async run.
type QueueStats struct {
	MaxDepth         float64 `json:"max_depth"`           // accepted + retry_scheduled + in_flight (DB)
	MaxOldestAgeS    float64 `json:"max_oldest_age_s"`    // oldest due unclaimed envelope (DB, 1 s samples)
	MaxOldestMetricS float64 `json:"max_oldest_metric_s"` // queue_oldest_age_s as exported (10 s samples)
	MaxDepthMetric   float64 `json:"max_depth_metric"`    // queue_depth as exported (10 s samples)
	DepthAtEnd       float64 `json:"depth_at_end"`        // when traffic stopped
	DrainS           float64 `json:"drain_s"`             // traffic stop -> queue empty (-1: not within -drain)
	WorkerRate       float64 `json:"worker_rate"`         // envelopes finished per second while the queue was non-empty
}

type rateConfig struct {
	label, mode, url, integration, apiKey string
	rps                                   float64
	duration, warmup, timeout, drain      time.Duration
	bodyKB, maxInFlight, vendorLatency    int
	mockControl, pgDSN, out               string
	metrics                               namedURLs
	cpu                                   cpuTargets
	settleLoad                            float64
	settleMax                             time.Duration
}

func rateCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	var c rateConfig
	fs := flag.NewFlagSet("hubload rate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.label, "label", "", "result label (required)")
	fs.StringVar(&c.mode, "mode", "sync", "sync | async (invoke the gateway) | direct (POST the vendor body to mockvendor)")
	fs.StringVar(&c.url, "url", "http://127.0.0.1:8080", "gateway base URL (sync, async) or mockvendor vendor URL (direct)")
	fs.StringVar(&c.integration, "integration", "", "integration id (sync, async)")
	fs.Float64Var(&c.rps, "rps", 100, "arrivals per second (open loop)")
	fs.DurationVar(&c.duration, "duration", 30*time.Second, "measured duration")
	fs.DurationVar(&c.warmup, "warmup", 5*time.Second, "unmeasured warm-up at the same rate")
	fs.DurationVar(&c.timeout, "timeout", 10*time.Second, "client timeout per request")
	fs.DurationVar(&c.drain, "drain", 0, "async: after traffic, wait up to this long for the queue to empty")
	fs.IntVar(&c.bodyKB, "body-kb", 4, "canonical body size in KiB")
	fs.IntVar(&c.maxInFlight, "max-inflight", 4000, "arrivals beyond this many outstanding are dropped (counted)")
	fs.IntVar(&c.vendorLatency, "vendor-latency-ms", -1, "program mockvendor POST /1/upload with this fixed latency first (needs -mock-control)")
	fs.StringVar(&c.mockControl, "mock-control", "", "mockvendor control URL")
	fs.StringVar(&c.pgDSN, "pg-dsn", getenv("HUB_PG_DSN"), "Postgres DSN for outbox sampling (async)")
	fs.StringVar(&c.out, "out", "", "write the JSON result here (default stdout)")
	fs.Var(&c.metrics, "metrics", "name=url of a Hub /metrics endpoint (repeatable)")
	fs.Var(&c.cpu, "cpu", "name=pid or name=comm:<comm> to report CPU for (repeatable)")
	fs.Float64Var(&c.settleLoad, "settle-load", 0, "wait for the 1-minute load average to drop below this first (0: off)")
	fs.DurationVar(&c.settleMax, "settle-max", time.Minute, "longest settle wait")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c.apiKey = getenv("HUBLOAD_API_KEY")
	if err := c.validate(); err != nil {
		fmt.Fprintln(stderr, "hubload rate:", err)
		return 2
	}
	res, err := runRate(ctx, c, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "hubload rate:", err)
		return 1
	}
	if err := writeJSON(c.out, stdout, res); err != nil {
		fmt.Fprintln(stderr, "hubload rate:", err)
		return 1
	}
	return 0
}

func (c rateConfig) validate() error {
	var errs []error
	if c.label == "" {
		errs = append(errs, errors.New("-label is required"))
	}
	switch c.mode {
	case "sync", "async":
		if c.integration == "" {
			errs = append(errs, errors.New("-integration is required for sync and async"))
		}
		if c.apiKey == "" {
			errs = append(errs, errors.New("HUBLOAD_API_KEY is required for sync and async"))
		}
	case "direct":
	default:
		errs = append(errs, fmt.Errorf("-mode: want sync, async or direct, got %q", c.mode))
	}
	if c.rps <= 0 || c.duration <= 0 {
		errs = append(errs, errors.New("-rps and -duration must be positive"))
	}
	if c.vendorLatency >= 0 && c.mockControl == "" {
		errs = append(errs, errors.New("-vendor-latency-ms needs -mock-control"))
	}
	return errors.Join(errs...)
}

func (c rateConfig) target() target {
	h := http.Header{"Content-Type": {"application/json"}}
	if c.mode == "direct" {
		return target{method: http.MethodPost, url: strings.TrimRight(c.url, "/") + "/1/upload", header: h, body: vendorBody(c.bodyKB)}
	}
	h.Set("Authorization", "Bearer "+c.apiKey)
	return target{method: http.MethodPost, url: strings.TrimRight(c.url, "/") + "/v1/integrations/" + c.integration + "/invoke", header: h, body: canonicalBody(c.bodyKB)}
}

func runRate(ctx context.Context, c rateConfig, log io.Writer) (*RateResult, error) {
	ctl := &http.Client{Timeout: 10 * time.Second}
	if c.settleLoad > 0 {
		settle(ctx, c.settleLoad, 5*time.Second, c.settleMax, log)
	}
	if c.vendorLatency >= 0 {
		if err := setHealthyVendor(ctx, ctl, c.mockControl, c.vendorLatency); err != nil {
			return nil, err
		}
	}
	var pool *pgxpool.Pool
	if c.mode != "direct" && c.pgDSN != "" {
		p, err := pgxpool.New(ctx, c.pgDSN)
		if err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
		defer p.Close()
		pool = p
	}
	g := &generator{client: newHTTPClient(c.maxInFlight, c.timeout), target: c.target(), rate: c.rps, maxInFlight: c.maxInFlight}
	if c.warmup > 0 {
		fmt.Fprintf(log, "%s: warm-up %s at %.0f/s\n", c.label, c.warmup, c.rps)
		g.run(ctx, c.warmup)
		if pool != nil && c.mode == "async" && c.drain > 0 { // start the measured window with an empty queue
			waitQueueEmpty(ctx, pool, c.drain)
		}
	}
	res := &RateResult{Kind: "rate", Label: c.label, Mode: c.mode, Integration: c.integration, VendorLatencyMS: c.vendorLatency, TargetRPS: c.rps,
		Overhead: map[string]HistSummary{}, Stages: map[string]HistSummary{}, VendorLatency: map[string]HistSummary{}, Finished: map[string]float64{}}
	before := scrapeAll(ctx, ctl, c.metrics, log)
	cpu0 := c.cpu.snap()
	res.LoadAvgStart, res.StartedAt = loadAvg(), time.Now().UTC()

	sampler := newQueueSampler(pool, ctl, c.metrics)
	sctx, stopSampler := context.WithCancel(ctx)
	var swg sync.WaitGroup
	if pool != nil && c.mode == "async" {
		swg.Go(func() { sampler.run(sctx) })
	}
	fmt.Fprintf(log, "%s: %s at %.0f/s for %s\n", c.label, c.mode, c.rps, c.duration)
	gr := g.run(ctx, c.duration)
	cpu1 := c.cpu.snap()
	after := scrapeAll(ctx, ctl, c.metrics, log)
	res.LoadAvgEnd = loadAvg()
	res.CPU = cpuBetween(cpu0, cpu1)
	res.DurationS = round3(gr.elapsed.Seconds())
	fillLatency(res, gr)
	fillServerMetrics(res, before, after, c.integration)

	if pool != nil && c.mode == "async" {
		q := &QueueStats{DrainS: -1}
		q.DepthAtEnd = sampler.depthNow(ctx)
		drainStart := time.Now()
		if c.drain > 0 && waitQueueEmpty(ctx, pool, c.drain) {
			q.DrainS = round3(time.Since(drainStart).Seconds())
		}
		// Worker throughput over traffic plus drain: the arrival rate when workers kept up,
		// their capacity when a backlog built (they were busy the whole time).
		finished := sumFinished(scrapeAll(ctx, ctl, c.metrics, log)) - sumFinished(before)
		q.WorkerRate = round3(finished / (gr.elapsed + time.Since(drainStart)).Seconds())
		stopSampler()
		swg.Wait()
		q.MaxDepth, q.MaxOldestAgeS, q.MaxOldestMetricS = sampler.maxDepth, round3(sampler.maxOldest), sampler.maxOldestMetric
		q.MaxDepthMetric = sampler.maxDepthMetric
		res.Queue = q
	}
	stopSampler()
	if pool != nil {
		res.Ledger = ledgerOverhead(ctx, pool, c.integration, res.StartedAt, res.StartedAt.Add(gr.elapsed))
		res.AuditDropped = sumAll(after, "audit_events_dropped_total") - sumAll(before, "audit_events_dropped_total")
	}
	for name, a := range after {
		for _, reason := range labelValues(a, "audit_events_dropped_total", "reason") {
			m := map[string]string{"reason": reason}
			if d := a.sumValue("audit_events_dropped_total", m) - before[name].sumValue("audit_events_dropped_total", m); d > 0 {
				if res.AuditDroppedBy == nil {
					res.AuditDroppedBy = map[string]float64{}
				}
				res.AuditDroppedBy[name+"/"+reason] = d
			}
		}
	}
	if p := res.SendLag.P99; p > 5 {
		res.Notes = append(res.Notes, fmt.Sprintf("generator send lag p99 %.1f ms: the load generator was CPU-starved; latency includes that lag", p))
	}
	return res, nil
}

func fillLatency(res *RateResult, gr genResult) {
	res.Outcomes = map[string]int{}
	var lat, latOK, svc, lag []time.Duration
	for _, s := range gr.samples {
		res.Outcomes[s.outcome]++
		if s.outcome == "dropped" {
			continue
		}
		lat, svc, lag = append(lat, s.latency), append(svc, s.service), append(lag, s.lag)
		if s.ok {
			latOK = append(latOK, s.latency)
		}
	}
	res.Sent, res.OK = len(gr.samples), len(latOK)
	res.AchievedRPS = round3(float64(res.OK) / gr.elapsed.Seconds())
	res.Latency, res.LatencyOK, res.Service, res.SendLag = quantilesOf(lat), quantilesOf(latOK), quantilesOf(svc), quantilesOf(lag)
}

const overheadBudgetMS = 5

func fillServerMetrics(res *RateResult, before, after map[string]scrape, integration string) {
	for name, a := range after {
		b := before[name]
		if integration != "" {
			for _, mode := range labelValues(a, "hub_overhead_ms", "mode") {
				m := map[string]string{"integration": integration, "mode": mode}
				d := a.histOf("hub_overhead_ms", m).minus(b.histOf("hub_overhead_ms", m))
				if d.count > 0 {
					res.Overhead[name+"/"+mode] = summarize(d, overheadBudgetMS)
				}
			}
		}
		for _, stage := range labelValues(a, "hub_stage_duration_ms", "stage") {
			m := map[string]string{"stage": stage}
			d := a.histOf("hub_stage_duration_ms", m).minus(b.histOf("hub_stage_duration_ms", m))
			if d.count > 0 {
				res.Stages[name+"/"+stage] = summarize(d, 0)
			}
		}
		if d := a.histOf("vendor_latency_ms", nil).minus(b.histOf("vendor_latency_ms", nil)); d.count > 0 {
			res.VendorLatency[name] = summarize(d, 0)
		}
		if f := a.sumValue("hub_requests_total", nil) - b.sumValue("hub_requests_total", nil); f > 0 {
			res.Finished[name] = f
		}
	}
}

func labelValues(s scrape, name, label string) []string {
	set := map[string]bool{}
	for _, m := range s[name].GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == label {
				set[lp.GetValue()] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func scrapeAll(ctx context.Context, c *http.Client, urls namedURLs, log io.Writer) map[string]scrape {
	out := map[string]scrape{}
	for _, u := range urls {
		s, err := scrapeMetrics(ctx, c, u.url)
		if err != nil {
			fmt.Fprintf(log, "warning: %v\n", err)
			s = scrape{}
		}
		out[u.name] = s
	}
	return out
}

func sumFinished(s map[string]scrape) float64 {
	return s["worker"].sumValue("hub_requests_total", nil)
}

// queueSampler samples outbox depth and oldest due age from Postgres every second (the same
// queries as the scheduler's queue_depth / queue_oldest_age_s, which it refreshes every 10 s).
type queueSampler struct {
	pool                                 *pgxpool.Pool
	ctl                                  *http.Client
	metrics                              namedURLs
	mu                                   sync.Mutex
	maxDepth, maxOldest, maxOldestMetric float64
	maxDepthMetric                       float64
}

func newQueueSampler(pool *pgxpool.Pool, ctl *http.Client, metrics namedURLs) *queueSampler {
	return &queueSampler{pool: pool, ctl: ctl, metrics: metrics}
}

const (
	depthSQL  = `SELECT count(*) FROM hub.outbox WHERE status IN ('accepted', 'retry_scheduled', 'in_flight')`
	oldestSQL = `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(next_attempt_at)), 0)::float8 FROM hub.outbox
		WHERE status IN ('accepted', 'retry_scheduled') AND next_attempt_at <= now()`
)

func (q *queueSampler) depthNow(ctx context.Context) float64 {
	var n int64
	if err := q.pool.QueryRow(ctx, depthSQL).Scan(&n); err != nil {
		return -1
	}
	return float64(n)
}

func (q *queueSampler) run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		d := q.depthNow(ctx)
		var age float64
		_ = q.pool.QueryRow(ctx, oldestSQL).Scan(&age)
		var metricAge, metricDepth float64
		for _, u := range q.metrics {
			if u.name != "scheduler" {
				continue
			}
			if s, err := scrapeMetrics(ctx, q.ctl, u.url); err == nil {
				metricAge = s.maxValue("queue_oldest_age_s", nil)
				metricDepth = s.sumValue("queue_depth", nil)
			}
		}
		q.mu.Lock()
		q.maxDepth, q.maxOldest, q.maxOldestMetric = math.Max(q.maxDepth, d), math.Max(q.maxOldest, age), math.Max(q.maxOldestMetric, metricAge)
		q.maxDepthMetric = math.Max(q.maxDepthMetric, metricDepth)
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// waitQueueEmpty polls until the outbox holds nothing due or in flight; false on timeout.
func waitQueueEmpty(ctx context.Context, pool *pgxpool.Pool, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var n int64
		if err := pool.QueryRow(ctx, depthSQL).Scan(&n); err == nil && n == 0 {
			return true
		}
		t := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
	return false
}

// setHealthyVendor programs mockvendor's upload route (contracts/openapi/mockvendor.v1.yaml).
func setHealthyVendor(ctx context.Context, c *http.Client, control string, latencyMS int) error {
	b := map[string]any{"status": 200, "headers": map[string]string{"X-Request-Id": "mock-load"},
		"body": map[string]any{"status": "success", "processed": 1, "unprocessed": []any{}}}
	if latencyMS > 0 {
		b["latency"] = map[string]any{"mode": "fixed", "fixed_ms": latencyMS}
	}
	return mockPut(ctx, c, control, "/__control/routes/upload", map[string]any{"match": map[string]string{"method": "POST", "path": "/1/upload"}, "behaviour": b})
}

func mockPut(ctx context.Context, c *http.Client, control, path string, body any) error {
	return mockCall(ctx, c, http.MethodPut, control, path, body)
}

func mockCall(ctx context.Context, c *http.Client, method, control, path string, body any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(control, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("mockvendor %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 && (method != http.MethodDelete || resp.StatusCode != http.StatusNotFound) {
		return fmt.Errorf("mockvendor %s %s: status %d", method, path, resp.StatusCode)
	}
	return nil
}

func writeJSON(path string, stdout io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path == "" {
		_, err = stdout.Write(b)
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func sumAll(s map[string]scrape, name string) float64 {
	var v float64
	for _, sc := range s {
		v += sc.sumValue(name, nil)
	}
	return v
}

// LedgerStats is the server-side overhead as the audit ledger stores it (hub.requests,
// computed when the request is recorded: wall time minus vendor time minus deliberate waits).
type LedgerStats struct {
	Rows      int     `json:"rows"`
	P50       float64 `json:"p50"`
	P95       float64 `json:"p95"`
	P99       float64 `json:"p99"`
	Max       float64 `json:"max"`
	Over5     float64 `json:"over_5ms_fraction"`
	VendorP50 float64 `json:"vendor_p50"`
	VendorP99 float64 `json:"vendor_p99"`
	// Wall* is (completed_at - created_at) - vendor_ms: the overhead without excluding the
	// account limiter's wait (its Redis round trip is counted as a deliberate wait, HUB-408).
	WallP50   float64 `json:"wall_overhead_p50"`
	WallP95   float64 `json:"wall_overhead_p95"`
	WallP99   float64 `json:"wall_overhead_p99"`
	WallOver5 float64 `json:"wall_over_5ms_fraction"`
}

const ledgerSQL = `SELECT count(*),
	COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY hub_overhead_ms), -1),
	COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY hub_overhead_ms), -1),
	COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY hub_overhead_ms), -1),
	COALESCE(max(hub_overhead_ms), -1),
	COALESCE(avg((hub_overhead_ms > 5)::int), 0)::float8,
	COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY vendor_ms), -1),
	COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY vendor_ms), -1),
	COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY wall), -1),
	COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY wall), -1),
	COALESCE(percentile_cont(0.99) WITHIN GROUP (ORDER BY wall), -1),
	COALESCE(avg((wall > 5)::int), 0)::float8
	FROM (SELECT hub_overhead_ms, vendor_ms,
			EXTRACT(EPOCH FROM completed_at - created_at)::float8 * 1000 - COALESCE(vendor_ms, 0) AS wall
		FROM hub.requests WHERE integration_id = $1 AND created_at >= $2 AND created_at < $3
			AND hub_overhead_ms IS NOT NULL AND completed_at IS NOT NULL) w`

// ledgerOverhead waits for the buffered audit writer to flush the window, then reads it.
func ledgerOverhead(ctx context.Context, pool *pgxpool.Pool, integration string, from, to time.Time) *LedgerStats {
	var l LedgerStats
	prev := -1
	for range 20 { // until the row count is stable for one poll, at most 10 s
		if err := pool.QueryRow(ctx, ledgerSQL, integration, from, to).Scan(&l.Rows, &l.P50, &l.P95, &l.P99, &l.Max, &l.Over5, &l.VendorP50, &l.VendorP99,
			&l.WallP50, &l.WallP95, &l.WallP99, &l.WallOver5); err != nil {
			return nil
		}
		if l.Rows == prev {
			break
		}
		prev = l.Rows
		if !sleepCtx(ctx, 500*time.Millisecond) {
			break
		}
	}
	l.P50, l.P95, l.P99, l.Max, l.Over5 = round3(l.P50), round3(l.P95), round3(l.P99), round3(l.Max), round3(l.Over5)
	l.VendorP50, l.VendorP99 = round3(l.VendorP50), round3(l.VendorP99)
	l.WallP50, l.WallP95, l.WallP99, l.WallOver5 = round3(l.WallP50), round3(l.WallP95), round3(l.WallP99), round3(l.WallOver5)
	return &l
}
