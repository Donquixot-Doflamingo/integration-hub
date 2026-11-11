package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Defaults (DESIGN §10.1, §10.8; TECH_SPEC §12.2).
const (
	DefaultTimeout          = 3000 * time.Millisecond
	DefaultConnectTimeout   = 500 * time.Millisecond
	DefaultBulkhead         = 64
	DefaultMaxResponseBytes = 4 << 20
)

// vendorLatencyBucketsMS are the go-observability buckets for vendor_latency_ms.
var vendorLatencyBucketsMS = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}

// ContextDialer opens TCP connections. *net.Dialer implements it; tests inject fakes.
type ContextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Options tunes a Client. Zero values take the documented defaults.
type Options struct {
	// Bulkhead caps connections per account and host (MaxConnsPerHost). Default 64.
	Bulkhead int
	// MaxIdleConnsPerHost is kept warm per account. Default 32.
	MaxIdleConnsPerHost int
	// IdleConnTimeout closes idle connections. Default 90 s.
	IdleConnTimeout time.Duration
	// MaxResponseBytes bounds the response body read; the rest is dropped and Attempt.Truncated
	// is set. Default 4 MiB.
	MaxResponseBytes int64
	// TracerProvider makes the client spans. Default: no-op.
	TracerProvider trace.TracerProvider
	// ForwardTraceparent lists vendors (by name) that accept a W3C traceparent. Default none:
	// no trace context leaves the Hub, and tracestate never does (OBSERVABILITY §5).
	ForwardTraceparent map[string]bool
	// Dialer opens TCP connections after the egress check. Default net.Dialer, keep-alive 30 s.
	Dialer ContextDialer
	// RootCAs overrides the system roots (tests). Verification is always on.
	RootCAs *x509.CertPool
	// UserAgent is sent unless the transformer set one. Default "integration-hub".
	UserAgent string
}

func (o *Options) withDefaults() {
	if o.Bulkhead <= 0 {
		o.Bulkhead = DefaultBulkhead
	}
	if o.MaxIdleConnsPerHost <= 0 {
		o.MaxIdleConnsPerHost = min(32, o.Bulkhead)
	}
	if o.IdleConnTimeout <= 0 {
		o.IdleConnTimeout = 90 * time.Second
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.TracerProvider == nil {
		o.TracerProvider = noop.NewTracerProvider()
	}
	if o.Dialer == nil {
		o.Dialer = &resolvingDialer{base: &net.Dialer{KeepAlive: 30 * time.Second}}
	}
	if o.UserAgent == "" {
		o.UserAgent = "integration-hub"
	}
}

// Client calls vendors (DESIGN §10.1, §10.8, §16.5): one HTTP pool per account, endpoint
// timeouts capped by the caller's deadline, TLS 1.2+ with verification, and egress only to
// hosts declared in the vendor's regions. It returns attempt facts; classification into
// canonical codes, retries, breaker and audit belong to the caller.
type Client struct {
	opts    Options
	tracer  trace.Tracer
	tlsBase *tls.Config

	pools sync.Map // account ID → *http.Client

	latency      *prometheus.HistogramVec
	egressDenied *prometheus.CounterVec
}

// New builds a client and registers vendor_latency_ms and upstream_egress_denied_total on
// m.Registry.
func New(m *telemetry.Metrics, opts Options) (*Client, error) {
	if m == nil {
		return nil, errors.New("upstream: New needs metrics")
	}
	opts.withDefaults()
	c := &Client{
		opts:   opts,
		tracer: opts.TracerProvider.Tracer("github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"),
		tlsBase: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    opts.RootCAs,
			NextProtos: []string{"h2", "http/1.1"},
		},
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:                           "vendor_latency_ms",
			Help:                           "Vendor call duration in ms, from send to end of body, by transport outcome or status class.",
			Buckets:                        vendorLatencyBucketsMS,
			NativeHistogramBucketFactor:    1.1,
			NativeHistogramMaxBucketNumber: 160,
		}, []string{"vendor", "account", "endpoint", "outcome"}),
		egressDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "upstream_egress_denied_total",
			Help: "Vendor calls refused before dialing because the host is not declared in the vendor's regions.",
		}, []string{"vendor"}),
	}
	for _, col := range []prometheus.Collector{c.latency, c.egressDenied} {
		if err := m.Registry.Register(col); err != nil {
			return nil, fmt.Errorf("upstream: register metrics: %w", err)
		}
	}
	return c, nil
}

