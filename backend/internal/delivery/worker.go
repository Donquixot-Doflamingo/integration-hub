package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Snapshots is the read side of the registry store.
type Snapshots interface {
	Snapshot() *registry.Snapshot
}

// PoisonCrashes is how many expired claims (worker crashes) dead-letter an envelope without
// another attempt (DESIGN §10.6).
const PoisonCrashes = 2

// Worker is the §18.4 worker loop for one pool: claim, TTL check, re-resolve, attempt,
// classify → ack / retry / dead-letter, all through guarded transitions.
//
// Isolation (HUB-412, docs/tech/ISOLATION.md): Run claims continuously, as many envelopes as it
// has free slots, so one slow attempt never holds up the next claim; and no (account, endpoint)
// may hold more than TargetCap of the Concurrency slots, so a slow or hanging vendor can only
// fill its own share. A claim takes no more of a target's backlog than the target's free slots;
// an envelope that still finds its target full when it starts is put back unattempted (no attempt
// counted, the breaker sees nothing) and is claimed again once the target has a free slot.
type Worker struct {
	Name        string // unique per process: claims are owned by it
	Pool        string // queue.PoolMessaging or queue.PoolLLM
	DB          *pgxpool.Pool
	Queue       *queue.Outbox
	Transitions *Transitions
	Snapshots   Snapshots
	Attempter   *Attempter
	Audit       audit.Sink
	Masker      *audit.Masker
	Sealer      *audit.Sealer // sealed policy encryption (nil: sealed stores hashes and sizes only)
	Backoff     Backoff
	Budget      *Budget
	Metrics     *telemetry.Metrics
	Log         *slog.Logger
	// TracerProvider makes the hub.worker.attempt spans (a new root per attempt, linked to the
	// accepting trace stored in the envelope). nil: no spans.
	TracerProvider trace.TracerProvider
	Now            func() time.Time
	// Batch is the most envelopes one claim takes (HUB_WORKER_CLAIM_BATCH, default 32). Run
	// claims min(Batch, free slots).
	Batch int
	// Concurrency is the attempts in flight (HUB_WORKER_CONCURRENCY; default 256 for the
	// messaging pool, 64 for llm, TECH_SPEC §12.1).
	Concurrency int
	// TargetCap is the most attempts in flight for one (account, endpoint)
	// (HUB_WORKER_TARGET_CAP; default a quarter of Concurrency, at most the per-account
	// connection bulkhead of 64, DESIGN §10.8).
	TargetCap int
	Idle      time.Duration // wait when nothing was due (default 200 ms)

	mu       sync.Mutex
	inflight map[queue.Target]int
	// reserved counts envelopes claimed for a target whose attempt has not started yet. rooms()
	// subtracts them, so a claim cannot take a target's free slots twice while the previous
	// claim's goroutines are still starting (HUB-444).
	reserved map[queue.Target]int
	waited   bool // the no-snapshot wait was logged
	metrics  workerMetrics
}

// workerMetrics are the isolation gauges and counter (nil fields: not registered).
type workerMetrics struct {
	inflight       *prometheus.GaugeVec   // worker_inflight{pool}
	targetInflight *prometheus.GaugeVec   // worker_target_inflight{account, endpoint}
	capDeferred    *prometheus.CounterVec // worker_target_cap_deferred_total{account, endpoint}
	credsDeferred  *prometheus.CounterVec // worker_credentials_deferred_total{account}
}

// RegisterMetrics adds worker_inflight, worker_target_inflight and
// worker_target_cap_deferred_total to reg. Target labels are breaker keys: bounded by the
// registry's accounts × endpoints, like parked_count.
func (w *Worker) RegisterMetrics(reg prometheus.Registerer) {
	w.metrics = workerMetrics{
		inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "worker_inflight",
			Help: "Attempts in flight in this worker process, by pool."}, []string{"pool"}),
		targetInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "worker_target_inflight",
			Help: "Attempts in flight in this worker process per (account, endpoint); capped at the worker's target cap."}, []string{"account", "endpoint"}),
		capDeferred: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "worker_target_cap_deferred_total",
			Help: "Claimed envelopes put back unattempted because their (account, endpoint) had the worker's target cap in flight."}, []string{"account", "endpoint"}),
		credsDeferred: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "worker_credentials_deferred_total",
			Help: "Claimed envelopes put back unattempted because the account's credentials were not in the cache (no attempt counted)."}, []string{"account"}),
	}
	reg.MustRegister(w.metrics.inflight, w.metrics.targetInflight, w.metrics.capDeferred, w.metrics.credsDeferred)
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// DefaultBatch, DefaultConcurrency and DefaultLLMConcurrency are the TECH_SPEC §8.1 / §12.1
// defaults.
const (
	DefaultBatch          = 32
	DefaultConcurrency    = 256
	DefaultLLMConcurrency = 64
)

