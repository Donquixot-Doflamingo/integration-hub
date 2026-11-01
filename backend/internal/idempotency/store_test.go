package idempotency

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// call is one script invocation seen by fakeRedis.
type call struct {
	script string // "begin", "complete", "release", "load"
	keys   []string
	args   []any
	// left is the time to the context deadline when the call was made (0: no deadline).
	left time.Duration
}

// fakeRedis implements redis.Scripter with canned replies, so the unit tests cover everything
// on the Go side of the scripts (validation, key scoping, argument encoding, reply parsing,
// error mapping, metrics) without a server. The scripts themselves run against real Redis in
// store_integration_test.go.
type fakeRedis struct {
	mu    sync.Mutex
	calls []call
	reply any
	err   error
}

var _ redis.Scripter = (*fakeRedis)(nil)

func scriptName(sha string) string {
	for name, src := range map[string]string{"begin": beginSrc, "complete": completeSrc, "release": releaseSrc} {
		if sha == redis.NewScript(src).Hash() {
			return name
		}
	}
	return "unknown"
}

func (f *fakeRedis) record(ctx context.Context, name string, keys []string, args []any) *redis.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	var left time.Duration
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	f.calls = append(f.calls, call{script: name, keys: keys, args: args, left: left})
	return redis.NewCmdResult(f.reply, f.err)
}

func (f *fakeRedis) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *redis.Cmd {
	return f.record(ctx, scriptName(sha), keys, args)
}

func (f *fakeRedis) Eval(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.record(ctx, "eval", keys, args)
}

func (f *fakeRedis) EvalRO(ctx context.Context, src string, keys []string, args ...any) *redis.Cmd {
	return f.Eval(ctx, src, keys, args...)
}

func (f *fakeRedis) EvalShaRO(ctx context.Context, sha string, keys []string, args ...any) *redis.Cmd {
	return f.EvalSha(ctx, sha, keys, args...)
}

func (f *fakeRedis) ScriptExists(_ context.Context, hashes ...string) *redis.BoolSliceCmd {
	return redis.NewBoolSliceResult(make([]bool, len(hashes)), f.err)
}

func (f *fakeRedis) ScriptLoad(_ context.Context, src string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{script: "load"})
	return redis.NewStringResult(redis.NewScript(src).Hash(), f.err) // the digest Redis would return
}

func (f *fakeRedis) lastCall(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no Redis call was made")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeRedis) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newTestStore(t testing.TB, reply any, err error, opts Options) (*Store, *fakeRedis, *telemetry.Metrics) {
	t.Helper()
	f := &fakeRedis{reply: reply, err: err}
	m := telemetry.NewMetrics("gateway", "test")
	return New(f, m, opts), f, m
}

var testKey = Key{Tenant: "ten_acme", Integration: "int_pay_upi", Key: "order-8821-created"}

func TestBeginMapsReplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		reply     any
		err       error
		want      Outcome
		wantErr   error
		wantCause error
	}{
		{name: "free key is fresh", reply: []any{"f"}, want: Outcome{State: Fresh}},
		{name: "running request is in progress", reply: []any{"p", "req_1"}, want: Outcome{State: InProgress, RequestID: "req_1"}},
		{name: "different body is a conflict", reply: []any{"x", "req_1"}, want: Outcome{State: Conflict, RequestID: "req_1"}},
		{
			name:  "completed 200 replays status headers and body",
			reply: []any{"c", "req_1", "200", `{"Content-Type":"application/json","X-Hub-Served-By":"primary"}`, `{"ok":true}`},
			want: Outcome{State: Completed, RequestID: "req_1", Response: Response{
				Status: 200, Header: http.Header{"Content-Type": {"application/json"}, "X-Hub-Served-By": {"primary"}}, Body: []byte(`{"ok":true}`),
			}},
		},
		{
			name:  "completed 202 replays the location",
			reply: []any{"c", "req_1", "202", `{"Location":"/v1/requests/req_1"}`, `{"request_id":"req_1","status":"accepted"}`},
			want: Outcome{State: Completed, RequestID: "req_1", Response: Response{
				Status: 202, Header: http.Header{"Location": {"/v1/requests/req_1"}}, Body: []byte(`{"request_id":"req_1","status":"accepted"}`),
			}},
		},
		{name: "connection refused is unavailable", err: errors.New("dial tcp: connection refused"), wantErr: ErrUnavailable},
		{name: "cancelled context is unavailable and keeps its cause", err: context.Canceled, wantErr: ErrUnavailable, wantCause: context.Canceled},
		{name: "unknown tag is unavailable", reply: []any{"?"}, wantErr: ErrUnavailable},
		{name: "short in-progress reply is unavailable", reply: []any{"p"}, wantErr: ErrUnavailable},
		{name: "non-numeric status is unavailable", reply: []any{"c", "req_1", "ok", `{}`, ``}, wantErr: ErrUnavailable},
		{name: "corrupt headers are unavailable", reply: []any{"c", "req_1", "200", `{`, ``}, wantErr: ErrUnavailable},
		{name: "non-array reply is unavailable", reply: int64(1), wantErr: ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _, _ := newTestStore(t, tt.reply, tt.err, Options{})
			got, err := s.Begin(t.Context(), testKey, "bh", "req_2")
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				var ue *UnavailableError
				if !errors.As(err, &ue) || ue.Op != "begin" {
					t.Fatalf("err = %#v, want *UnavailableError{Op: begin}", err)
				}
				if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
					t.Fatalf("err = %v does not wrap %v", err, tt.wantCause)
				}
				return
			}
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			assertOutcome(t, got, tt.want)
		})
	}
}

func assertOutcome(t *testing.T, got, want Outcome) {
	t.Helper()
	if got.State != want.State || got.RequestID != want.RequestID || got.Response.Status != want.Response.Status ||
		string(got.Response.Body) != string(want.Response.Body) {
		t.Fatalf("outcome = %+v, want %+v", got, want)
	}
	if len(got.Response.Header) != len(want.Response.Header) {
		t.Fatalf("headers = %v, want %v", got.Response.Header, want.Response.Header)
	}
	for k := range want.Response.Header {
		if got.Response.Header.Get(k) != want.Response.Header.Get(k) {
			t.Fatalf("header %s = %q, want %q", k, got.Response.Header.Get(k), want.Response.Header.Get(k))
		}
	}
}

func TestBeginSendsScopedHashedKeyAndTTLs(t *testing.T) {
	t.Parallel()
	s, f, _ := newTestStore(t, []any{"f"}, nil, Options{Prefix: "p:", LeaseTTL: 90 * time.Second})
	if _, err := s.Begin(t.Context(), testKey, "bodyhash", "req_1"); err != nil {
		t.Fatal(err)
	}
	c := f.lastCall(t)
	if c.script != "begin" || len(c.keys) != 1 {
		t.Fatalf("call = %+v, want one begin call with one key", c)
	}
	key := c.keys[0]
	if !strings.HasPrefix(key, "p:") || len(key) != len("p:")+64 {
		t.Fatalf("redis key %q is not prefix + 64 hex chars", key)
	}
	for _, raw := range []string{testKey.Key, testKey.Tenant, testKey.Integration} {
		if strings.Contains(key, raw) {
			t.Fatalf("redis key %q contains the raw value %q", key, raw)
		}
	}
	want := []any{"bodyhash", "req_1", int64(90_000), int64(24 * time.Hour / time.Millisecond)}
	if len(c.args) != len(want) {
		t.Fatalf("args = %v, want %v", c.args, want)
	}
	for i := range want {
		if c.args[i] != want[i] {
			t.Fatalf("arg %d = %#v, want %#v", i, c.args[i], want[i])
		}
	}
}

func TestEveryRoundTripIsBounded(t *testing.T) {
	t.Parallel()
	s, f, _ := newTestStore(t, int64(1), nil, Options{Timeout: 20 * time.Millisecond})
	_, _ = s.Begin(t.Context(), testKey, "bh", "req_1")
	_ = s.Complete(t.Context(), testKey, "req_1", Response{Status: 200})
	_ = s.Release(t.Context(), testKey, "req_1")
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.left <= 0 || c.left > 20*time.Millisecond {
			t.Errorf("%s ran with %s to its deadline, want at most the 20ms timeout", c.script, c.left)
		}
	}
	if len(f.calls) != 3 {
		t.Fatalf("%d calls, want 3", len(f.calls))
	}
}

