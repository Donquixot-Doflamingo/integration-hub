package edge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// CredsFunc returns the active credential values of an account from the warm cache. It never
// decrypts on the request path: a miss is an error (DESIGN §18.5 rule 4).
type CredsFunc func(ctx context.Context, accountID string) (map[string]string, error)

// hdrAllowlist maps the caller headers a transformer may read (hdr.*, DESIGN §7.2).
var hdrAllowlist = map[string]string{
	"Idempotency-Key": "idempotency_key",
}

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
	account     *registry.Account
	baseURL     string
	request     *transform.Program
	response    *transform.Program
	input       transform.Input
	timings     telemetry.Timings
	trail       *audit.Trail
	attempts    int
}

// prepare runs DESIGN §5.1 steps 1–4 for one call: scope, operation, body limit, schema
// validation, resolve and the transformer input, recording request.received, request.validated
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
	capability := c.Snap.Capabilities[in.Capability]
	p.op = capability.Operations[operation]
	pii := []registry.PIIPath(nil)
	if p.op != nil {
		pii = p.op.PII
	}

	start := time.Now()
	body, raw, ce := readJSONBody(r, bodyLimit(in.Capability))
	p.timings.Observe(telemetry.StageBodyLimit, start)
	p.trail = audit.NewTrail(audit.TrailOptions{
		RequestID: RequestID(r.Context()), Tenant: c.Tenant.Tenant, Policy: in.Active().Audit.Payloads,
		PII: pii, Canonical: body, Masker: s.deps.Masker, Now: s.deps.Now,
	})
	p.trail.Add(audit.RequestReceived, 0, raw, receivedMeta(r, c))
	if ce != nil {
		return p, ce
	}
	p.body, p.raw = body, raw
	if p.op == nil {
		return p, &core.CanonicalError{Code: core.CodeOperationNotFound, Message: "The capability has no operation " + operation}
	}
	if r.Header.Get("X-Hub-Session") != "" {
		// Vendor sessions arrive with multi-step capabilities (M6); none is open before then.
		return p, &core.CanonicalError{Code: core.CodeSessionNotFound, Message: "Unknown or expired session"}
	}

	start = time.Now()
	ce = validate(p.op, p.body)
	p.timings.Observe(telemetry.StageValidate, start)
	if ce != nil {
		return p, ce
	}
	canonical, _ := json.Marshal(p.body)
	p.trail.Add(audit.RequestValidated, 0, canonical, map[string]any{"schema_id": p.op.RequestID})

	start = time.Now()
	routeKey := r.Header.Get("Idempotency-Key")
	if routeKey == "" {
		routeKey = RequestID(r.Context())
	}
	res, err := resolve.Resolve(c.Snap, c.Tenant.Tenant, id, 0, routeKey)
	p.timings.Observe(telemetry.StageResolve, start)
	if err != nil {
		// authorize already handled unknown and not granted; anything else is a broken snapshot.
		s.log.ErrorContext(r.Context(), "resolve failed", slog.String("integration", id), slog.String("error", err.Error()))
		return p, internalError()
	}
	p.res = res
	p.account = c.Snap.Accounts[res.Target.Account]
	p.baseURL = c.Snap.Vendors[res.Target.Vendor].Regions[p.account.Region]
	p.request = c.Snap.Programs[res.Target.Transformer.Request]
	p.response = c.Snap.Programs[res.Target.Transformer.Response]
	p.trail.Add(audit.IntegrationResolved, 0, nil, map[string]any{
		"integration": id, "version": res.VersionNumber, "vendor": res.Target.Vendor, "account": res.Target.Account,
		"transformers": map[string]string{"request": res.Target.Transformer.Request.String(), "response": res.Target.Transformer.Response.String()},
		"target_index": res.TargetIndex, "policy": string(p.trail.Policy()), "snapshot_version": res.SnapshotVersion,
	})

	var creds map[string]string
	if s.deps.Creds != nil {
		if creds, err = s.deps.Creds(r.Context(), p.account.Account); err != nil {
			s.log.ErrorContext(r.Context(), "credentials unavailable", slog.String("integration", id), slog.String("error", err.Error()))
			return p, internalError()
		}
	}
	hdr := map[string]any{}
	for name, key := range hdrAllowlist {
		if v := r.Header.Get(name); v != "" {
			hdr[key] = v
		}
	}
	cfg := make(map[string]any, len(p.account.Config))
	for k, v := range p.account.Config {
		cfg[k] = v
	}
	p.input = transform.Input{
		Req: p.body, Hdr: hdr, Creds: creds, Now: s.deps.Now(),
		Account: map[string]any{"id": p.account.Account, "region": p.account.Region, "config": cfg},
	}
	return p, nil
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
	p.trail.Add(audit.RequestFailed, 0, nil, map[string]any{"code": string(ce.Code), "status": ce.Code.HTTPStatus()})
	s.record(r, p, "failed", ce.Code.HTTPStatus(), string(ce.Code))
}

// finish records request.completed and the final record of a successful request.
func (s *Server) finish(r *http.Request, p *prepared, status int) {
	p.trail.Add(audit.RequestCompleted, 0, nil, map[string]any{"status": status, "attempts": p.attempts})
	s.record(r, p, "succeeded", status, "")
}

func (s *Server) record(r *http.Request, p *prepared, status string, httpStatus int, code string) {
	now := s.deps.Now()
	in := p.integration
	rec := audit.Record{
		RequestID: RequestID(r.Context()), CreatedAt: p.timings.Start, TenantID: p.caller.Tenant.Tenant,
		IntegrationID: in.Integration, IntegrationVersion: p.res.VersionNumber, Capability: in.Capability,
		Operation: p.operation, Mode: string(in.Active().Delivery.Mode), DryRun: p.dryRun, Status: status,
		SnapshotVersion: p.caller.Snap.Version, IdempotencyKey: r.Header.Get("Idempotency-Key"),
		HTTPStatus: httpStatus, ErrorCode: code, Attempts: p.attempts,
		HubOverheadMS: telemetry.Millis(p.timings.Overhead(now)), VendorMS: telemetry.Millis(p.timings.Stage(telemetry.StageUpstream)),
		CompletedAt: now,
	}
	start := time.Now()
	s.deps.Audit.Emit(p.trail.Batch(rec))
	p.timings.Observe(telemetry.StageAudit, start)
}

// transformError maps a request-transformer failure to TRANSFORM_FAILED (500). Path and
// expression go to the log; the engine detail never does (it may quote input values).
func (s *Server) transformError(r *http.Request, p *prepared, err error) *core.CanonicalError {
	attrs := []any{slog.String("integration", p.integration.Integration), slog.String("transformer", p.request.Ref())}
	var ee *transform.EvalError
	if errors.As(err, &ee) {
		attrs = append(attrs, slog.String("path", ee.Path), slog.String("expr", ee.Expr))
	}
	s.log.WarnContext(r.Context(), "transform failed", attrs...)
	return &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The request could not be transformed"}
}
