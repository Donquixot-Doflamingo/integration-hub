package edge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// prepared is one request from authorization to the transformer input. Fields fill in as the
// pipeline advances; integration and trail are set as soon as the caller is authorized, so every
// later failure is audited.
type prepared struct {
	caller      Caller
	operation   string
	dryRun      bool
	integration *registry.Integration
	op          *registry.Operation
	body        map[string]any
	raw         []byte
	res         resolve.Resolution
	hdr         map[string]any
	timings     telemetry.Timings
	trail       *audit.Trail
	trailOpts   audit.TrailOptions // for the shadow copy's own trail
	attempts    int
	attemptRows []audit.Attempt
	// session is the vendor session of a later step (X-Hub-Session), nil on step 1.
	session *sessions.Session
}

// prepare runs DESIGN §5.1 steps 1–4 for one call: scope, operation, body limit, schema
// validation and resolve, recording request.received, request.validated
// and integration.resolved. A nil *prepared with an error means the caller was not authorized
// (nothing is audited); otherwise the caller finishes the request with s.fail or s.finish.
func (s *Server) prepare(r *http.Request, scope, operation string) (*prepared, *core.CanonicalError) {
	c, _ := CallerFrom(r.Context())
	p := &prepared{caller: c, operation: operation, dryRun: scope == ScopeDryRun, timings: telemetry.NewTimings(s.deps.Now())}
	id := r.PathValue("integration_id")
	in, ce := authorizeIntegration(c, id, scope)
	if ce != nil {
		return nil, ce
	}
	p.integration = in
	start := time.Now()
	ok, wait := s.tenants.allow(c.Tenant, in.Integration, s.deps.Now())
	p.timings.Observe(telemetry.StageRateLimit, start)
	if !ok && scope != ScopeDryRun {
		if s.rejections != nil {
			s.rejections.WithLabelValues("tenant").Inc()
		}
		return nil, tenantQuotaError(wait) // before any body is read: nothing to audit
	}
	p.op = in.Active().Op(operation)
	pii := []registry.PIIPath(nil)
	if p.op != nil {
		pii = p.op.PII
	}

	start = time.Now()
	body, raw, ce := readJSONBody(r, DefaultBodyLimit)
	p.timings.Observe(telemetry.StageBodyLimit, start)
	p.trailOpts = audit.TrailOptions{
		RequestID: RequestID(r.Context()), Tenant: c.Tenant.Tenant, Policy: in.Active().Audit.Payloads,
		PII: pii, Canonical: body, Masker: s.deps.Masker, Sealer: s.deps.Sealer, Now: s.deps.Now,
	}
	p.trail = audit.NewTrail(p.trailOpts)
	p.trail.Add(audit.RequestReceived, 0, raw, receivedMeta(r, c))
	if ce != nil {
		return p, ce
	}
	p.body, p.raw = body, raw
	if p.op == nil {
		return p, &core.CanonicalError{Code: core.CodeOperationNotFound, Message: "The integration has no operation " + operation}
	}
	start = time.Now()
	if p.op.Validates() { // validate_input off: callers are not checked (ADR-0023)
		ce = validate(p.op, p.body, r.Header)
	}
	p.timings.Observe(telemetry.StageValidate, start)
	if ce != nil {
		return p, ce
	}
	canonical, _ := json.Marshal(p.body)
	p.trail.Add(audit.RequestValidated, 0, canonical, map[string]any{"operation": operation, "validated": p.op.Validates()})

	start = time.Now()
	routeKey := r.Header.Get("Idempotency-Key")
	if routeKey == "" {
		routeKey = RequestID(r.Context())
	}
	var res resolve.Resolution
	if sid := r.Header.Get("X-Hub-Session"); sid != "" {
		// A later step of a vendor session: its exact version and account (DESIGN §6.3 row 6).
		res, ce = s.resolveSession(r.Context(), p, id, sid)
		p.timings.Observe(telemetry.StageResolve, start)
		if ce != nil {
			return p, ce
		}
	} else {
		var err error
		res, err = resolve.Resolve(c.Snap, c.Tenant.Tenant, id, 0, routeKey)
		p.timings.Observe(telemetry.StageResolve, start)
		if err != nil {
			// authorize already handled unknown and not granted; anything else is a broken snapshot.
			s.log.ErrorContext(r.Context(), "resolve failed", slog.String("integration", id), slog.String("error", err.Error()))
			return p, internalError()
		}
	}
	res.Operation = operation // selects the target's transformer pair (HUB-343)
	p.res = res
	pair := res.Pair()
	resolved := map[string]any{
		"integration": id, "version": res.VersionNumber, "vendor": res.Target.Vendor, "account": res.Target.Account,
		"transformers": transformersMeta(pair),
		"target_index": res.TargetIndex, "policy": string(p.trail.Policy()), "snapshot_version": res.SnapshotVersion,
	}
	if p.session != nil { // meta only: never the session data
		resolved["session"] = delivery.SessionMeta(p.session.ID, "used", p.session.Version, p.session.AccountID)
	}
	p.trail.Add(audit.IntegrationResolved, 0, nil, resolved)

	p.hdr = callerHeaders(p.op, r.Header)
	return p, nil
}

// callerHeaders is the transformer's hdr.*: the caller headers the operation declares in
// request_schema.headers (keyed as declared), plus the Hub-provided idempotency_key. No other
// caller header is visible to transformers (ADR-0023).
func callerHeaders(op *registry.Operation, h http.Header) map[string]any {
	out := make(map[string]any, len(op.HeaderNames())+1)
	for _, name := range op.HeaderNames() {
		if v := h.Get(name); v != "" {
			out[name] = v
		}
	}
	if v := h.Get("Idempotency-Key"); v != "" {
		out[registry.HdrIdempotencyKey] = v
	}
	return out
}

