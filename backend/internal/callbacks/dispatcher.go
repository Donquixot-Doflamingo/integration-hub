package callbacks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// DefaultSchedule is the callback retry schedule (DESIGN §12.1: own schedule, independent of
// vendor delivery; F-11 asks for the async backoff shape within about 24 h). Entry n is the
// wait after failed attempt n+1, each with ±20 % jitter. 11 attempts in all over ≈ 19.7 h
// (15.8–23.7 h with jitter); the 11th failure dead-letters the delivery.
var DefaultSchedule = []time.Duration{
	5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour,
}

// AuditSeqBase offsets the request_events seq of callback.delivered rows: seq = AuditSeqBase +
// subscription×100 + attempt. The request's own trail numbers its events 1…n, so callback rows
// never collide with it, and deliveries of one request differ by subscription and attempt.
const AuditSeqBase = 1_000_000

// maxScheduleLen keeps attempt ≤ 99 so the audit seq stays unique per subscription.
const maxScheduleLen = 98

// Delivery status values (hub.callback_deliveries.status) and outcome labels.
const (
	statusDelivered      = "delivered"
	statusRetryScheduled = "retry_scheduled"
	statusDeadLettered   = "dead_lettered"
)

// Error classes recorded in last_error, the audit meta and logs. Never a body or header value.
const (
	errEndpointUnknown   = "endpoint_unknown"
	errTenantMismatch    = "tenant_mismatch"
	errEndpointDisabled  = "endpoint_disabled"
	errSecretUnavailable = "secret_unavailable"
	errSSRFBlocked       = "ssrf_blocked"
	errPolicy            = "url_refused"
	errTimeout           = "timeout"
	errConnect           = "connect"
	errRedirect          = "redirect"
	errHTTP4xx           = "http_4xx"
	errHTTP5xx           = "http_5xx"
	errHTTPOther         = "http_other"
)

// DispatcherDeps are the collaborators of a Dispatcher.
type DispatcherDeps struct {
	Pool      *pgxpool.Pool  // hub.callback_deliveries
	Snapshots SnapshotSource // registrar URL and secret_ref at send time (latest config wins)
	Secrets   SigningSecrets // creds.RegistrarSecrets over the warm cache
	Audit     audit.Sink     // callback.delivered events (non-blocking)
	Metrics   *telemetry.Metrics
	Log       *slog.Logger
	// TracerProvider for hub.callback.deliver spans. Default: no-op.
	TracerProvider trace.TracerProvider

	store store // tests replace the Postgres store
}

// DispatcherOptions tune a Dispatcher. Zero values take the defaults.
type DispatcherOptions struct {
	// ID identifies this dispatcher in claimed_by. Default "cb-<hostname>-<ULID>"; it must be
	// unique per running dispatcher.
	ID string
	// Senders bounds concurrent deliveries (TECH_SPEC §12.1: 1 claimer + 32 senders). Default 32.
	Senders int
	// ClaimBatch is the most rows claimed per query. Default 32.
	ClaimBatch int
	// PollInterval is the wait after a claim that found less than it asked for. Default 500 ms.
	PollInterval time.Duration
	// Visibility is how long a claim is exclusive; a row still in_flight after it is reclaimed
	// (crashed dispatcher). Must exceed the send timeout. Default 30 s.
	Visibility time.Duration
	// Schedule is the retry schedule; len+1 attempts in all. Default DefaultSchedule.
	Schedule []time.Duration
	// SampleEvery is the period of the callback_queue_oldest_age_s / callback_dlq_count gauges.
	// Default 15 s.
	SampleEvery time.Duration
	// Guard configures the SSRF guard. Production leaves AllowLoopback false.
	Guard GuardOptions
	// Now is the clock for signature timestamps and audit times. Default time.Now.
	Now func() time.Time
}

// Dispatcher claims due callback deliveries, signs and POSTs them through the SSRF guard,
// records each outcome with a guarded transition and retries on its own schedule.
type Dispatcher struct {
	deps   DispatcherDeps
	opts   DispatcherOptions
	guard  *Guard
	tracer trace.Tracer
	rand   func(n int64) int64

	deliveries *prometheus.CounterVec
	latency    *prometheus.HistogramVec
	blocked    *prometheus.CounterVec
	oldest     prometheus.Gauge
	dlq        prometheus.Gauge
}

