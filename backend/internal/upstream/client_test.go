package upstream

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// countingDialer counts dials and delegates to a real dialer (or blocks, for connect timeouts).
type countingDialer struct {
	dials     atomic.Int64
	blackhole bool
}

func (d *countingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.dials.Add(1)
	if d.blackhole { // a SYN that is never answered
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

type env struct {
	mv     *mockvendor.Instance
	client *Client
	dialer *countingDialer
	m      *telemetry.Metrics
	vendor *registry.Vendor
}

func newEnv(t *testing.T, opts Options) *env {
	t.Helper()
	mv := mockvendor.Start(t)
	d := &countingDialer{}
	if opts.Dialer == nil {
		opts.Dialer = d
	}
	m := telemetry.NewMetrics("test", "dev")
	c, err := New(m, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	v := &registry.Vendor{
		Vendor: "mock", Auth: "static_headers",
		Regions: map[string]string{"in": mv.VendorURL},
		Endpoints: map[string]registry.Endpoint{
			"upload@1": {Method: "POST", Path: "/1/upload", TimeoutMS: 3000, ConnectTimeoutMS: 500, Idempotent: true},
		},
	}
	return &env{mv: mv, client: c, dialer: d, m: m, vendor: v}
}

func (e *env) target(timeoutMS, connectMS int) Target {
	ep := e.vendor.Endpoints["upload@1"]
	if timeoutMS > 0 {
		ep.TimeoutMS = timeoutMS
	}
	if connectMS > 0 {
		ep.ConnectTimeoutMS = connectMS
	}
	return Target{AccountID: "acct-a", Region: "in", Vendor: e.vendor, EndpointName: "upload@1", Endpoint: ep, BaseURL: e.mv.VendorURL}
}

func (e *env) route(t *testing.T, b mockvendor.Behaviour) {
	t.Helper()
	e.mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "*", Path: "/**"}, Behaviour: b})
}

func uploadReq() core.VendorRequest {
	return core.VendorRequest{
		Method: "POST", Path: "/1/upload",
		Query:   map[string]string{"dry": "1"},
		Headers: map[string]string{"X-CleverTap-Account-Id": "TEST-ACCT-not-real", "traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", "Tracestate": "x=1"},
		Body:    map[string]any{"d": []any{map[string]any{"identity": "u_1", "type": "event"}}},
	}
}

func within(t *testing.T, got, want time.Duration, what string) {
	t.Helper()
	lo, hi := want-want/10, want+want/10
	if got < lo || got > hi {
		t.Fatalf("%s took %v, want %v ±10%% [%v, %v]", what, got, want, lo, hi)
	}
}

func TestDoSendsJSONAndParsesResponse(t *testing.T) {
	e := newEnv(t, Options{})
	e.route(t, mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)})
	resp, err := e.client.Do(t.Context(), e.target(0, 0), uploadReq())
	if err != nil {
		t.Fatal(err)
	}
	body, ok := resp.Body.(map[string]any)
	if resp.Status != 200 || !ok || body["status"] != "success" || body["processed"] != float64(1) {
		t.Fatalf("response = %+v", resp)
	}
	recs := e.mv.Requests(t, nil)
	if len(recs) != 1 {
		t.Fatalf("%d recorded requests", len(recs))
	}
	r := recs[0]
	var sent map[string]any
	if err := json.Unmarshal(r.Body, &sent); err != nil || r.Method != "POST" || r.Path != "/1/upload" || r.Query["dry"][0] != "1" {
		t.Fatalf("recorded %s %s %v %s (%v)", r.Method, r.Path, r.Query, r.Body, err)
	}
	hdr := http.Header(r.Headers)
	if hdr.Get("Content-Type") != "application/json" || hdr.Get("X-Clevertap-Account-Id") != "TEST-ACCT-not-real" {
		t.Fatalf("headers = %v", hdr)
	}
	if hdr.Get("Traceparent") != "" || hdr.Get("Tracestate") != "" {
		t.Fatalf("trace context forwarded to a vendor that did not opt in: %v", hdr)
	}
	if n := testutil.CollectAndCount(e.client.latency, "vendor_latency_ms"); n != 1 {
		t.Fatalf("vendor_latency_ms series = %d, want 1", n)
	}
}

