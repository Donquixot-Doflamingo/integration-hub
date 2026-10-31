package edge

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
)

// invoke is POST /v1/integrations/{id}/invoke and /invoke/{operation}: the sync pipeline of
// DESIGN §18.4 (resolve → transform → upstream → response map → respond).
//
// M3 delivers every mode synchronously: wait, async and async_callback get their persistence
// and the 202 path with the outbox (M4, HUB-115/116). See docs/milestones/M3.md, Deviations.
func (s *Server) invoke(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("operation")
	if op == "" {
		op = "default"
	}
	p, ce := s.prepare(r, ScopeInvoke, op)
	if ce == nil {
		ce = s.deliver(w, r, p)
	}
	if ce != nil {
		s.fail(w, r, p, ce)
	}
	if p != nil {
		s.metrics.RecordTimings(&p.timings, s.deps.Now(), p.integration.Integration, string(p.integration.Active().Delivery.Mode))
	}
}

// deliver runs the request after prepare. It writes the 200 itself and returns nil, or returns
// the error for the caller to write and audit.
func (s *Server) deliver(w http.ResponseWriter, r *http.Request, p *prepared) *core.CanonicalError {
	in := p.integration
	if in.Paused {
		return &core.CanonicalError{Code: core.CodeIntegrationPaused, Message: "The integration is paused", Reason: core.Ptr("paused")}
	}
	idemKey := r.Header.Get("Idempotency-Key")
	if in.MovesMoneyOrQuota && idemKey == "" {
		return &core.CanonicalError{Code: core.CodeIdempotencyKeyRequired, Message: "This integration requires an Idempotency-Key"}
	}

	ctx := r.Context()
	if v := r.Header.Get("X-Request-Deadline"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 1 || ms > 600_000 {
			return &core.CanonicalError{Code: core.CodeValidationFailed, Message: "X-Request-Deadline must be 1..600000 ms", Reason: core.Ptr("header"),
				Details: []core.ErrorDetail{{Pointer: "", Message: "X-Request-Deadline must be an integer 1..600000"}}}
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
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
