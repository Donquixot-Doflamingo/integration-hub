package telemetry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const (
	callerTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpanID  = "00f067aa0ba902b7"
)

func TestHTTPHandlerContinuesCallerTraceparent(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp, err := NewTracerProvider(t.Context(), TraceConfig{Role: "gateway", SampleRatio: 0, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	var seen trace.SpanContext
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/integrations/{id}/invoke", func(w http.ResponseWriter, r *http.Request) {
		seen = trace.SpanContextFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := HTTPHandler(mux, tp, "gateway")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", nil)
	req.Header.Set("traceparent", "00-"+callerTraceID+"-"+callerSpanID+"-01")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen.TraceID().String() != callerTraceID {
		t.Fatalf("handler trace id = %s, want caller's %s", seen.TraceID(), callerTraceID)
	}
	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	s := spans[0]
	if s.Parent().SpanID().String() != callerSpanID {
		t.Errorf("parent span = %s, want %s", s.Parent().SpanID(), callerSpanID)
	}
	if s.SpanKind() != trace.SpanKindServer {
		t.Errorf("kind = %v, want server", s.SpanKind())
	}
	if got, want := s.Name(), "POST /v1/integrations/{id}/invoke"; got != want {
		t.Errorf("span name = %q, want route pattern %q", got, want)
	}
}

func TestTracingWithoutEndpointStartsNoExporter(t *testing.T) {
	tp, err := NewTracerProvider(t.Context(), TraceConfig{Role: "worker", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	// With no exporter there is nothing to flush; shutdown is immediate and must not fail.
	if err := tp.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	var _ trace.TracerProvider = (*sdktrace.TracerProvider)(nil)
}

func TestTraceparentRoundTripsIntoALink(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp, err := NewTracerProvider(t.Context(), TraceConfig{Role: "gateway", SampleRatio: 1, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	if got := Traceparent(t.Context()); got != "" {
		t.Fatalf("no span: traceparent %q, want empty", got)
	}
	ctx, span := tp.Tracer("t").Start(t.Context(), "accept")
	defer span.End()
	tpHdr := Traceparent(ctx)
	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01"
	if tpHdr != want {
		t.Fatalf("traceparent %q, want %q", tpHdr, want)
	}
	l, ok := LinkTo(tpHdr)
	if !ok || l.SpanContext.TraceID() != span.SpanContext().TraceID() || l.SpanContext.SpanID() != span.SpanContext().SpanID() || !l.SpanContext.IsSampled() {
		t.Fatalf("link %+v ok %v", l, ok)
	}
	for _, bad := range []string{"", "garbage", "00-00000000000000000000000000000000-0000000000000000-01"} {
		if _, ok := LinkTo(bad); ok {
			t.Errorf("LinkTo(%q) ok", bad)
		}
	}
}

// TestSamplerKeepsLinkedRootsSampled: with a 0 root ratio, a new root linked to a sampled span is
// sampled (the async hop), one linked to an unsampled span or unlinked is not, and a sampled
// parent is honoured.
func TestSamplerKeepsLinkedRootsSampled(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp, err := NewTracerProvider(t.Context(), TraceConfig{Role: "worker", SampleRatio: 0, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	sampled, _ := LinkTo("00-" + callerTraceID + "-" + callerSpanID + "-01")
	unsampled, _ := LinkTo("00-" + callerTraceID + "-" + callerSpanID + "-00")
	tr := tp.Tracer("t")
	tests := []struct {
		name string
		opts []trace.SpanStartOption
		want bool
	}{
		{"linked to a sampled span", []trace.SpanStartOption{trace.WithNewRoot(), trace.WithLinks(sampled)}, true},
		{"linked to an unsampled span", []trace.SpanStartOption{trace.WithNewRoot(), trace.WithLinks(unsampled)}, false},
		{"no link", []trace.SpanStartOption{trace.WithNewRoot()}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, span := tr.Start(t.Context(), "attempt", tt.opts...)
			span.End()
			if got := span.SpanContext().IsSampled(); got != tt.want {
				t.Fatalf("sampled = %v, want %v", got, tt.want)
			}
		})
	}
	parent := trace.ContextWithRemoteSpanContext(t.Context(), sampled.SpanContext)
	if _, span := tr.Start(parent, "child"); !span.SpanContext().IsSampled() {
		t.Fatal("a sampled parent was not honoured")
	}
}

// TestSpanAttributeViolations proves the allowlist check itself: an unknown key, a forbidden value
// in an attribute, an event or the status are all reported; allowlisted ids pass.
func TestSpanAttributeViolations(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp, err := NewTracerProvider(t.Context(), TraceConfig{Role: "gateway", SampleRatio: 1, SpanProcessor: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	tr := tp.Tracer("t")
	end := func(name string, f func(trace.Span)) {
		_, s := tr.Start(t.Context(), name)
		f(s)
		s.End()
	}
	end("clean", func(s trace.Span) {
		s.SetAttributes(attribute.String("hub.request_id", "req_1"), attribute.Int("hub.attempt", 1))
	})
	if v := SpanAttributeViolations(rec.Ended(), "s3cr3t-value"); len(v) != 0 {
		t.Fatalf("clean span reported: %v", v)
	}
	end("unknown key", func(s trace.Span) { s.SetAttributes(attribute.String("http.request.header.authorization", "x")) })
	end("forbidden value", func(s trace.Span) { s.SetAttributes(attribute.String("hub.endpoint", "a-s3cr3t-value-b")) })
	end("event", func(s trace.Span) {
		s.AddEvent("e", trace.WithAttributes(attribute.String("hub.outcome", "s3cr3t-value")))
	})
	end("status", func(s trace.Span) { s.SetStatus(codes.Error, "bad s3cr3t-value") })
	v := SpanAttributeViolations(rec.Ended(), "s3cr3t-value")
	for _, want := range []string{`unknown key: attribute key "http.request.header.authorization" is not allowlisted`,
		`forbidden value: attribute "hub.endpoint" carries forbidden value`, `event: event e "hub.outcome" carries forbidden value`,
		`status: name or status carries forbidden value`} {
		found := false
		for _, line := range v {
			found = found || strings.Contains(line, want)
		}
		if !found {
			t.Errorf("missing violation %q in %v", want, v)
		}
	}
}
