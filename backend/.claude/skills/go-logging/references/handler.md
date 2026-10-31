# Handler chain for internal/telemetry

All APIs below are stdlib `log/slog`, `strings`, `sync/atomic`, and `go.opentelemetry.io/otel/trace`.

## Secret set (replace by value)
```go
const minSecretLen = 6

type SecretSet struct{ r atomic.Pointer[strings.Replacer] }

// Update replaces the whole set. Call on credential load/rotation, before serving.
func (s *SecretSet) Update(values []string) {
	vs := slices.DeleteFunc(slices.Clone(values), func(v string) bool { return len(v) < minSecretLen })
	slices.SortFunc(vs, func(a, b string) int { return cmp.Compare(len(b), len(a)) }) // longest first
	vs = slices.Compact(vs)
	pairs := make([]string, 0, 2*len(vs))
	for _, v := range vs {
		pairs = append(pairs, v, "***")
	}
	s.r.Store(strings.NewReplacer(pairs...))
}

func (s *SecretSet) Redact(str string) string {
	if r := s.r.Load(); r != nil {
		return r.Replace(str)
	}
	return str
}
```
Also register derived forms that can appear in text: `base64(user:pass)` for basic auth, `Bearer <token>` is covered by the token itself.

## Redacting handler
```go
var deniedKeys = map[string]bool{ // lookup table, read-only
	"authorization": true, "password": true, "passcode": true, "api_key": true,
	"secret": true, "token": true, "cookie": true, "set_cookie": true,
}

type RedactHandler struct {
	next    slog.Handler
	secrets *SecretSet
}

func NewRedactHandler(next slog.Handler, s *SecretSet) *RedactHandler {
	return &RedactHandler{next: next, secrets: s}
}

func (h *RedactHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *RedactHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, h.secrets.Redact(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *RedactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(as))
	for i, a := range as {
		red[i] = h.redactAttr(a)
	}
	return &RedactHandler{next: h.next.WithAttrs(red), secrets: h.secrets}
}

func (h *RedactHandler) WithGroup(name string) slog.Handler {
	return &RedactHandler{next: h.next.WithGroup(name), secrets: h.secrets}
}

func (h *RedactHandler) redactAttr(a slog.Attr) slog.Attr {
	if deniedKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "***")
	}
	v := a.Value.Resolve() // runs LogValuer
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.secrets.Redact(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = h.redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, h.secrets.Redact(x.Error()))
		case []byte:
			return slog.String(a.Key, h.secrets.Redact(string(x)))
		default: // stringify so nothing escapes through the JSON encoder unredacted
			return slog.String(a.Key, h.secrets.Redact(fmt.Sprint(x)))
		}
	default:
		return slog.Attr{Key: a.Key, Value: v}
	}
}
```
Caveat: attrs attached with `logger.With(...)` are redacted against the secret set *at that moment*. Build long-lived loggers after credentials are warmed, and never `With` a value that might later become a secret.

## Enrich handler (ctx fields + trace ids)
```go
type RequestFields struct {
	RequestID          string
	Tenant             string
	IntegrationID      string
	IntegrationVersion int
	Attempt            int
}

type fieldsKey struct{}

func WithRequestFields(ctx context.Context, f RequestFields) context.Context {
	return context.WithValue(ctx, fieldsKey{}, f)
}

type EnrichHandler struct{ next slog.Handler }

func (h EnrichHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }
func (h EnrichHandler) WithAttrs(as []slog.Attr) slog.Handler          { return EnrichHandler{h.next.WithAttrs(as)} }
func (h EnrichHandler) WithGroup(n string) slog.Handler                { return EnrichHandler{h.next.WithGroup(n)} }

func (h EnrichHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone() // records share attr storage between copies; never mutate the caller's
	if f, ok := ctx.Value(fieldsKey{}).(RequestFields); ok {
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
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.next.Handle(ctx, r)
}
```
Note: if a `WithGroup` is active, these attrs land inside the group. Keep groups rare (we use flat keys) or add the enrich handler beneath any grouping.

## Construction in main
```go
func NewLogger(w io.Writer, level *slog.LevelVar, secrets *SecretSet, service, role string) *slog.Logger {
	jh := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	h := EnrichHandler{next: NewRedactHandler(jh, secrets)}
	return slog.New(h).With(slog.String("service", service), slog.String("role", role))
}
// per component, once, in the constructor:
log = log.With(slog.String("component", "delivery.worker"))
```

## Tests
```go
func TestRedactHandler(t *testing.T) {
	t.Parallel()
	const secret = "fixture-canary-9f2c41"
	tests := []struct {
		name string
		log  func(l *slog.Logger)
	}{
		{"message", func(l *slog.Logger) { l.Info("token " + secret) }},
		{"string attr", func(l *slog.Logger) { l.Info("x", slog.String("h", "Bearer "+secret)) }},
		{"group", func(l *slog.Logger) { l.Info("x", slog.Group("g", slog.String("v", secret))) }},
		{"error", func(l *slog.Logger) { l.Info("x", slog.Any("error", fmt.Errorf("auth %s: %w", secret, io.EOF))) }},
		{"bytes", func(l *slog.Logger) { l.Info("x", slog.Any("b", []byte(secret))) }},
		{"with", func(l *slog.Logger) { l.With(slog.String("k", secret)).Info("x") }},
		{"denied key", func(l *slog.Logger) { l.Info("x", slog.String("Authorization", "anything")) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			s := &SecretSet{}
			s.Update([]string{secret})
			tt.log(NewLogger(&buf, new(slog.LevelVar), s, "hub", "test"))
			assert.NotContains(t, buf.String(), secret)
			assert.NotContains(t, buf.String(), "anything")
		})
	}
}
```