func TestContentLengthIsSet(t *testing.T) {
	var gotLen int64
	var gotTE []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen, gotTE = r.ContentLength, r.TransferEncoding
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	e := newEnv(t, Options{})
	e.vendor.Regions["in"] = srv.URL
	tg := e.target(0, 0)
	tg.BaseURL = srv.URL
	req := uploadReq()
	want, _ := json.Marshal(req.Body)
	if _, err := e.client.Do(t.Context(), tg, req); err != nil {
		t.Fatal(err)
	}
	if gotLen != int64(len(want)) || len(gotTE) != 0 {
		t.Fatalf("Content-Length = %d (transfer-encoding %v), want %d", gotLen, gotTE, len(want))
	}
}

// TestEndpointTimeout: timeout_ms holds within ±10 % against a slow vendor and a hung one.
func TestEndpointTimeout(t *testing.T) {
	for _, b := range []mockvendor.Behaviour{
		{Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 3000}},
		{Down: &mockvendor.Down{Mode: "hang"}},
	} {
		e := newEnv(t, Options{})
		e.route(t, b)
		start := time.Now()
		_, err := e.client.Do(t.Context(), e.target(400, 0), uploadReq())
		within(t, time.Since(start), 400*time.Millisecond, "endpoint timeout")
		var te *TransportError
		if !errors.Is(err, ErrTimeout) || !errors.As(err, &te) || te.CallerDeadline {
			t.Fatalf("err = %v, want an endpoint timeout", err)
		}
	}
}

// TestCallerDeadlineBeatsEndpointTimeout: an X-Request-Deadline lower than timeout_ms wins.
func TestCallerDeadlineBeatsEndpointTimeout(t *testing.T) {
	e := newEnv(t, Options{})
	e.route(t, mockvendor.Behaviour{Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 3000}})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.client.Do(ctx, e.target(2000, 0), uploadReq())
	within(t, time.Since(start), 300*time.Millisecond, "caller deadline")
	var te *TransportError
	if !errors.As(err, &te) || te.Kind != KindTimeout || !te.CallerDeadline {
		t.Fatalf("err = %v, want a timeout at the caller deadline", err)
	}

	// A caller deadline above the endpoint timeout does not extend it.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel2()
	start = time.Now()
	_, err = e.client.Do(ctx2, e.target(300, 0), uploadReq())
	within(t, time.Since(start), 300*time.Millisecond, "endpoint timeout under a longer deadline")
	if !errors.As(err, &te) || te.Kind != KindTimeout || te.CallerDeadline {
		t.Fatalf("err = %v, want an endpoint timeout", err)
	}
}

// TestConnectTimeout: connect_timeout_ms holds within ±10 % when the SYN is never answered.
func TestConnectTimeout(t *testing.T) {
	d := &countingDialer{blackhole: true}
	e := newEnv(t, Options{Dialer: d})
	start := time.Now()
	_, err := e.client.Do(t.Context(), e.target(3000, 300), uploadReq())
	within(t, time.Since(start), 300*time.Millisecond, "connect timeout")
	var te *TransportError
	if !errors.As(err, &te) || te.Kind != KindTimeout || te.Phase != "connect" {
		t.Fatalf("err = %v, want a connect-phase timeout", err)
	}
	if d.dials.Load() != 1 {
		t.Fatalf("dials = %d", d.dials.Load())
	}
}