// NewDispatcher validates deps and opts and registers the callback metrics on deps.Metrics.
func NewDispatcher(deps DispatcherDeps, opts DispatcherOptions) (*Dispatcher, error) {
	if deps.store == nil {
		if deps.Pool == nil {
			return nil, errNoPool
		}
		deps.store = pgStore{pool: deps.Pool}
	}
	if deps.Snapshots == nil || deps.Secrets == nil || deps.Audit == nil || deps.Metrics == nil {
		return nil, errors.New("callbacks: NewDispatcher needs snapshots, secrets, an audit sink and metrics")
	}
	if deps.Log == nil {
		deps.Log = slog.New(slog.DiscardHandler)
	}
	if deps.TracerProvider == nil {
		deps.TracerProvider = noop.NewTracerProvider()
	}
	if opts.ID == "" {
		host, _ := os.Hostname()
		opts.ID = "cb-" + host + "-" + core.NewULID(time.Now())
	}
	if opts.Senders <= 0 {
		opts.Senders = 32
	}
	if opts.ClaimBatch <= 0 {
		opts.ClaimBatch = 32
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	if opts.Guard.Timeout <= 0 {
		opts.Guard.Timeout = DefaultTimeout
	}
	if opts.Visibility <= 0 {
		opts.Visibility = 30 * time.Second
	}
	if opts.Visibility <= opts.Guard.Timeout {
		return nil, fmt.Errorf("callbacks: visibility %v must exceed the send timeout %v", opts.Visibility, opts.Guard.Timeout)
	}
	if opts.Schedule == nil {
		opts.Schedule = DefaultSchedule
	}
	if len(opts.Schedule) > maxScheduleLen {
		return nil, fmt.Errorf("callbacks: schedule has %d entries, at most %d", len(opts.Schedule), maxScheduleLen)
	}
	if opts.SampleEvery <= 0 {
		opts.SampleEvery = 15 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	d := &Dispatcher{
		deps: deps, opts: opts, guard: NewGuard(opts.Guard),
		tracer: deps.TracerProvider.Tracer("github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"),
		rand:   rand.Int64N,
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callback_deliveries_total",
			Help: "Callback attempts by outcome: delivered, retry_scheduled or dead_lettered.",
		}, []string{"tenant", "outcome"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "callback_latency_seconds",
			Help:    "Duration of one callback attempt (request sent to response or error).",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"tenant"}),
		blocked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "callback_ssrf_blocked_total",
			Help: "Callback attempts refused by the SSRF guard, by refused address class.",
		}, []string{"class"}),
		oldest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "callback_queue_oldest_age_s",
			Help: "Age in seconds of the oldest due callback delivery not yet sent.",
		}),
		dlq: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "callback_dlq_count",
			Help: "Callback deliveries in the callback DLQ (status dead_lettered).",
		}),
	}
	for _, c := range []prometheus.Collector{d.deliveries, d.latency, d.blocked, d.oldest, d.dlq} {
		if err := deps.Metrics.Registry.Register(c); err != nil {
			return nil, fmt.Errorf("callbacks: register metrics: %w", err)
		}
	}
	return d, nil
}

// ID is the dispatcher's claimed_by identity.
func (d *Dispatcher) ID() string { return d.opts.ID }

