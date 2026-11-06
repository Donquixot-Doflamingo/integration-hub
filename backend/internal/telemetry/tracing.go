package telemetry

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

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
		sdktrace.WithSampler(Sampler(cfg.SampleRatio)),
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

// Sampler is parent-based with a link-aware root: a new root span (the worker's attempt, a
// callback delivery) is sampled when any of its links is sampled, so an async request sampled
// at the gateway stays sampled across the outbox hop; other roots use ratio.
func Sampler(ratio float64) sdktrace.Sampler {
	return sdktrace.ParentBased(linkSampler{ratio: sdktrace.TraceIDRatioBased(ratio)})
}

type linkSampler struct{ ratio sdktrace.Sampler }

func (s linkSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, l := range p.Links {
		if l.SpanContext.IsSampled() {
			return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample,
				Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState()}
		}
	}
	return s.ratio.ShouldSample(p)
}

func (s linkSampler) Description() string { return "LinkSampled{" + s.ratio.Description() + "}" }

// Traceparent is the W3C traceparent of ctx's span, "" when ctx has none. It is what an envelope
// or a callback row stores so the next hop can link back (DESIGN §11.3). tracestate is never
// stored.
func Traceparent(ctx context.Context) string {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ""
	}
	c := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, c)
	return c["traceparent"]
}

// LinkTo parses a stored traceparent (W3C version 00 layout) into a span link to that remote
// span; false when it is empty or invalid.
func LinkTo(traceparent string) (trace.Link, bool) {
	parts := strings.Split(traceparent, "-")
	if len(parts) != 4 || len(parts[0]) != 2 || parts[0] == "ff" || len(parts[3]) != 2 {
		return trace.Link{}, false
	}
	tid, err := trace.TraceIDFromHex(parts[1])
	if err != nil {
		return trace.Link{}, false
	}
	sid, err := trace.SpanIDFromHex(parts[2])
	if err != nil {
		return trace.Link{}, false
	}
	flags, err := hex.DecodeString(parts[3])
	if err != nil {
		return trace.Link{}, false
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid,
		TraceFlags: trace.TraceFlags(flags[0]) & trace.FlagsSampled, Remote: true})
	return trace.Link{SpanContext: sc}, sc.IsValid()
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