// TestUndeclaredHostRefusedBeforeDial: hosts outside the vendor's regions never reach the dialer.
func TestUndeclaredHostRefusedBeforeDial(t *testing.T) {
	e := newEnv(t, Options{})
	closed, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + closed.Addr().String()
	_ = closed.Close()
	tests := []struct{ name, base string }{
		{"undeclared port on a declared host", closedURL},
		{"undeclared host", "https://attacker.example.com"},
		{"declared host, other scheme port", strings.Replace(e.mv.VendorURL, "http://", "https://", 1)},
		{"unparsable", "://nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := e.target(0, 0)
			tg.BaseURL = tt.base
			_, err := e.client.Do(t.Context(), tg, uploadReq())
			var te *TransportError
			if !errors.Is(err, ErrEgressDenied) || !errors.As(err, &te) || te.Phase != "egress" {
				t.Fatalf("err = %v, want egress denied", err)
			}
		})
	}
	// Plain http is refused for a declared host that is not loopback.
	e.vendor.Regions["us"] = "http://vendor.example.com"
	tg := e.target(0, 0)
	tg.BaseURL = "http://vendor.example.com"
	if _, err := e.client.Do(t.Context(), tg, uploadReq()); !errors.Is(err, ErrEgressDenied) {
		t.Fatalf("plain http to a remote host: %v", err)
	}
	if n := e.dialer.dials.Load(); n != 0 {
		t.Fatalf("dialer called %d times for refused hosts", n)
	}
	if got := testutil.ToFloat64(e.client.egressDenied.WithLabelValues("mock")); got != 5 {
		t.Fatalf("upstream_egress_denied_total = %v, want 5", got)
	}
	if len(e.mv.Requests(t, nil)) != 0 {
		t.Fatal("mockvendor saw a request")
	}
}

// TestDialerChecksEgress: the dial functions refuse addresses outside the allowlist and dials
// without one (defence in depth for anything that bypasses the URL check).
func TestDialerChecksEgress(t *testing.T) {
	e := newEnv(t, Options{})
	ctx := context.WithValue(t.Context(), dialKey{}, &dialInfo{connectTimeout: time.Second, allowed: vendorHosts(e.vendor), vendor: "mock"})
	for name, dial := range map[string]func(context.Context, string, string) (net.Conn, error){"plain": e.client.dialPlain, "tls": e.client.dialTLS} {
		if _, err := dial(ctx, "tcp", "10.0.0.1:443"); !errors.Is(err, ErrEgressDenied) {
			t.Fatalf("%s: undeclared address: %v", name, err)
		}
		if _, err := dial(t.Context(), "tcp", strings.TrimPrefix(e.mv.VendorURL, "http://")); !errors.Is(err, ErrEgressDenied) {
			t.Fatalf("%s: dial without allowlist: %v", name, err)
		}
	}
	if n := e.dialer.dials.Load(); n != 0 {
		t.Fatalf("dialer called %d times", n)
	}
}

func TestTransportFailures(t *testing.T) {
	tests := []struct {
		name string
		b    mockvendor.Behaviour
		want error
	}{
		{"refused", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "refuse"}}, ErrConnect},
		{"reset", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "reset"}}, ErrReset},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Options{})
			e.route(t, tt.b)
			req := uploadReq()
			req.Query = map[string]string{"api_key": "query-secret-not-real"}
			_, err := e.client.Do(t.Context(), e.target(0, 0), req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "query-secret-not-real") || strings.Contains(err.Error(), "api_key") {
				t.Fatalf("error leaks the query string: %v", err)
			}
		})
	}
}