func (w *Worker) batch() int {
	if w.Batch <= 0 {
		return DefaultBatch
	}
	return w.Batch
}

func (w *Worker) concurrency() int {
	switch {
	case w.Concurrency > 0:
		return w.Concurrency
	case w.Pool == queue.PoolLLM:
		return DefaultLLMConcurrency
	}
	return DefaultConcurrency
}

// targetCap is TargetCap, or a quarter of the slots (so up to three misbehaving targets still
// leave a quarter to everyone else) but never more than the per-account connection bulkhead:
// attempts beyond it would only queue for a connection while holding a slot.
func (w *Worker) targetCap() int {
	if w.TargetCap > 0 {
		return min(w.TargetCap, w.concurrency())
	}
	return min(upstream.DefaultBulkhead, max(1, w.concurrency()/4))
}

func (w *Worker) idle() time.Duration {
	if w.Idle <= 0 {
		return 200 * time.Millisecond
	}
	return w.Idle
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ready reports whether a registry snapshot is loaded. Before the first one a worker must not
// claim: it could not resolve anything, and a claim it cannot finish would expire and count as a
// crash (HUB-406).
func (w *Worker) ready(ctx context.Context) bool {
	if w.Snapshots.Snapshot() != nil {
		return true
	}
	w.mu.Lock()
	first := !w.waited
	w.waited = true
	w.mu.Unlock()
	if first {
		w.Log.InfoContext(ctx, "worker.waiting_for_snapshot", slog.String("pool", w.Pool))
	}
	return false
}

// Run claims and delivers until ctx ends; in-flight attempts finish first. It claims as soon
// as a slot is free, up to min(Batch, free slots) at a time (DESIGN §18.4, go-system-design
// "claim n = free slots"), so nothing waits for a whole batch. Envelopes are claimed only
// when they can start at once: there is no in-memory queue to drain at shutdown.
func (w *Worker) Run(ctx context.Context) {
	for !w.ready(ctx) {
		if sleep(ctx, w.idle()); ctx.Err() != nil {
			return
		}
	}
	slots := make(chan struct{}, w.concurrency())
	var wg sync.WaitGroup
	defer wg.Wait() // graceful: every claimed envelope is finished
	for ctx.Err() == nil {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		n := 1
	more:
		for n < w.batch() {
			select {
			case slots <- struct{}{}:
				n++
			default:
				break more
			}
		}
		claimed, err := w.claim(ctx, n)
		w.reserve(claimed)
		for range n - len(claimed) {
			<-slots
		}
		for _, c := range claimed {
			wg.Go(func() {
				defer func() { <-slots }()
				w.handle(context.WithoutCancel(ctx), c) // a claim outlives the caller's cancellation
			})
		}
		if err != nil && ctx.Err() == nil {
			w.Log.ErrorContext(ctx, "claim failed", slog.String("pool", w.Pool), slog.String("error", err.Error()))
		}
		if err != nil || len(claimed) == 0 { // nothing claimable is due now
			sleep(ctx, w.idle())
		}
	}
}

// claim takes up to n due envelopes, no more of a target's backlog than its free slots.
func (w *Worker) claim(ctx context.Context, n int) ([]queue.Claimed, error) {
	return w.Queue.ClaimWithin(ctx, w.DB, w.Name, w.Pool, n, w.targetCap(), w.rooms())
}

// ProcessOnce claims one batch and handles all of it before returning (tests and tools; Run
// claims continuously). It returns how many envelopes it handled; none before a snapshot exists.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	if !w.ready(ctx) {
		return 0, nil
	}
	claimed, err := w.claim(ctx, w.batch())
	if err != nil {
		return 0, err
	}
	w.reserve(claimed)
	sem := make(chan struct{}, w.concurrency())
	var wg sync.WaitGroup
	for _, c := range claimed {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			w.handle(context.WithoutCancel(ctx), c)
		})
	}
	wg.Wait()
	return len(claimed), nil
}

// acquire takes one of target's slots; false when the target already has its cap in flight.
func (w *Worker) acquire(t queue.Target) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.inflight == nil {
		w.inflight = map[queue.Target]int{}
	}
	n := w.inflight[t]
	if n >= w.targetCap() {
		return false
	}
	w.inflight[t] = n + 1
	if w.metrics.targetInflight != nil {
		w.metrics.targetInflight.WithLabelValues(t.Account, t.Endpoint).Set(float64(n + 1))
		w.metrics.inflight.WithLabelValues(w.Pool).Inc()
	}
	return true
}

