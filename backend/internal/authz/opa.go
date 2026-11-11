package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Defaults of Config.
const (
	DefaultPackage = "integrationhub" // client id "integration-hub" with '-' removed
	DefaultTimeout = 200 * time.Millisecond
	maxBody        = 1 << 20
)

// PackageOf is the Rego package of an ISFC client id: the id with '-' removed, the rule the
// sidecar middleware uses to build /v1/data/<package>/decision.
func PackageOf(clientID string) string { return strings.ReplaceAll(clientID, "-", "") }

// SpanName is the span around one OPA call (OBSERVABILITY: admin.authz.opa).
const SpanName = "admin.authz.opa"

// Decision labels of hub_admin_authz_duration_ms.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
	DecisionError = "error"
)

// ErrUnavailable means OPA could not decide (timeout, transport, status, malformed or missing
// decision). The request must be refused (fail closed) and may be retried.
var ErrUnavailable = errors.New("authz: policy engine unavailable")

// APIKey is the caller's x-isfc-api-key. It marshals as itself (OPA must see it to match the
// bundle's allowed_api_keys) and prints as *** everywhere else.
type APIKey string

// String implements fmt.Stringer.
func (APIKey) String() string { return "***" }

// GoString implements fmt.GoStringer.
func (APIKey) GoString() string { return `"***"` }

// LogValue implements slog.LogValuer.
func (APIKey) LogValue() slog.Value { return slog.StringValue("***") }

// Input is the sidecar-compatible OPA input (go-commons/sidecar-middleware OPAInput).
type Input struct {
	// Sub is the subject: id, type, and every key of the decoded x-isfc-sub-attrs.
	Sub    map[string]any `json:"sub"`
	HTTP   HTTP           `json:"http"`
	Client Caller         `json:"client"`
	// Tenant is the x-isfc-tenant-id; nil (omitted) when the header is absent.
	Tenant *Tenant `json:"tenant,omitempty"`
}

// HTTP is the request as the service sees it (after the gateway's prefix rewrite).
type HTTP struct {
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	QueryParams map[string]any `json:"queryParams"`
	Metadata    map[string]any `json:"metadata"`
}

