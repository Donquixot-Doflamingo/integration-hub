package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Upstream sends one attempt to a vendor (upstream.Client).
type Upstream interface {
	Attempt(ctx context.Context, t upstream.Target, req core.VendorRequest) (upstream.Attempt, error)
}

// Attempter is the one function that runs resolve → transform → inject → upstream → classify →
// map response for sync, wait and worker attempts (TECH_SPEC §4.2). It reads only the snapshot
// and the warm credential cache.
type Attempter struct {
	Creds    creds.Source
	Upstream Upstream
	// Breakers and Limiter are admission (DESIGN §10.4, §10.8); nil disables each.
	Breakers *upstream.Breakers
	Limiter  *upstream.AccountLimiter
	Log      *slog.Logger
	Now      func() time.Time
}

// Job is one delivery attempt (with the sync path's one immediate retry when allowed).
type Job struct {
	RequestID string
	Snap      *registry.Snapshot
	Res       resolve.Resolution
	Body      map[string]any // validated canonical body
	Hdr       map[string]any // allowlisted caller headers (transformer hdr.*)
	// Session is the data of the vendor session a later step runs in (transformer session.*);
	// Res must then be the session's pinned resolution (resolve.ResolveSession).
	Session        map[string]any
	IdempotencyKey string
	Trail          *audit.Trail
	Timings        *telemetry.Timings
	// SyncRetry allows the DESIGN §10.2 sync retry (timeout/reset, idempotent or keyed). Queued
	// attempts retry through the backoff schedule instead.
	SyncRetry bool
	// FirstAttempt numbers this run's first vendor call (attempts already made + 1).
	FirstAttempt int
	// Live marks caller traffic (sync, wait, a worker's first attempt): it may use the whole
	// account bucket; retries and drain leave a reserve for it.
	Live bool
	// LimiterWait caps the wait for an account token (default: 250 ms live, 5 s otherwise).
	LimiterWait time.Duration
}

// Result is what a job produced. Exactly one of Body and Err is set.
type Result struct {
	Body []byte // canonical response JSON
	// Session is the vendor session data the response transformer mapped on success (its
	// session: section); the caller opens a session with it. nil when none.
	Session  map[string]any
	Err      *core.CanonicalError
	Attempts []audit.Attempt
	// Reached reports that a request left the Hub for the vendor (attempt records exist).
	Reached bool
	// LastRequest and LastResponse are the audit view (credential-free) of the last exchange,
	// kept with a dead letter (DESIGN §10.6).
	LastRequest  json.RawMessage
	LastResponse json.RawMessage
	// CircuitOpen reports that admission refused the call (Err carries circuit "open" and
	// retry_after_ms): the caller applies on_circuit_open, or parks. Account and Endpoint name
	// the breaker for parking.
	CircuitOpen bool
	Account     string
	Endpoint    string
}

// AuditRequest is the audit and dry-run view of an upstream request.
type AuditRequest struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         any               `json:"body,omitempty"`
	BodyEncoding string            `json:"body_encoding,omitempty"`
}

// UpstreamURL joins the regional base URL, the path and the sorted query.
func UpstreamURL(base string, req core.VendorRequest) string {
	u := strings.TrimSuffix(base, "/") + req.Path
	if len(req.Query) > 0 {
		q := url.Values{}
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	return u
}

func (a *Attempter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func internalError() *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true}
}

// Input loads the account's credentials from the cache and builds the transformer input. The
// credential values are added to the trail's secrets before anything else is recorded.
func (a *Attempter) Input(ctx context.Context, snap *registry.Snapshot, res resolve.Resolution, body, hdr map[string]any, trail *audit.Trail) (transform.Input, creds.Credentials, *core.CanonicalError) {
	account := snap.Accounts[res.Target.Account]
	c, err := a.Creds.Get(ctx, account.Account)
	if err != nil {
		a.Log.ErrorContext(ctx, "credentials unavailable", slog.String("integration", res.Integration.Integration), slog.String("error", err.Error()))
		if errors.Is(err, creds.ErrCacheMiss) {
			return transform.Input{}, c, &core.CanonicalError{Code: core.CodeUpstreamUnavailable, Message: "The upstream is unavailable", Retryable: true,
				RetryAfterMS: core.Ptr[int64](5000), Reason: core.Ptr("credentials_unavailable"), Upstream: &core.Upstream{}}
		}
		return transform.Input{}, c, internalError()
	}
	if trail != nil {
		trail.AddSecrets(slices.Collect(maps.Values(c.Values))...)
	}
	cfg := make(map[string]any, len(account.Config))
	for k, v := range account.Config {
		cfg[k] = v
	}
	return transform.Input{
		Req: body, Hdr: hdr, Creds: c.Values, Now: a.now(),
		Account: map[string]any{"id": account.Account, "region": account.Region, "config": cfg},
	}, c, nil
}