func (w *Worker) release(t queue.Target) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := w.inflight[t] - 1
	if n <= 0 {
		delete(w.inflight, t)
		n = 0
	} else {
		w.inflight[t] = n
	}
	if w.metrics.targetInflight != nil {
		w.metrics.targetInflight.WithLabelValues(t.Account, t.Endpoint).Set(float64(n))
		w.metrics.inflight.WithLabelValues(w.Pool).Dec()
	}
}

// rooms lists the free slots of every target with attempts in flight (0: at its cap).
func (w *Worker) rooms() []queue.TargetRoom {
	w.mu.Lock()
	defer w.mu.Unlock()
	limit := w.targetCap()
	out := make([]queue.TargetRoom, 0, len(w.inflight)+len(w.reserved))
	for t, n := range w.inflight {
		out = append(out, queue.TargetRoom{Target: t, Slots: max(limit-n-w.reserved[t], 0)})
	}
	for t, r := range w.reserved {
		if _, ok := w.inflight[t]; !ok {
			out = append(out, queue.TargetRoom{Target: t, Slots: max(limit-r, 0)})
		}
	}
	return out
}

// reserve holds one slot of each claimed envelope's target until its attempt starts or ends.
func (w *Worker) reserve(claimed []queue.Claimed) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range claimed {
		if c.Target == nil {
			continue
		}
		if w.reserved == nil {
			w.reserved = map[queue.Target]int{}
		}
		w.reserved[*c.Target]++
	}
}

// unreserver returns a func that gives c's reservation back once; later calls do nothing.
func (w *Worker) unreserver(c queue.Claimed) func() {
	if c.Target == nil {
		return func() {}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if n := w.reserved[*c.Target] - 1; n > 0 {
				w.reserved[*c.Target] = n
			} else {
				delete(w.reserved, *c.Target)
			}
		})
	}
}

// InFlight returns the attempts in flight for (account, endpoint) (tests, debugging).
func (w *Worker) InFlight(account, endpoint string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inflight[queue.Target{Account: account, Endpoint: endpoint}]
}

