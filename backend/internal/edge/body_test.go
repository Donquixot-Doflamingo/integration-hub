package edge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

type unknownLengthReader struct{ r *strings.Reader }

func (u unknownLengthReader) Read(p []byte) (int, error) { return u.r.Read(p) }

func TestBodyLimits(t *testing.T) {
	big := `{"x":"` + strings.Repeat("a", 2048) + `"}`
	// Declared too large: refused before reading.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(big))
	if _, _, ce := readJSONBody(req, 1024); ce == nil || ce.Code != core.CodePayloadTooLarge {
		t.Fatalf("declared length: %+v", ce)
	}
	// Unknown length (chunked): capped while reading.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", unknownLengthReader{strings.NewReader(big)})
	req.ContentLength = -1
	if _, _, ce := readJSONBody(req, 1024); ce == nil || ce.Code != core.CodePayloadTooLarge {
		t.Fatalf("streamed: %+v", ce)
	}
}

// sizedTrackBody is a valid events.track body of exactly n bytes (padding in a property).
func sizedTrackBody(t *testing.T, n int) string {
	t.Helper()
	const prefix = `{"user": {"id": "u_48213"}, "event": {"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": {"pad": "`
	const suffix = `"}}}`
	pad := n - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("size %d too small", n)
	}
	return prefix + strings.Repeat("a", pad) + suffix
}

// TestBodyLimitsAtTheEdge (HUB-234, DESIGN §16.5): through the real handler, a body up to the
// limit passes the body stage (it is validated; what happens after is not the limit's business)
// and one byte more is 413 PAYLOAD_TOO_LARGE before validation, resolution or any vendor call:
// 1 MB, declared or chunked.
func TestBodyLimitsAtTheEdge(t *testing.T) {
	const path = "/v1/integrations/int_evt_brandA/invoke"
	cases := []struct {
		name    string
		snap    *registry.Snapshot
		size    int
		chunked bool
		refused bool
	}{
		{"default: exactly 1 MB", devSnapshot(t), DefaultBodyLimit, false, false},
		{"default: 1 MB + 1", devSnapshot(t), DefaultBodyLimit + 1, false, true},
		{"default: 1 MB + 1, chunked", devSnapshot(t), DefaultBodyLimit + 1, true, true},
		{"default: exactly 1 MB, chunked", devSnapshot(t), DefaultBodyLimit, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUpstream{steps: []step{ok200()}}
			h, sink := invokeServer(t, tc.snap, up)
			body := sizedTrackBody(t, tc.size)
			var rec *httptest.ResponseRecorder
			if tc.chunked {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, unknownLengthReader{strings.NewReader(body)})
				req.ContentLength = -1
				req.Header.Set("Authorization", "Bearer "+devKey)
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				h.ServeHTTP(rec, req)
			} else {
				rec = post(t, h, devKey, path, body)
			}
			events := types(sink.only(t))
			if !tc.refused {
				if rec.Code == http.StatusRequestEntityTooLarge || !strings.Contains(events, "request.validated") {
					t.Fatalf("a body within the limit was refused: %d %.300s (events %s)", rec.Code, rec.Body, events)
				}
				return
			}
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status %d, want 413: %.300s", rec.Code, rec.Body)
			}
			if code := errorCode(t, rec.Body.String())["code"]; code != string(core.CodePayloadTooLarge) {
				t.Fatalf("code %v, want %s", code, core.CodePayloadTooLarge)
			}
			if up.calls() != 0 {
				t.Fatalf("an oversized body reached the vendor %d times", up.calls())
			}
			if events != "request.received,request.failed" {
				t.Fatalf("events %s", events)
			}
		})
	}
}