func TestRedisKeyScoping(t *testing.T) {
	t.Parallel()
	s, _, _ := newTestStore(t, nil, nil, Options{})
	base := s.redisKey(testKey)
	tests := []struct {
		name string
		k    Key
		same bool
	}{
		{name: "same scope gives the same record", k: testKey, same: true},
		{name: "other tenant never collides", k: Key{Tenant: "ten_other", Integration: testKey.Integration, Key: testKey.Key}},
		{name: "other integration never collides", k: Key{Tenant: testKey.Tenant, Integration: "int_other", Key: testKey.Key}},
		{name: "other key", k: Key{Tenant: testKey.Tenant, Integration: testKey.Integration, Key: "order-8822"}},
		{name: "boundaries between parts matter", k: Key{Tenant: testKey.Tenant + "int", Integration: "_pay_upi", Key: testKey.Key}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := s.redisKey(tt.k) == base; got != tt.same {
				t.Fatalf("same record = %v, want %v", got, tt.same)
			}
		})
	}
}

func TestInputValidationNeverCallsRedis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		k         Key
		bodyHash  string
		requestID string
	}{
		{name: "empty tenant", k: Key{Integration: "i", Key: "k"}, bodyHash: "h", requestID: "r"},
		{name: "empty integration", k: Key{Tenant: "t", Key: "k"}, bodyHash: "h", requestID: "r"},
		{name: "empty key", k: Key{Tenant: "t", Integration: "i"}, bodyHash: "h", requestID: "r"},
		{name: "key over 255 chars", k: Key{Tenant: "t", Integration: "i", Key: strings.Repeat("k", 256)}, bodyHash: "h", requestID: "r"},
		{name: "key with a space", k: Key{Tenant: "t", Integration: "i", Key: "order 1"}, bodyHash: "h", requestID: "r"},
		{name: "key with non-ASCII", k: Key{Tenant: "t", Integration: "i", Key: "ordré"}, bodyHash: "h", requestID: "r"},
		{name: "empty body hash", k: testKey, requestID: "r"},
		{name: "empty request id", k: testKey, bodyHash: "h"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, f, _ := newTestStore(t, []any{"f"}, nil, Options{})
			if _, err := s.Begin(t.Context(), tt.k, tt.bodyHash, tt.requestID); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Begin err = %v, want ErrInvalidInput", err)
			}
			if tt.bodyHash != "" { // Complete and Release take no body hash
				if err := s.Complete(t.Context(), tt.k, tt.requestID, Response{Status: 200}); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("Complete err = %v, want ErrInvalidInput", err)
				}
				if err := s.Release(t.Context(), tt.k, tt.requestID); !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("Release err = %v, want ErrInvalidInput", err)
				}
			}
			if n := f.callCount(); n != 0 {
				t.Fatalf("%d Redis calls, want 0", n)
			}
		})
	}
	s, _, _ := newTestStore(t, []any{"f"}, nil, Options{})
	if _, err := s.Begin(t.Context(), Key{Tenant: "t", Integration: "i", Key: strings.Repeat("k", 255)}, "h", "r"); err != nil {
		t.Fatalf("255-char key: %v", err)
	}
}

func TestCompleteStoresOnlyReplayableResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   int
		body     []byte
		maxBytes int
		reply    any
		err      error
		wantErr  error
		wantCall bool
	}{
		{name: "200 is stored", status: 200, reply: int64(1), wantCall: true},
		{name: "202 is stored", status: 202, reply: int64(1), wantCall: true},
		{name: "201 is not replayable", status: 201, wantErr: ErrNotReplayable},
		{name: "400 error envelope is not replayable", status: 400, wantErr: ErrNotReplayable},
		{name: "409 is not replayable", status: 409, wantErr: ErrNotReplayable},
		{name: "422 upstream rejection is not replayable", status: 422, wantErr: ErrNotReplayable},
		{name: "500 is not replayable", status: 500, wantErr: ErrNotReplayable},
		{name: "503 is not replayable", status: 503, wantErr: ErrNotReplayable},
		{name: "504 is not replayable", status: 504, wantErr: ErrNotReplayable},
		{name: "body over the limit is refused", status: 200, body: make([]byte, 101), maxBytes: 100, wantErr: ErrTooLarge},
		{name: "body at the limit is stored", status: 200, body: make([]byte, 100-len("200")-len(`{}`)), maxBytes: 100, reply: int64(1), wantCall: true},
		{name: "expired lease is not owner", status: 200, reply: int64(0), wantErr: ErrNotOwner, wantCall: true},
		{name: "key claimed by another request is not owner", status: 200, reply: int64(-1), wantErr: ErrNotOwner, wantCall: true},
		{name: "redis down is unavailable", status: 200, err: errors.New("i/o timeout"), wantErr: ErrUnavailable, wantCall: true},
		{name: "unexpected reply is unavailable", status: 200, reply: int64(7), wantErr: ErrUnavailable, wantCall: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, f, _ := newTestStore(t, tt.reply, tt.err, Options{MaxBytes: tt.maxBytes})
			err := s.Complete(t.Context(), testKey, "req_1", Response{Status: tt.status, Body: tt.body})
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := f.callCount() > 0; got != tt.wantCall {
				t.Fatalf("Redis called = %v, want %v", got, tt.wantCall)
			}
		})
	}
}

func TestCompleteStoresOnlyAllowlistedHeaders(t *testing.T) {
	t.Parallel()
	s, f, _ := newTestStore(t, int64(1), nil, Options{})
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Location", "/v1/requests/req_1")
	h.Set("X-Hub-Served-By", "failover:1")
	h.Set("X-Hub-Session", "ses_1")
	h.Set("X-Hub-Request-Id", "req_1")
	h.Set("X-Hub-Integration-Version", "7")
	h.Set("Authorization", "Bearer canary-secret")
	h.Set("Set-Cookie", "sid=canary-secret")
	h.Set("X-Api-Key", "canary-secret")
	body := []byte(`{"request_id":"req_1","status":"accepted"}`)
	if err := s.Complete(t.Context(), testKey, "req_1", Response{Status: 202, Header: h, Body: body}); err != nil {
		t.Fatal(err)
	}
	c := f.lastCall(t)
	if c.script != "complete" || len(c.args) != 4 || c.args[0] != "req_1" || c.args[1] != "202" {
		t.Fatalf("call = %+v", c)
	}
	hdr, _ := c.args[2].(string)
	for _, gone := range []string{"canary-secret", "X-Hub-Request-Id", "X-Hub-Integration-Version", "Authorization", "Set-Cookie"} {
		if strings.Contains(hdr, gone) {
			t.Fatalf("stored headers %s contain %q", hdr, gone)
		}
	}

	// What Complete wrote is exactly what Begin replays.
	r, _, _ := newTestStore(t, []any{"c", "req_1", c.args[1], hdr, string(c.args[3].([]byte))}, nil, Options{})
	got, err := r.Begin(t.Context(), testKey, "bh", "req_2")
	if err != nil {
		t.Fatal(err)
	}
	want := http.Header{}
	for _, k := range []string{"Content-Type", "Location", "X-Hub-Served-By", "X-Hub-Session"} {
		want.Set(k, h.Get(k))
	}
	assertOutcome(t, got, Outcome{State: Completed, RequestID: "req_1", Response: Response{Status: 202, Header: want, Body: body}})
}

func TestReleaseMapsReplies(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reply   any
		err     error
		wantErr error
	}{
		{name: "owner deletes", reply: int64(1)},
		{name: "missing record is fine", reply: int64(0)},
		{name: "other owner is kept", reply: int64(-1), wantErr: ErrNotOwner},
		{name: "redis down is unavailable", err: errors.New("connection refused"), wantErr: ErrUnavailable},
		{name: "unexpected reply is unavailable", reply: int64(3), wantErr: ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, f, _ := newTestStore(t, tt.reply, tt.err, Options{})
			err := s.Release(t.Context(), testKey, "req_1")
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if c := f.lastCall(t); c.script != "release" || c.args[0] != "req_1" {
				t.Fatalf("call = %+v", c)
			}
		})
	}
}

