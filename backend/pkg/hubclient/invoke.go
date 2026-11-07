package hubclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Outcome is what a successful Invoke returns: either *Result (200, the request completed) or
// *Queued (202, accepted for later delivery; NOT done). The interface is sealed, so no other
// type implements it and a 202 can only be observed as *Queued. Type-switch on it:
//
//	switch o := out.(type) {
//	case *hubclient.Result: // done; o.Decode(&resp)
//	case *hubclient.Queued: // not done; poll o.RequestID or wait for a callback
//	}
type Outcome interface {
	outcome()
}

// Result is a completed request: the integration's response (HTTP 200), or, in pass-through
// (PassThrough true: the integration has no response transformer), the third party's own 2xx
// answer returned unchanged.
type Result struct {
	// Body is the response body exactly as the Hub sent it: JSON from a response transformer;
	// in pass-through, whatever the third party sent (check ContentType before decoding).
	Body json.RawMessage
	// PassThrough is true when the Hub returned the third party's status and body unchanged
	// (X-Hub-Pass-Through: true). Status and ContentType then are the third party's.
	PassThrough bool
	Status      int
	ContentType string
	// RequestID is the Hub request id (X-Hub-Request-Id).
	RequestID string
	// IdempotentReplay is true when the Hub replayed the stored result of an earlier request with
	// the same Idempotency-Key (X-Hub-Idempotent-Replay); nothing was sent upstream again.
	IdempotentReplay bool
	// Session is the X-Hub-Session returned by step 1 of a multi-step operation. Pass it to later
	// steps with WithSession. Empty otherwise.
	Session string
}

// Decode unmarshals the JSON response body into v.
func (r *Result) Decode(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("hubclient: decode result of request %s: %w", r.RequestID, err)
	}
	return nil
}

func (*Result) outcome() {}

// QueueStatus says why a request was accepted rather than completed.
type QueueStatus string

// Values of Queued.Status. The contract may add values; treat unknown ones like StatusAccepted.
const (
	StatusAccepted QueueStatus = "accepted"  // persisted for async delivery
	StatusQueued   QueueStatus = "queued"    // parked because the upstream is unavailable
	StatusInFlight QueueStatus = "in_flight" // wait window elapsed; a worker is finishing it
)

// PassThroughError is a third party's error answer the Hub returned unchanged (X-Hub-Pass-Through:
// true, status >= 300): the integration has no response transformer, retries (if any) are
// exhausted. Body is the third party's own body, never the Hub's error envelope.
type PassThroughError struct {
	Status      int
	ContentType string
	Body        []byte
	RetryAfter  string // the third party's Retry-After, when it sent one
	RequestID   string
}

func (e *PassThroughError) Error() string {
	return fmt.Sprintf("hubclient: upstream answered HTTP %d (pass-through, request %s)", e.Status, e.RequestID)
}

// Queued is a request accepted for later delivery (HTTP 202). It is not a success: the outcome
// is known only from GetRequest(ctx, RequestID) or a callback.
type Queued struct {
	RequestID string
	Status    QueueStatus
	// Reason is machine-readable (async_mode, circuit_open, wait_window_elapsed,
	// respond_async_preferred, ...). New values may appear.
	Reason string
	// Poll is the relative URL of the request status resource (/v1/requests/<id>).
	Poll string
	// IdempotentReplay is true when this 202 was replayed for a repeated Idempotency-Key.
	IdempotentReplay bool
}

func (*Queued) outcome() {}

// InvokeOption sets an optional header or the operation of one Invoke (or DryRun) call.
type InvokeOption func(*invokeConfig)

type invokeConfig struct {
	headers        map[string]string
	idempotencyKey string
	operation      string
	preferAsync    bool
	deadline       time.Duration
	session        string
	traceparent    string
}

// WithIdempotencyKey sends Idempotency-Key. It is mandatory for integrations that move money or
// quota (IntegrationSummary.IdempotencyKeyRequired) and recommended for every call: the same key
// within 24 h returns the stored result instead of calling the upstream again. Derive it from
// your business id, or create one with NewIdempotencyKey and reuse it on every retry.
func WithIdempotencyKey(key string) InvokeOption {
	return func(c *invokeConfig) { c.idempotencyKey = key }
}

// WithHeaders sends caller headers the operation declares in request_schema.headers (the request
// transformer reads them as hdr.*; the Hub passes no other caller header on). The Hub's own
// headers (Authorization, Idempotency-Key, Prefer, X-Request-Deadline, X-Hub-Session,
// traceparent) are set by their own options and ignored here.
func WithHeaders(h map[string]string) InvokeOption {
	return func(c *invokeConfig) { c.headers = h }
}

// hubHeaders are the headers the client sets itself; WithHeaders never overrides them.
var hubHeaders = map[string]bool{"Authorization": true, "Idempotency-Key": true, "Prefer": true, "X-Request-Deadline": true,
	"X-Hub-Session": true, "Traceparent": true, "Content-Type": true, "Accept": true, "User-Agent": true}

func (cfg invokeConfig) setHeaders(h http.Header) {
	for k, v := range cfg.headers {
		if name := http.CanonicalHeaderKey(k); !hubHeaders[name] {
			h.Set(name, v)
		}
	}
}

// WithOperation invokes one operation of a multi-operation integration
// (POST /v1/integrations/{id}/invoke/{op}); for DryRun it sets the operation query parameter.
func WithOperation(op string) InvokeOption {
	return func(c *invokeConfig) { c.operation = op }
}

