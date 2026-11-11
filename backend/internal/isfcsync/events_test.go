package isfcsync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func signedRequest(t *testing.T, secret []byte, now time.Time, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/internal/isfc/events", strings.NewReader(body))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(HeaderSignature, Sign(secret, now, []byte(body)))
	return req
}

func TestWebhookAcceptsOnlySignedFreshEvents(t *testing.T) {
	secret := []byte("events-secret")
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	const body = `{"event_id":"e1","type":"tenant.created","occurred_at":"2026-11-11T09:59:59Z","client_id":"integration-hub-console","tenant_id":"6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f"}`
	tests := []struct {
		name   string
		mutate func(r *http.Request)
		secret []byte
		body   string
		want   int
		fires  int
	}{
		{"valid", nil, secret, body, 202, 1},
		{"wrong signature", func(r *http.Request) { r.Header.Set(HeaderSignature, "sha256=00") }, secret, body, 401, 0},
		{"signed with another secret", func(r *http.Request) { r.Header.Set(HeaderSignature, Sign([]byte("other"), now, []byte(body))) }, secret, body, 401, 0},
		{"body changed after signing", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"event_id":"e2"}`)) }, secret, body, 401, 0},
		{"no signature", func(r *http.Request) { r.Header.Del(HeaderSignature) }, secret, body, 401, 0},
		{"no timestamp", func(r *http.Request) { r.Header.Del(HeaderTimestamp) }, secret, body, 401, 0},
		{"stale timestamp (replay)", func(r *http.Request) {
			old := now.Add(-time.Hour)
			r.Header.Set(HeaderTimestamp, strconv.FormatInt(old.Unix(), 10))
			r.Header.Set(HeaderSignature, Sign(secret, old, []byte(body)))
		}, secret, body, 401, 0},
		{"future timestamp", func(r *http.Request) {
			f := now.Add(time.Hour)
			r.Header.Set(HeaderTimestamp, strconv.FormatInt(f.Unix(), 10))
			r.Header.Set(HeaderSignature, Sign(secret, f, []byte(body)))
		}, secret, body, 401, 0},
		{"not JSON, correctly signed", nil, secret, "nope", 400, 0},
		{"no secret configured", nil, nil, body, 503, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fired atomic.Int32
			h := &WebhookHandler{Secret: tt.secret, Trigger: func(context.Context, Event) { fired.Add(1) }, Now: func() time.Time { return now }}
			req := signedRequest(t, secret, now, tt.body)
			if tt.mutate != nil {
				tt.mutate(req)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want || int(fired.Load()) != tt.fires {
				t.Fatalf("status %d (want %d), fired %d (want %d): %s", rec.Code, tt.want, fired.Load(), tt.fires, rec.Body)
			}
			if strings.Contains(rec.Body.String(), string(secret)) {
				t.Fatal("the secret is in the response")
			}
		})
	}
}

func TestWebhookBodyIsBounded(t *testing.T) {
	secret := []byte("s")
	now := time.Now()
	big := `{"event_id":"` + strings.Repeat("x", maxEventBody) + `"}`
	rec := httptest.NewRecorder()
	(&WebhookHandler{Secret: secret, Now: func() time.Time { return now }}).ServeHTTP(rec, signedRequest(t, secret, now, big))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", rec.Code)
	}
}

// TestDuplicateAndReorderedEventsOnlyCollapseIntoTriggers: events carry no state, so any number of
// duplicates, in any order, feed the same trigger and cost at most a few runs.
func TestDuplicateAndReorderedEventsOnlyCollapseIntoTriggers(t *testing.T) {
	r := newRunner()
	r.delay = 50 * time.Millisecond // a run takes time: that is what collapses the events that arrive meanwhile
	var mu sync.Mutex
	var leader string
	l := &Loop{Runner: r, Elector: &fakeElector{mu: &mu, leader: &leader, name: "a"}, Interval: time.Hour, Retry: time.Hour}
	ctx, cancel := context.WithCancel(t.Context())
	fc := &FakeConsumer{}
	var wg sync.WaitGroup
	wg.Go(func() { l.Run(ctx) })
	wg.Go(func() { _ = Consume(ctx, fc, l.Trigger) })
	waitRun(t, r) // the run at start
	types := []string{"tenant.updated", "tenant.created", "client.api_key.changed", "tenant.created", "client.tenants_changed"}
	for i := range 200 { // duplicates and "out of order": a later change's event arrives first, twice
		fc.Send(Event{EventID: "e" + strconv.Itoa(i%3), Type: types[(i*7)%len(types)], OccurredAt: time.Unix(int64(1000-i), 0)})
	}
	waitRun(t, r)
	time.Sleep(100 * time.Millisecond)
	if n := r.count(); n > 6 {
		t.Fatalf("200 duplicate events caused %d runs", n)
	}
	cancel()
	wg.Wait()
}

func TestSignatureIsStable(t *testing.T) {
	ts := time.Unix(1_700_000_000, 0)
	a, b := Sign([]byte("k"), ts, []byte("body")), Sign([]byte("k"), ts, []byte("body"))
	if a != b || !strings.HasPrefix(a, "sha256=") || len(a) != len("sha256=")+64 {
		t.Fatalf("signature %q", a)
	}
	if Sign([]byte("k"), ts.Add(time.Second), []byte("body")) == a {
		t.Fatal("timestamp is not part of the signature")
	}
}
