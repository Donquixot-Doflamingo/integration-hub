package delivery

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Resilience is the scheduler's breaker work (DESIGN §10.4, §10.5): synthetic health probes for
// open breakers, the ramped drain of parked envelopes after a close, and the parked metrics.
// Only the scheduler leader runs it.
type Resilience struct {
	Breakers  *upstream.Breakers
	Upstream  Upstream
	Snapshots Snapshots
	Queue     *queue.Outbox
	Log       *slog.Logger
	Now       func() time.Time
	// RampStep is how long each drain rate holds before doubling (DESIGN §10.5: 30 s).
	RampStep time.Duration
	// ProbeEvery overrides the vendors' health_probe.every_s (tests).
	ProbeEvery time.Duration

	mu        sync.Mutex
	lastProbe map[upstream.BreakerKey]time.Time
	carry     map[upstream.BreakerKey]float64
	parked    *prometheus.GaugeVec
	oldest    *prometheus.GaugeVec
	released  *prometheus.CounterVec
	probes    *prometheus.CounterVec
}

// RegisterMetrics adds parked_count, parked_oldest_age_s, drain_released_total and
// health_probes_total to reg.
func (r *Resilience) RegisterMetrics(reg prometheus.Registerer) {
	r.parked = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "parked_count", Help: "Envelopes parked per account and endpoint."}, []string{"account", "endpoint"})
	r.oldest = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "parked_oldest_age_s", Help: "Age of the oldest parked envelope per account and endpoint."}, []string{"account", "endpoint"})
	r.released = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "drain_released_total", Help: "Parked envelopes released by the drain."}, []string{"account", "endpoint"})
	r.probes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "health_probes_total", Help: "Synthetic health probes by result."}, []string{"vendor", "account", "result"})
	reg.MustRegister(r.parked, r.oldest, r.released, r.probes)
}

func (r *Resilience) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// ReleasedCounter is drain_released_total (tests measure the ramp from it).
func (r *Resilience) ReleasedCounter() *prometheus.CounterVec { return r.released }

// Tick runs one round: probe due open breakers, then drain closed ones.
func (r *Resilience) Tick(ctx context.Context, db queue.DB, tick time.Duration) {
	r.mu.Lock()
	if r.lastProbe == nil {
		r.lastProbe, r.carry = map[upstream.BreakerKey]time.Time{}, map[upstream.BreakerKey]float64{}
	}
	r.mu.Unlock()
	r.probeOpen(ctx)
	r.drain(ctx, db, tick)
}

func (r *Resilience) probeOpen(ctx context.Context) {
	open, err := r.Breakers.SharedOpen(ctx)
	if err != nil {
		r.Log.WarnContext(ctx, "listing open breakers failed", slog.String("error", err.Error()))
	}
	snap := r.Snapshots.Snapshot()
	if snap == nil {
		return
	}
	for key := range open {
		vendor, _, _ := strings.Cut(key.Endpoint, ".")
		v := snap.Vendors[vendor]
		if v == nil || v.Defaults.HealthProbe == nil {
			continue
		}
		every := time.Duration(v.Defaults.HealthProbe.EveryS) * time.Second
		if r.ProbeEvery > 0 {
			every = r.ProbeEvery
		}
		if every <= 0 {
			every = 30 * time.Second
		}
		r.mu.Lock()
		due := r.now().Sub(r.lastProbe[key]) >= every
		if due {
			r.lastProbe[key] = r.now()
		}
		r.mu.Unlock()
		if due {
			r.probe(ctx, snap, v, key)
		}
	}
}

// probe sends the vendor's health probe. A healthy answer moves the breaker to half-open and
// runs its half-open probes at once, so recovery closes the circuit without live traffic; a
// failure leaves it open (or re-opens a half-open one with a doubled window).
func (r *Resilience) probe(ctx context.Context, snap *registry.Snapshot, v *registry.Vendor, key upstream.BreakerKey) {
	hp := v.Defaults.HealthProbe
	target, err := upstream.TargetFor(snap, key.Account, v.Vendor+"."+hp.Endpoint)
	if err != nil {
		return
	}
	policy := upstream.PolicyFrom(v.Defaults.Breaker)
	b := r.Breakers.Get(key, policy)
	if shared, ok := r.Breakers.SharedState(ctx, key); ok {
		b.Adopt(shared)
	}
	healthy := r.send(ctx, target, hp)
	r.probes.WithLabelValues(v.Vendor, key.Account, map[bool]string{true: "healthy", false: "unhealthy"}[healthy]).Inc()
	now := r.now()
	if !healthy {
		if b.Snapshot().State == upstream.HalfOpen {
			if ok, probe, _ := b.Allow(now); ok {
				b.Record(now, upstream.OutcomeFailure, probe)
			}
		}
		r.Log.InfoContext(ctx, "probe.failed", slog.String("breaker", key.String()))
		return
	}
	if b.Snapshot().State == upstream.Open {
		b.Force(now, upstream.HalfOpen, 0)
	}
	for range policy.HalfOpenProbes {
		ok, probe, _ := b.Allow(r.now())
		if !ok {
			break
		}
		if !r.send(ctx, target, hp) {
			b.Record(r.now(), upstream.OutcomeFailure, probe)
			return
		}
		b.Record(r.now(), upstream.OutcomeSuccess, probe)
	}
	if b.Snapshot().State == upstream.Closed {
		r.Log.InfoContext(ctx, "probe.recovered", slog.String("breaker", key.String()))
	}
}

