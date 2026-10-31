package edge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

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

// prepared is a validated, resolved request, ready for the request transformer.
type prepared struct {
	caller    Caller
	operation string
	op        *registry.Operation
	body      map[string]any
	raw       []byte
	res       resolve.Resolution
	account   *registry.Account
	baseURL   string
	request   *transform.Program
	response  *transform.Program
	input     transform.Input
	timings   telemetry.Timings
}

// prepare runs DESIGN §5.1 steps 1–4 for one call: scope, operation, body limit, schema
// validation, resolve and the transformer input. It writes the error and returns false when the
// request must stop. Every stage is timed into p.timings.
func (s *Server) prepare(w http.ResponseWriter, r *http.Request, scope, operation string) (*prepared, bool) {
	c, _ := CallerFrom(r.Context())
	p := &prepared{caller: c, operation: operation, timings: telemetry.NewTimings(s.deps.Now())}
	id := r.PathValue("integration_id")
	in, ok := authorize(w, r, c, id, scope)
	if !ok {
		return nil, false
	}
	capability := c.Snap.Capabilities[in.Capability]
	if p.op = capability.Operations[operation]; p.op == nil {
		WriteError(w, r, &core.CanonicalError{Code: core.CodeOperationNotFound, Message: "The capability has no operation " + operation})
		return nil, false
	}
	if r.Header.Get("X-Hub-Session") != "" {
		// Vendor sessions arrive with multi-step capabilities (M6); none is open before then.
		WriteError(w, r, &core.CanonicalError{Code: core.CodeSessionNotFound, Message: "Unknown or expired session"})
		return nil, false
	}

	start := time.Now()
	var ce *core.CanonicalError
	p.body, p.raw, ce = readJSONBody(w, r, bodyLimit(in.Capability))
	p.timings.Observe(telemetry.StageBodyLimit, start)
	if ce != nil {
		WriteError(w, r, ce)
		return nil, false
	}
	start = time.Now()
	ce = validate(p.op, p.body)
	p.timings.Observe(telemetry.StageValidate, start)
	if ce != nil {
		WriteError(w, r, ce)
		return nil, false
	}

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
		WriteError(w, r, &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true})
		return nil, false
	}
	p.res = res
	p.account = c.Snap.Accounts[res.Target.Account]
	p.baseURL = c.Snap.Vendors[res.Target.Vendor].Regions[p.account.Region]
	p.request = c.Snap.Programs[res.Target.Transformer.Request]
	p.response = c.Snap.Programs[res.Target.Transformer.Response]

	var creds map[string]string
	if s.deps.Creds != nil {
		if creds, err = s.deps.Creds(r.Context(), p.account.Account); err != nil {
			s.log.ErrorContext(r.Context(), "credentials unavailable", slog.String("integration", id), slog.String("error", err.Error()))
			WriteError(w, r, &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true})
			return nil, false
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
	return p, true
}

// transformError maps a request-transformer failure to TRANSFORM_FAILED (500). Path and
// expression go to the log; the engine detail never does (it may quote input values).
func (s *Server) transformError(r *http.Request, p *prepared, err error) *core.CanonicalError {
	attrs := []any{slog.String("integration", p.res.Integration.Integration), slog.String("transformer", p.request.Ref())}
	var ee *transform.EvalError
	if errors.As(err, &ee) {
		attrs = append(attrs, slog.String("path", ee.Path), slog.String("expr", ee.Expr))
	}
	s.log.WarnContext(r.Context(), "transform failed", attrs...)
	return &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The request could not be transformed"}
}
