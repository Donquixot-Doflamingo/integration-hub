package edge

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// invoke is POST /v1/integrations/{id}/invoke and /invoke/{operation} (DESIGN §5, §18.4):
// prepare (auth → body → validate → resolve), admission (paused, Idempotency-Key rules),
// idempotency, then the integration's delivery mode: sync, wait (persist, attempt in the
// gateway, 200 within the window or 202) or async (persist, 202, worker delivers).
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("operation")
	if op == "" {
		op = "default"
	}
	p, ce := s.prepare(r, ScopeInvoke, op)
	if p != nil {
		defer s.metrics.RecordTimings(&p.timings, s.deps.Now(), p.integration.Integration, string(p.integration.Active().Delivery.Mode))
	}
	if ce == nil {
		ce = admit(r, p)
	}
	if ce != nil {
		s.fail(w, r, p, ce)
		return
	}
	claim, handled := s.idempotencyBegin(w, r, p)
	if handled {
		return
	}
	// The response is buffered so a fresh idempotency claim stores it before the caller (or a
	// duplicate arriving right after) can see it.
	bw := newBufferedWriter()
	switch mode := p.integration.Active().Delivery.Mode; {
	case !s.queued() || mode == registry.ModeSync || s.interactive(p):
		if ce := s.deliver(bw, r, p); ce != nil {
			s.fail(bw, r, p, ce)
		}
	case mode == registry.ModeWait:
		s.deliverWait(bw, r, p)
	default:
		s.accept(bw, r, p)
	}
	claim.settle(r.Context(), s, bw)
	bw.flushTo(w)
}

// admit applies the integration's own rules before any delivery (DESIGN §4.2, §10.4).
func admit(r *http.Request, p *prepared) *core.CanonicalError {
	in := p.integration
	if in.Paused {
		return &core.CanonicalError{Code: core.CodeIntegrationPaused, Message: "The integration is paused", Reason: core.Ptr("paused")}
	}
	if in.MovesMoneyOrQuota && r.Header.Get("Idempotency-Key") == "" {
		return &core.CanonicalError{Code: core.CodeIdempotencyKeyRequired, Message: "This integration requires an Idempotency-Key"}
	}
	return nil
}

// requestDeadline parses X-Request-Deadline (1..600000 ms).
func requestDeadline(r *http.Request) (time.Duration, *core.CanonicalError) {
	v := r.Header.Get("X-Request-Deadline")
	if v == "" {
		return 0, nil
	}
	ms, err := strconv.Atoi(v)
	if err != nil || ms < 1 || ms > 600_000 {
		return 0, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "X-Request-Deadline must be 1..600000 ms", Reason: core.Ptr("header"),
			Details: []core.ErrorDetail{{Pointer: "", Message: "X-Request-Deadline must be an integer 1..600000"}}}
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// deliver runs a sync request after prepare. It writes the 200 itself and returns nil, or
// returns the error for the caller to write and audit.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, p *prepared) *core.CanonicalError {
	idemKey := r.Header.Get("Idempotency-Key")
	ctx := r.Context()
	deadline, ce := requestDeadline(r)
	if ce != nil {
		return ce
	}
	if deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, deadline)
		defer cancel()
	}

	res := s.attempter.Run(ctx, delivery.Job{
		RequestID: RequestID(r.Context()), Snap: p.caller.Snap, Res: p.res, Body: p.body, Hdr: p.hdr,
		IdempotencyKey: idemKey, Trail: p.trail, Timings: &p.timings, SyncRetry: true, FirstAttempt: 1,
	})
	p.attempts += len(res.Attempts)
	p.attemptRows = append(p.attemptRows, res.Attempts...)
	if res.Err != nil {
		return res.Err
	}
	body := res.Body

	w.Header().Set("X-Hub-Served-By", "primary")
	if s.deps.DebugHeaders {
		w.Header().Set("X-Hub-Integration-Version", strconv.Itoa(p.res.VersionNumber))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
	s.finish(r, p, http.StatusOK)
	return nil
}