// transformersMeta names the pair of the resolved target; response is null for pass-through.
func transformersMeta(pair registry.TransformerPair) map[string]any {
	m := map[string]any{"request": pair.Request.String(), "response": nil}
	if !pair.PassThrough() {
		m["response"] = pair.Response.String()
	}
	return m
}

// receivedMeta is the request.received context: the tenant key appears only as its id and hash
// (DESIGN §13.1).
func receivedMeta(r *http.Request, c Caller) map[string]any {
	m := map[string]any{"method": r.Method, "path": r.URL.Path, "key_id": c.Key.KeyID, "key_hash": c.Key.Hash,
		"content_type": r.Header.Get("Content-Type")}
	for _, h := range []string{"Idempotency-Key", "Prefer", "X-Request-Deadline", "Traceparent"} {
		if v := r.Header.Get(h); v != "" {
			m[h] = v
		}
	}
	return m
}

func internalError() *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true}
}

// fail writes ce and, when the request got past authorization, records request.failed and the
// final record.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, p *prepared, ce *core.CanonicalError) {
	WriteError(w, r, ce)
	if p == nil {
		return
	}
	// Codes and counts only: never bodies, credentials or vendor payloads.
	s.log.InfoContext(r.Context(), "request failed", slog.String("integration", p.integration.Integration),
		slog.String("code", string(ce.Code)), slog.Int("status", ce.Code.HTTPStatus()), slog.Int("attempts", p.attempts))
	p.trail.Add(audit.RequestFailed, 0, nil, map[string]any{"code": string(ce.Code), "status": ce.Code.HTTPStatus()})
	s.record(r, p, "failed", ce.Code.HTTPStatus(), string(ce.Code))
}

// failed records a request whose vendor error the caller already got in pass-through: the
// same audit as fail, with the vendor's status, without writing the canonical envelope.
func (s *Server) failed(r *http.Request, p *prepared, ce *core.CanonicalError, status int) {
	s.log.InfoContext(r.Context(), "request failed", slog.String("integration", p.integration.Integration),
		slog.String("code", string(ce.Code)), slog.Int("status", status), slog.Int("attempts", p.attempts), slog.Bool("pass_through", true))
	p.trail.Add(audit.RequestFailed, 0, nil, map[string]any{"code": string(ce.Code), "status": status, "pass_through": true})
	s.record(r, p, "failed", status, string(ce.Code))
}

// finish records request.completed and the final record of a successful request.
// shadow starts the version's shadow copy of the request, if any (DESIGN §6.4). It never
// delays or changes the caller's answer.
func (s *Server) shadow(ctx context.Context, r *http.Request, p *prepared) {
	// A later session step is bound to its vendor session, which the shadow target never saw.
	if s.deps.Shadow == nil || p.res.Version == nil || p.res.Version.Shadow == nil || p.session != nil {
		return
	}
	s.deps.Shadow.Copy(ctx, delivery.Job{
		RequestID: p.trailOpts.RequestID, Snap: p.caller.Snap, Res: p.res, Body: p.body, Hdr: p.hdr,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	}, p.trailOpts)
}

func (s *Server) finish(r *http.Request, p *prepared, status int) {
	p.trail.Add(audit.RequestCompleted, 0, nil, map[string]any{"status": status, "attempts": p.attempts})
	s.record(r, p, "succeeded", status, "")
}

func (s *Server) record(r *http.Request, p *prepared, status string, httpStatus int, code string) {
	now := s.deps.Now()
	in := p.integration
	rec := audit.Record{
		RequestID: RequestID(r.Context()), CreatedAt: p.timings.Start, TenantID: p.caller.Tenant.Tenant,
		IntegrationID: in.Integration, IntegrationVersion: p.res.VersionNumber,
		Operation: p.operation, Mode: string(in.Active().Delivery.Mode), DryRun: p.dryRun, Status: status,
		SnapshotVersion: p.caller.Snap.Version, IdempotencyKey: r.Header.Get("Idempotency-Key"), TraceID: traceID(r.Context()),
		HTTPStatus: httpStatus, ErrorCode: code, Attempts: p.attempts,
		HubOverheadMS: telemetry.Millis(p.timings.Overhead(now)), VendorMS: telemetry.Millis(p.timings.Stage(telemetry.StageUpstream)),
		CompletedAt: now,
	}
	start := time.Now()
	s.deps.Audit.Emit(p.trail.Batch(rec, p.attemptRows...))
	if !p.dryRun {
		s.metrics.CountRequest(requestLabels(p, rec.Mode, code))
	}
	p.timings.Observe(telemetry.StageAudit, start)
}

// requestLabels names the target that served the request: the account of the last attempt
// (a failover target when one answered), else the resolved target (refused before any attempt).
func requestLabels(p *prepared, mode, code string) telemetry.RequestLabels {
	vendor, account := p.res.Target.Vendor, p.res.Target.Account
	if n := len(p.attemptRows); n > 0 && p.attemptRows[n-1].AccountID != "" {
		account = p.attemptRows[n-1].AccountID
		if a := p.caller.Snap.Accounts[account]; a != nil {
			vendor = a.Vendor
		}
	}
	return telemetry.RequestLabels{
		Tenant: p.caller.Tenant.Tenant, Integration: p.integration.Integration, Vendor: vendor, Account: account,
		Version: p.res.VersionNumber, Mode: mode, Code: code,
	}
}
