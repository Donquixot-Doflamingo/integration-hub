package callbacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

const (
	secretActive = "cb-test-secret-active-0123456789" //nolint:gosec // G101: fake test secret
	secretNext   = "cb-test-secret-next-9876543210"   //nolint:gosec // G101: fake test secret
	piiMarker    = "PII-MARKER-7f3a"
)

// fakeStore serves queued rows to claim and records outcomes.
type fakeStore struct {
	mu      sync.Mutex
	queue   []delivery
	records map[string]outcome
	lose    bool
	maxN    int
	claims  int
}

func (f *fakeStore) claim(_ context.Context, _ string, n int, _ time.Duration) ([]delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	f.maxN = max(f.maxN, n)
	k := min(n, len(f.queue))
	out := f.queue[:k]
	f.queue = f.queue[k:]
	return out, nil
}

func (f *fakeStore) record(_ context.Context, id, _ string, o outcome) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lose {
		return false, nil
	}
	if f.records == nil {
		f.records = map[string]outcome{}
	}
	f.records[id] = o
	return true, nil
}

func (f *fakeStore) sample(context.Context) (time.Duration, int64, error) {
	return 3 * time.Second, 2, nil
}

func (f *fakeStore) outcome(id string) (outcome, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, ok := f.records[id]
	return o, ok
}

func (f *fakeStore) recorded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

type fakeSecrets map[string][][]byte

func (f fakeSecrets) SigningSecrets(_ context.Context, ref string) ([][]byte, error) {
	if s, ok := f[ref]; ok {
		return s, nil
	}
	return nil, errors.New("not in cache")
}

// captureSink collects audit batches.
type captureSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *captureSink) Emit(b audit.Batch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, b.Events...)
}

func (c *captureSink) all() []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]audit.Event(nil), c.events...)
}

// receiver is a test tenant endpoint that verifies every callback with the receiver's secret.
type receiver struct {
	srv      *httptest.Server
	status   atomic.Int64
	hits     atomic.Int64
	verified atomic.Int64
	mu       sync.Mutex
	headers  []http.Header
	bodies   [][]byte
	secrets  [][]byte
}

// first returns the headers and body of the first request.
func (r *receiver) first() (http.Header, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers[0], r.bodies[0]
}

