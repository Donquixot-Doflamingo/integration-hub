package delivery

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

func TestTransportErrors(t *testing.T) {
	cases := []struct {
		err    error
		code   core.Code
		reason string
	}{
		{&upstream.TransportError{Kind: upstream.KindTimeout}, core.CodeUpstreamTimeout, ""},
		{&upstream.TransportError{Kind: upstream.KindTimeout, CallerDeadline: true}, core.CodeUpstreamTimeout, "request_deadline"},
		{&upstream.TransportError{Kind: upstream.KindCanceled}, core.CodeUpstreamTimeout, "caller_canceled"},
		{&upstream.TransportError{Kind: upstream.KindConnect}, core.CodeUpstreamUnavailable, ""},
		{&upstream.TransportError{Kind: upstream.KindReset}, core.CodeUpstreamUnavailable, ""},
		{&upstream.TransportError{Kind: upstream.KindProtocol}, core.CodeUpstreamUnavailable, ""},
		{&upstream.TransportError{Kind: upstream.KindEgressDenied}, core.CodeUpstreamUnavailable, ""},
		{errors.New("anything else"), core.CodeUpstreamUnavailable, ""},
	}
	for _, tc := range cases {
		ce := TransportError(tc.err)
		reason := ""
		if ce.Reason != nil {
			reason = *ce.Reason
		}
		if ce.Code != tc.code || reason != tc.reason || !ce.Retryable || ce.Upstream == nil {
			t.Errorf("%v → %+v", tc.err, ce)
		}
	}
}

func TestRetrySync(t *testing.T) {
	to := &upstream.TransportError{Kind: upstream.KindTimeout}
	cases := []struct {
		err        error
		idempotent bool
		key        string
		want       bool
	}{
		{to, true, "", true},
		{to, false, "k", true},
		{to, false, "", false},
		{&upstream.TransportError{Kind: upstream.KindReset}, true, "", true},
		{&upstream.TransportError{Kind: upstream.KindTimeout, CallerDeadline: true}, true, "k", false},
		{&upstream.TransportError{Kind: upstream.KindConnect}, true, "k", false},
		{&upstream.TransportError{Kind: upstream.KindCanceled}, true, "k", false},
		{errors.New("plain"), true, "k", false},
	}
	for _, tc := range cases {
		if got := RetrySync(tc.err, tc.idempotent, tc.key); got != tc.want {
			t.Errorf("RetrySync(%v, %v, %q) = %v", tc.err, tc.idempotent, tc.key, got)
		}
	}
}

func TestEnrichVendorError(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", now.Add(4*time.Second).Format(http.TimeFormat))
	ce := &core.CanonicalError{Code: core.CodeUpstreamRateLimited, Upstream: &core.Upstream{}}
	EnrichVendorError(ce, core.VendorResponse{Status: 429, Headers: h}, now)
	if ce.RetryAfterMS == nil || *ce.RetryAfterMS != 4000 {
		t.Fatalf("HTTP-date Retry-After: %v", ce.RetryAfterMS)
	}
	pinned := &core.CanonicalError{Code: core.CodeUpstreamRateLimited, RetryAfterMS: core.Ptr[int64](10)}
	EnrichVendorError(pinned, core.VendorResponse{Headers: http.Header{"Retry-After": {"9"}}}, now)
	if *pinned.RetryAfterMS != 10 {
		t.Fatal("a rule's retry_after_ms wins over the header")
	}

	for body, want := range map[string]any{
		"top message":   map[string]any{"message": "bad name"},
		"error string":  map[string]any{"error": "bad name"},
		"error.message": map[string]any{"error": map[string]any{"message": "bad name"}},
		"errors[0]":     map[string]any{"errors": []any{map[string]any{"message": "bad name"}}},
	} {
		ce := &core.CanonicalError{Code: core.CodeUpstreamRejected, Message: defaultRejected}
		EnrichVendorError(ce, core.VendorResponse{Status: 400, Body: want}, now)
		if ce.Message != "bad name" {
			t.Errorf("%s: %q", body, ce.Message)
		}
	}
	ruled := &core.CanonicalError{Code: core.CodeUpstreamRejected, Message: "from the rule"}
	EnrichVendorError(ruled, core.VendorResponse{Body: map[string]any{"message": "vendor"}}, now)
	if ruled.Message != "from the rule" {
		t.Fatal("a rule's message wins")
	}
	coded := &core.CanonicalError{Code: core.CodeUpstreamRejected, Message: defaultRejected, Upstream: &core.Upstream{}}
	EnrichVendorError(coded, core.VendorResponse{Body: map[string]any{"error": map[string]any{"code": 512.0}}}, now)
	if coded.Upstream.Code == nil || *coded.Upstream.Code != "512" {
		t.Fatalf("vendor code %v", coded.Upstream.Code)
	}
}
