package telemetry

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TraceConfig configures tracing for one process.
type TraceConfig struct {
	Role        string
	Version     string
	Endpoint    string  // OTLP/gRPC endpoint; empty means no exporter is started
	SampleRatio float64 // head sampling for new traces; a sampled parent is always honoured
	Insecure    bool    // plaintext OTLP (local collector)
	// SpanProcessor, when set, is added in place of the OTLP exporter (tests use an in-memory one).
	SpanProcessor sdktrace.SpanProcessor
}

// Propagator is W3C trace context + baggage. Callers' traceparent is continued (DESIGN §16.4).
var Propagator propagation.TextMapPropagator = propagation.NewCompositeTextMapPropagator(
	propagation.TraceContext{}, propagation.Baggage{})

// NewTracerProvider builds the process tracer provider. Spans are always created (so trace ids
// reach logs and the audit trail), but nothing is exported unless Endpoint is set.
// Call Shutdown on the result during graceful shutdown.
func NewTracerProvider(ctx context.Context, cfg TraceConfig) (*sdktrace.TracerProvider, error) {
	res := resource.NewSchemaless(
		semconv.ServiceName("hub"),
		semconv.ServiceVersion(cfg.Version),
		attribute.String("hub.role", cfg.Role),
	)
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	}
	switch {
	case cfg.SpanProcessor != nil:
		opts = append(opts, sdktrace.WithSpanProcessor(cfg.SpanProcessor))
	case cfg.Endpoint != "":
		eopts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.Endpoint)}
		if cfg.Insecure {
			eopts = append(eopts, otlptracegrpc.WithInsecure())
		}
		exp, err := otlptracegrpc.New(ctx, eopts...)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	return sdktrace.NewTracerProvider(opts...), nil
}

// HTTPHandler wraps h so each request runs in a server span that continues an incoming
// traceparent. operation is the low-cardinality span name prefix (e.g. "gateway").
func HTTPHandler(h http.Handler, tp trace.TracerProvider, operation string) http.Handler {
	return otelhttp.NewHandler(h, operation,
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(Propagator),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern // route pattern, never the raw path
			}
			return r.Method
		}),
	)
}