func TestCallerCancel(t *testing.T) {
	e := newEnv(t, Options{})
	e.route(t, mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "hang"}})
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	_, err := e.client.Do(ctx, e.target(0, 0), uploadReq())
	if !errors.Is(err, ErrCanceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestResponseBodies(t *testing.T) {
	tests := []struct {
		name          string
		b             mockvendor.Behaviour
		max           int64
		status        int
		wantString    string // "" = expect JSON map or nil
		wantNil       bool
		malformed     bool
		truncated     bool
		latencyOutcom string
	}{
		{name: "5xx is a response", b: mockvendor.Behaviour{Status: 503, Body: json.RawMessage(`{"status":"fail","error":"come back later"}`)}, status: 503, latencyOutcom: "5xx"},
		{name: "invalid json", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"a":1}`), Malformed: &mockvendor.Malformed{Kind: "invalid_json"}}, status: 200, wantString: `{"status": "success", processed: }`, malformed: true},
		{name: "truncated json", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","x":1}`), Malformed: &mockvendor.Malformed{Kind: "truncated_json"}}, status: 200, wantString: `{"status":"s`, malformed: true},
		{name: "html error page", b: mockvendor.Behaviour{Status: 502, Malformed: &mockvendor.Malformed{Kind: "html_error_page"}}, status: 502, wantString: "<!DOCTYPE html>"},
		{name: "json as text/plain stays text", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"a":1}`), Malformed: &mockvendor.Malformed{Kind: "wrong_content_type"}}, status: 200, wantString: `{"a":1}`},
		{name: "empty body", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"a":1}`), Malformed: &mockvendor.Malformed{Kind: "empty_body"}}, status: 200, wantNil: true},
		{name: "bounded read", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"padding":"0123456789012345678901234567890123456789"}`)}, max: 16, status: 200, wantString: `{"padding":"0123`, truncated: true},
		{name: "slow drip completes", b: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"a":1}`), SlowDrip: &mockvendor.SlowDrip{ChunkBytes: 2, IntervalMS: 20}}, status: 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, Options{MaxResponseBytes: tt.max})
			e.route(t, tt.b)
			a, err := e.client.Attempt(t.Context(), e.target(0, 0), uploadReq())
			if err != nil {
				t.Fatal(err)
			}
			if a.Response.Status != tt.status || a.MalformedBody != tt.malformed || a.Truncated != tt.truncated {
				t.Fatalf("attempt = %+v", a)
			}
			switch body := a.Response.Body.(type) {
			case string:
				if tt.wantString == "" || !strings.HasPrefix(body, tt.wantString) {
					t.Fatalf("body %q, want prefix %q", body, tt.wantString)
				}
			case nil:
				if !tt.wantNil {
					t.Fatal("nil body")
				}
			case map[string]any:
				if tt.wantString != "" || tt.wantNil {
					t.Fatalf("body parsed as JSON: %v", body)
				}
			default:
				t.Fatalf("body type %T", body)
			}
		})
	}
}

// TestPerAccountPoolsAndBulkhead: each account gets its own transport with MaxConnsPerHost =
// bulkhead, so a full account does not borrow connections from another.
func TestPerAccountPoolsAndBulkhead(t *testing.T) {
	e := newEnv(t, Options{Bulkhead: 1})
	e.route(t, mockvendor.Behaviour{Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 200}})
	a, b := e.client.pool("acct-a"), e.client.pool("acct-b")
	if a == b || a.Transport.(*http.Transport).MaxConnsPerHost != 1 || e.client.pool("acct-a") != a {
		t.Fatal("want one transport per account with MaxConnsPerHost = bulkhead")
	}
	tr := a.Transport.(*http.Transport)
	if tr.Proxy != nil || tr.DialTLSContext == nil {
		t.Fatal("transport must not use a proxy and must dial TLS itself")
	}
	var wg sync.WaitGroup
	durations := make([]time.Duration, 3)
	for i, acct := range []string{"acct-a", "acct-a", "acct-b"} {
		wg.Go(func() {
			tg := e.target(0, 0)
			tg.AccountID = acct
			start := time.Now()
			if _, err := e.client.Do(t.Context(), tg, uploadReq()); err != nil {
				t.Error(err)
			}
			durations[i] = time.Since(start)
		})
	}
	wg.Wait()
	slowest := max(durations[0], durations[1])
	if slowest < 380*time.Millisecond {
		t.Fatalf("two calls on a bulkhead of 1 finished in %v; the second should wait for the first", slowest)
	}
	if durations[2] > 350*time.Millisecond {
		t.Fatalf("other account waited on acct-a's bulkhead: %v", durations[2])
	}
	snap := &registry.Snapshot{Accounts: map[string]*registry.Account{"acct-b": {}}}
	e.client.Prune(snap)
	if _, ok := e.client.pools.Load("acct-a"); ok {
		t.Fatal("Prune kept a removed account")
	}
	if _, ok := e.client.pools.Load("acct-b"); !ok {
		t.Fatal("Prune dropped a live account")
	}
}

// tlsVendor is an https vendor that speaks exactly TLS minVersion..maxVersion (0 = Go default).
// MinVersion must be explicit for old versions: since Go 1.22 a server's default floor is TLS 1.2,
// so a server with only MaxVersion TLS 1.1 speaks nothing and would refuse every client, which
// proves nothing about ours. negotiated records the version each served request used.
func tlsVendor(t *testing.T, minVersion, maxVersion uint16) (srv *httptest.Server, negotiated *atomic.Uint32) {
	t.Helper()
	negotiated = &atomic.Uint32{}
	srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		negotiated.Store(uint32(r.TLS.Version))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	srv.TLS = &tls.Config{MinVersion: minVersion, MaxVersion: maxVersion}         // deliberately old TLS: the vendor under test
	srv.Config.ErrorLog = slog.NewLogLogger(slog.DiscardHandler, slog.LevelError) // expected handshake failures
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, negotiated
}

func trustedRoots(srv *httptest.Server) Options {
	return Options{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
}

// TestTLS is HUB-234's "TLS < 1.2 vendor handshake fails" (DESIGN §16.5, SECURITY.md §5): the
// real upstream client completes TLS 1.2 and 1.3 handshakes with verification, and refuses a
// vendor that offers only TLS 1.0 or 1.1 before any request is sent.
func TestTLS(t *testing.T) {
	tests := []struct {
		name          string
		min, max      uint16
		untrusted     bool
		wantErr       error
		wantVersion   uint16
		wantErrSubstr string
	}{
		{name: "TLS 1.3 vendor", wantVersion: tls.VersionTLS13},
		{name: "TLS 1.2-only vendor", min: tls.VersionTLS12, max: tls.VersionTLS12, wantVersion: tls.VersionTLS12},
		{name: "untrusted certificate", untrusted: true, wantErr: ErrConnect, wantErrSubstr: "certificate"},
		{name: "TLS 1.1-only vendor", min: tls.VersionTLS10, max: tls.VersionTLS11, wantErr: ErrConnect, wantErrSubstr: "protocol version"},
		{name: "TLS 1.0-only vendor", min: tls.VersionTLS10, max: tls.VersionTLS10, wantErr: ErrConnect, wantErrSubstr: "protocol version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, negotiated := tlsVendor(t, tt.min, tt.max)
			opts := trustedRoots(srv)
			if tt.untrusted {
				opts = Options{}
			}
			if tt.max != 0 && tt.max < tls.VersionTLS12 {
				// Positive control: the vendor really completes an old handshake with a client
				// that allows it, so the Hub client's failure below is its own floor at work.
				d := &tls.Dialer{Config: &tls.Config{RootCAs: opts.RootCAs, ServerName: "example.com", MinVersion: tls.VersionTLS10}}
				conn, err := d.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
				if err != nil {
					t.Fatalf("control: a TLS 1.0+ client cannot reach the old vendor: %v", err)
				}
				if v := conn.(*tls.Conn).ConnectionState().Version; v != tt.max {
					t.Fatalf("control negotiated %x, want %x", v, tt.max)
				}
				_ = conn.Close()
			}
			e := newEnv(t, opts)
			e.vendor.Regions["in"] = srv.URL
			tg := e.target(0, 0)
			tg.BaseURL = srv.URL
			resp, err := e.client.Do(t.Context(), tg, uploadReq())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil {
				if resp.Status != 200 {
					t.Fatalf("status %d", resp.Status)
				}
				if got := uint16(negotiated.Load()); got != tt.wantVersion { // TLS versions are 16-bit
					t.Fatalf("negotiated %x, want %x", got, tt.wantVersion)
				}
				return
			}
			var te *TransportError
			if !errors.As(err, &te) || te.Phase != "tls" || !strings.Contains(err.Error(), tt.wantErrSubstr) {
				t.Fatalf("err = %v, want a tls-phase failure mentioning %q", err, tt.wantErrSubstr)
			}
			if negotiated.Load() != 0 {
				t.Fatal("the vendor served a request over a refused handshake")
			}
		})
	}
}

// TestTLSFloorIsExplicit pins the client's own TLS policy instead of relying on Go's default
// client floor (which a GODEBUG or a future default could lower): TLS 1.2 minimum, verification on.
func TestTLSFloorIsExplicit(t *testing.T) {
	c, err := New(telemetry.NewMetrics("test", "dev"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if c.tlsBase.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2 (%x)", c.tlsBase.MinVersion, tls.VersionTLS12)
	}
	if c.tlsBase.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify is set")
	}
}

func TestSpanAndTraceparentOptIn(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	e := newEnv(t, Options{TracerProvider: tp, ForwardTraceparent: map[string]bool{"mock": true}})
	e.route(t, mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{}`)})
	ctx, parent := tp.Tracer("test").Start(t.Context(), "parent")
	if _, err := e.client.Do(ctx, e.target(0, 0), uploadReq()); err != nil {
		t.Fatal(err)
	}
	parent.End()
	hdr := http.Header(e.mv.Requests(t, nil)[0].Headers)
	tpHdr := hdr.Get("Traceparent")
	if tpHdr == "" || strings.Contains(tpHdr, "0af7651916cd43dd8448eb211c80319c") || hdr.Get("Tracestate") != "" {
		t.Fatalf("opted-in vendor: traceparent %q (must be the Hub's, not the transformer's), tracestate %q", tpHdr, hdr.Get("Tracestate"))
	}
	if !strings.Contains(tpHdr, parent.SpanContext().TraceID().String()) {
		t.Fatalf("traceparent %q is not in the caller's trace", tpHdr)
	}
	var found bool
	for _, s := range rec.Ended() {
		if s.Name() == "upstream mock.upload@1" {
			found = true
			for _, kv := range s.Attributes() {
				if strings.Contains(kv.Value.String(), "TEST-ACCT") {
					t.Fatalf("span attribute carries a header value: %v", kv)
				}
			}
		}
	}
	if !found {
		t.Fatal("no upstream client span")
	}
}

