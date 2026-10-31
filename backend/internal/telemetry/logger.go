package telemetry

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// NewLogger builds the process logger: JSON lines, request fields from the context, and
// value-based secret redaction last before encoding (go-logging skill). role is attached to
// every line.
func NewLogger(w io.Writer, level slog.Leveler, secrets *SecretSet, role string) *slog.Logger {
	jh := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(EnrichHandler{next: NewRedactHandler(jh, secrets)}).With(
		slog.String("service", "hub"), slog.String("role", role))
}

// RequestFields are the per-request correlation fields every log line on the request path
// carries (OBSERVABILITY.md, logging schema).
type RequestFields struct {
	RequestID          string
	Tenant             string
	IntegrationID      string
	IntegrationVersion int
	Attempt            int
}

type fieldsKey struct{}

// WithRequestFields returns ctx carrying f for EnrichHandler.
func WithRequestFields(ctx context.Context, f RequestFields) context.Context {
	return context.WithValue(ctx, fieldsKey{}, f)
}

// RequestFieldsFrom returns the fields stored in ctx, if any.
func RequestFieldsFrom(ctx context.Context) (RequestFields, bool) {
	f, ok := ctx.Value(fieldsKey{}).(RequestFields)
	return f, ok
}

// EnrichHandler adds RequestFields and the active trace and span ids from the context to each record. Use the *Context slog
// methods (enforced by sloglint) so the fields attach.
type EnrichHandler struct{ next slog.Handler }

// NewEnrichHandler wraps next.
func NewEnrichHandler(next slog.Handler) EnrichHandler { return EnrichHandler{next: next} }

// Enabled reports whether next handles level.
func (h EnrichHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

// WithAttrs delegates to next.
func (h EnrichHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return EnrichHandler{h.next.WithAttrs(as)}
}

// WithGroup delegates to next.
func (h EnrichHandler) WithGroup(n string) slog.Handler { return EnrichHandler{h.next.WithGroup(n)} }

// Handle adds the request fields and passes the record on.
func (h EnrichHandler) Handle(ctx context.Context, r slog.Record) error {
	if f, ok := RequestFieldsFrom(ctx); ok {
		r = r.Clone() // never mutate the caller's record storage
		if f.RequestID != "" {
			r.AddAttrs(slog.String("request_id", f.RequestID))
		}
		if f.Tenant != "" {
			r.AddAttrs(slog.String("tenant", f.Tenant))
		}
		if f.IntegrationID != "" {
			r.AddAttrs(slog.String("integration_id", f.IntegrationID), slog.Int("integration_version", f.IntegrationVersion))
		}
		if f.Attempt > 0 {
			r.AddAttrs(slog.Int("attempt", f.Attempt))
		}
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		if _, cloned := RequestFieldsFrom(ctx); !cloned {
			r = r.Clone()
		}
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.next.Handle(ctx, r)
}
