package telemetry

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Stage names one step of the DESIGN §18.4 pipeline. Values are bounded label values of
// hub_stage_duration_ms; add new ones here, never build them at runtime.
type Stage string

const (
	StageHTTP        Stage = "http"
	StageAuth        Stage = "auth"
	StageRateLimit   Stage = "rate_limit"
	StageBodyLimit   Stage = "body_limit"
	StageValidate    Stage = "validate"
	StageResolve     Stage = "resolve"
	StageIdempotency Stage = "idempotency"
	StageTransform   Stage = "transform"
	StageAdmission   Stage = "admission" // breaker, account limiter, bulkhead
	// StageWait is deliberate waiting (account limiter tokens): excluded from hub_overhead_ms
	// like vendor time, and reported in hub_wait_ms.
	StageWait        Stage = "wait"
	StageUpstream    Stage = "upstream" // vendor time: excluded from hub_overhead_ms
	StageResponseMap Stage = "response_map"
	StageAudit       Stage = "audit"
)

// Histogram boundaries in ms. Each budget (0.1, 0.5, 5, 10 ms) is a boundary, so the share of
// observations over budget is exact (go-observability skill).
var (
	stageBucketsMS    = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
	overheadBucketsMS = []float64{0.25, 0.5, 1, 2, 3, 4, 5, 7.5, 10, 25, 50}
	// redisBucketsS covers the 1 ms idempotency budget (DESIGN §18.6) with boundaries on both
	// sides of it, plus the tail up to a client timeout.
	redisBucketsS = []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.1, 0.5}
)

// Metrics owns the Prometheus registry of one process. Constructors receive it and register
// their own instruments; there is no global registry.
type Metrics struct {
	Registry *prometheus.Registry
	// Overhead is hub_overhead_ms{integration, mode}: server time minus vendor time minus
	// deliberate waits (DESIGN §1, OBSERVABILITY §3.2).
	Overhead *prometheus.HistogramVec
	// StageDuration is hub_stage_duration_ms{stage}: own time of each pipeline stage.
	StageDuration *prometheus.HistogramVec
	// RedisOpDuration is redis_op_duration_seconds{op}: one observation per Redis round trip,
	// shared by every package that talks to Redis (idempotency now; breakers and limiters later).
	// op values are constants of the owning package, never built at runtime.
	RedisOpDuration *prometheus.HistogramVec
}

// NewMetrics creates the registry with Go runtime and process collectors and the Hub-wide
// histograms. role is a constant label on hub_build_info.
func NewMetrics(role, version string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(collectors.MetricsAll)),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{
		Registry: reg,
		Overhead: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:                           "hub_overhead_ms",
			Help:                           "Hub-added latency per request in ms (server time minus vendor time and deliberate waits).",
			Buckets:                        overheadBucketsMS,
			NativeHistogramBucketFactor:    1.1,
			NativeHistogramMaxBucketNumber: 160,
		}, []string{"integration", "mode"}),
		StageDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:                           "hub_stage_duration_ms",
			Help:                           "Own duration of one request-pipeline stage in ms (DESIGN §18.4).",
			Buckets:                        stageBucketsMS,
			NativeHistogramBucketFactor:    1.1,
			NativeHistogramMaxBucketNumber: 160,
		}, []string{"stage"}),
		RedisOpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:                           "redis_op_duration_seconds",
			Help:                           "Duration of one Redis round trip in seconds, by operation.",
			Buckets:                        redisBucketsS,
			NativeHistogramBucketFactor:    1.1,
			NativeHistogramMaxBucketNumber: 160,
		}, []string{"op"}),
	}
	build := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "hub_build_info",
		Help:        "Always 1; labels carry the build version and role.",
		ConstLabels: prometheus.Labels{"version": version, "role": role},
	})
	build.Set(1)
	reg.MustRegister(m.Overhead, m.StageDuration, m.RedisOpDuration, build)
	return m
}

// Handler serves /metrics (ops port only). OpenMetrics is enabled so exemplars are exposed.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{EnableOpenMetrics: true, Registry: m.Registry})
}

// Millis converts d to fractional milliseconds for the _ms histograms.
func Millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Timings accumulates per-stage durations of one request without allocating. The edge and
// delivery pipelines own one per request and record it once at the end (RecordTimings).
type Timings struct {
	Start time.Time
	arr   [12]stageTime
	n     int
}

type stageTime struct {
	stage Stage
	d     time.Duration
}

// NewTimings starts a request clock.
func NewTimings(start time.Time) Timings { return Timings{Start: start} }

// Observe adds the time since since to stage.
func (t *Timings) Observe(stage Stage, since time.Time) { t.Add(stage, time.Since(since)) }

// Add adds d to stage.
func (t *Timings) Add(stage Stage, d time.Duration) {
	for i := range t.n {
		if t.arr[i].stage == stage {
			t.arr[i].d += d
			return
		}
	}
	if t.n < len(t.arr) {
		t.arr[t.n] = stageTime{stage, d}
		t.n++
	}
}

// Stage returns the accumulated time of stage.
func (t *Timings) Stage(stage Stage) time.Duration {
	for i := range t.n {
		if t.arr[i].stage == stage {
			return t.arr[i].d
		}
	}
	return 0
}

// Overhead is wall time since Start minus vendor time.
func (t *Timings) Overhead(now time.Time) time.Duration {
	return now.Sub(t.Start) - t.Stage(StageUpstream) - t.Stage(StageWait)
}

// RecordTimings publishes every stage and hub_overhead_ms for one finished request.
func (m *Metrics) RecordTimings(t *Timings, now time.Time, integration, mode string) {
	for i := range t.n {
		if t.arr[i].stage != StageUpstream {
			m.StageDuration.WithLabelValues(string(t.arr[i].stage)).Observe(Millis(t.arr[i].d))
		}
	}
	m.Overhead.WithLabelValues(integration, mode).Observe(Millis(t.Overhead(now)))
}

// TimedMiddleware wraps an HTTP middleware so hub_stage_duration_ms{stage} records only the
// middleware's own time, excluding the handlers after it (DESIGN §18.5 rule 5).
func (m *Metrics) TimedMiddleware(stage Stage, mw func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	obs := m.StageDuration.WithLabelValues(string(stage))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var downstream time.Duration
			inner := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s := time.Now()
				next.ServeHTTP(w, r)
				downstream += time.Since(s)
			}))
			start := time.Now()
			inner.ServeHTTP(w, r)
			obs.Observe(Millis(time.Since(start) - downstream))
		})
	}
}
