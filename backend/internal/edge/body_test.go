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
	if bodyLimit("llm.chat@v1") != LLMBodyLimit || bodyLimit("events.track@v1") != DefaultBodyLimit {
		t.Fatal("limits per capability")
	}
}

func TestBodyMustBeOneJSONObject(t *testing.T) {
	for _, b := range []string{`[1,2]`, `{"a":1}{"b":2}`, `nope`, `null`, ``} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(b))
		if _, _, ce := readJSONBody(req, 1024); ce == nil || ce.Code != core.CodeValidationFailed {
			t.Errorf("%q accepted: %+v", b, ce)
		}
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(`{"a": 1.5}`))
	body, raw, ce := readJSONBody(req, bodyLimit("events.track@v1"))
	if ce != nil || body["a"] != 1.5 || string(raw) != `{"a": 1.5}` {
		t.Fatalf("decoded %v %q %+v", body, raw, ce)
	}
}

func TestValidationListsJSONPointers(t *testing.T) {
	op := devSnapshot(t).Capabilities["events.track@v1"].Operations["default"]
	var body map[string]any
	_ = json.Unmarshal([]byte(`{"user": {}, "event": {"name": "1bad", "timestamp": "yesterday"}, "extra": true}`), &body)
	ce := validate(op, body)
	if ce == nil || ce.Code != core.CodeValidationFailed || ce.Reason == nil || *ce.Reason != "schema" {
		t.Fatalf("%+v", ce)
	}
	var ptrs []string
	for _, d := range ce.Details {
		ptrs = append(ptrs, d.Pointer)
	}
	for _, want := range []string{"/user", "/event/name", "/event/timestamp", ""} {
		if !slices.Contains(ptrs, want) {
			t.Errorf("no detail at %q in %v", want, ce.Details)
		}
	}
	body = nil // Unmarshal merges into a non-nil map
	_ = json.Unmarshal([]byte(`{"user": {"id": "u_1"}, "event": {"name": "order_placed"}}`), &body)
	if ce := validate(op, body); ce != nil {
		t.Fatalf("valid body rejected: %+v", ce)
	}
}

// BenchmarkValidate4KB: schema validation ≤ 0.8 ms p99 (DESIGN §18.6).
func BenchmarkValidate4KB(b *testing.B) {
	op := devSnapshot(b).Capabilities["events.track@v1"].Operations["default"]
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
		if ce := validate(op, body); ce != nil {
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