// send is one synthetic probe: the endpoint's method and path, an empty JSON body, marked
// X-Hub-Probe (and dry=1 when the vendor supports a dry run). Without credentials: an auth
// error still proves the vendor answers. Healthy = any answer below 500 other than 429.
func (r *Resilience) send(ctx context.Context, t upstream.Target, hp *registry.HealthProbe) bool {
	req := core.VendorRequest{Method: t.Endpoint.Method, Path: t.Endpoint.Path, Headers: map[string]string{
		"X-Hub-Probe": "1", "Content-Type": "application/json"}, Body: map[string]any{}}
	if hp.Dry {
		req.Query = map[string]string{"dry": "1"}
	}
	a, err := r.Upstream.Attempt(ctx, t, req)
	return err == nil && a.Response.Status < 500 && a.Response.Status != 429
}

// drain releases parked envelopes of closed breakers at a ramp: 20 % of the account rate for
// RampStep after the close, then 40 %, 80 %, 100 %. A re-open stops it (the breaker is not
// closed any more); the next close starts the ramp again.
func (r *Resilience) drain(ctx context.Context, db queue.DB, tick time.Duration) {
	groups, err := r.Queue.Parked(ctx, db)
	if err != nil {
		r.Log.WarnContext(ctx, "listing parked envelopes failed", slog.String("error", err.Error()))
		return
	}
	snap := r.Snapshots.Snapshot()
	r.parked.Reset()
	r.oldest.Reset()
	step := r.RampStep
	if step <= 0 {
		step = 30 * time.Second
	}
	for _, g := range groups {
		r.parked.WithLabelValues(g.Account, g.Endpoint).Set(float64(g.Count))
		r.oldest.WithLabelValues(g.Account, g.Endpoint).Set(r.now().Sub(g.Oldest).Seconds())
		key := upstream.BreakerKey{Account: g.Account, Endpoint: g.Endpoint}
		state, ok := r.Breakers.SharedState(ctx, key)
		if ok && state.State != upstream.Closed {
			r.mu.Lock()
			delete(r.carry, key)
			r.mu.Unlock()
			continue
		}
		closedAt := time.UnixMilli(state.Version)
		if !ok { // never tripped here (or Redis is down): treat as long closed
			closedAt = time.Time{}
		}
		factor := RampFactor(r.now().Sub(closedAt), step)
		rate := float64(accountRPS(snap, g.Account, g.Endpoint)) * factor
		r.mu.Lock()
		want := r.carry[key] + rate*tick.Seconds()
		n := int(math.Floor(want))
		r.carry[key] = want - float64(n)
		r.mu.Unlock()
		if n <= 0 {
			continue
		}
		released, err := r.Queue.Release(ctx, db, g.Account, g.Endpoint, n)
		if err != nil {
			r.Log.WarnContext(ctx, "drain release failed", slog.String("error", err.Error()))
			continue
		}
		r.released.WithLabelValues(g.Account, g.Endpoint).Add(float64(released))
		if released > 0 {
			r.Log.InfoContext(ctx, "drain.released", slog.String("account", g.Account), slog.String("endpoint", g.Endpoint),
				slog.Int("count", released), slog.Float64("rate_factor", factor))
		}
	}
}

// RampFactor is the drain rate as a share of the account rate after a close: 0.2, 0.4, 0.8 then
// 1 (DESIGN §10.5).
func RampFactor(sinceClose, step time.Duration) float64 {
	if sinceClose < 0 {
		sinceClose = 0
	}
	f := 0.2 * math.Pow(2, math.Floor(float64(sinceClose)/float64(step)))
	return math.Min(f, 1)
}

// accountRPS is the account's quota (or the vendor default per-account rate; 10/s without one).
func accountRPS(snap *registry.Snapshot, account, endpointRef string) int {
	if snap != nil {
		if a := snap.Accounts[account]; a != nil {
			if a.Quota.RPS > 0 {
				return a.Quota.RPS
			}
			vendor, _, _ := strings.Cut(endpointRef, ".")
			if v := snap.Vendors[vendor]; v != nil && v.Defaults.RateLimit.PerAccountRPS > 0 {
				return v.Defaults.RateLimit.PerAccountRPS
			}
		}
	}
	return 10
}
