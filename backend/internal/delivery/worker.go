package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
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
	Batch          int           // envelopes per claim (default 32)
	Concurrency    int           // attempts in flight (default 16)
	Idle           time.Duration // wait when nothing was due (default 200 ms)

	mu     sync.Mutex
	waited bool // the no-snapshot wait was logged
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) batch() int {
	if w.Batch <= 0 {
		return 32
	}
	return w.Batch
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

// Run claims and delivers until ctx ends; in-flight attempts finish first. It claims nothing
// before the first registry snapshot is loaded.
func (w *Worker) Run(ctx context.Context) {
	idle := w.Idle
	if idle <= 0 {
		idle = 200 * time.Millisecond
	}
	for !w.ready(ctx) {
		if sleep(ctx, idle); ctx.Err() != nil {
			return
		}
	}
	for ctx.Err() == nil {
		n, err := w.ProcessOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.Log.ErrorContext(ctx, "claim failed", slog.String("pool", w.Pool), slog.String("error", err.Error()))
		}
		if n == 0 || err != nil {
			sleep(ctx, idle)
		}
	}
}

// ProcessOnce claims one batch and handles it; it returns how many envelopes it handled (none
// before a snapshot exists).
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
	if !w.ready(ctx) {
		return 0, nil
	}
	claimed, err := w.Queue.Claim(ctx, w.DB, w.Name, w.Pool, w.batch())
	if err != nil {
		return 0, err
	}
	concurrency := w.Concurrency
	if concurrency <= 0 {
		concurrency = 16
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, c := range claimed {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			// A claim outlives the caller's cancellation: finish (or lose) it cleanly.
			w.handle(context.WithoutCancel(ctx), c)
		})
	}
	wg.Wait()
	return len(claimed), nil
}

func (w *Worker) handle(ctx context.Context, c queue.Claimed) {
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
	if c.Attempt == 1 {
		w.Budget.Live(account)
	}
	hdr := map[string]any{}
	if env.IdempotencyKey != nil {
		hdr["idempotency_key"] = *env.IdempotencyKey
	}
	queueWait := w.now().Sub(env.CreatedAt)
	out := w.Attempter.Run(ctx, Job{
		RequestID: env.RequestID, Snap: snap, Res: res, Body: body, Hdr: hdr, Session: sessionData, Trail: trail, Timings: &timings,
		FirstAttempt: c.Attempt, Live: c.Attempt == 1 && c.Status == core.StateAccepted, // drained and retried: not live
	})
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
		if c := snap.Capabilities[env.Capability]; c != nil {
			if op := c.Operations[env.Operation]; op != nil {
				pii = op.PII
			}
		}
	}
	var canonical map[string]any
	_ = json.Unmarshal(env.Canonical, &canonical)
	return audit.NewTrail(audit.TrailOptions{
		RequestID: env.RequestID, Tenant: env.Tenant, Policy: policy, PII: pii, Canonical: canonical,
		Masker: w.Masker, Sealer: w.Sealer, Now: w.Now, SeqBase: attempt * 100,
	})
}
