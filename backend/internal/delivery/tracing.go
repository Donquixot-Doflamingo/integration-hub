package delivery

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// tracerName scopes the delivery spans (transform stages, worker attempt).
const tracerName = "github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"

// startTransform starts a transform stage span (hub.transform.request / hub.transform.response)
// under ctx's span, only when that span is recording: an unsampled request pays a context
// lookup and nothing else (docs/tech/OBSERVABILITY.md §5.1). nil means no span.
func startTransform(ctx context.Context, name string, ref registry.TransformerRef) trace.Span {
	parent := trace.SpanFromContext(ctx)
	if !parent.IsRecording() {
		return nil
	}
	_, span := parent.TracerProvider().Tracer(tracerName).Start(ctx, name,
		trace.WithAttributes(attribute.String("hub.transformer", ref.ID), attribute.Int("hub.transformer_version", ref.Version)))
	return span
}

// endTransform ends a transform span. The status carries only the error class: an engine error
// may quote input values, so its text never reaches a span.
func endTransform(span trace.Span, err error) {
	if span == nil {
		return
	}
	if err != nil {
		class := "transform_failed"
		if errors.Is(err, transform.ErrTimeBudget) {
			class = "time_budget"
		}
		span.SetStatus(codes.Error, class)
	}
	span.End()
}

// startAttempt starts hub.worker.attempt (consumer) for one claimed envelope: a new root per
// attempt, linked to the accepting trace stored in the envelope (DESIGN §11.3), so a retry hours
// later does not stretch the gateway's trace. The link-aware sampler (telemetry.Sampler) keeps
// the attempt sampled when the accepting trace was. Every later span of the attempt (transform,
// upstream, the callback enqueue) is its child.
func (w *Worker) startAttempt(ctx context.Context, c queue.Claimed) (context.Context, trace.Span) {
	if w.TracerProvider == nil {
		return ctx, noopSpan
	}
	env := c.Envelope
	opts := []trace.SpanStartOption{trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("hub.request_id", env.RequestID), attribute.String("hub.tenant", env.Tenant),
			attribute.String("hub.integration_id", env.IntegrationID), attribute.String("hub.pool", w.Pool),
			attribute.Int("hub.attempt", c.Attempt), attribute.Float64("hub.queue_wait_ms", telemetry.Millis(w.now().Sub(env.CreatedAt))),
		)}
	if l, ok := telemetry.LinkTo(env.Trace["traceparent"]); ok {
		opts = append(opts, trace.WithLinks(l))
	}
	return w.TracerProvider.Tracer(tracerName).Start(ctx, "hub.worker.attempt", opts...)
}

// endAttempt records the attempt's outcome (a bounded label: a state or a short reason) and
// marks a failed transition; the error text stays in the log.
func endAttempt(span trace.Span, err error, outcome string) {
	if !span.IsRecording() {
		return
	}
	span.SetAttributes(attribute.String("hub.outcome", strings.ReplaceAll(outcome, " ", "_")))
	if err != nil && !IsLost(err) {
		span.SetStatus(codes.Error, "transition_failed")
	}
}

var noopSpan = trace.SpanFromContext(context.Background())
