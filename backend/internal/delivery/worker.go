package delivery

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
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
	Backoff     Backoff
	Budget      *Budget
	Metrics     *telemetry.Metrics
	Log         *slog.Logger
	Now         func() time.Time
	Batch       int           // envelopes per claim (default 32)
	Concurrency int           // attempts in flight (default 16)
	Idle        time.Duration // wait when nothing was due (default 200 ms)
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

// Run claims and delivers until ctx ends; in-flight attempts finish first.
func (w *Worker) Run(ctx context.Context) {
	idle := w.Idle
	if idle <= 0 {
		idle = 200 * time.Millisecond
	}
	for ctx.Err() == nil {
		n, err := w.ProcessOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.Log.ErrorContext(ctx, "claim failed", slog.String("pool", w.Pool), slog.String("error", err.Error()))
		}
		if n == 0 || err != nil {
			t := time.NewTimer(idle)
			select {
			case <-ctx.Done():
				t.Stop()
			case <-t.C:
			}
		}
	}
}

// ProcessOnce claims one batch and handles it; it returns how many envelopes it handled.
func (w *Worker) ProcessOnce(ctx context.Context) (int, error) {
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
	log := w.Log.With(slog.String("request_id", env.RequestID), slog.String("integration", env.IntegrationID))
	snap := w.Snapshots.Snapshot()
	timings := telemetry.NewTimings(w.now())
	trail := w.trail(snap, env, c.Attempt)
	emit := func() { w.Audit.Emit(audit.Batch{Events: trail.Events()}) }
	report := func(err error, what string) {
		if IsLost(err) {
			log.InfoContext(ctx, "outcome dropped: another actor finished the request", slog.String("outcome", what))
		} else if err != nil {
			log.ErrorContext(ctx, "transition failed", slog.String("outcome", what), slog.String("error", err.Error()))
		}
	}

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

	version := 0 // re-resolve the active version at send time (D10) ...
	if env.Pinned || env.SessionID != nil {
		version = env.ResolvedVersion // ... unless pinned or bound to a vendor session
	}
	routeKey := env.RequestID
	if env.IdempotencyKey != nil {
		routeKey = *env.IdempotencyKey
	}
	res, err := resolve.Resolve(snap, env.Tenant, env.IntegrationID, version, routeKey)
	if err != nil {
		emit()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{
			Final: audit.Final{ErrorCode: "resolve_failed", Attempts: c.Attempt - 1},
			Err:   &core.CanonicalError{Code: core.CodeInternalError, Message: "The request could not be routed"},
			DL:    queue.DeadLetter{Reason: "resolve_failed", LastError: err.Error()},
		}), "resolve failed")
		return
	}
	trail.Add(audit.IntegrationResolved, c.Attempt, nil, map[string]any{"version": res.VersionNumber, "vendor": res.Target.Vendor,
		"account": res.Target.Account, "snapshot_version": res.SnapshotVersion, "attempt": c.Attempt})
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
		RequestID: env.RequestID, Snap: snap, Res: res, Body: body, Hdr: hdr, Trail: trail, Timings: &timings,
		FirstAttempt: c.Attempt,
	})
	final := audit.Final{
		Attempts: c.Attempt, IntegrationVersion: res.VersionNumber,
		HubOverheadMS: telemetry.Millis(timings.Overhead(w.now())), VendorMS: telemetry.Millis(timings.Stage(telemetry.StageUpstream)),
		QueueWaitMS: telemetry.Millis(queueWait),
	}
	w.Audit.Emit(audit.Batch{Events: trail.Events(), Attempts: out.Attempts})

	switch {
	case out.Err == nil:
		final.HTTPStatus = 200
		report(w.Transitions.Succeed(ctx, env, w.Name, Outcome{Final: final, Result: out.Body}), "succeeded")
	case Retryable(out.Err):
		var retryAfter time.Duration
		if out.Err.RetryAfterMS != nil {
			retryAfter = time.Duration(*out.Err.RetryAfterMS) * time.Millisecond
		}
		delay, more := w.Backoff.Delay(c.Attempt, retryAfter)
		if more {
			if !w.Budget.AllowRetry(account) {
				delay = max(delay, 30*time.Second) // deferred: live traffic first
			}
			report(w.Transitions.Retry(ctx, env, w.Name, w.now().Add(delay), string(out.Err.Code), c.Attempt), "retry")
			return
		}
		fallthrough
	default:
		final.ErrorCode, final.HTTPStatus = string(out.Err.Code), out.Err.Code.HTTPStatus()
		report(w.Transitions.DeadLetter(ctx, env, w.Name, Outcome{Final: final, Err: out.Err, DL: queue.DeadLetter{
			Reason: string(out.Err.Code), LastError: out.Err.Message, AccountID: account,
			LastVendorRequest: out.LastRequest, LastVendorResponse: out.LastResponse,
		}}), "dead-lettered")
	}
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
		Masker: w.Masker, Now: w.Now, SeqBase: attempt * 100,
	})
}
