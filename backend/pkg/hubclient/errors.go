package hubclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Code is a canonical Hub error code (DESIGN §4.4, contracts/schemas/events/canonical-error.json).
// The Hub may add codes in minor versions: treat an unknown code by its Status and Retryable.
type Code string

// Canonical error codes, mirroring the contract's enum.
const (
	CodeValidationFailed       Code = "VALIDATION_FAILED"
	CodeIdempotencyKeyRequired Code = "IDEMPOTENCY_KEY_REQUIRED"
	CodeUnauthorized           Code = "UNAUTHORIZED"
	CodeForbidden              Code = "FORBIDDEN"
	CodeIntegrationNotFound    Code = "INTEGRATION_NOT_FOUND"
	CodeOperationNotFound      Code = "OPERATION_NOT_FOUND"
	CodeCapabilityNotFound     Code = "CAPABILITY_NOT_FOUND"
	CodeRequestNotFound        Code = "REQUEST_NOT_FOUND"
	CodeSessionNotFound        Code = "SESSION_NOT_FOUND"
	CodeIdempotencyConflict    Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotentReplay       Code = "IDEMPOTENT_REPLAY" // signalled by Result/Queued.IdempotentReplay, never as an error status
	CodePayloadTooLarge        Code = "PAYLOAD_TOO_LARGE"
	CodeTransformFailed        Code = "TRANSFORM_FAILED"
	CodeInternalError          Code = "INTERNAL_ERROR"
	CodeUpstreamTimeout        Code = "UPSTREAM_TIMEOUT"
	CodeUpstreamUnavailable    Code = "UPSTREAM_UNAVAILABLE"
	CodeUpstreamRateLimited    Code = "UPSTREAM_RATE_LIMITED"
	CodeUpstreamRejected       Code = "UPSTREAM_REJECTED"
	CodeUpstreamAuthFailed     Code = "UPSTREAM_AUTH_FAILED"
	CodeIntegrationPaused      Code = "INTEGRATION_PAUSED"
	CodeNotReady               Code = "NOT_READY"
	CodeExpired                Code = "EXPIRED" // request status and callbacks only
)

// Error is a failure reported by the Hub: the canonical error envelope of a 4xx/5xx response,
// or the error of a failed, dead-lettered or expired request (RequestStatus.Error, Status 0).
// Get it with errors.As:
//
//	var he *hubclient.Error
//	if errors.As(err, &he) && he.Code == hubclient.CodeUpstreamUnavailable { ... }
//
// A 4xx/5xx without a Hub envelope (for example an HTML 502 from a proxy in front of the Hub)
// still yields an *Error: Code is CodeInternalError, Message says no envelope was received,
// Retryable is true only for 429, 502, 503 and 504, and RetryAfter comes from the Retry-After
// header.
type Error struct {
	Status     int           // HTTP status; 0 when the error comes from a request status resource
	Code       Code          // canonical code
	Message    string        // safe to show; for UPSTREAM_REJECTED the sanitised vendor message
	Retryable  bool          // the same request may be retried, with the same Idempotency-Key
	RetryAfter time.Duration // minimum wait before retrying; 0 when not given
	RequestID  string        // Hub request id (req_...), for support
	Reason     string        // machine-readable sub-reason (circuit_open, tenant_rate_limit, ...); may be empty
	Circuit    string        // breaker state seen by the request (open, half_open, closed); may be empty
	Upstream   *Upstream     // vendor-side outcome for UPSTREAM_* codes, without vendor identity; nil otherwise
	Details    []ErrorDetail // field-level details (VALIDATION_FAILED)
}

// Upstream is the vendor-side outcome attached to UPSTREAM_* errors. Its values are opaque: log
// them for support, do not branch on them.
type Upstream struct {
	Status int    // vendor HTTP status; 0 when no response was received
	Code   string // vendor error code; empty when the vendor sent none
}

// ErrorDetail is one field-level problem, typically a schema violation.
type ErrorDetail struct {
	Pointer string // RFC 6901 JSON pointer into the request body
	Keyword string // failing JSON Schema keyword (required, type, ...)
	Message string
}

