package delivery

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// ShadowSeqBase offsets the request_events seq of a request's shadow block, after the attempt
// blocks (n×100) and before callbacks (callbacks.AuditSeqBase).
const ShadowSeqBase = 900_000

// Shadower copies served requests to their version's shadow target (DESIGN §6.4). The copy
// runs after the caller has its answer, in its own goroutine, bounded by Max in flight and
// Timeout each. Its events go to their own audit block; its result is never returned.
type Shadower struct {
	Attempter *Attempter
	Audit     audit.Sink
	Log       *slog.Logger
	// Timeout bounds one copy (default 5 s); Max bounds copies in flight (default 64): over it,
	// copies are dropped, never queued.
	Timeout time.Duration
	Max     int
	// Rand draws the sample (default math/rand/v2).
	Rand func() float64

	once  sync.Once
	sem   chan struct{}
	wg    sync.WaitGroup
	total *prometheus.CounterVec
}

// NewShadower registers shadow_total{integration, mode, outcome} on reg (may be nil).
func NewShadower(a *Attempter, sink audit.Sink, log *slog.Logger, reg prometheus.Registerer) *Shadower {
	s := &Shadower{Attempter: a, Audit: sink, Log: log, total: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "shadow_total",
		Help: "Shadow copies by outcome (ok, vendor_error, failed, dropped). Results are audited, never returned.",
	}, []string{"integration", "mode", "outcome"})}
	if reg != nil {
		reg.MustRegister(s.total)
	}
	return s
}

func (s *Shadower) init() {
	s.once.Do(func() {
		if s.Timeout <= 0 {
			s.Timeout = 5 * time.Second
		}
		if s.Max <= 0 {
			s.Max = 64
		}
		if s.Rand == nil {
			s.Rand = rand.Float64
		}
		if s.Log == nil {
			s.Log = slog.New(slog.DiscardHandler)
		}
		s.sem = make(chan struct{}, s.Max)
	})
}

// Copy starts the shadow copy of j when its version has a shadow and the sample hits. j is the
// primary job (its trail and timings are not touched); opts are the primary trail's options.
func (s *Shadower) Copy(ctx context.Context, j Job, opts audit.TrailOptions) {
	sh := j.Res.Version.Shadow
	if s == nil || sh == nil || sh.Ratio() == 0 {
		return
	}
	s.init()
	if sh.Ratio() < 1 && s.Rand() >= sh.Ratio() {
		return
	}
	integration := j.Res.Integration.Integration
	select {
	case s.sem <- struct{}{}:
	default:
		s.count(integration, sh.Mode, "dropped")
		return
	}
	opts.SeqBase = ShadowSeqBase
	j.Trail = audit.NewTrail(opts)
	j.Timings = &telemetry.Timings{}
	j.Res.Target, j.Res.TargetIndex, j.Res.Failover = sh.Target, -1, nil
	j.SyncRetry, j.Live, j.FirstAttempt, j.LimiterWait = false, false, 1, 250*time.Millisecond
	ctx = context.WithoutCancel(ctx)
	s.wg.Go(func() {
		defer func() { <-s.sem }()
		ctx, cancel := context.WithTimeout(ctx, s.Timeout)
		defer cancel()
		outcome := s.run(ctx, j, sh)
		s.count(integration, sh.Mode, outcome)
		s.Audit.Emit(audit.Batch{Events: j.Trail.Events()})
	})
}

// Wait blocks until every started copy has finished (shutdown, tests).
func (s *Shadower) Wait() { s.wg.Wait() }

func (s *Shadower) run(ctx context.Context, j Job, sh *registry.Shadow) string {
	a := s.Attempter
	j.Trail.Add(audit.IntegrationResolved, 0, nil, map[string]any{"served_by": "shadow", "mode": string(sh.Mode),
		"vendor": sh.Target.Vendor, "account": sh.Target.Account, "version": j.Res.VersionNumber})
	if sh.Mode == registry.ShadowDryRun {
		return s.dryRun(ctx, j)
	}
	out := a.Run(ctx, j)
	switch {
	case out.Err == nil:
		return "ok"
	case out.Reached:
		return "vendor_error"
	default:
		return "failed"
	}
}

// dryRun only builds the shadow target's request: nothing leaves the Hub.
func (s *Shadower) dryRun(ctx context.Context, j Job) string {
	a := s.Attempter
	in, _, ce := a.Input(ctx, j.Snap, j.Res, j.Body, j.Hdr, j.Trail)
	if ce != nil {
		return "failed"
	}
	ref := j.Res.Pair().Request
	req, err := j.Snap.Programs[ref].BuildRequest(in)
	if errors.Is(err, transform.ErrTimeBudget) {
		req, err = j.Snap.Programs[ref].BuildRequest(in)
	}
	if err != nil {
		s.Log.WarnContext(ctx, "shadow dry run: transform failed", slog.String("integration", j.Res.Integration.Integration), slog.String("transformer", ref.String()))
		return "failed"
	}
	tf := j.Snap.Transformers[ref]
	url := req.Path
	if t, err := upstream.TargetFor(j.Snap, j.Res.Target.Account, tf.VendorEndpoint); err == nil {
		url = UpstreamURL(t.BaseURL, req)
	}
	j.Trail.AddJSON(audit.RequestTransformed, 0, AuditRequest{Method: req.Method, URL: url, Headers: req.Headers, Body: req.Body},
		map[string]any{"transformer": ref.String(), "endpoint": tf.VendorEndpoint, "served_by": "shadow", "dry_run": true})
	return "ok"
}

func (s *Shadower) count(integration string, mode registry.ShadowMode, outcome string) {
	if s.total != nil {
		s.total.WithLabelValues(integration, string(mode), outcome).Inc()
	}
}
