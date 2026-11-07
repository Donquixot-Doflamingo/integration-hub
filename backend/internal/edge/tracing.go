package edge

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// tracerName scopes the edge's own spans (hub.queue.enqueue).
const tracerName = "github.com/Donquixot-Doflamingo/integration-hub/backend/internal/edge"

// The server span itself comes from telemetry.HTTPHandler (otelhttp) around the edge; the edge
// only names and tags it. Every helper returns at once when the span is not recording, so an
// unsampled request pays a context lookup and nothing else (DESIGN §18.5). Attributes are ids
// and enums only, never a body or a header value (docs/tech/OBSERVABILITY.md §5.1).

// tagRoute names the server span after the route pattern (the edge's mux sets r.Pattern on its
// own request copy, which otelhttp never sees) and records the request id.
func tagRoute(r *http.Request) {
	span := trace.SpanFromContext(r.Context())
	if !span.IsRecording() {
		return
	}
	span.SetName(r.Pattern)
	span.SetAttributes(attribute.String("http.route", r.Pattern), attribute.String("hub.request_id", RequestID(r.Context())))
}

// tagTenant records the authenticated tenant.
func tagTenant(ctx context.Context, tenant string) {
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String("hub.tenant", tenant))
	}
}

// tagIntegration records the integration, its delivery mode and the resolved version.
func tagIntegration(ctx context.Context, p *prepared) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() || p == nil {
		return
	}
	span.SetAttributes(attribute.String("hub.integration_id", p.integration.Integration),
		attribute.String("hub.mode", string(p.integration.Active().Delivery.Mode)))
	if p.res.VersionNumber > 0 {
		span.SetAttributes(attribute.Int("hub.integration_version", p.res.VersionNumber))
	}
}

// tagCode records the canonical error code of a failed request.
func tagCode(ctx context.Context, code string) {
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetAttributes(attribute.String("hub.code", code))
	}
}

// startEnqueue starts hub.queue.enqueue (producer) under the edge span. The envelope stores
// this span's context, so the worker's attempt links back to the accepting trace (DESIGN §11.3).
// Without a tracer provider upstream of the edge the span is a no-op and nothing is stored.
func startEnqueue(ctx context.Context) (context.Context, trace.Span) {
	return trace.SpanFromContext(ctx).TracerProvider().Tracer(tracerName).Start(ctx, "hub.queue.enqueue",
		trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attribute.String("hub.pool", queue.PoolMessaging)))
}

// endEnqueue ends the enqueue span, marking a failed persist (the error text stays in the log).
func endEnqueue(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "persist_failed")
	}
	span.End()
}

// traceID is the trace id recorded on the requests row (admin request explorer), "" without one.
func traceID(ctx context.Context) string {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		return sc.TraceID().String()
	}
	return ""
}

// envelopeTrace is the envelope's trace field: the enqueue span's traceparent.
func envelopeTrace(ctx context.Context) map[string]string {
	if tp := telemetry.Traceparent(ctx); tp != "" {
		return map[string]string{"traceparent": tp}
	}
	return nil
}