// Attempt is what one vendor call produced.
type Attempt struct {
	Response core.VendorResponse
	// Latency is send to end of body (or to the failure).
	Latency time.Duration
	// Truncated means the body exceeded MaxResponseBytes; Response.Body holds the first
	// MaxResponseBytes as a string.
	Truncated bool
	// MalformedBody means the response declared JSON but did not parse; Body is the raw text.
	MalformedBody bool
	// Raw is the body as read (bounded by MaxResponseBytes): what pass-through returns unchanged
	// (ADR-0023). It shares the buffer Body was parsed from; nothing copies it.
	Raw []byte
}

// Do sends req to t and returns the vendor's response (any status) or a *TransportError.
func (c *Client) Do(ctx context.Context, t Target, req core.VendorRequest) (core.VendorResponse, error) {
	a, err := c.Attempt(ctx, t, req)
	return a.Response, err
}

type dialKey struct{}

// dialInfo travels to the dial functions in the request context (net/http keeps context
// values, not cancellation, when it dials).
type dialInfo struct {
	connectTimeout time.Duration
	allowed        allowedHosts
	vendor         string
}

// Attempt is Do with the attempt facts (latency, truncation, malformed body).
func (c *Client) Attempt(ctx context.Context, t Target, req core.VendorRequest) (Attempt, error) {
	if t.Vendor == nil {
		return Attempt{}, &TransportError{Kind: KindProtocol, Phase: "request", Err: errors.New("target has no vendor")}
	}
	vendor, endpoint := t.Vendor.Vendor, t.EndpointName
	ctx, span := c.tracer.Start(ctx, "upstream "+vendor+"."+endpoint, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("hub.vendor", vendor), attribute.String("hub.account", t.AccountID), attribute.String("hub.endpoint", endpoint)))
	defer span.End()

	fail := func(te *TransportError, latency time.Duration) (Attempt, error) {
		if te.Kind == KindEgressDenied {
			c.egressDenied.WithLabelValues(vendor).Inc()
		} else {
			c.latency.WithLabelValues(vendor, t.AccountID, endpoint, string(te.Kind)).Observe(telemetry.Millis(latency))
		}
		span.SetStatus(codes.Error, string(te.Kind))
		span.SetAttributes(attribute.String("hub.transport_error", string(te.Kind)))
		return Attempt{Latency: latency}, te
	}

	// Egress: the base URL must be one of the vendor's declared regions. Checked here, before
	// any dial, and again in the dialer for every connection.
	allowed := vendorHosts(t.Vendor)
	base, err := url.Parse(t.BaseURL)
	if err != nil {
		return fail(&TransportError{Kind: KindEgressDenied, Phase: "egress", Err: errors.New("base URL does not parse")}, 0)
	}
	hp, ok := allowed.allowsURL(base)
	if !ok {
		return fail(&TransportError{Kind: KindEgressDenied, Phase: "egress", Err: fmt.Errorf("%s://%s is not declared in the regions of vendor %s", base.Scheme, hp, vendor)}, 0)
	}
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		return fail(&TransportError{Kind: KindEgressDenied, Phase: "egress", Err: fmt.Errorf("plain http to %s: only https leaves the host", hp)}, 0)
	}
	span.SetAttributes(attribute.String("server.address", base.Hostname()))

	timeout := msOr(t.Endpoint.TimeoutMS, DefaultTimeout)
	connectTimeout := msOr(t.Endpoint.ConnectTimeoutMS, DefaultConnectTimeout)
	callerDeadline, hasDeadline := ctx.Deadline()
	callerCapped := hasDeadline && time.Until(callerDeadline) < timeout
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	attemptCtx = context.WithValue(attemptCtx, dialKey{}, &dialInfo{connectTimeout: connectTimeout, allowed: allowed, vendor: vendor})

	hreq, err := c.newRequest(attemptCtx, t, base, req)
	if err != nil {
		return fail(&TransportError{Kind: KindProtocol, Phase: "request", Err: err}, 0)
	}
	span.SetAttributes(attribute.String("http.request.method", hreq.Method))

	start := time.Now()
	resp, err := c.pool(t.AccountID).Do(hreq)
	if err != nil {
		return fail(classify(err, "request", attemptCtx, ctx, callerCapped), time.Since(start))
	}
	defer func() { _ = resp.Body.Close() }()
	body, truncated, err := readBounded(resp.Body, c.opts.MaxResponseBytes)
	latency := time.Since(start)
	if err != nil {
		return fail(classify(err, "response", attemptCtx, ctx, callerCapped), latency)
	}
	parsed, malformed := parseBody(resp.Header.Get("Content-Type"), body, truncated)

	c.latency.WithLabelValues(vendor, t.AccountID, endpoint, statusClass(resp.StatusCode)).Observe(telemetry.Millis(latency))
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= 500 {
		span.SetStatus(codes.Error, statusClass(resp.StatusCode))
	}
	return Attempt{
		Response:  core.VendorResponse{Status: resp.StatusCode, Headers: resp.Header, Body: parsed},
		Latency:   latency,
		Truncated: truncated, MalformedBody: malformed, Raw: body,
	}, nil
}

