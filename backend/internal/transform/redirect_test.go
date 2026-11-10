package transform

import (
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

const redirectTarget = "https://moved.example.net/v2/secret-path?token=abc"

// The upstream client never follows redirects, so a 3xx reaches the transformer. Unmatched, it
// is a failure of the attempt (HUB-449): UPSTREAM_REJECTED, not retryable (the same call gets
// the same redirect), and its message never carries the Location.
func TestUnmatchedRedirectIsRejected(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\n")
	for _, status := range []int{300, 301, 302, 303, 305, 307, 308} {
		out, ce, err := p.MapResponse(vendor(status, "<a href=\""+redirectTarget+"\">moved</a>", "Location", redirectTarget), Input{})
		if err != nil || ce == nil || out != nil {
			t.Fatalf("%d → out %v ce %+v err %v, want a canonical error", status, out, ce, err)
		}
		if ce.Code != core.CodeUpstreamRejected || ce.Retryable {
			t.Errorf("%d → %s retryable=%v, want UPSTREAM_REJECTED, not retryable", status, ce.Code, ce.Retryable)
		}
		if ce.Reason == nil || *ce.Reason != "upstream_redirect" {
			t.Errorf("%d → reason %v, want upstream_redirect", status, ce.Reason)
		}
		if strings.Contains(ce.Message, "moved.example") || strings.Contains(ce.Message, "http") || strings.Contains(ce.Message, "token") {
			t.Errorf("%d → message leaks the Location: %q", status, ce.Message)
		}
		if ce.Upstream == nil || ce.Upstream.Status == nil || *ce.Upstream.Status != status {
			t.Errorf("%d → upstream %+v", status, ce.Upstream)
		}
	}
}

// 304 answers a conditional request. The Hub sends no validators of its own, so a request
// transformer that sets If-None-Match / If-Modified-Since asked for it: 304 is not a redirect to
// fail and goes to the response expression, which can map it (res.status == 304).
func TestNotModifiedIsNotARedirectFailure(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  unchanged: res.status == 304\n")
	out, ce, err := p.MapResponse(vendor(304, nil), Input{})
	if err != nil || ce != nil {
		t.Fatalf("304 → %+v %v, want the response expression's output", ce, err)
	}
	if m, _ := out.(map[string]any); m["unchanged"] != true {
		t.Fatalf("304 output %v", out)
	}
}

// An explicit errors: rule matches before the default redirect classification.
func TestErrorRuleMatchesRedirectFirst(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\nerrors:\n"+
		"  - when: res.status == 301\n    code: UPSTREAM_UNAVAILABLE\n    retryable: true\n")
	_, ce, err := p.MapResponse(vendor(301, nil, "Location", redirectTarget), Input{})
	if err != nil || ce == nil || ce.Code != core.CodeUpstreamUnavailable || !ce.Retryable {
		t.Fatalf("301 with a rule → %+v %v, want the rule's code", ce, err)
	}
	_, ce, _ = p.MapResponse(vendor(302, nil), Input{})
	if ce == nil || ce.Code != core.CodeUpstreamRejected {
		t.Fatalf("302 without a rule → %+v, want the default", ce)
	}
}

func TestFailureStatus(t *testing.T) {
	for status, want := range map[int]bool{200: false, 204: false, 299: false, 300: true, 301: true, 304: false, 308: true, 399: true, 400: true, 500: true} {
		if got := IsFailureStatus(status); got != want {
			t.Errorf("IsFailureStatus(%d) = %v, want %v", status, got, want)
		}
	}
}