func TestWarmLoadsScripts(t *testing.T) {
	t.Parallel()
	s, f, _ := newTestStore(t, nil, nil, Options{})
	if err := s.Warm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := f.callCount(); n != 3 {
		t.Fatalf("%d script loads, want 3", n)
	}
	d, _, _ := newTestStore(t, nil, errors.New("connection refused"), Options{})
	if err := d.Warm(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Warm err = %v, want ErrUnavailable", err)
	}
}

func TestOptionsDefaults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   Options
		want Options
	}{
		{name: "zero takes defaults", want: Options{Prefix: DefaultPrefix, TTL: DefaultTTL, LeaseTTL: DefaultLeaseTTL, MaxBytes: DefaultMaxBytes, Timeout: DefaultTimeout}},
		{name: "lease is capped at the TTL", in: Options{TTL: time.Minute, LeaseTTL: time.Hour},
			want: Options{Prefix: DefaultPrefix, TTL: time.Minute, LeaseTTL: time.Minute, MaxBytes: DefaultMaxBytes, Timeout: DefaultTimeout}},
		{name: "explicit values are kept", in: Options{Prefix: "x:", TTL: time.Hour, LeaseTTL: time.Second, MaxBytes: 10, Timeout: time.Second},
			want: Options{Prefix: "x:", TTL: time.Hour, LeaseTTL: time.Second, MaxBytes: 10, Timeout: time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.in
			got.withDefaults()
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBodyHash(t *testing.T) {
	t.Parallel()
	a := BodyHash("default", []byte(`{"amount":100}`))
	if len(a) != 64 || a != BodyHash("default", []byte(`{"amount":100}`)) {
		t.Fatalf("BodyHash is not a stable 64-char hex digest: %q", a)
	}
	for name, other := range map[string]string{
		"other body":                 BodyHash("default", []byte(`{"amount":101}`)),
		"other operation":            BodyHash("verify", []byte(`{"amount":100}`)),
		"operation/body boundary":    BodyHash("defaul", []byte(`t{"amount":100}`)),
		"empty operation same bytes": BodyHash("", []byte(`default{"amount":100}`)),
	} {
		if other == a {
			t.Errorf("%s: hash collides", name)
		}
	}
}

func TestMetricsCountOutcomes(t *testing.T) {
	t.Parallel()
	m := telemetry.NewMetrics("gateway", "test")
	f := &fakeRedis{}
	s := New(f, m, Options{})
	for _, reply := range []any{[]any{"f"}, []any{"p", "r"}, []any{"c", "r", "200", "{}", ""}, []any{"c", "r", "200", "{}", ""}, []any{"x", "r"}} {
		f.reply = reply
		if _, err := s.Begin(t.Context(), testKey, "bh", "req_1"); err != nil {
			t.Fatal(err)
		}
	}
	f.reply, f.err = nil, errors.New("down")
	_, _ = s.Begin(t.Context(), testKey, "bh", "req_1")
	_ = s.Complete(t.Context(), testKey, "req_1", Response{Status: 503})

	out := scrapeMetrics(t, m)
	for _, want := range []string{
		`idempotency_hits_total{result="miss"} 1`,
		`idempotency_hits_total{result="in_progress"} 1`,
		`idempotency_hits_total{result="replay"} 2`,
		`idempotency_hits_total{result="conflict"} 1`,
		`idempotency_hits_total{result="error"} 1`,
		`idempotency_writes_total{op="complete",result="not_replayable"} 1`,
		`redis_op_duration_seconds_count{op="idempotency_begin"} 6`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics is missing %s", want)
		}
	}
	if got := testutil.ToFloat64(s.m.completeOK); got != 0 {
		t.Errorf("complete stored = %v, want 0", got)
	}
}

func scrapeMetrics(t *testing.T, m *telemetry.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

func TestStateString(t *testing.T) {
	t.Parallel()
	for s, want := range map[State]string{Fresh: "fresh", InProgress: "in_progress", Completed: "completed", Conflict: "conflict", 0: "invalid"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q, want %q", s, s.String(), want)
		}
	}
}