func msOr(ms int, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}

func (c *Client) newRequest(ctx context.Context, t Target, base *url.URL, req core.VendorRequest) (*http.Request, error) {
	method := req.Method
	if method == "" {
		method = t.Endpoint.Method
	}
	if method == "" {
		return nil, errors.New("no method")
	}
	path := req.Path
	if path == "" {
		path = t.Endpoint.Path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + path
	u.RawPath = ""
	if len(req.Query) > 0 {
		q := make(url.Values, len(req.Query))
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	var body io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
		body = bytes.NewReader(b) // sets ContentLength and GetBody
	}
	hreq, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range req.Headers {
		if isTraceHeader(k) {
			continue // trace context is the client's decision, not the transformer's
		}
		hreq.Header.Set(k, v)
	}
	if body != nil && hreq.Header.Get("Content-Type") == "" {
		hreq.Header.Set("Content-Type", "application/json")
	}
	if hreq.Header.Get("User-Agent") == "" {
		hreq.Header.Set("User-Agent", c.opts.UserAgent)
	}
	if c.opts.ForwardTraceparent[t.Vendor.Vendor] {
		propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(hreq.Header))
		hreq.Header.Del("Tracestate")
	}
	return hreq, nil
}

func isTraceHeader(k string) bool {
	return strings.EqualFold(k, "traceparent") || strings.EqualFold(k, "tracestate")
}

// pool returns the account's client, creating it on first use.
func (c *Client) pool(accountID string) *http.Client {
	if p, ok := c.pools.Load(accountID); ok {
		return p.(*http.Client)
	}
	tr := &http.Transport{
		Proxy:                 nil, // never an env proxy: it would bypass the egress allowlist
		DialContext:           c.dialPlain,
		DialTLSContext:        c.dialTLS,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       c.opts.Bulkhead,
		MaxIdleConns:          c.opts.MaxIdleConnsPerHost * 4,
		MaxIdleConnsPerHost:   c.opts.MaxIdleConnsPerHost,
		IdleConnTimeout:       c.opts.IdleConnTimeout,
		ExpectContinueTimeout: time.Second,
		ResponseHeaderTimeout: 0, // the attempt context bounds the whole call
	}
	hc := &http.Client{
		Transport:     tr,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // a redirect is the vendor's answer, never followed
	}
	p, loaded := c.pools.LoadOrStore(accountID, hc)
	if loaded {
		tr.CloseIdleConnections()
	}
	return p.(*http.Client)
}