// Run executes one job. It records request.transformed, vendor.sent, vendor.responded and
// response.transformed (or response.transform_failed) on the job's trail.
func (a *Attempter) Run(ctx context.Context, j Job) Result {
	var r Result
	in, c, ce := a.Input(ctx, j.Snap, j.Res, j.Body, j.Hdr, j.Trail)
	if ce != nil {
		r.Err = ce
		return r
	}
	in.Session = j.Session
	reqProg := j.Snap.Programs[j.Res.Target.Transformer.Request]
	respProg := j.Snap.Programs[j.Res.Target.Transformer.Response]
	integration := j.Res.Integration.Integration
	ref := j.Res.Target.Transformer.Request
	tf := j.Snap.Transformers[ref]
	target, err := upstream.TargetFor(j.Snap, c.AccountID, tf.VendorEndpoint)
	if err != nil {
		a.Log.ErrorContext(ctx, "no upstream target", slog.String("integration", integration), slog.String("error", err.Error()))
		r.Err = internalError()
		return r
	}
	r.Account, r.Endpoint = target.AccountID, tf.VendorEndpoint

	// Breakers first: a local check, so a refused call costs no transform (DESIGN §10.4).
	start := time.Now()
	adm, ce := a.admitBreakers(j, target, tf.VendorEndpoint)
	j.Timings.Observe(telemetry.StageAdmission, start)
	if ce != nil {
		r.Err, r.CircuitOpen = ce, true
		return r
	}

	start = time.Now()
	req, err := reqProg.BuildRequest(in)
	if errors.Is(err, transform.ErrTimeBudget) { // a stall, most likely: transforms are pure, run again
		req, err = reqProg.BuildRequest(in)
	}
	j.Timings.Observe(telemetry.StageTransform, start)
	if err != nil {
		adm.record(a.now(), upstream.OutcomeIgnore)
		r.Err = a.transformError(ctx, integration, reqProg.Ref(), err)
		return r
	}
	// The audit snapshot is taken before the auth scheme injects anything; credential values
	// that the transformer itself mapped (static headers) are redacted by value (trail secrets).
	view := AuditRequest{Method: req.Method, URL: UpstreamURL(target.BaseURL, req), Headers: req.Headers, Body: req.Body}
	j.Trail.AddJSON(audit.RequestTransformed, 0, view, map[string]any{"transformer": ref.String(), "endpoint": tf.VendorEndpoint})
	r.LastRequest = j.Trail.Stored(audit.RequestTransformed)
	if err := creds.ApplyVendor(target.Vendor, c, &req); err != nil {
		adm.record(a.now(), upstream.OutcomeIgnore)
		a.Log.ErrorContext(ctx, "auth scheme failed", slog.String("integration", integration), slog.String("error", err.Error()))
		r.Err = internalError()
		return r
	}
	// The account limiter last: its wait is the time right before the send.
	start = time.Now()
	ce = a.waitLimiter(ctx, j, target)
	j.Timings.Observe(telemetry.StageAdmission, start)
	if ce != nil {
		adm.record(a.now(), upstream.OutcomeIgnore)
		r.Err = ce
		return r
	}
	n := max(j.FirstAttempt, 1)
	att, err := a.attempt(ctx, j, &r, n, target, req)
	if err != nil && j.SyncRetry && !adm.probe() && RetrySync(err, target.Endpoint.Idempotent, j.IdempotencyKey) {
		adm.record(a.now(), transportOutcome(err)) // the retry is a fresh call for the breaker
		adm = adm.again(a.now())
		n++
		att, err = a.attempt(ctx, j, &r, n, target, req)
	}
	if err != nil {
		adm.record(a.now(), transportOutcome(err))
		r.Err = TransportError(err)
		return r
	}

	start = time.Now()
	out, session, vce, err := respProg.MapResponseSession(att.Response, in)
	if errors.Is(err, transform.ErrTimeBudget) {
		out, session, vce, err = respProg.MapResponseSession(att.Response, in)
	}
	j.Timings.Observe(telemetry.StageResponseMap, start)
	adm.record(a.now(), responseOutcome(att.Response.Status, vce))
	if err != nil {
		j.Trail.Add(audit.ResponseTransformFailed, n, nil, map[string]any{"transformer": respProg.Ref()})
		a.Log.WarnContext(ctx, "response transform failed", slog.String("integration", integration), slog.String("transformer", respProg.Ref()))
		r.Err = &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The upstream response could not be mapped"}
		if errors.Is(err, transform.ErrTimeBudget) {
			r.Err.Retryable, r.Err.Reason = true, core.Ptr("time_budget")
		}
		return r
	}
	if vce != nil {
		EnrichVendorError(vce, att.Response, a.now())
		j.Trail.Add(audit.ResponseTransformed, n, nil, map[string]any{"transformer": respProg.Ref(), "code": string(vce.Code)})
		r.Err = vce
		return r
	}
	body, err := json.Marshal(out)
	if err != nil {
		r.Err = internalError()
		return r
	}
	j.Trail.Add(audit.ResponseTransformed, n, body, map[string]any{"transformer": respProg.Ref()})
	r.Body, r.Session = body, session
	return r
}

