package delivery

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// TestTransformSpans: a transform stage span exists only under a recording span, carries the
// transformer id and version, and a failure sets only an error class as status (never the engine
// text, which may quote input values). Unsampled, it costs no allocation (DESIGN §18.5).
func TestTransformSpans(t *testing.T) {
	ref := registry.TransformerRef{ID: "T1", Version: 7}
	unsampled := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}}))
	for name, ctx := range map[string]context.Context{"no span": context.Background(), "unsampled": unsampled} {
		if n := testing.AllocsPerRun(100, func() {
			endTransform(startTransform(ctx, "hub.transform.request", ref), nil)
		}); n != 0 {
			t.Errorf("%s: %v allocs", name, n)
		}
		if s := startTransform(ctx, "hub.transform.request", ref); s != nil {
			t.Errorf("%s: a span was started", name)
		}
	}

	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, parent := tp.Tracer("t").Start(context.Background(), "attempt")
	tests := []struct {
		err        error
		wantStatus string
	}{
		{nil, ""},
		{errors.New(`eval req.user.phone = "+919800000000": type mismatch`), "transform_failed"},
		{transform.ErrTimeBudget, "time_budget"},
	}
	for _, tt := range tests {
		endTransform(startTransform(ctx, "hub.transform.request", ref), tt.err)
	}
	parent.End()
	spans := rec.Ended()
	if len(spans) != len(tests)+1 {
		t.Fatalf("%d spans", len(spans))
	}
	for i, tt := range tests {
		s := spans[i]
		if s.Parent().SpanID() != parent.SpanContext().SpanID() || s.Status().Description != tt.wantStatus {
			t.Errorf("span %d: parent %s status %q, want %q", i, s.Parent().SpanID(), s.Status().Description, tt.wantStatus)
		}
		got := map[string]string{}
		for _, kv := range s.Attributes() {
			got[string(kv.Key)] = kv.Value.String()
		}
		if got["hub.transformer"] != "T1" || got["hub.transformer_version"] != "7" || len(got) != 2 {
			t.Errorf("span %d attributes %v", i, got)
		}
	}
}