func TestTargetFor(t *testing.T) {
	v := &registry.Vendor{Vendor: "clevertap", Regions: map[string]string{"in": "https://in1.example.test"},
		Endpoints: map[string]registry.Endpoint{"upload@1": {Method: "POST", Path: "/1/upload", TimeoutMS: 3000}}}
	snap := &registry.Snapshot{
		Vendors: map[string]*registry.Vendor{"clevertap": v},
		Accounts: map[string]*registry.Account{
			"ct-a":  {Account: "ct-a", Vendor: "clevertap", Region: "in"},
			"ct-eu": {Account: "ct-eu", Vendor: "clevertap", Region: "eu"},
			"tw":    {Account: "tw", Vendor: "twilio", Region: "in"},
		},
	}
	tg, err := TargetFor(snap, "ct-a", "clevertap.upload@1")
	if err != nil || tg.BaseURL != "https://in1.example.test" || tg.EndpointName != "upload@1" || tg.Endpoint.Path != "/1/upload" || tg.Vendor != v || tg.Region != "in" {
		t.Fatalf("TargetFor = %+v, %v", tg, err)
	}
	for _, bad := range [][2]string{
		{"nobody", "clevertap.upload@1"}, {"tw", "clevertap.upload@1"}, {"ct-a", "clevertap.track@1"},
		{"ct-eu", "clevertap.upload@1"}, {"ct-a", "upload@1"}, {"ct-a", ".upload@1"},
	} {
		if _, err := TargetFor(snap, bad[0], bad[1]); !errors.Is(err, ErrNoTarget) {
			t.Errorf("TargetFor(%s, %s) err = %v", bad[0], bad[1], err)
		}
	}
}