// attempt sends one vendor call and records vendor.sent, vendor.responded and the attempt row.
func (a *Attempter) attempt(ctx context.Context, j Job, r *Result, n int, t upstream.Target, req core.VendorRequest) (upstream.Attempt, error) {
	r.Reached = true
	j.Trail.Add(audit.VendorSent, n, nil, map[string]any{"attempt": n, "endpoint": t.EndpointName, "account": t.AccountID,
		"version": j.Res.VersionNumber})
	started := a.now()
	att, err := a.Upstream.Attempt(ctx, t, req)
	j.Timings.Add(telemetry.StageUpstream, att.Latency)
	row := audit.Attempt{StartedAt: started, RequestID: j.RequestID, AttemptNo: n, AccountID: t.AccountID, EndpointID: t.EndpointName,
		IntegrationVersion: j.Res.VersionNumber, DurationMS: telemetry.Millis(att.Latency)}
	if err != nil {
		kind := "transport"
		var te *upstream.TransportError
		if errors.As(err, &te) {
			kind = string(te.Kind)
		}
		row.ErrorClass = kind
		j.Trail.Add(audit.VendorResponded, n, nil, map[string]any{"attempt": n, "error": kind})
		r.LastResponse = json.RawMessage(`{"error":"` + kind + `"}`)
	} else {
		row.HTTPStatus = att.Response.Status
		j.Trail.Add(audit.VendorResponded, n, responseBytes(att.Response.Body), map[string]any{"attempt": n, "status": att.Response.Status,
			"truncated": att.Truncated, "malformed": att.MalformedBody, "latency_ms": telemetry.Millis(att.Latency)})
		stored := j.Trail.Stored(audit.VendorResponded)
		resp := map[string]any{"status": att.Response.Status}
		if len(stored) > 0 {
			resp["body"] = string(stored)
		}
		r.LastResponse, _ = json.Marshal(resp)
	}
	r.Attempts = append(r.Attempts, row)
	return att, err
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

// transformError maps a request-transformer failure to TRANSFORM_FAILED (500). Path and
// expression go to the log; the engine detail never does (it may quote input values).
func (a *Attempter) transformError(ctx context.Context, integration, transformer string, err error) *core.CanonicalError {
	attrs := []any{slog.String("integration", integration), slog.String("transformer", transformer)}
	var ee *transform.EvalError
	if errors.As(err, &ee) {
		attrs = append(attrs, slog.String("path", ee.Path), slog.String("expr", ee.Expr))
	}
	a.Log.WarnContext(ctx, "transform failed", attrs...)
	ce := &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The request could not be transformed"}
	if errors.Is(err, transform.ErrTimeBudget) { // overran twice: retryable, not a broken mapping
		ce.Retryable, ce.Reason = true, core.Ptr("time_budget")
	}
	return ce
}

// TransformError is the exported form for the edge's dry-run.
func (a *Attempter) TransformError(ctx context.Context, integration, transformer string, err error) *core.CanonicalError {
	return a.transformError(ctx, integration, transformer, err)
}