// Prune closes the pools of accounts that are no longer in snap. Call it after a swap.
func (c *Client) Prune(snap *registry.Snapshot) {
	c.pools.Range(func(k, v any) bool {
		if _, ok := snap.Accounts[k.(string)]; !ok {
			c.pools.Delete(k)
			v.(*http.Client).CloseIdleConnections()
		}
		return true
	})
}

// Close closes every pool's idle connections. In-flight calls finish on their own contexts.
func (c *Client) Close() {
	c.pools.Range(func(k, v any) bool {
		c.pools.Delete(k)
		v.(*http.Client).CloseIdleConnections()
		return true
	})
}

// dialTCP checks egress and opens a TCP connection within the endpoint's connect timeout.
func (c *Client) dialTCP(ctx context.Context, network, addr string) (net.Conn, context.Context, context.CancelFunc, error) {
	di, _ := ctx.Value(dialKey{}).(*dialInfo)
	if di == nil {
		return nil, nil, nil, &TransportError{Kind: KindEgressDenied, Phase: "egress", Err: errors.New("dial without an egress allowlist")}
	}
	if !di.allowed.allows(addr) {
		return nil, nil, nil, &TransportError{Kind: KindEgressDenied, Phase: "egress", Err: fmt.Errorf("host %s is not declared in the regions of vendor %s", addr, di.vendor)}
	}
	dctx, cancel := context.WithTimeout(ctx, di.connectTimeout)
	conn, err := c.opts.Dialer.DialContext(dctx, network, addr)
	if err != nil {
		timedOut := errors.Is(dctx.Err(), context.DeadlineExceeded)
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			timedOut = true
		}
		cancel()
		return nil, nil, nil, &dialError{phase: "connect", timeout: timedOut, err: err}
	}
	return conn, dctx, cancel, nil
}

func (c *Client) dialPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, _, cancel, err := c.dialTCP(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	cancel()
	return conn, nil
}

// dialTLS dials and completes the TLS handshake inside the same connect timeout.
func (c *Client) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, dctx, cancel, err := c.dialTCP(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	defer cancel()
	host, _, _ := net.SplitHostPort(addr)
	cfg := c.tlsBase.Clone()
	cfg.ServerName = host
	tc := tls.Client(raw, cfg)
	if err := tc.HandshakeContext(dctx); err != nil {
		_ = raw.Close()
		return nil, &dialError{phase: "tls", timeout: errors.Is(dctx.Err(), context.DeadlineExceeded), err: err}
	}
	return tc, nil
}

// readBounded reads at most limit bytes and reports whether more were available.
func readBounded(r io.Reader, limit int64) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > limit {
		return b[:limit], true, nil
	}
	return b, false, nil
}

// parseBody returns the JSON tree when the response declares JSON (or declares nothing) and
// parses; otherwise the raw text. malformed is set when JSON was declared but a complete body
// did not parse (a truncated body is reported as truncated, not malformed).
func parseBody(contentType string, b []byte, truncated bool) (any, bool) {
	if len(b) == 0 {
		return nil, false
	}
	declaredJSON := false
	if contentType != "" {
		mt, _, err := mime.ParseMediaType(contentType)
		declaredJSON = err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
		if !declaredJSON {
			return string(b), false
		}
	}
	if !truncated {
		var v any
		if err := json.Unmarshal(b, &v); err == nil {
			return v, false
		}
	}
	return string(b), declaredJSON && !truncated
}

func statusClass(status int) string {
	if status < 100 || status > 599 {
		return "other"
	}
	return strconv.Itoa(status/100) + "xx"
}
