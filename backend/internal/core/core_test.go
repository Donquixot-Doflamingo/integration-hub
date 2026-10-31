package core

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestNewRequestIDMatchesContractPattern(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewRequestID()
		if !RequestIDPattern.MatchString(id) {
			t.Fatalf("%q does not match %s", id, RequestIDPattern)
		}
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
}

func TestULIDsSortByTime(t *testing.T) {
	base := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	ids := []string{NewULID(base), NewULID(base.Add(time.Millisecond)), NewULID(base.Add(time.Hour))}
	if !slices.IsSorted(ids) {
		t.Fatalf("not sorted by time: %v", ids)
	}
	// Known prefix: the first 10 chars encode the millisecond timestamp.
	if got := NewULID(time.UnixMilli(0))[:10]; got != "0000000000" {
		t.Fatalf("epoch timestamp chars = %q", got)
	}
}

func TestHTTPStatusPerDesignTable(t *testing.T) {
	tests := map[Code]int{
		CodeValidationFailed:    http.StatusBadRequest,
		CodeUnauthorized:        http.StatusUnauthorized,
		CodeForbidden:           http.StatusForbidden,
		CodeIntegrationNotFound: http.StatusNotFound,
		CodeIntegrationPaused:   http.StatusServiceUnavailable,
		CodeSessionNotFound:     http.StatusNotFound,
		CodeTransformFailed:     http.StatusInternalServerError,
		CodeUpstreamTimeout:     http.StatusGatewayTimeout,
		CodeUpstreamUnavailable: http.StatusServiceUnavailable,
		CodeUpstreamRateLimited: http.StatusTooManyRequests,
		CodeUpstreamRejected:    http.StatusUnprocessableEntity,
		CodeUpstreamAuthFailed:  http.StatusBadGateway,
		CodePayloadTooLarge:     http.StatusRequestEntityTooLarge,
		CodeIdempotencyConflict: http.StatusConflict,
		CodeNotReady:            http.StatusServiceUnavailable,
		CodeIdempotentReplay:    0,
		CodeExpired:             0,
	}
	for c, want := range tests {
		if got := c.HTTPStatus(); got != want {
			t.Errorf("%s: %d, want %d", c, got, want)
		}
	}
}
