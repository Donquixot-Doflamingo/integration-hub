package transform

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

func responseValidator(t *testing.T) func(any) error {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/schemas/capabilities/events.track.v1.response.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("resp.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("resp.json")
	if err != nil {
		t.Fatal(err)
	}
	return func(v any) error { return sch.Validate(normalizeForSchema(v)) }
}

// normalizeForSchema converts Go ints to float64, as JSON decoding would.
func normalizeForSchema(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeForSchema(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeForSchema(e)
		}
		return out
	}
	return v
}

func compileResponse(t *testing.T, src string) *Program {
	t.Helper()
	p, err := Compile(specFromYAML(t, src), Options{ValidateResponse: responseValidator(t)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func devResponse(t *testing.T, rel string) *Program {
	t.Helper()
	p, err := Compile(devSpec(t, rel), Options{ValidateResponse: responseValidator(t)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func vendor(status int, body any, headers ...string) core.VendorResponse {
	h := http.Header{}
	for i := 0; i+1 < len(headers); i += 2 {
		h.Set(headers[i], headers[i+1])
	}
	return core.VendorResponse{Status: status, Headers: h, Body: body}
}

func TestR1MapsSuccessAndPartial(t *testing.T) {
	p := devResponse(t, "R1/2.yaml")
	out, ce, err := p.MapResponse(vendor(200, map[string]any{"status": "success", "processed": 1.0, "unprocessed": []any{}}, "X-Request-Id", "vr-1"), Input{})
	if err != nil || ce != nil {
		t.Fatalf("%v %v", ce, err)
	}
	m := out.(map[string]any)
	if m["status"] != "ok" || m["accepted"] != 1.0 || m["vendor_ref"] != "vr-1" {
		t.Fatalf("out %v", m)
	}
	out, _, err = p.MapResponse(vendor(200, map[string]any{"processed": 0.0, "unprocessed": []any{map[string]any{"index": 0.0, "reason": "bad"}}}), Input{})
	if err != nil || out.(map[string]any)["status"] != "partial" {
		t.Fatalf("partial: %v %v", out, err)
	}
}

func TestR2IsMinimal(t *testing.T) {
	out, _, err := devResponse(t, "R2/1.yaml").MapResponse(vendor(200, map[string]any{"processed": 1.0, "unprocessed": []any{}}, "x-request-id", "vr-2"), Input{})
	if err != nil || !equalJSON(out, map[string]any{"status": "ok", "vendor_ref": "vr-2"}) {
		t.Fatalf("out %v err %v", out, err)
	}
}

func TestR1ErrorRules(t *testing.T) {
	p := devResponse(t, "R1/2.yaml")
	tests := []struct {
		resp       core.VendorResponse
		code       core.Code
		retryable  bool
		retryAfter int64
	}{
		{vendor(401, nil), core.CodeUpstreamAuthFailed, false, -1},
		{vendor(403, map[string]any{}), core.CodeUpstreamAuthFailed, false, -1},
		{vendor(429, nil, "Retry-After", "7"), core.CodeUpstreamRateLimited, true, 7000},
		{vendor(429, nil), core.CodeUpstreamRateLimited, true, 1000},
		{vendor(503, "<html>"), core.CodeUpstreamUnavailable, true, -1},
		{vendor(400, map[string]any{"error": "bad"}), core.CodeUpstreamRejected, false, -1},
	}
	for _, tt := range tests {
		_, ce, err := p.MapResponse(tt.resp, Input{})
		if err != nil || ce == nil || ce.Code != tt.code || ce.Retryable != tt.retryable || *ce.Upstream.Status != tt.resp.Status {
			t.Errorf("status %d: %+v %v", tt.resp.Status, ce, err)
			continue
		}
		if (tt.retryAfter < 0) != (ce.RetryAfterMS == nil) || (ce.RetryAfterMS != nil && *ce.RetryAfterMS != tt.retryAfter) {
			t.Errorf("status %d: retry_after_ms %v, want %d", tt.resp.Status, ce.RetryAfterMS, tt.retryAfter)
		}
		if strings.Contains(strings.ToLower(ce.Message), "clevertap") {
			t.Errorf("message names the vendor: %q", ce.Message)
		}
	}
}

func TestFirstMatchingRuleWins(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\nerrors:\n"+
		"  - when: res.status >= 400\n    code: UPSTREAM_REJECTED\n"+
		"  - when: res.status == 429\n    code: UPSTREAM_RATE_LIMITED\n    retryable: true\n"+
		"  - when: res.status == 200 && res.body.error != nil\n    code: UPSTREAM_REJECTED\n    message: res.body.error\n")
	if _, ce, _ := p.MapResponse(vendor(429, nil), Input{}); ce == nil || ce.Code != core.CodeUpstreamRejected {
		t.Fatalf("429 → %+v, want the first (broader) rule", ce)
	}
	if _, ce, _ := p.MapResponse(vendor(200, map[string]any{"error": "quota exhausted"}), Input{}); ce == nil || ce.Message != "quota exhausted" {
		t.Fatalf("200 with error body → %+v", ce)
	}
}

func TestUnmatchedErrorStatusIsClassified(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\n")
	for status, code := range map[int]core.Code{500: core.CodeUpstreamUnavailable, 429: core.CodeUpstreamRateLimited, 401: core.CodeUpstreamAuthFailed, 404: core.CodeUpstreamRejected} {
		if _, ce, err := p.MapResponse(vendor(status, nil), Input{}); err != nil || ce == nil || ce.Code != code {
			t.Errorf("%d → %+v %v, want %s", status, ce, err, code)
		}
	}
}

func TestResponseMustMatchCapabilitySchema(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"great\"'\n")
	_, _, err := p.MapResponse(vendor(200, nil), Input{})
	var ee *EvalError
	if !errors.As(err, &ee) || !strings.Contains(err.Error(), "does not match the capability response schema") {
		t.Fatalf("err %v", err)
	}
}

func TestThrowingExpressionIsTransformFailed(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\n  accepted: int(res.body.processed)\n")
	_, _, err := p.MapResponse(vendor(200, map[string]any{"processed": "lots"}), Input{})
	var ee *EvalError
	if !errors.As(err, &ee) || ee.Path != "response.accepted" || ee.Expr != "int(res.body.processed)" {
		t.Fatalf("err %#v", err)
	}
	if strings.Contains(err.Error(), "lots") {
		t.Fatalf("EvalError leaks a value: %v", err)
	}
	if !strings.Contains(ee.Detail(), "lots") {
		t.Fatalf("Detail() should keep the engine message for fixture debugging: %q", ee.Detail())
	}
}

func TestWhenMustBeBoolean(t *testing.T) {
	p := compileResponse(t, respHead+"response:\n  status: '\"ok\"'\nerrors:\n  - when: res.status\n    code: UPSTREAM_REJECTED\n")
	if _, _, err := p.MapResponse(vendor(200, nil), Input{}); err == nil || !strings.Contains(err.Error(), "must evaluate to true or false") {
		t.Fatalf("err %v", err)
	}
}