// Error formats the code, status, message and request id. It never contains credentials.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("hubclient: ")
	b.WriteString(string(e.Code))
	if e.Status != 0 {
		b.WriteString(" (HTTP ")
		b.WriteString(strconv.Itoa(e.Status))
		b.WriteString(")")
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" [request ")
		b.WriteString(e.RequestID)
		b.WriteString("]")
	}
	return b.String()
}

// IsRetryable reports whether err is an *Error the Hub marked retryable. Transport failures
// (connection reset, timeout, cancelled context) return false: the request may have reached the
// Hub, so retry those only with an Idempotency-Key and your own policy.
func IsRetryable(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Retryable
}

// wireError is the contract's CanonicalError object.
type wireError struct {
	Code         Code    `json:"code"`
	Message      string  `json:"message"`
	Retryable    bool    `json:"retryable"`
	RetryAfterMS *int64  `json:"retry_after_ms"`
	Circuit      *string `json:"circuit"`
	Reason       *string `json:"reason"`
	Upstream     *struct {
		Status *int    `json:"status"`
		Code   *string `json:"code"`
	} `json:"upstream"`
	Details []struct {
		Pointer string `json:"pointer"`
		Keyword string `json:"keyword"`
		Message string `json:"message"`
	} `json:"details"`
	RequestID string `json:"request_id"`
}

func (w *wireError) toError(status int) *Error {
	e := &Error{
		Status:    status,
		Code:      w.Code,
		Message:   w.Message,
		Retryable: w.Retryable,
		RequestID: w.RequestID,
		Reason:    deref(w.Reason),
		Circuit:   deref(w.Circuit),
	}
	if w.RetryAfterMS != nil && *w.RetryAfterMS > 0 {
		e.RetryAfter = time.Duration(*w.RetryAfterMS) * time.Millisecond
	}
	if w.Upstream != nil {
		e.Upstream = &Upstream{Code: deref(w.Upstream.Code)}
		if w.Upstream.Status != nil {
			e.Upstream.Status = *w.Upstream.Status
		}
	}
	for _, d := range w.Details {
		e.Details = append(e.Details, ErrorDetail(d))
	}
	return e
}

// decodeError builds the *Error for a 4xx/5xx response. It always returns one, envelope or not.
func decodeError(status int, header http.Header, body []byte) *Error {
	var env struct {
		Error *wireError `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != nil && env.Error.Code != "" {
		e := env.Error.toError(status)
		if e.RequestID == "" {
			e.RequestID = header.Get("X-Hub-Request-Id")
		}
		if e.RetryAfter == 0 {
			e.RetryAfter = parseRetryAfter(header.Get("Retry-After"), time.Now())
		}
		return e
	}
	return &Error{
		Status:     status,
		Code:       CodeInternalError,
		Message:    "HTTP " + strconv.Itoa(status) + " " + http.StatusText(status) + " without a Hub error envelope",
		Retryable:  status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout,
		RetryAfter: parseRetryAfter(header.Get("Retry-After"), time.Now()),
		RequestID:  header.Get("X-Hub-Request-Id"),
	}
}

// parseRetryAfter reads a Retry-After header: delay-seconds (what the Hub sends) or an HTTP-date.
// It returns 0 for an absent, invalid or past value and caps the result at maxRetryAfter.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	var d time.Duration
	if s, err := strconv.ParseInt(v, 10, 64); err == nil {
		if s <= 0 {
			return 0
		}
		d = maxRetryAfter
		if s < int64(maxRetryAfter/time.Second) {
			d = time.Duration(s) * time.Second
		}
	} else if t, err := http.ParseTime(v); err == nil && t.After(now) {
		d = min(t.Sub(now), maxRetryAfter)
	}
	return d
}

// maxRetryAfter bounds a Retry-After header so an absurd value cannot overflow a Duration.
const maxRetryAfter = 24 * time.Hour

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