func TestBodyMustBeOneJSONObject(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{"array", `[1,2]`, false},
		{"second value", `{"a":1}{"b":2}`, false},
		{"not json", `nope`, false},
		{"null", `null`, false},
		{"empty", ``, false},
		{"whitespace only", " \n ", false},
		{"trailing bracket", `{"a":1}]`, false},
		{"trailing brace", `{"a":1} }`, false},
		{"trailing comma", `{"a":1},`, false},
		{"trailing word", `{"a":1} x`, false},
		{"trailing whitespace", "{\"a\":1} \r\n\t ", true},
		{"leading whitespace", " {\"a\":1}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(tt.body))
			_, _, ce := readJSONBody(req, 1024)
			if tt.ok {
				if ce != nil {
					t.Fatalf("%q refused: %+v", tt.body, ce)
				}
				return
			}
			if ce == nil || ce.Code != core.CodeValidationFailed || ce.Reason == nil || *ce.Reason != "schema" {
				t.Fatalf("%q accepted or wrong error: %+v", tt.body, ce)
			}
		})
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"a": 1.5}`))
	body, raw, ce := readJSONBody(req, DefaultBodyLimit)
	if ce != nil || body["a"] != 1.5 || string(raw) != `{"a": 1.5}` {
		t.Fatalf("decoded %v %q %+v", body, raw, ce)
	}
}

func TestValidationListsJSONPointers(t *testing.T) {
	op := devSnapshot(t).Integrations["int_evt_brandA"].Active().Op("default")
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"user": {}, "event": {"name": "1bad", "timestamp": "yesterday"}, "extra": true}`), &body)
	ce := validate(op, body, http.Header{})
	if ce == nil || ce.Code != core.CodeValidationFailed || ce.Reason == nil || *ce.Reason != "schema" {
		t.Fatalf("%+v", ce)
	}
	var ptrs []string
	for _, d := range ce.Details {
		ptrs = append(ptrs, d.Pointer)
	}
	for _, want := range []string{"/user/id", "/event/name", "/event/timestamp", ""} {
		if !slices.Contains(ptrs, want) {
			t.Errorf("no detail at %q in %v", want, ce.Details)
		}
	}
	body = nil // Unmarshal merges into a non-nil map
	_ = json.Unmarshal([]byte(`{"user": {"id": "u_1"}, "event": {"name": "order_placed"}}`), &body)
	if ce := validate(op, body, http.Header{}); ce != nil {
		t.Fatalf("valid body rejected: %+v", ce)
	}
}

// Declared caller headers are checked too (ADR-0023): a violation has no pointer (the pointer is
// into the body) and names the header.
func TestValidationChecksDeclaredHeaders(t *testing.T) {
	op := &registry.Operation{Name: "default", RequestSchema: registry.RequestSchema{
		Body:    registry.NewJSONSchema(map[string]any{"type": "object"}),
		Headers: registry.NewJSONSchema(map[string]any{"type": "object", "required": []any{"X-Request-Source"}, "properties": map[string]any{"X-Request-Source": map[string]any{"enum": []any{"web", "app"}}}}),
	}}
	if err := registry.CompileOperation(op, "default"); err != nil {
		t.Fatal(err)
	}
	ce := validate(op, map[string]any{}, http.Header{})
	if ce == nil || len(ce.Details) != 1 || ce.Details[0].Pointer != "" || !strings.Contains(ce.Details[0].Message, "header X-Request-Source") {
		t.Fatalf("missing header: %+v", ce)
	}
	h := http.Header{}
	h.Set("x-request-source", "tv")
	if ce := validate(op, map[string]any{}, h); ce == nil || !strings.Contains(ce.Details[0].Message, "header X-Request-Source") {
		t.Fatalf("bad header value: %+v", ce)
	}
	h.Set("X-Request-Source", "web")
	if ce := validate(op, map[string]any{}, h); ce != nil {
		t.Fatalf("valid header rejected: %+v", ce)
	}
	if got := callerHeaders(op, h); len(got) != 1 || got["X-Request-Source"] != "web" {
		t.Fatalf("hdr.* %v", got)
	}
	h.Set("Idempotency-Key", "k1")
	h.Set("X-Undeclared", "secret")
	if got := callerHeaders(op, h); len(got) != 2 || got["idempotency_key"] != "k1" || got["X-Undeclared"] != nil {
		t.Fatalf("hdr.* must hold only declared headers and idempotency_key: %v", got)
	}
}

// BenchmarkValidate4KB: schema validation ≤ 0.8 ms p99 (DESIGN §18.6).
func BenchmarkValidate4KB(b *testing.B) {
	op := devSnapshot(b).Integrations["int_evt_brandA"].Active().Op("default")
	props := map[string]any{}
	for i := range 60 {
		props[fmt.Sprintf("property_%02d", i)] = fmt.Sprintf("value-%02d-abcdefghijklmnopqrstuvwxyz0123456789", i)
	}
	raw, _ := json.Marshal(map[string]any{"user": map[string]any{"id": "u_1"}, "event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": props}})
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	durations := make([]time.Duration, 0, 1<<16)
	b.ReportAllocs()
	for b.Loop() {
		start := time.Now()
		if ce := validate(op, body, http.Header{}); ce != nil {
			b.Fatal(ce)
		}
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	p99 := durations[len(durations)*99/100]
	b.ReportMetric(float64(p99.Microseconds()), "p99-µs")
	if p99 > 800*time.Microsecond {
		b.Errorf("p99 %s over 0.8 ms", p99)
	}
}
