package transform

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// NaN and ±Inf cannot be JSON, and "+Inf" must never reach a header or query value: any of them in
// a transformer's output fails the call as TRANSFORM_FAILED before it leaves the Hub (HUB-447).
func TestRequestRejectsNonFiniteNumbers(t *testing.T) {
	tests := []struct {
		name, tpl string
		req       map[string]any
		wantPath  string
	}{
		{"body +Inf", "  body:\n    x: req.a / req.b\n", map[string]any{"a": 1.0, "b": 0.0}, "request.body.x"},
		{"body -Inf", "  body:\n    x: req.a / req.b\n", map[string]any{"a": -1.0, "b": 0.0}, "request.body.x"},
		{"body NaN", "  body:\n    x: req.a / req.b\n", map[string]any{"a": 0.0, "b": 0.0}, "request.body.x"},
		{"body nested in object", "  body:\n    o:\n      x: req.a / req.b\n", map[string]any{"a": 1.0, "b": 0.0}, "request.body.o.x"},
		{"body nested in array", "  body:\n    l:\n      - req.a / req.b\n", map[string]any{"a": 1.0, "b": 0.0}, "request.body.l"},
		{"body inside an expression result", "  body:\n    x: '{k: [req.a / req.b]}'\n", map[string]any{"a": 1.0, "b": 0.0}, "request.body.x"},
		{"header", "  headers:\n    X-Ratio: req.a / req.b\n", map[string]any{"a": 1.0, "b": 0.0}, "request.headers.X-Ratio"},
		{"query", "  query:\n    ratio: req.a / req.b\n", map[string]any{"a": 1.0, "b": 0.0}, "request.query.ratio"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := compileResponse(t, reqHead+tt.tpl)
			_, err := p.BuildRequest(Input{Req: tt.req, Now: fixedNow})
			var ee *EvalError
			if !errors.As(err, &ee) {
				t.Fatalf("want EvalError, got %v", err)
			}
			if !strings.Contains(ee.Error(), "NaN or Inf") {
				t.Fatalf("error %v", err)
			}
			if !strings.HasPrefix(ee.Path, tt.wantPath) {
				t.Fatalf("path %q, want prefix %q", ee.Path, tt.wantPath)
			}
		})
	}
}

func TestRequestFiniteNumbersStillPass(t *testing.T) {
	p := compileResponse(t, reqHead+"  headers:\n    X-Ratio: req.a / req.b\n  body:\n    x: req.a / req.b\n")
	req, err := p.BuildRequest(Input{Req: map[string]any{"a": 3.0, "b": 2.0}, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if req.Headers["X-Ratio"] != "1.5" || req.Body.(map[string]any)["x"] != 1.5 {
		t.Fatalf("got %#v %#v", req.Headers, req.Body)
	}
}

func TestResponseRejectsNonFiniteNumbers(t *testing.T) {
	tests := []struct{ name, src string }{
		{"response field", respHead + "response:\n  ratio: res.body.a / res.body.b\n"},
		{"response nested", respHead + "response:\n  o:\n    - res.body.a / res.body.b\n"},
		{"session", respHead + "response:\n  ok: 'true'\nsession:\n  r: res.body.a / res.body.b\n"},
		{"error message", respHead + "response:\n  ok: 'true'\nerrors:\n  - when: 'true'\n    code: UPSTREAM_REJECTED\n    message: res.body.a / res.body.b\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := compileResponse(t, tt.src)
			resp := vendor(200, map[string]any{"a": 1.0, "b": 0.0})
			var err error
			if tt.name == "session" {
				_, _, _, err = p.MapResponseSession(resp, Input{})
			} else {
				_, _, err = p.MapResponse(resp, Input{})
			}
			var ee *EvalError
			if !errors.As(err, &ee) || !strings.Contains(ee.Error(), "NaN or Inf") {
				t.Fatalf("want non-finite EvalError, got %v", err)
			}
		})
	}
}

func TestScalarStringRejectsNonFinite(t *testing.T) {
	for _, f := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
		if s, err := scalarString(f); err == nil {
			t.Errorf("scalarString(%v) = %q, want an error", f, s)
		}
	}
}
