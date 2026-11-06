package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// Invoke, request status and dry-run tools (HUB-303).

const (
	// integrationCacheTTL bounds how stale hub_invoke's pre-check may be (contract rule 2: ≤ 60 s).
	integrationCacheTTL   = 60 * time.Second
	maxCachedIntegrations = 1024
	// maxAutoRetryWait: a retryable error asking for a longer wait is returned to the model
	// instead of being retried (it can retry itself after retry_after_ms).
	maxAutoRetryWait = 2 * time.Second
	minAutoRetryWait = 250 * time.Millisecond
	// pausedRetryAfter mirrors the Hub's INTEGRATION_PAUSED hint and the pre-check cache TTL.
	pausedRetryAfter = int64(60_000)
)

// integrationCache caches getIntegration results for the invoke pre-check, per tenant key.
type integrationCache struct {
	now func() time.Time

	mu sync.Mutex
	m  map[string]cachedIntegration
}

type cachedIntegration struct {
	detail  *hubclient.IntegrationDetail
	fetched time.Time
}

func newIntegrationCache(now func() time.Time) *integrationCache {
	return &integrationCache{now: now, m: map[string]cachedIntegration{}}
}

// get returns the integration, from the cache when it was fetched less than 60 s ago.
func (ic *integrationCache) get(ctx context.Context, c *toolCall, id string) (*hubclient.IntegrationDetail, error) {
	key := c.tenant + "\x00" + id
	ic.mu.Lock()
	e, ok := ic.m[key]
	ic.mu.Unlock()
	if ok && ic.now().Sub(e.fetched) < integrationCacheTTL {
		return e.detail, nil
	}
	d, err := c.hub.GetIntegration(ctx, id)
	if err != nil {
		return nil, err
	}
	ic.mu.Lock()
	if len(ic.m) >= maxCachedIntegrations {
		clear(ic.m)
	}
	ic.m[key] = cachedIntegration{detail: d, fetched: ic.now()}
	ic.mu.Unlock()
	return d, nil
}

type invokeInput struct {
	IntegrationID  string          `json:"integration_id"`
	Operation      string          `json:"operation"`
	Body           json.RawMessage `json:"body"`
	IdempotencyKey string          `json:"idempotency_key"`
	PreferAsync    bool            `json:"prefer_async"`
	DeadlineMS     int             `json:"deadline_ms"`
	Session        string          `json:"session"`
	Confirm        bool            `json:"confirm"`
}

// operationOption selects a named operation; "default" (or none) uses the plain invoke path.
func operationOption(op string) []hubclient.InvokeOption {
	if op == "" || op == "default" {
		return nil
	}
	return []hubclient.InvokeOption{hubclient.WithOperation(op)}
}

func (a *app) invoke(ctx context.Context, c *toolCall) toolResult {
	var in invokeInput
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolInvoke, err)
	}
	res := a.invokeChecked(ctx, c, &in)
	res.integrationID = in.IntegrationID
	return res
}

func (a *app) invokeChecked(ctx context.Context, c *toolCall, in *invokeInput) toolResult {
	// Pre-check (contract hub_invoke safety): read the integration, cached ≤ 60 s.
	d, err := a.integrations.get(ctx, c, in.IntegrationID)
	if err != nil {
		e, status := a.hubErrorObject(err)
		return failure(toolInvoke, e, status)
	}
	if d.Paused {
		retry := pausedRetryAfter
		return failure(toolInvoke, &errorObject{
			Code:         string(hubclient.CodeIntegrationPaused),
			Message:      "Integration is paused by an administrator; hubmcp did not call it. Try again later.",
			Retryable:    true,
			RetryAfterMS: &retry,
			Reason:       optString("paused"),
		}, 0)
	}

	opts := operationOption(in.Operation)
	if in.IdempotencyKey != "" {
		opts = append(opts, hubclient.WithIdempotencyKey(in.IdempotencyKey))
	}
	if in.PreferAsync {
		opts = append(opts, hubclient.WithPreferAsync())
	}
	if in.DeadlineMS > 0 {
		opts = append(opts, hubclient.WithDeadline(time.Duration(in.DeadlineMS)*time.Millisecond))
	}
	if in.Session != "" {
		opts = append(opts, hubclient.WithSession(in.Session))
	}
	out, err := c.hub.Invoke(ctx, in.IntegrationID, in.Body, opts...)
	if wait, ok := autoRetry(err, in.IdempotencyKey); ok {
		// Contract rule 5: at most one retry, retryable errors only, only with an idempotency key.
		if serr := a.sleep(ctx, wait); serr == nil {
			out, err = c.hub.Invoke(ctx, in.IntegrationID, in.Body, opts...)
		}
	}
	if err != nil {
		e, status := a.hubErrorObject(err)
		return failure(toolInvoke, e, status)
	}
	return invokeResult(out)
}

// autoRetry reports whether err may be retried once and after how long.
func autoRetry(err error, idempotencyKey string) (time.Duration, bool) {
	var he *hubclient.Error
	if idempotencyKey == "" || !errors.As(err, &he) || !he.Retryable || he.RetryAfter > maxAutoRetryWait {
		return 0, false
	}
	return max(he.RetryAfter, minAutoRetryWait), true
}

