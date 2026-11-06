package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Queueing is what the wait and async modes need (DESIGN §5.2, §11). Without it (tests, a
// gateway without a database) every mode is delivered synchronously.
type Queueing struct {
	DB      *pgxpool.Pool
	Queue   *queue.Outbox
	Settler delivery.Settler
	Ledger  audit.Ledger
	// Name owns the gateway's wait-mode claims (unique per process).
	Name string
}

func (s *Server) queued() bool { return s.deps.Queueing != nil && s.deps.Queueing.DB != nil }

func (s *Server) interactive(p *prepared) bool {
	c := p.caller.Snap.Capabilities[p.integration.Capability]
	return c != nil && c.Interactive // a person is waiting: never a 202 (DESIGN §5.4)
}

// accepted is the contract Accepted body (202).
type accepted struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	Poll      string `json:"poll"`
}

func writeAccepted(w http.ResponseWriter, requestID, status, reason string) {
	poll := "/v1/requests/" + requestID
	w.Header().Set("Location", poll)
	writeJSON(w, http.StatusAccepted, accepted{RequestID: requestID, Status: status, Reason: reason, Poll: poll})
}

// envelope builds the queued form of a prepared request: canonical body and integration id,
// never the vendor payload (DESIGN §11.3).
func (s *Server) envelope(r *http.Request, p *prepared) core.Envelope {
	d := p.res.Delivery
	ttl := d.TTL.Std()
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	now := p.timings.Start.UTC()
	env := core.Envelope{
		RequestID: RequestID(r.Context()), Tenant: p.caller.Tenant.Tenant, IntegrationID: p.integration.Integration,
		ResolvedVersion: p.res.VersionNumber, Pinned: d.Pending == registry.Pinned, Capability: p.integration.Capability,
		Operation: p.operation, Canonical: json.RawMessage(p.raw), Mode: string(d.Mode),
		NextAttemptAt: now, ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	if k := r.Header.Get("Idempotency-Key"); k != "" {
		env.IdempotencyKey = &k
	}
	if p.session != nil { // the worker pins the attempt to the session's version and account
		id := p.session.ID
		env.SessionID = &id
	}
	if tp := r.Header.Get("Traceparent"); tp != "" {
		env.Trace = map[string]string{"traceparent": tp}
	}
	return env
}

func (s *Server) ledgerRecord(r *http.Request, p *prepared, status core.State) audit.Record {
	return audit.Record{
		RequestID: RequestID(r.Context()), CreatedAt: p.timings.Start.UTC(), TenantID: p.caller.Tenant.Tenant,
		IntegrationID: p.integration.Integration, IntegrationVersion: p.res.VersionNumber, Capability: p.integration.Capability,
		Operation: p.operation, Mode: string(p.res.Delivery.Mode), Status: string(status), PayloadPolicy: string(p.trail.Policy()),
		SnapshotVersion: p.caller.Snap.Version, IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}
}

// persist writes the requests row and the outbox envelope in one transaction: a 202 is only
// sent after it committed ("accepted" means committed, DESIGN §11.1).
func (s *Server) persist(ctx context.Context, rec audit.Record, env core.Envelope, claimFor time.Duration) error {
	q := s.deps.Queueing
	return pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		if err := q.Ledger.Insert(ctx, tx, rec); err != nil {
			return err
		}
		if claimFor > 0 {
			return q.Queue.EnqueueClaimed(ctx, tx, env, q.Name, claimFor)
		}
		return q.Queue.Enqueue(ctx, tx, env)
	})
}

// persistFailed is the canonical error when the Hub cannot persist (TECH_SPEC §11 "Hub
// dependency failure": no dedicated code yet).
func persistFailed() *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeInternalError, Message: "The request could not be accepted", Retryable: true, Reason: core.Ptr("persist_failed")}
}

// emitEvents sends the trail's events without a requests record: queued requests own their
// row in the ledger (written in the delivery transactions), never through the buffered writer.
func (s *Server) emitEvents(p *prepared) {
	start := time.Now()
	s.deps.Audit.Emit(audit.Batch{Events: p.trail.Events()})
	p.timings.Observe(telemetry.StageAudit, start)
}