func (w *Worker) handle(ctx context.Context, c queue.Claimed) {
	unreserve := w.unreserver(c) // the claim's slot reservation ends when the attempt takes a slot, or here
	defer unreserve()
	env := c.Envelope
	ctx, span := w.startAttempt(ctx, c)
	defer span.End()
	log := w.Log.With(slog.String("request_id", env.RequestID), slog.String("integration", env.IntegrationID))
	report := func(err error, what string) {
		endAttempt(span, err, what)
		if IsLost(err) {
			log.InfoContext(ctx, "outcome dropped: another actor finished the request", slog.String("outcome", what))
		} else if err != nil {
			log.ErrorContext(ctx, "transition failed", slog.String("outcome", what), slog.String("error", err.Error()))
		}
	}
	snap := w.Snapshots.Snapshot()
	if snap == nil { // claimed before any snapshot (HUB-406): put back, not an attempt, not a crash
		report(w.Transitions.Hold(ctx, env, w.Name, w.now().Add(time.Second), "no registry snapshot yet"), "no snapshot")
		return
	}
	timings := telemetry.NewTimings(w.now())
	trail := w.trail(snap, env, c.Attempt)
	emit := func() { w.Audit.Emit(audit.Batch{Events: trail.Events()}) }

	if c.Crashes >= PoisonCrashes {
		trail.Add(audit.RequestDeadLettered, c.Attempt, nil, map[string]any{"reason": "poison", "crashes": c.Crashes})
		emit()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
			Final: audit.Final{ErrorCode: "poison", Attempts: c.Attempt - 1},
			Err:   &core.CanonicalError{Code: core.CodeInternalError, Message: "The request could not be processed", Reason: core.Ptr("poison")},
			DL:    queue.DeadLetter{Reason: "poison", LastError: "worker crashed twice while delivering"},
		}), "poison")
		return
	}
	if !w.now().Before(env.ExpiresAt) {
		trail.Add(audit.RequestExpired, c.Attempt, nil, map[string]any{"expires_at": env.ExpiresAt})
		emit()
		report(w.Transitions.Expire(ctx, env, w.Name, c.Attempt-1), "expired")
		return
	}
	in, ok := snap.Integrations[env.IntegrationID]
	if !ok {
		emit()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
			Final: audit.Final{ErrorCode: string(core.CodeIntegrationNotFound), Attempts: c.Attempt - 1},
			Err:   &core.CanonicalError{Code: core.CodeIntegrationNotFound, Message: "The integration no longer exists"},
			DL:    queue.DeadLetter{Reason: string(core.CodeIntegrationNotFound)},
		}), "integration gone")
		return
	}
	if in.Paused { // kill switch: hold, do not attempt (DESIGN §10.4)
		report(w.Transitions.Hold(ctx, env, w.Name, w.now().Add(30*time.Second), "integration paused"), "paused")
		return
	}

	var session *sessions.Session
	if env.SessionID != nil {
		// A later step of a vendor session: the session's version and account (DESIGN §6.3).
		s, err := sessions.Get(ctx, w.DB, env.Tenant, env.IntegrationID, *env.SessionID)
		if err != nil {
			if !errors.Is(err, sessions.ErrNotFound) {
				log.ErrorContext(ctx, "session lookup failed", slog.String("error", err.Error()))
				report(w.Transitions.Hold(ctx, env, w.Name, w.now().Add(5*time.Second), "session lookup failed"), "session lookup")
				return
			}
			trail.Add(audit.IntegrationResolved, c.Attempt, nil, map[string]any{"session": map[string]any{"id": *env.SessionID, "found": false}, "attempt": c.Attempt})
			emit()
			report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
				Final: audit.Final{ErrorCode: string(core.CodeSessionNotFound), Attempts: c.Attempt - 1},
				Err:   &core.CanonicalError{Code: core.CodeSessionNotFound, Message: "Unknown or expired session"},
				DL:    queue.DeadLetter{Reason: string(core.CodeSessionNotFound), LastError: "the vendor session expired before delivery"},
			}), "session gone")
			return
		}
		session = &s
	}
	res, err := w.resolve(snap, env, session)
	if err != nil {
		emit()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
			Final: audit.Final{ErrorCode: "resolve_failed", Attempts: c.Attempt - 1},
			Err:   &core.CanonicalError{Code: core.CodeInternalError, Message: "The request could not be routed"},
			DL:    queue.DeadLetter{Reason: "resolve_failed", LastError: err.Error()},
		}), "resolve failed")
		return
	}
	resolved := map[string]any{"version": res.VersionNumber, "vendor": res.Target.Vendor,
		"account": res.Target.Account, "snapshot_version": res.SnapshotVersion, "attempt": c.Attempt}
	var sessionData map[string]any
	if session != nil { // meta only: never the session data
		resolved["session"] = SessionMeta(session.ID, "used", session.Version, session.AccountID)
		sessionData = session.Data
	}
	trail.Add(audit.IntegrationResolved, c.Attempt, nil, resolved)
	var body map[string]any
	if err := json.Unmarshal(env.Canonical, &body); err != nil {
		emit()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
			Final: audit.Final{ErrorCode: "poison", Attempts: c.Attempt - 1},
			Err:   &core.CanonicalError{Code: core.CodeInternalError, Message: "The request could not be processed", Reason: core.Ptr("poison")},
			DL:    queue.DeadLetter{Reason: "poison", LastError: "canonical body does not decode"},
		}), "bad envelope")
		return
	}

	account := res.Target.Account
	hdr := make(map[string]any, len(env.Headers)+1) // the declared caller headers (ADR-0023)
	for k, v := range env.Headers {
		hdr[k] = v
	}
	if env.IdempotencyKey != nil {
		hdr[registry.HdrIdempotencyKey] = *env.IdempotencyKey
	}
	queueWait := w.now().Sub(env.CreatedAt)
	// Per-target isolation (HUB-412): the attempt takes one of its (account, endpoint)'s slots
	// once the breakers admitted it; over the cap nothing counts (no attempt, no breaker outcome,
	// no retry budget) and the envelope goes back so other targets can use the slot.
	var held *queue.Target
	defer func() {
		if held != nil {
			w.release(*held)
		}
	}()
	out := w.Attempter.Run(ctx, Job{
		RequestID: env.RequestID, Snap: snap, Res: res, Body: body, Hdr: hdr, Session: sessionData, Trail: trail, Timings: &timings,
		FirstAttempt: c.Attempt, Live: c.Attempt == 1 && c.Status == core.StateAccepted, // drained and retried: not live
		Acquire: func(t queue.Target) bool {
			unreserve()
			if !w.acquire(t) {
				return false
			}
			held = &t
			return true
		},
	})
	if out.OverCap {
		if w.metrics.capDeferred != nil {
			w.metrics.capDeferred.WithLabelValues(out.Account, out.Endpoint).Inc()
		}
		log.DebugContext(ctx, "worker.target_at_cap", slog.String("account", out.Account), slog.String("endpoint", out.Endpoint))
		// Due at once: the claim's room filter holds it back until the target has a free slot.
		report(w.Transitions.HoldForTarget(ctx, env, w.Name, w.now(), queue.Target{Account: out.Account, Endpoint: out.Endpoint}), "target at cap")
		return
	}
	if out.CredentialsUnavailable {
		// Nothing reached the vendor: not an attempt on the retry schedule, no breaker outcome and
		// no budget use. The envelope waits for the credentials; its TTL bounds the wait (HUB-453).
		if w.metrics.credsDeferred != nil {
			w.metrics.credsDeferred.WithLabelValues(account).Inc()
		}
		log.WarnContext(ctx, "worker.credentials_unavailable", slog.String("account", account), slog.Duration("hold", credentialsHold))
		report(w.Transitions.Hold(ctx, env, w.Name, w.now().Add(credentialsHold), "credentials unavailable"), "credentials unavailable")
		return
	}
	if c.Attempt == 1 {
		w.Budget.Live(account)
	}
	final := audit.Final{
		Attempts: c.Attempt, IntegrationVersion: res.VersionNumber,
		HubOverheadMS: telemetry.Millis(timings.Overhead(w.now())), VendorMS: telemetry.Millis(timings.Stage(telemetry.StageUpstream)),
		QueueWaitMS: telemetry.Millis(queueWait),
	}
	state, err := w.Settler().Settle(ctx, env, w.Name, c.Attempt, account, out, final)
	report(err, "settle")
	if err == nil {
		span.SetAttributes(attribute.String("hub.outcome", string(state)))
	}
	TerminalAudit(trail, c.Attempt, state, err, out.Err)
	w.Audit.Emit(audit.Batch{Events: trail.Events(), Attempts: out.Attempts})
}