// invokeResult maps a hubclient outcome: 200 → completed, 202 → queued (never success).
func invokeResult(out hubclient.Outcome) toolResult {
	switch o := out.(type) {
	case *hubclient.Result:
		status := 200
		body := o.Body
		if len(body) == 0 || body[0] != '{' {
			body = nil // the contract's result is an object; anything else is not passed on
		}
		return toolResult{
			outcome: "completed", requestID: o.RequestID,
			structured: invokeOutput{Outcome: "completed", HTTPStatus: &status, RequestID: optString(o.RequestID),
				IdempotentReplay: o.IdempotentReplay, Result: body, Session: optString(o.Session)},
		}
	case *hubclient.Queued:
		status := 202
		return toolResult{
			outcome: "queued", requestID: o.RequestID,
			note: "Accepted but NOT done yet (" + string(o.Status) + "). Tell the user it is pending and check later with hub_get_request request_id=" + o.RequestID + ".",
			structured: invokeOutput{Outcome: "queued", HTTPStatus: &status, RequestID: optString(o.RequestID),
				IdempotentReplay: o.IdempotentReplay, Accepted: &acceptedBody{Status: string(o.Status), Reason: o.Reason, Poll: o.Poll}},
		}
	default:
		return failure(toolInvoke, &errorObject{Code: string(hubclient.CodeInternalError), Message: "unexpected invoke outcome"}, 0)
	}
}

// requestOutput is hub_get_request's structured content (hub-public RequestStatus subset).
type requestOutput struct {
	RequestID     string          `json:"request_id"`
	IntegrationID string          `json:"integration_id"`
	Capability    string          `json:"capability,omitempty"`
	Operation     string          `json:"operation,omitempty"`
	Mode          string          `json:"mode,omitempty"`
	Status        string          `json:"status"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt *string         `json:"next_attempt_at"`
	ExpiresAt     *string         `json:"expires_at"`
	CompletedAt   *string         `json:"completed_at"`
	Result        json.RawMessage `json:"result"`
	Error         *errorObject    `json:"error"`
}

func (a *app) getRequest(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolGetRequest, err)
	}
	st, err := c.hub.GetRequest(ctx, in.RequestID)
	if err != nil {
		e, status := a.hubErrorObject(err)
		res := failure(toolGetRequest, e, status)
		res.requestID = in.RequestID
		return res
	}
	out := requestOutput{
		RequestID: st.RequestID, IntegrationID: st.IntegrationID, Capability: st.Capability, Operation: st.Operation,
		Mode: st.Mode, Status: string(st.Status), Attempts: st.Attempts,
		NextAttemptAt: optTime(st.NextAttemptAt), ExpiresAt: optTime(st.ExpiresAt), CompletedAt: optTime(st.CompletedAt),
	}
	if len(st.Result) > 0 && st.Result[0] == '{' {
		out.Result = st.Result
	}
	if st.Error != nil {
		out.Error, _ = a.hubErrorObject(st.Error)
	}
	return toolResult{structured: out, requestID: st.RequestID, integrationID: st.IntegrationID, outcome: string(st.Status)}
}

func optTime(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

// dryRunOutput is hub_dry_run's structured content.
type dryRunOutput struct {
	RequestID       string         `json:"request_id"`
	UpstreamRequest upstreamOutput `json:"upstream_request"`
	Redacted        []string       `json:"redacted"`
	Warnings        []string       `json:"warnings"`
}

type upstreamOutput struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// dryRun shows the upstream request the Hub would send. It is the documented exception to the
// no-vendor rule (contract rule 1); hubmcp still forces *** on every header the Hub lists as
// redacted, in case a value slipped through.
func (a *app) dryRun(ctx context.Context, c *toolCall) toolResult {
	var in struct {
		IntegrationID string          `json:"integration_id"`
		Operation     string          `json:"operation"`
		Body          json.RawMessage `json:"body"`
		Session       string          `json:"session"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolDryRun, err)
	}
	opts := operationOption(in.Operation)
	if in.Session != "" {
		opts = append(opts, hubclient.WithSession(in.Session))
	}
	r, err := c.hub.DryRun(ctx, in.IntegrationID, in.Body, opts...)
	if err != nil {
		e, status := a.hubErrorObject(err)
		res := failure(toolDryRun, e, status)
		res.integrationID = in.IntegrationID
		return res
	}
	out := dryRunOutput{
		RequestID: r.RequestID,
		UpstreamRequest: upstreamOutput{Method: r.UpstreamRequest.Method, URL: r.UpstreamRequest.URL,
			Headers: map[string]string{}, Body: r.UpstreamRequest.Body},
		Redacted: append([]string{}, r.Redacted...),
		Warnings: append([]string{}, r.Warnings...),
	}
	for k, v := range r.UpstreamRequest.Headers {
		out.UpstreamRequest.Headers[k] = v
	}
	for _, path := range r.Redacted {
		name, ok := strings.CutPrefix(path, "headers.")
		if !ok {
			continue
		}
		for k, v := range out.UpstreamRequest.Headers {
			if strings.EqualFold(k, name) && v != "***" {
				out.UpstreamRequest.Headers[k] = "***"
				out.Warnings = append(out.Warnings, "hubmcp replaced the value of redacted header "+k+" with ***")
			}
		}
	}
	return toolResult{structured: out, requestID: r.RequestID, integrationID: in.IntegrationID}
}
