package core

import "net/http"

// Code is a canonical error code (DESIGN §4.4 plus the hub-public additions in contracts/README.md).
type Code string

const (
	CodeValidationFailed       Code = "VALIDATION_FAILED"
	CodeIdempotencyKeyRequired Code = "IDEMPOTENCY_KEY_REQUIRED"
	CodeUnauthorized           Code = "UNAUTHORIZED"
	CodeForbidden              Code = "FORBIDDEN"
	CodeIntegrationNotFound    Code = "INTEGRATION_NOT_FOUND"
	CodeOperationNotFound      Code = "OPERATION_NOT_FOUND"
	CodeRequestNotFound        Code = "REQUEST_NOT_FOUND"
	CodeSessionNotFound        Code = "SESSION_NOT_FOUND"
	CodeIdempotencyConflict    Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotentReplay       Code = "IDEMPOTENT_REPLAY"
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
	CodeExpired                Code = "EXPIRED"
)

// HTTPStatus is the response status for c (DESIGN §4.4). Codes that never travel as an HTTP
// error (IDEMPOTENT_REPLAY, EXPIRED) map to 0.
func (c Code) HTTPStatus() int {
	switch c {
	case CodeValidationFailed, CodeIdempotencyKeyRequired:
		return http.StatusBadRequest
	case CodeUnauthorized:
		return http.StatusUnauthorized
	case CodeForbidden:
		return http.StatusForbidden
	case CodeIntegrationNotFound, CodeOperationNotFound, CodeRequestNotFound, CodeSessionNotFound:
		return http.StatusNotFound
	case CodeIdempotencyConflict:
		return http.StatusConflict
	case CodePayloadTooLarge:
		return http.StatusRequestEntityTooLarge
	case CodeUpstreamRejected:
		return http.StatusUnprocessableEntity
	case CodeUpstreamRateLimited:
		return http.StatusTooManyRequests
	case CodeTransformFailed, CodeInternalError:
		return http.StatusInternalServerError
	case CodeUpstreamAuthFailed:
		return http.StatusBadGateway
	case CodeUpstreamUnavailable, CodeIntegrationPaused, CodeNotReady:
		return http.StatusServiceUnavailable
	case CodeUpstreamTimeout:
		return http.StatusGatewayTimeout
	case CodeIdempotentReplay, CodeExpired:
		return 0
	}
	return http.StatusInternalServerError
}

// ErrorDetail is one field-level problem.
type ErrorDetail struct {
	Pointer string `json:"pointer,omitempty"`
	Keyword string `json:"keyword,omitempty"`
	Message string `json:"message"`
}

// Upstream is the vendor-side outcome, stripped of vendor identity.
type Upstream struct {
	Status *int    `json:"status"`
	Code   *string `json:"code"`
}

// CanonicalError is the caller-facing error object (contracts/schemas/events/canonical-error.json).
// It never names a vendor, an account or a version (D12).
type CanonicalError struct {
	Code         Code          `json:"code"`
	Message      string        `json:"message"`
	Retryable    bool          `json:"retryable"`
	RetryAfterMS *int64        `json:"retry_after_ms,omitempty"`
	Circuit      *string       `json:"circuit,omitempty"`
	Reason       *string       `json:"reason,omitempty"`
	Upstream     *Upstream     `json:"upstream,omitempty"`
	Details      []ErrorDetail `json:"details,omitempty"`
	RequestID    string        `json:"request_id"`
}

// Error implements error.
func (e *CanonicalError) Error() string { return string(e.Code) + ": " + e.Message }

// ErrorEnvelope is the single error response body: {"error": {...}}.
type ErrorEnvelope struct {
	Error *CanonicalError `json:"error"`
}

// Ptr returns a pointer to v, for the optional fields above.
func Ptr[T any](v T) *T { return &v }
