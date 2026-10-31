package edge

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// Outcome classification (DESIGN §4.4, §10.2). Vendor answers are classified by the response
// transformer (error rules first, then status); this file covers transport failures, the sync
// retry rule and the caller-facing details added to a classified vendor error.

// transportError maps a failed attempt to the canonical error: timeouts (endpoint or caller
// deadline) → 504 UPSTREAM_TIMEOUT; refused, DNS, TLS, reset, protocol and egress failures →
// 503 UPSTREAM_UNAVAILABLE. The message never names the vendor or the host.
func transportError(err error) *core.CanonicalError {
	var te *upstream.TransportError
	if errors.As(err, &te) && te.Kind == upstream.KindTimeout {
		ce := &core.CanonicalError{Code: core.CodeUpstreamTimeout, Message: "The upstream did not answer in time", Retryable: true, Upstream: &core.Upstream{}}
		if te.CallerDeadline {
			ce.Reason = core.Ptr("request_deadline")
		}
		return ce
	}
	if errors.Is(err, upstream.ErrCanceled) {
		// The caller went away; nobody reads this, but the record needs a code.
		return &core.CanonicalError{Code: core.CodeUpstreamTimeout, Message: "The request was canceled", Retryable: true, Reason: core.Ptr("caller_canceled"), Upstream: &core.Upstream{}}
	}
	return &core.CanonicalError{Code: core.CodeUpstreamUnavailable, Message: "The upstream is unavailable", Retryable: true, Upstream: &core.Upstream{}}
}

// retrySync reports whether a failed sync attempt gets its one immediate retry: only after a
// timeout or reset, and only if the endpoint is idempotent or the caller sent an
// Idempotency-Key (DESIGN §10.2). A caller deadline or cancellation is never retried.
func retrySync(err error, endpointIdempotent bool, idempotencyKey string) bool {
	var te *upstream.TransportError
	if !errors.As(err, &te) || te.CallerDeadline {
		return false
	}
	if te.Kind != upstream.KindTimeout && te.Kind != upstream.KindReset {
		return false
	}
	return endpointIdempotent || idempotencyKey != ""
}

// enrichVendorError adds what the transformer's classification cannot know: retry_after_ms
// from Retry-After on a 429, and the vendor's own message on UPSTREAM_REJECTED (the caller needs
// it to fix the request) when no error rule set one.
func enrichVendorError(ce *core.CanonicalError, resp core.VendorResponse, now time.Time) {
	if ce.Code == core.CodeUpstreamRateLimited && ce.RetryAfterMS == nil {
		if ms, ok := retryAfterMS(resp.Headers.Get("Retry-After"), now); ok {
			ce.RetryAfterMS = &ms
		}
	}
	if ce.Code == core.CodeUpstreamRejected && ce.Message == defaultRejected {
		if msg := vendorMessage(resp.Body); msg != "" {
			ce.Message = msg
		}
	}
	if ce.Upstream != nil && ce.Upstream.Code == nil {
		if code := vendorCode(resp.Body); code != "" {
			ce.Upstream.Code = &code
		}
	}
}

const defaultRejected = "The upstream rejected the request" // transform's default message

func retryAfterMS(v string, now time.Time) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if s, err := strconv.ParseInt(v, 10, 64); err == nil && s >= 0 {
		return min(s, 86_400) * 1000, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(int64(t.Sub(now)/time.Millisecond), 0), true
	}
	return 0, false
}

// vendorMessage finds a human message in common error body shapes: message, error (string),
// error.message, error_description, errors[0].message. Truncated to 512 characters.
func vendorMessage(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	pick := func(v any) string {
		s, _ := v.(string)
		return strings.TrimSpace(s)
	}
	candidates := []string{pick(m["message"]), pick(m["error"]), pick(m["error_description"])}
	if e, ok := m["error"].(map[string]any); ok {
		candidates = append(candidates, pick(e["message"]))
	}
	if list, ok := m["errors"].([]any); ok && len(list) > 0 {
		if e, ok := list[0].(map[string]any); ok {
			candidates = append(candidates, pick(e["message"]))
		}
	}
	for _, c := range candidates {
		if c != "" {
			if r := []rune(c); len(r) > 512 {
				return string(r[:512])
			}
			return c
		}
	}
	return ""
}

// vendorCode finds the vendor's error code (code or error.code), as a string, ≤ 64 characters.
func vendorCode(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	v := m["code"]
	if e, ok := m["error"].(map[string]any); ok && v == nil {
		v = e["code"]
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