// accept is async / async_callback: persist, answer 202, a worker delivers (DESIGN §5.2).
func (s *Server) accept(w http.ResponseWriter, r *http.Request, p *prepared) {
	env := s.envelope(r, p)
	if err := s.persist(r.Context(), s.ledgerRecord(r, p, core.StateAccepted), env, 0); err != nil {
		s.log.ErrorContext(r.Context(), "accept failed", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		s.fail(w, r, p, persistFailed())
		return
	}
	p.trail.Add(audit.RequestPersisted, 0, nil, map[string]any{"expires_at": env.ExpiresAt, "mode": env.Mode})
	p.trail.Add(audit.PolicyApplied, 0, nil, map[string]any{"policy": string(p.trail.Policy())})
	p.trail.Add(audit.RequestAccepted, 0, nil, map[string]any{"status": "accepted", "reason": "async_mode"})
	writeAccepted(w, env.RequestID, "accepted", "async_mode")
	s.emitEvents(p)
}

type waitOutcome struct {
	state     core.State
	res       delivery.Result
	err       error
	sessionID string
}

// deliverWait is wait mode (D3): persist claimed by this gateway, attempt at once, answer 200
// (or the canonical error) within wait_ms, else 202 in_flight while the attempt finishes here.
// The claim (wait + endpoint timeout + 5 s) keeps workers away; if this gateway dies the claim
// expires and a worker reclaims the envelope (guarded: never both deliver).
func (s *Server) deliverWait(w http.ResponseWriter, r *http.Request, p *prepared) {
	q := s.deps.Queueing
	wait := time.Duration(p.res.Delivery.WaitMS) * time.Millisecond
	if wait <= 0 {
		wait = 3 * time.Second
	}
	endpointTimeout := 3 * time.Second
	if tf := p.caller.Snap.Transformers[p.res.Pair().Request]; tf != nil {
		if t, err := targetTimeout(p.caller.Snap, p.res.Target.Account, tf.VendorEndpoint); err == nil {
			endpointTimeout = t
		}
	}
	claimFor := wait + endpointTimeout + 5*time.Second
	env := s.envelope(r, p)
	env.Attempt = 1
	if err := s.persist(r.Context(), s.ledgerRecord(r, p, core.StateInFlight), env, claimFor); err != nil {
		s.log.ErrorContext(r.Context(), "persist failed", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		s.fail(w, r, p, persistFailed())
		return
	}
	p.trail.Add(audit.RequestPersisted, 0, nil, map[string]any{"expires_at": env.ExpiresAt, "mode": env.Mode, "wait_ms": wait.Milliseconds()})
	p.trail.Add(audit.PolicyApplied, 0, nil, map[string]any{"policy": string(p.trail.Policy())})

	done := make(chan waitOutcome, 1)
	// The attempt belongs to the request, not the connection: it finishes after a 202. From
	// here the goroutine owns the trail; it works on its own copy of the stage timings.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), claimFor)
	timings, idemKey := p.timings, r.Header.Get("Idempotency-Key")
	go func() {
		defer cancel()
		res := s.attempter.Run(ctx, delivery.Job{
			RequestID: env.RequestID, Snap: p.caller.Snap, Res: p.res, Body: p.body, Hdr: p.hdr, Session: p.sessionData(),
			IdempotencyKey: idemKey, Trail: p.trail, Timings: &timings, FirstAttempt: 1, Live: true,
		})
		// Step 1 succeeded: open the session before the success commits. A failure to open it
		// fails the request like a broken mapping (TRANSFORM_FAILED) or a Hub error.
		var sessionID string
		if res.Err == nil {
			var ce *core.CanonicalError
			if sessionID, ce = s.openSession(ctx, p, p.trail, res); ce != nil {
				res.Err, res.Body = ce, nil
			}
		}
		final := audit.Final{
			Attempts: max(len(res.Attempts), 1), IntegrationVersion: p.res.VersionNumber,
			HubOverheadMS: telemetry.Millis(timings.Overhead(s.deps.Now())), VendorMS: telemetry.Millis(timings.Stage(telemetry.StageUpstream)),
		}
		state, err := q.Settler.Settle(ctx, env, q.Name, final.Attempts, p.res.Target.Account, res, final)
		if err != nil && !delivery.IsLost(err) {
			s.log.ErrorContext(ctx, "wait-mode settle failed", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		}
		delivery.TerminalAudit(p.trail, final.Attempts, state, err, res.Err)
		s.deps.Audit.Emit(audit.Batch{Events: p.trail.Events(), Attempts: res.Attempts})
		done <- waitOutcome{state: state, res: res, err: err, sessionID: sessionID}
		s.shadow(ctx, r, p)
	}()

	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case o := <-done:
		switch {
		case o.err != nil && !delivery.IsLost(o.err):
			WriteError(w, r, persistFailed())
		case o.state == core.StateSucceeded:
			setSessionHeader(w, o.sessionID)
			if s.deps.DebugHeaders {
				w.Header().Set("X-Hub-Integration-Version", strconv.Itoa(p.res.VersionNumber))
			}
			w.Header().Set("X-Hub-Served-By", "primary")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(append(o.res.Body, '\n'))
		case o.state == core.StateRetryScheduled:
			writeAccepted(w, env.RequestID, "accepted", "retry_scheduled")
		case o.state == core.StateParked: // circuit open: nothing was sent (DESIGN §5.2)
			writeAccepted(w, env.RequestID, "queued", "circuit_open")
		default: // dead-lettered within the window: the caller gets the canonical error
			WriteError(w, r, o.res.Err)
		}
	case <-t.C:
		writeAccepted(w, env.RequestID, "in_flight", "wait_window_elapsed")
	}
}

// parkNow is queue_if_allowed with an open circuit: persist the request as parked (no attempt)
// and answer 202 queued; the drain delivers it after recovery (DESIGN §5.3).
func (s *Server) parkNow(w http.ResponseWriter, r *http.Request, p *prepared, res delivery.Result) *core.CanonicalError {
	q, ctx := s.deps.Queueing, r.Context()
	env, rec := s.envelope(r, p), s.ledgerRecord(r, p, core.StateParked)
	err := pgx.BeginFunc(ctx, q.DB, func(tx pgx.Tx) error {
		if err := q.Ledger.Insert(ctx, tx, rec); err != nil {
			return err
		}
		return q.Queue.EnqueueParked(ctx, tx, env, res.Account, res.Endpoint)
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "park failed", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		return persistFailed()
	}
	p.trail.Add(audit.RequestPersisted, 0, nil, map[string]any{"expires_at": env.ExpiresAt, "mode": env.Mode})
	p.trail.Add(audit.RequestParked, 0, nil, map[string]any{"reason": "circuit_open", "account": res.Account, "endpoint": res.Endpoint})
	writeAccepted(w, env.RequestID, "queued", "circuit_open")
	s.emitEvents(p)
	return nil
}

// targetTimeout is the endpoint timeout of the request transformer's vendor endpoint.
func targetTimeout(snap *registry.Snapshot, account, endpointRef string) (time.Duration, error) {
	acct := snap.Accounts[account]
	if acct == nil {
		return 0, errors.New("unknown account")
	}
	v := snap.Vendors[acct.Vendor]
	for name, ep := range v.Endpoints {
		if acct.Vendor+"."+name == endpointRef && ep.TimeoutMS > 0 {
			return time.Duration(ep.TimeoutMS) * time.Millisecond, nil
		}
	}
	return 0, errors.New("unknown endpoint")
}

// getRequestStatus is GET /v1/requests/{request_id}: tenant-scoped, other tenants' ids are 404.
func (s *Server) getRequestStatus(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerFrom(r.Context())
	notFound := &core.CanonicalError{Code: core.CodeRequestNotFound, Message: "Unknown request"}
	if !s.queued() {
		WriteError(w, r, notFound)
		return
	}
	st, err := s.deps.Queueing.Ledger.Status(r.Context(), s.deps.Queueing.DB, c.Tenant.Tenant, r.PathValue("request_id"))
	switch {
	case errors.Is(err, audit.ErrRequestNotFound):
		WriteError(w, r, notFound)
	case err != nil:
		s.log.ErrorContext(r.Context(), "request status failed", slog.String("error", err.Error()))
		WriteError(w, r, &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true})
	default:
		writeJSON(w, http.StatusOK, st)
	}
}

// bufferedWriter holds a response until the idempotency record is settled.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newBufferedWriter() *bufferedWriter { return &bufferedWriter{header: http.Header{}} }

func (b *bufferedWriter) Header() http.Header { return b.header }
func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *bufferedWriter) flushTo(w http.ResponseWriter) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	if b.status == 0 {
		b.status = http.StatusOK
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
