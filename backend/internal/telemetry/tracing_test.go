package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
