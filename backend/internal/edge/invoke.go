package edge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
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

	// Transform.
	start := time.Now()
	req, err := p.request.BuildRequest(p.input)
	p.timings.Observe(telemetry.StageTransform, start)
	if err != nil {
		return s.transformError(r, p, err)
	}
	ref := p.res.Target.Transformer.Request
	tf := p.caller.Snap.Transformers[ref]
	target, err := upstream.TargetFor(p.caller.Snap, p.account.Account, tf.VendorEndpoint)
	if err != nil {
		s.log.ErrorContext(ctx, "no upstream target", slog.String("integration", in.Integration), slog.String("error", err.Error()))
		return internalError()
	}
	// The audit snapshot is taken before the auth scheme injects anything; credential values
	// that the transformer itself mapped (static headers) are redacted by value (trail secrets).
	p.trail.AddJSON(audit.RequestTransformed, 0, upstreamRequest{Method: req.Method, URL: upstreamURL(target.BaseURL, req), Headers: req.Headers, Body: req.Body},
		map[string]any{"transformer": ref.String(), "endpoint": tf.VendorEndpoint})
	if err := creds.ApplyVendor(target.Vendor, p.creds, &req); err != nil {
		s.log.ErrorContext(ctx, "auth scheme failed", slog.String("integration", in.Integration), slog.String("error", err.Error()))
		return internalError()
	}

	// Upstream: one attempt, plus one immediate retry after a timeout or reset when it is safe.
	attempt, err := s.attempt(ctx, p, target, req)
	if err != nil && retrySync(err, target.Endpoint.Idempotent, idemKey) {
		attempt, err = s.attempt(ctx, p, target, req)
	}
	if err != nil {
		return transportError(err)
	}

	// Response map.
	start = time.Now()
	out, vce, err := p.response.MapResponse(attempt.Response, p.input)
	p.timings.Observe(telemetry.StageResponseMap, start)
	if err != nil {
		p.trail.Add(audit.ResponseTransformFailed, p.attempts, nil, map[string]any{"transformer": p.response.Ref()})
		s.log.WarnContext(ctx, "response transform failed", slog.String("integration", in.Integration), slog.String("transformer", p.response.Ref()))
		return &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The upstream response could not be mapped"}
	}
	if vce != nil {
		enrichVendorError(vce, attempt.Response, s.deps.Now())
		p.trail.Add(audit.ResponseTransformed, p.attempts, nil, map[string]any{"transformer": p.response.Ref(), "code": string(vce.Code)})
		return vce
	}
	body, err := json.Marshal(out)
	if err != nil {
		return internalError()
	}
	p.trail.Add(audit.ResponseTransformed, p.attempts, body, map[string]any{"transformer": p.response.Ref()})

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

// attempt sends one vendor call and records vendor.sent, vendor.responded and the attempt row.
func (s *Server) attempt(ctx context.Context, p *prepared, t upstream.Target, req core.VendorRequest) (upstream.Attempt, error) {
	p.attempts++
	n := p.attempts
	p.trail.Add(audit.VendorSent, n, nil, map[string]any{"attempt": n, "endpoint": t.EndpointName, "account": t.AccountID})
	started := s.deps.Now()
	a, err := s.deps.Upstream.Attempt(ctx, t, req)
	p.timings.Add(telemetry.StageUpstream, a.Latency)
	row := audit.Attempt{StartedAt: started, RequestID: RequestID(ctx), AttemptNo: n, AccountID: t.AccountID, EndpointID: t.EndpointName,
		DurationMS: telemetry.Millis(a.Latency)}
	if err != nil {
		var te *upstream.TransportError
		kind := "transport"
		if errors.As(err, &te) {
			kind = string(te.Kind)
		}
		row.ErrorClass = kind
		p.trail.Add(audit.VendorResponded, n, nil, map[string]any{"attempt": n, "error": kind})
	} else {
		row.HTTPStatus = a.Response.Status
		raw := responseBytes(a.Response.Body)
		p.trail.Add(audit.VendorResponded, n, raw, map[string]any{"attempt": n, "status": a.Response.Status,
			"truncated": a.Truncated, "malformed": a.MalformedBody, "latency_ms": telemetry.Millis(a.Latency)})
	}
	p.attemptRows = append(p.attemptRows, row)
	return a, err
}

// responseBytes is the vendor body as bytes: text as is, JSON re-encoded.
func responseBytes(body any) []byte {
	switch b := body.(type) {
	case nil:
		return []byte{}
	case string:
		return []byte(b)
	case []byte:
		return b
	}
	out, _ := json.Marshal(body)
	return out
}