// Run claims and delivers until ctx ends, then waits for the attempts already started (each
// bounded by the 5 s send timeout) to finish and record their outcome, and returns. No claim
// is left behind unattended: every claimed row is being sent when ctx ends, and a row whose
// outcome could not be recorded is reclaimed after the visibility timeout. Run once per
// dispatcher.
func (d *Dispatcher) Run(ctx context.Context) error {
	defer d.guard.Close()
	// Attempts outlive ctx briefly so a shutdown never cuts a request mid-flight.
	work := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Go(func() { d.sampleLoop(ctx) })

	slots := make(chan struct{}, d.opts.Senders)
	for range d.opts.Senders {
		slots <- struct{}{}
	}
	backoff := d.opts.PollInterval
	for {
		// Claim only as many rows as there are free senders, so no claimed row waits in memory.
		select {
		case <-ctx.Done():
			return nil
		case <-slots:
		}
		n := 1
	fill:
		for n < d.opts.ClaimBatch {
			select {
			case <-slots:
				n++
			default:
				break fill
			}
		}
		// The claim itself is not cut by ctx: a claim committed but not read back would strand
		// its rows until the visibility timeout.
		cctx, cancel := context.WithTimeout(work, 5*time.Second)
		claimed, err := d.deps.store.claim(cctx, d.opts.ID, n, d.opts.Visibility)
		cancel()
		for range n - len(claimed) {
			slots <- struct{}{}
		}
		if err != nil {
			if ctx.Err() == nil {
				d.deps.Log.WarnContext(ctx, "callback.claim_failed", slog.String("error", err.Error()))
			}
			backoff = min(backoff*2, 5*time.Second)
		} else {
			backoff = d.opts.PollInterval
		}
		for _, row := range claimed {
			wg.Go(func() {
				defer func() { slots <- struct{}{} }()
				d.attempt(work, row)
			})
		}
		if err != nil || len(claimed) < n {
			if !sleep(ctx, backoff) {
				return nil
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (d *Dispatcher) sampleLoop(ctx context.Context) {
	for {
		if oldest, dlq, err := d.deps.store.sample(ctx); err == nil {
			d.oldest.Set(oldest.Seconds())
			d.dlq.Set(float64(dlq))
		} else if ctx.Err() == nil {
			d.deps.Log.WarnContext(ctx, "callback.sample_failed", slog.String("error", err.Error()))
		}
		if !sleep(ctx, d.opts.SampleEvery) {
			return
		}
	}
}

// retryDelay returns the wait after failed attempt (1-based), jittered ±20 %, and whether the
// schedule is exhausted (the delivery dead-letters).
func (d *Dispatcher) retryDelay(attempt int) (time.Duration, bool) {
	if attempt < 1 || attempt > len(d.opts.Schedule) {
		return 0, true
	}
	base := d.opts.Schedule[attempt-1]
	spread := int64(base) * 2 / 5
	if spread <= 0 {
		return base, false
	}
	return time.Duration(int64(base) - spread/2 + d.rand(spread+1)), false
}

// result is what one send produced, before it is recorded.
type result struct {
	httpStatus int
	errClass   string
	permanent  bool // dead-letter now, retrying cannot help
	blocked    *BlockedError
	sent       bool // a request was attempted (latency observed)
	duration   time.Duration
}

// attempt sends one claimed delivery and records the outcome.
func (d *Dispatcher) attempt(ctx context.Context, row delivery) {
	ctx, span := d.tracer.Start(ctx, "hub.callback.deliver", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("hub.endpoint_id", row.EndpointID),
			attribute.Int("hub.callback.attempt", row.Attempts),
			attribute.String("hub.callback.event", row.EventType),
			attribute.String("hub.request_id", row.RequestID),
		))
	defer span.End()

	res := d.send(ctx, row)
	o := outcome{Status: statusDelivered, HTTPStatus: res.httpStatus, Error: res.errClass}
	if res.errClass != "" {
		delay, exhausted := d.retryDelay(row.Attempts)
		switch {
		case res.permanent || exhausted:
			o.Status = statusDeadLettered
		default:
			o.Status, o.RetryIn = statusRetryScheduled, delay
		}
		span.SetStatus(codes.Error, res.errClass)
	}
	if res.httpStatus != 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", res.httpStatus))
	}
	span.SetAttributes(attribute.String("hub.outcome", o.Status))
	if res.sent {
		d.latency.WithLabelValues(row.TenantID).Observe(res.duration.Seconds())
	}
	if res.blocked != nil {
		d.blocked.WithLabelValues(res.blocked.Class).Inc()
		d.deps.Log.WarnContext(ctx, "callback.ssrf_blocked", slog.String("endpoint_id", row.EndpointID),
			slog.String("tenant", row.TenantID), slog.String("class", res.blocked.Class), slog.String("delivery_id", row.ID))
	}

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	won, err := d.deps.store.record(rctx, row.ID, d.opts.ID, o)
	switch {
	case err != nil:
		// The claim expires and the row is retried: at-least-once (receivers dedupe on event_id).
		d.deps.Log.WarnContext(ctx, "callback.record_failed", slog.String("delivery_id", row.ID), slog.String("error", err.Error()))
		return
	case !won:
		d.deps.Log.InfoContext(ctx, "callback.claim_lost", slog.String("delivery_id", row.ID), slog.Int("attempt", row.Attempts))
		return
	}
	d.deliveries.WithLabelValues(row.TenantID, o.Status).Inc()
	d.audit(row, o, res)
	attrs := []slog.Attr{
		slog.String("delivery_id", row.ID), slog.String("request_id", row.RequestID), slog.String("tenant", row.TenantID),
		slog.String("endpoint_id", row.EndpointID), slog.String("event", row.EventType), slog.Int("attempt", row.Attempts),
		slog.Int("http_status", res.httpStatus), slog.String("error", o.Error),
	}
	switch o.Status {
	case statusDeadLettered:
		d.deps.Log.LogAttrs(ctx, slog.LevelWarn, "callback.dead_lettered", attrs...)
	case statusRetryScheduled:
		d.deps.Log.LogAttrs(ctx, slog.LevelDebug, "callback.retry_scheduled", append(attrs, slog.Duration("retry_in", o.RetryIn))...)
	}
}

// send resolves the endpoint from the current snapshot, signs and POSTs the stored body.
func (d *Dispatcher) send(ctx context.Context, row delivery) result {
	snap := d.deps.Snapshots.Snapshot()
	if snap == nil {
		return result{errClass: errEndpointUnknown}
	}
	ep := snap.Registrar[row.EndpointID]
	switch {
	case ep == nil:
		return result{errClass: errEndpointUnknown, permanent: true}
	case ep.Tenant != row.TenantID:
		return result{errClass: errTenantMismatch, permanent: true}
	case ep.Disabled: // held, not lost: deliveries resume on the schedule once it is re-enabled
		return result{errClass: errEndpointDisabled}
	}
	secrets, err := d.deps.Secrets.SigningSecrets(ctx, ep.SecretRef)
	if err != nil {
		return result{errClass: errSecretUnavailable}
	}
	sig, err := Sign(secrets, d.opts.Now(), row.Payload)
	if err != nil {
		return result{errClass: errSecretUnavailable}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(row.Payload))
	if err != nil {
		return result{errClass: errPolicy, permanent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "IntegrationHub-Callbacks/1")
	req.Header.Set(SignatureHeader, sig)
	req.Header.Set(EventHeader, row.EventType)
	req.Header.Set(DeliveryIDHeader, row.ID)

	start := time.Now()
	resp, err := d.guard.Do(req)
	res := result{sent: true}
	if err != nil {
		res.duration = time.Since(start)
		var be *BlockedError
		var ne net.Error
		switch {
		case errors.As(err, &be) && be.Class == ClassPolicy:
			res.errClass, res.permanent, res.sent = errPolicy, true, false
		case errors.As(err, &be):
			res.errClass, res.blocked, res.sent = errSSRFBlocked, be, false
		case errors.As(err, &ne) && ne.Timeout():
			res.errClass = errTimeout
		default:
			res.errClass = errConnect
		}
		return res
	}
	// Read a little of the body so the connection can be reused; it is never stored or logged.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	res.duration, res.httpStatus = time.Since(start), resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.errClass = errRedirect
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		res.errClass = errHTTP4xx
	case resp.StatusCode >= 500:
		res.errClass = errHTTP5xx
	default:
		res.errClass = errHTTPOther
	}
	return res
}

// audit emits callback.delivered for one recorded attempt: hash and size of the body, never the
// body, the signature or any header.
func (d *Dispatcher) audit(row delivery, o outcome, res result) {
	sum := sha256.Sum256(row.Payload)
	meta := map[string]any{
		"delivery_id": row.ID, "event_id": row.EventID, "event": row.EventType, "endpoint_id": row.EndpointID,
		"attempt": row.Attempts, "outcome": o.Status, "duration_ms": telemetry.Millis(res.duration),
	}
	if res.httpStatus != 0 {
		meta["http_status"] = res.httpStatus
	}
	if o.Error != "" {
		meta["error"] = o.Error
	}
	if o.Status == statusRetryScheduled {
		meta["retry_in_ms"] = o.RetryIn.Milliseconds()
	}
	d.deps.Audit.Emit(audit.Batch{Events: []audit.Event{{
		At: d.opts.Now(), RequestID: row.RequestID, Seq: AuditSeqBase + row.Subscription*100 + row.Attempts,
		Type: audit.CallbackDelivered, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Bytes: len(row.Payload),
		PayloadState: audit.PayloadOmitted, Meta: meta,
	}}})
}
