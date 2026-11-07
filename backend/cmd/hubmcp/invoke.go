package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	IntegrationID  string            `json:"integration_id"`
	Operation      string            `json:"operation"`
	Body           json.RawMessage   `json:"body"`
	Headers        map[string]string `json:"headers"`
	IdempotencyKey string            `json:"idempotency_key"`
	PreferAsync    bool              `json:"prefer_async"`
	DeadlineMS     int               `json:"deadline_ms"`
	Session        string            `json:"session"`
	Confirm        bool              `json:"confirm"`
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
	if in.IdempotencyKey != "" && !printableASCII(in.IdempotencyKey) {
		// The Idempotency-Key header allows printable ASCII without spaces (hub-public).
		return invalidArguments(toolInvoke, errors.New("idempotency_key must be printable ASCII without spaces"))
	}
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

	if d.MovesMoneyOrQuota {
		if res, refused := confirmGuard(in); refused {
			return res
		}
	}

	opts := operationOption(in.Operation)
	if len(in.Headers) > 0 {
		if err := declaredHeaders(d, in.Operation, in.Headers); err != nil {
			return invalidArguments(toolInvoke, err)
		}
		opts = append(opts, hubclient.WithHeaders(in.Headers))
	}
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
	var pt *hubclient.PassThroughError
	if errors.As(err, &pt) {
		return passThroughFailure(pt)
	}
	if err != nil {
		e, status := a.hubErrorObject(err)
		return failure(toolInvoke, e, status)
	}
	return invokeResult(out)
}

// hubOwnHeaders are the headers hubmcp sets from dedicated inputs; the headers input never
// carries them (contract hub_invoke/hub_dry_run headers).
var hubOwnHeaders = []string{"authorization", "idempotency-key", "prefer", "x-request-deadline", "x-hub-session", "traceparent"}

// declaredHeaders refuses caller headers the operation does not declare in
// request_schema.headers (the Hub would not pass them to the transformer anyway) and the Hub's
// own headers.
func declaredHeaders(d *hubclient.IntegrationDetail, op string, headers map[string]string) error {
	if op == "" {
		op = "default"
	}
	var declared struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if o := d.Operation(op); o != nil && len(o.RequestSchema.Headers) > 0 {
		_ = json.Unmarshal(o.RequestSchema.Headers, &declared)
	}
	for name := range headers {
		if slices.Contains(hubOwnHeaders, strings.ToLower(name)) {
			return fmt.Errorf("headers.%s is a Hub header: use the dedicated input instead", name)
		}
		ok := false
		for k := range declared.Properties {
			ok = ok || strings.EqualFold(k, name)
		}
		if !ok {
			return fmt.Errorf("headers.%s is not declared in operation %s's request_schema.headers (see hub_describe_integration)", name, op)
		}
	}
	return nil
}

// resultJSON is a response body as the tool's result: JSON as it is, anything else (a
// pass-through text body) as a JSON string, an empty body as null.
func resultJSON(body []byte, contentType string) json.RawMessage {
	b := bytes.TrimSpace(body)
	switch {
	case len(b) == 0:
		return nil
	case (contentType == "" || strings.Contains(strings.ToLower(contentType), "json")) && json.Valid(b):
		return json.RawMessage(b)
	}
	s, _ := json.Marshal(string(body))
	return s
}

// passThroughFailure is a third party's error answer the Hub returned unchanged: outcome error,
// pass_through true, no canonical error, result = the third party's body (contract hub_invoke).
func passThroughFailure(pt *hubclient.PassThroughError) toolResult {
	status := pt.Status
	return toolResult{isError: true, outcome: "error", requestID: pt.RequestID,
		note: "The third party answered HTTP " + strconv.Itoa(status) + " (pass-through: its own body is in result).",
		structured: invokeOutput{Outcome: "error", HTTPStatus: &status, RequestID: optString(pt.RequestID), PassThrough: true,
			Result: resultJSON(pt.Body, pt.ContentType)}}
}

// confirmGuard enforces contract rule 2 (HUB-304) for an integration that moves money or quota:
// without confirm=true, or without an idempotency_key, the call is refused locally and the Hub is
// not called. The refusal echoes what would be sent so the model can show it to the user.
func confirmGuard(in *invokeInput) (toolResult, bool) {
	var code, msg, pointer string
	switch {
	case !in.Confirm:
		code, pointer = codeConfirmationRequired, "/confirm"
		msg = "Integration " + in.IntegrationID + " moves money or quota. Nothing was sent. Show the user exactly what will be sent " +
			"(see the summary) and get explicit approval, then call hub_invoke again with confirm=true and an idempotency_key."
	case in.IdempotencyKey == "":
		code, pointer = codeIdempotencyKeyRequired, "/idempotency_key"
		msg = "Integration " + in.IntegrationID + " moves money or quota and needs an idempotency_key (a stable id for this " +
			"logical action, e.g. 'payout-8821'), so a retry cannot pay twice. Nothing was sent."
	default:
		return toolResult{}, false
	}
	return toolResult{
		isError: true, outcome: "refused",
		note: "REFUSED by hubmcp, the Hub was not called. Summary of the requested call: " + invokeSummary(in),
		structured: invokeOutput{Outcome: "refused", Error: &errorObject{
			Code: code, Message: msg, Retryable: false,
			Details: []errorDetail{{Pointer: pointer, Keyword: "required", Message: msg}},
		}},
	}, true
}

// invokeSummary describes the call a refusal held back: integration, operation, idempotency key
// and the canonical body the model built (already in its context; nothing new is disclosed).
func invokeSummary(in *invokeInput) string {
	op := in.Operation
	if op == "" {
		op = "default"
	}
	key := in.IdempotencyKey
	if key == "" {
		key = "(none)"
	}
	return "integration_id=" + in.IntegrationID + " operation=" + op + " idempotency_key=" + key + " body=" + string(in.Body)
}

func printableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
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
		status := o.Status
		if status == 0 {
			status = 200
		}
		return toolResult{
			outcome: "completed", requestID: o.RequestID,
			structured: invokeOutput{Outcome: "completed", HTTPStatus: &status, RequestID: optString(o.RequestID),
				IdempotentReplay: o.IdempotentReplay, Result: resultJSON(o.Body, o.ContentType), PassThrough: o.PassThrough,
				Session: optString(o.Session)},
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
		RequestID: st.RequestID, IntegrationID: st.IntegrationID, Operation: st.Operation,
		Mode: st.Mode, Status: string(st.Status), Attempts: st.Attempts,
		NextAttemptAt: optTime(st.NextAttemptAt), ExpiresAt: optTime(st.ExpiresAt), CompletedAt: optTime(st.CompletedAt),
	}
	if len(st.Result) > 0 { // any JSON value (ADR-0023)
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
		IntegrationID string            `json:"integration_id"`
		Operation     string            `json:"operation"`
		Body          json.RawMessage   `json:"body"`
		Headers       map[string]string `json:"headers"`
		Session       string            `json:"session"`
	}
	if err := json.Unmarshal(c.args, &in); err != nil {
		return invalidArguments(toolDryRun, err)
	}
	opts := operationOption(in.Operation)
	if len(in.Headers) > 0 {
		d, err := a.integrations.get(ctx, c, in.IntegrationID)
		if err != nil {
			e, status := a.hubErrorObject(err)
			res := failure(toolDryRun, e, status)
			res.integrationID = in.IntegrationID
			return res
		}
		if err := declaredHeaders(d, in.Operation, in.Headers); err != nil {
			return invalidArguments(toolDryRun, err)
		}
		opts = append(opts, hubclient.WithHeaders(in.Headers))
	}
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