func newReceiver(t *testing.T, secrets ...string) *receiver {
	t.Helper()
	r := &receiver{}
	for _, s := range secrets {
		r.secrets = append(r.secrets, []byte(s))
	}
	r.status.Store(http.StatusNoContent)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		body, _ := io.ReadAll(req.Body)
		if err := Verify(req.Header.Get(SignatureHeader), body, r.secrets, time.Now(), 0); err == nil {
			r.verified.Add(1)
		}
		r.mu.Lock()
		r.headers = append(r.headers, req.Header.Clone())
		r.bodies = append(r.bodies, body)
		r.mu.Unlock()
		if req.URL.Path == "/moved" {
			t.Error("redirect followed")
		}
		st := int(r.status.Load())
		if st == http.StatusFound {
			http.Redirect(w, req, "/moved", st)
			return
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

type dispatchFixture struct {
	d       *Dispatcher
	store   *fakeStore
	sink    *captureSink
	logs    *bytes.Buffer
	metrics *telemetry.Metrics
	rcv     *receiver
}

func dispatcherSnapshot(url string) *registry.Snapshot {
	s := testSnapshot(url)
	s.Registrar["reg_private"] = &registry.RegistrarEndpoint{EndpointID: "reg_private", Tenant: "orders-service", URL: "https://10.0.0.1/cb", SecretRef: "localfile://reg_orders_cb"}
	s.Registrar["reg_nosecret"] = &registry.RegistrarEndpoint{EndpointID: "reg_nosecret", Tenant: "orders-service", URL: url, SecretRef: "localfile://missing"}
	return s
}

func newDispatchFixture(t *testing.T, opts DispatcherOptions) *dispatchFixture {
	t.Helper()
	rcv := newReceiver(t, secretNext) // the receiver already switched to the next secret
	fx := &dispatchFixture{store: &fakeStore{}, sink: &captureSink{}, logs: &bytes.Buffer{}, metrics: telemetry.NewMetrics("test", "dev"), rcv: rcv}
	secrets := telemetry.NewSecretSet(secretActive, secretNext)
	if opts.Schedule == nil {
		opts.Schedule = []time.Duration{5 * time.Second, 30 * time.Second}
	}
	opts.Guard.AllowLoopback = true
	d, err := NewDispatcher(DispatcherDeps{
		Snapshots: staticSnapshots{dispatcherSnapshot(rcv.srv.URL + "/hub/callbacks")},
		Secrets:   fakeSecrets{"localfile://reg_orders_cb": {[]byte(secretActive), []byte(secretNext)}},
		Audit:     fx.sink, Metrics: fx.metrics,
		Log:   telemetry.NewLogger(&lockedWriter{w: fx.logs}, slog.LevelDebug, secrets, "test"),
		store: fx.store,
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.guard.Close)
	fx.d = d
	return fx
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func testRow(t *testing.T, endpoint string, attempt int) delivery {
	t.Helper()
	ev := baseEvent(&Completed{Result: json.RawMessage(`{"status":"ok","name":"` + piiMarker + `"}`), Attempts: 1, CompletedAt: t0})
	body, err := ev.Marshal(t0)
	if err != nil {
		t.Fatal(err)
	}
	return delivery{
		ID: NewDeliveryID(), EventID: ev.EventID, EventType: string(EventRequestCompleted), RequestID: ev.RequestID,
		TenantID: "orders-service", IntegrationID: "int_evt_brandB", IntegrationVersion: 1, EndpointID: endpoint,
		Subscription: 2, Payload: body, Attempts: attempt,
	}
}

func TestDispatcherAttemptOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endpoint   string
		attempt    int
		status     int
		want       string
		errClass   string
		hits       int64
		httpStatus int
	}{
		{name: "2xx delivered, signed for rotation overlap", endpoint: "reg_orders_cb", attempt: 1, status: 204, want: statusDelivered, hits: 1, httpStatus: 204},
		{name: "5xx retried", endpoint: "reg_orders_cb", attempt: 1, status: 503, want: statusRetryScheduled, errClass: errHTTP5xx, hits: 1, httpStatus: 503},
		{name: "4xx retried", endpoint: "reg_orders_cb", attempt: 2, status: 400, want: statusRetryScheduled, errClass: errHTTP4xx, hits: 1, httpStatus: 400},
		{name: "redirect not followed, retried", endpoint: "reg_orders_cb", attempt: 1, status: 302, want: statusRetryScheduled, errClass: errRedirect, hits: 1, httpStatus: 302},
		{name: "schedule exhausted dead-letters", endpoint: "reg_orders_cb", attempt: 3, status: 500, want: statusDeadLettered, errClass: errHTTP5xx, hits: 1, httpStatus: 500},
		{name: "unknown endpoint dead-letters", endpoint: "reg_gone", attempt: 1, want: statusDeadLettered, errClass: errEndpointUnknown},
		{name: "endpoint of another tenant dead-letters", endpoint: "reg_other_tenant", attempt: 1, want: statusDeadLettered, errClass: errTenantMismatch},
		{name: "secret not loaded retried", endpoint: "reg_nosecret", attempt: 1, want: statusRetryScheduled, errClass: errSecretUnavailable},
		{name: "ssrf blocked retried", endpoint: "reg_private", attempt: 1, want: statusRetryScheduled, errClass: errSSRFBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newDispatchFixture(t, DispatcherOptions{})
			if tc.status != 0 {
				fx.rcv.status.Store(int64(tc.status))
			}
			row := testRow(t, tc.endpoint, tc.attempt)
			fx.d.attempt(t.Context(), row)

			o, ok := fx.store.outcome(row.ID)
			if !ok || o.Status != tc.want || o.Error != tc.errClass || o.HTTPStatus != tc.httpStatus {
				t.Fatalf("outcome = %+v (recorded %v), want %s/%s/%d", o, ok, tc.want, tc.errClass, tc.httpStatus)
			}
			if tc.want == statusRetryScheduled {
				base := []time.Duration{5 * time.Second, 30 * time.Second}[tc.attempt-1]
				if o.RetryIn < base*8/10 || o.RetryIn > base*12/10 {
					t.Fatalf("retry in %v, want %v ±20 %%", o.RetryIn, base)
				}
			}
			if got := fx.rcv.hits.Load(); got != tc.hits {
				t.Fatalf("receiver hits = %d, want %d", got, tc.hits)
			}
			if tc.hits > 0 {
				if fx.rcv.verified.Load() != 1 {
					t.Fatal("receiver could not verify the signature with its (next) secret")
				}
				h, body := fx.rcv.first()
				if !bytes.Equal(body, row.Payload) || h.Get(EventHeader) != "request.completed" || h.Get(DeliveryIDHeader) != row.ID ||
					h.Get("Content-Type") != "application/json" || strings.Count(h.Get(SignatureHeader), ",v1=") != 2 {
					t.Fatalf("request headers %v body %s", h, body)
				}
			}
			if got := testutil.ToFloat64(fx.d.deliveries.WithLabelValues("orders-service", tc.want)); got != 1 {
				t.Fatalf("callback_deliveries_total{%s} = %v", tc.want, got)
			}
			if tc.errClass == errSSRFBlocked {
				if testutil.ToFloat64(fx.d.blocked.WithLabelValues(ClassPrivate)) != 1 || !strings.Contains(fx.logs.String(), "callback.ssrf_blocked") {
					t.Fatalf("ssrf block not counted or logged: %s", fx.logs)
				}
			}

			evs := fx.sink.all()
			if len(evs) != 1 {
				t.Fatalf("audit events = %d", len(evs))
			}
			e := evs[0]
			if e.Type != audit.CallbackDelivered || e.RequestID != row.RequestID || e.Seq != AuditSeqBase+200+tc.attempt ||
				e.Payload != nil || e.PayloadState != audit.PayloadOmitted || e.Bytes != len(row.Payload) || !strings.HasPrefix(e.SHA256, "sha256:") ||
				e.Meta["outcome"] != tc.want || e.Meta["delivery_id"] != row.ID || e.Meta["attempt"] != tc.attempt {
				t.Fatalf("audit event = %+v", e)
			}
			meta, _ := json.Marshal(e.Meta)
			sig := ""
			if tc.hits > 0 {
				h, _ := fx.rcv.first()
				sig = h.Get(SignatureHeader)
			}
			for _, leak := range []string{secretActive, secretNext, piiMarker, "v1="} {
				if strings.Contains(string(meta), leak) || strings.Contains(fx.logs.String(), leak) {
					t.Fatalf("audit meta or logs leak %q:\nmeta %s\nlogs %s", leak, meta, fx.logs)
				}
			}
			if sig != "" && strings.Contains(fx.logs.String(), sig[strings.Index(sig, "v1=")+3:]) {
				t.Fatal("logs contain the signature")
			}
		})
	}
}

func TestDispatcherProductionGuardRefusesPlainHTTP(t *testing.T) {
	rcv := newReceiver(t, secretActive)
	d, err := NewDispatcher(DispatcherDeps{
		Snapshots: staticSnapshots{dispatcherSnapshot(rcv.srv.URL + "/cb")},
		Secrets:   fakeSecrets{"localfile://reg_orders_cb": {[]byte(secretActive)}},
		Audit:     audit.Discard{}, Metrics: telemetry.NewMetrics("test", "dev"), store: &fakeStore{},
	}, DispatcherOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.guard.Close)
	row := testRow(t, "reg_orders_cb", 1)
	d.attempt(t.Context(), row)
	o, _ := d.deps.store.(*fakeStore).outcome(row.ID)
	if o.Status != statusDeadLettered || o.Error != errPolicy || rcv.hits.Load() != 0 {
		t.Fatalf("outcome %+v, hits %d: production wiring must refuse http://127.0.0.1", o, rcv.hits.Load())
	}
}

func TestDispatcherLostClaimHasNoSideEffects(t *testing.T) {
	fx := newDispatchFixture(t, DispatcherOptions{})
	fx.store.lose = true
	fx.d.attempt(t.Context(), testRow(t, "reg_orders_cb", 1))
	if len(fx.sink.all()) != 0 || testutil.ToFloat64(fx.d.deliveries.WithLabelValues("orders-service", statusDelivered)) != 0 {
		t.Fatal("a lost claim must not be audited or counted")
	}
}

func TestRetrySchedule(t *testing.T) {
	fx := newDispatchFixture(t, DispatcherOptions{Schedule: DefaultSchedule})
	fx.d.rand = func(n int64) int64 { return n / 2 } // no jitter: exactly the base
	var total time.Duration
	for i, want := range DefaultSchedule {
		got, dead := fx.d.retryDelay(i + 1)
		if dead || got != want {
			t.Fatalf("attempt %d: delay %v dead %v, want %v", i+1, got, dead, want)
		}
		total += got
	}
	if _, dead := fx.d.retryDelay(len(DefaultSchedule) + 1); !dead {
		t.Fatal("attempt 11 must dead-letter")
	}
	if total < 19*time.Hour || total > 20*time.Hour {
		t.Fatalf("schedule spans %v, want ≈ 19.7 h", total)
	}
	fx.d.rand = func(n int64) int64 { return 0 }
	if got, _ := fx.d.retryDelay(1); got != 4*time.Second {
		t.Fatalf("min jitter %v, want 4s", got)
	}
	fx.d.rand = func(n int64) int64 { return n - 1 }
	if got, _ := fx.d.retryDelay(1); got >= 6*time.Second+time.Nanosecond || got < 6*time.Second-time.Nanosecond {
		t.Fatalf("max jitter %v, want 6s", got)
	}
	if _, err := NewDispatcher(DispatcherDeps{Snapshots: staticSnapshots{}, Secrets: fakeSecrets{}, Audit: audit.Discard{}, Metrics: telemetry.NewMetrics("t", "d"), store: &fakeStore{}},
		DispatcherOptions{Schedule: make([]time.Duration, 99)}); err == nil {
		t.Fatal("a schedule longer than 98 entries must be refused")
	}
	if _, err := NewDispatcher(DispatcherDeps{Snapshots: staticSnapshots{}, Secrets: fakeSecrets{}, Audit: audit.Discard{}, Metrics: telemetry.NewMetrics("t", "d"), store: &fakeStore{}},
		DispatcherOptions{Visibility: 2 * time.Second}); err == nil {
		t.Fatal("visibility below the send timeout must be refused")
	}
}

// TestRunDeliversAndStopsCleanly: Run claims no more than its free senders, delivers every
// queued row and returns after cancel with no goroutine left (goleak in TestMain).
func TestRunDeliversAndStopsCleanly(t *testing.T) {
	fx := newDispatchFixture(t, DispatcherOptions{Senders: 4, ClaimBatch: 10, PollInterval: 5 * time.Millisecond, SampleEvery: 5 * time.Millisecond})
	for range 40 {
		fx.store.queue = append(fx.store.queue, testRow(t, "reg_orders_cb", 1))
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- fx.d.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for fx.store.recorded() < 40 {
		if time.Now().After(deadline) {
			t.Fatalf("delivered %d of 40", fx.store.recorded())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if fx.rcv.verified.Load() != 40 {
		t.Fatalf("verified %d of 40", fx.rcv.verified.Load())
	}
	if fx.store.maxN > 4 {
		t.Fatalf("claimed %d rows at once with 4 senders", fx.store.maxN)
	}
	if testutil.ToFloat64(fx.d.oldest) != 3 || testutil.ToFloat64(fx.d.dlq) != 2 {
		t.Fatal("queue gauges not sampled")
	}
}