// WithPreferAsync sends Prefer: respond-async: the caller accepts a 202 when the upstream is
// unavailable and the integration allows queueing. It never makes a healthy sync call async.
func WithPreferAsync() InvokeOption {
	return func(c *invokeConfig) { c.preferAsync = true }
}

// WithDeadline sends X-Request-Deadline, capping the synchronous time budget. It is rounded up to
// whole milliseconds and clamped to the contract's 1 ms to 10 min range; it can only lower the
// integration's own timeout. Bound the HTTP call itself with ctx.
func WithDeadline(d time.Duration) InvokeOption {
	return func(c *invokeConfig) { c.deadline = d }
}

// WithSession sends X-Hub-Session, the session id from step 1 of a multi-step operation
// (Result.Session). Use it together with WithOperation.
func WithSession(id string) InvokeOption {
	return func(c *invokeConfig) { c.session = id }
}

// WithTraceparent sends a W3C traceparent so the Hub continues the caller's trace.
func WithTraceparent(tp string) InvokeOption {
	return func(c *invokeConfig) { c.traceparent = tp }
}

const maxDeadline = 600_000 * time.Millisecond

// deadlineMS renders d as the X-Request-Deadline value.
func deadlineMS(d time.Duration) string {
	ms := int64(math.Ceil(float64(min(d, maxDeadline)) / float64(time.Millisecond)))
	return strconv.FormatInt(max(ms, 1), 10)
}

// validIdempotencyKey mirrors the contract: 1 to 255 printable ASCII characters, no spaces.
func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > 255 {
		return false
	}
	for i := range len(k) {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// Invoke sends the canonical body to an integration (operations invokeIntegration and
// invokeIntegrationOperation). body is any JSON-marshalable value; json.RawMessage and []byte are
// sent as they are and must be JSON.
//
// It returns *Result for 200 and *Queued for 202, never both, and an *Error for any Hub error
// response. Transport failures and context cancellation are returned wrapped (errors.Is works).
func (c *Client) Invoke(ctx context.Context, integrationID string, body any, opts ...InvokeOption) (Outcome, error) {
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}
	id, err := segment("integration id", integrationID)
	if err != nil {
		return nil, err
	}
	path := "/v1/integrations/" + id + "/invoke"
	if cfg.operation != "" {
		path += "/" + url.PathEscape(cfg.operation)
	}
	b, err := marshalBody(body)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	cfg.setHeaders(h)
	if cfg.idempotencyKey != "" {
		if !validIdempotencyKey(cfg.idempotencyKey) {
			return nil, errors.New("hubclient: idempotency key must be 1 to 255 printable ASCII characters without spaces")
		}
		h.Set("Idempotency-Key", cfg.idempotencyKey)
	}
	if cfg.preferAsync {
		h.Set("Prefer", "respond-async")
	}
	if cfg.deadline > 0 {
		h.Set("X-Request-Deadline", deadlineMS(cfg.deadline))
	}
	if cfg.session != "" {
		h.Set("X-Hub-Session", cfg.session)
	}
	if cfg.traceparent != "" {
		h.Set("Traceparent", cfg.traceparent)
	}

	resp, err := c.do(ctx, call{method: http.MethodPost, path: path, body: b, header: h})
	if err != nil {
		return nil, err
	}
	replay := resp.header.Get("X-Hub-Idempotent-Replay") == "true"
	if resp.header.Get("X-Hub-Pass-Through") == "true" { // the third party's own answer (ADR-0023)
		ct := resp.header.Get("Content-Type")
		if resp.status >= 300 {
			return nil, &PassThroughError{Status: resp.status, ContentType: ct, Body: resp.body,
				RetryAfter: resp.header.Get("Retry-After"), RequestID: resp.requestID}
		}
		return &Result{Body: json.RawMessage(resp.body), PassThrough: true, Status: resp.status, ContentType: ct,
			RequestID: resp.requestID, IdempotentReplay: replay, Session: resp.header.Get("X-Hub-Session")}, nil
	}
	switch resp.status {
	case http.StatusOK:
		if !json.Valid(resp.body) {
			return nil, fmt.Errorf("hubclient: 200 response of request %s is not valid JSON", resp.requestID)
		}
		return &Result{
			Body:             json.RawMessage(resp.body),
			Status:           http.StatusOK,
			ContentType:      resp.header.Get("Content-Type"),
			RequestID:        resp.requestID,
			IdempotentReplay: replay,
			Session:          resp.header.Get("X-Hub-Session"),
		}, nil
	case http.StatusAccepted:
		var a struct {
			RequestID string      `json:"request_id"`
			Status    QueueStatus `json:"status"`
			Reason    string      `json:"reason"`
			Poll      string      `json:"poll"`
		}
		if err := resp.decode(&a); err != nil {
			return nil, err
		}
		if a.RequestID == "" {
			a.RequestID = resp.requestID
		}
		if a.RequestID == "" || a.Status == "" {
			return nil, fmt.Errorf("hubclient: 202 response (request %s) lacks request_id or status", resp.requestID)
		}
		return &Queued{RequestID: a.RequestID, Status: a.Status, Reason: a.Reason, Poll: a.Poll, IdempotentReplay: replay}, nil
	default:
		return nil, resp.unexpected()
	}
}