// Caller is the OPA input's client: x-isfc-client-id and x-isfc-api-key.
type Caller struct {
	APIKey   APIKey `json:"apiKey,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// Tenant is the tenant the request acts in.
type Tenant struct {
	ID string `json:"id"`
}

// Decision is the answer of the policy's `decision` rule.
type Decision struct {
	Allow  bool
	Reason string
	// Meta is the matching rule's details (required permissions, and for API keys the key's own
	// permissions as `key_permissions`).
	Meta map[string]any
}

// Config builds a Client.
type Config struct {
	// URL is the OPA base URL, e.g. http://localhost:8181 (HUB_OPA_URL).
	URL string
	// Package is the Rego package of the bundle (default integrationhub).
	Package string
	// Timeout bounds one decision (HUB_OPA_TIMEOUT, default 200 ms).
	Timeout    time.Duration
	HTTPClient *http.Client
	Metrics    *Metrics
	Tracer     trace.Tracer
}

// Client asks OPA for decisions. It is safe for concurrent use.
type Client struct {
	endpoint string
	timeout  time.Duration
	http     *http.Client
	metrics  *Metrics
	tracer   trace.Tracer
}

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("authz: HUB_OPA_URL must be an http(s) URL, got %q", cfg.URL)
	}
	pkg := cfg.Package
	if pkg == "" {
		pkg = DefaultPackage
	}
	if strings.ContainsAny(pkg, "/?# ") {
		return nil, fmt.Errorf("authz: bad package %q", pkg)
	}
	c := &Client{
		endpoint: strings.TrimSuffix(cfg.URL, "/") + "/v1/data/" + pkg + "/decision",
		timeout:  cfg.Timeout, http: cfg.HTTPClient, metrics: cfg.Metrics, tracer: cfg.Tracer,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.http == nil {
		c.http = &http.Client{}
	}
	if c.tracer == nil {
		c.tracer = noop.NewTracerProvider().Tracer("authz")
	}
	return c, nil
}

// Decide asks OPA whether in is allowed. Every call is timed into hub_admin_authz_duration_ms
// and traced as admin.authz.opa. The input is never logged here (it carries the API key).
func (c *Client) Decide(ctx context.Context, in Input) (d Decision, err error) {
	ctx, span := c.tracer.Start(ctx, SpanName)
	start := time.Now()
	defer func() {
		label := DecisionError
		switch {
		case err == nil && d.Allow:
			label = DecisionAllow
		case err == nil:
			label = DecisionDeny
		default:
			span.RecordError(err)
			span.SetStatus(codes.Error, "opa unavailable")
		}
		span.SetAttributes(attribute.String("authz.decision", label))
		span.End()
		c.metrics.observe(label, time.Since(start))
	}()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if in.HTTP.QueryParams == nil {
		in.HTTP.QueryParams = map[string]any{}
	}
	if in.HTTP.Metadata == nil {
		in.HTTP.Metadata = map[string]any{}
	}
	body, err := json.Marshal(struct {
		Input Input `json:"input"`
	}{in})
	if err != nil {
		return Decision{}, fmt.Errorf("%w: encode input: %w", ErrUnavailable, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: %w", ErrUnavailable, scrub(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Decision{}, fmt.Errorf("%w: read answer: %w", ErrUnavailable, scrub(err))
	}
	if resp.StatusCode != http.StatusOK {
		return Decision{}, fmt.Errorf("%w: OPA answered %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Result *struct {
			Allow  bool           `json:"allow"`
			Reason string         `json:"reason"`
			Meta   map[string]any `json:"meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Decision{}, fmt.Errorf("%w: malformed answer", ErrUnavailable)
	}
	if out.Result == nil { // the package is not loaded (no bundle yet): OPA answers {}
		return Decision{}, fmt.Errorf("%w: no decision in the answer (policy bundle not loaded?)", ErrUnavailable)
	}
	return Decision{Allow: out.Result.Allow, Reason: out.Result.Reason, Meta: out.Result.Meta}, nil
}

// scrub drops the request URL from transport errors (it has no secret, but the message is logged).
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Decider is what the admin server needs from the policy engine (Client, Disabled, test fakes).
type Decider interface {
	Decide(ctx context.Context, in Input) (Decision, error)
}

// Disabled is a stand-in that allows everything (HUB_OPA_DISABLED=true, HUB_ENV=test only).
type Disabled struct{}

// Decide implements the decision call without asking anything.
func (Disabled) Decide(context.Context, Input) (Decision, error) {
	return Decision{Allow: true, Reason: "OPA disabled"}, nil
}

// CheckDisabled refuses HUB_OPA_DISABLED=true unless HUB_ENV=test: authorization may never be
// switched off in a real deployment.
func CheckDisabled(disabled bool, env string) error {
	if disabled && env != "test" {
		return errors.New("HUB_OPA_DISABLED=true is refused unless HUB_ENV=test")
	}
	return nil
}

// Metrics is hub_admin_authz_duration_ms{decision}.
type Metrics struct{ duration *prometheus.HistogramVec }

// authzBucketsMS covers a local sidecar (sub-millisecond) up to the default 200 ms timeout.
var authzBucketsMS = []float64{0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100, 200, 500}

// NewMetrics registers the histogram on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:                           "hub_admin_authz_duration_ms",
		Help:                           "Duration of one admin authorization decision (OPA call) in ms, by decision (allow, deny, error).",
		Buckets:                        authzBucketsMS,
		NativeHistogramBucketFactor:    1.1,
		NativeHistogramMaxBucketNumber: 160,
	}, []string{"decision"})}
	for _, d := range []string{DecisionAllow, DecisionDeny, DecisionError} {
		m.duration.WithLabelValues(d)
	}
	reg.MustRegister(m.duration)
	return m
}

func (m *Metrics) observe(decision string, d time.Duration) {
	if m != nil {
		m.duration.WithLabelValues(decision).Observe(float64(d) / float64(time.Millisecond))
	}
}