// credentialsHold is how long an envelope waits after a credential cache miss (matches the
// retry_after_ms of the error the sync path returns for it).
const credentialsHold = 5 * time.Second

// resolve picks the version and target of an attempt: the active version at send time (D10),
// resolved_version when pinned, and for a vendor session its exact version and account.
// The envelope's operation selects the transformer pair of the resolved target (HUB-343).
func (w *Worker) resolve(snap *registry.Snapshot, env core.Envelope, session *sessions.Session) (resolve.Resolution, error) {
	res, err := w.resolveTarget(snap, env, session)
	res.Operation = env.Operation
	return res, err
}

func (w *Worker) resolveTarget(snap *registry.Snapshot, env core.Envelope, session *sessions.Session) (resolve.Resolution, error) {
	if session != nil {
		return resolve.ResolveSession(snap, env.Tenant, env.IntegrationID, session.Version, session.AccountID)
	}
	version := 0
	if env.Pinned {
		version = env.ResolvedVersion
	}
	routeKey := env.RequestID
	if env.IdempotencyKey != nil {
		routeKey = *env.IdempotencyKey
	}
	return resolve.Resolve(snap, env.Tenant, env.IntegrationID, version, routeKey)
}

// SessionMeta is the audit view of a vendor session (DESIGN §13.1): id, what happened (opened,
// used), version and account. Never the session data.
func SessionMeta(id, event string, version int, account string) map[string]any {
	return map[string]any{"id": id, "event": event, "version": version, "account": account}
}

// Settler returns the worker's settle policy.
func (w *Worker) Settler() Settler {
	return Settler{Transitions: w.Transitions, Backoff: w.Backoff, Budget: w.Budget, Now: w.Now}
}

// trail starts the audit block of one attempt (SeqBase attempt×100).
func (w *Worker) trail(snap *registry.Snapshot, env core.Envelope, attempt int) *audit.Trail {
	policy := registry.PayloadMasked
	var pii []registry.PIIPath
	if in, ok := snap.Integrations[env.IntegrationID]; ok {
		policy = in.Active().Audit.Payloads
		if op := in.Active().Op(env.Operation); op != nil {
			pii = op.PII // x-pii tags of the operation's request schema (ADR-0023)
		}
	}
	var canonical map[string]any
	_ = json.Unmarshal(env.Canonical, &canonical)
	return audit.NewTrail(audit.TrailOptions{
		RequestID: env.RequestID, Tenant: env.Tenant, Policy: policy, PII: pii, Canonical: canonical,
		Masker: w.Masker, Sealer: w.Sealer, Now: w.Now, SeqBase: attempt * 100,
	})
}
