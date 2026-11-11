package isfcsync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event is a change notification from ISFC (AUTH_PLAN §6.2; the shape is the request of ISFC-CR-2:
// tenant.created, tenant.updated, client.tenants_changed, client.api_key.changed,
// client.mapping.changed, each with event_id, occurred_at, client_id, tenant_id). An event only says
// "something changed": the Hub reads the full state on every reconcile, so a lost, duplicated or
// reordered event is harmless, and no event field decides anything.
type Event struct {
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurred_at"`
	ClientID   string    `json:"client_id"`
	TenantID   string    `json:"tenant_id"`
}

// Consumer delivers ISFC events to the Hub (decision D8: SQS behind an interface). Run blocks until
// ctx ends, calling handle once per message; an error from handle makes the transport redeliver, so
// handle must stay cheap and idempotent. There is no SQS implementation yet (ISFC-CR-2 has not
// landed); FakeConsumer serves tests and the signed webhook below needs none.
type Consumer interface {
	Run(ctx context.Context, handle func(Event) error) error
}

// Consume feeds every event of c into trigger (usually Loop.Trigger, or a Redis ping) until ctx
// ends. Duplicates collapse in the trigger.
func Consume(ctx context.Context, c Consumer, trigger func()) error {
	return c.Run(ctx, func(Event) error { trigger(); return nil })
}

// FakeConsumer is an in-memory Consumer for tests: Send delivers an event to Run's handler.
type FakeConsumer struct {
	once sync.Once
	ch   chan Event
}

func (f *FakeConsumer) init() { f.once.Do(func() { f.ch = make(chan Event, 1024) }) }

// Send queues an event (it blocks only if 1024 are waiting).
func (f *FakeConsumer) Send(e Event) { f.init(); f.ch <- e }

// Run implements Consumer.
func (f *FakeConsumer) Run(ctx context.Context, handle func(Event) error) error {
	f.init()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-f.ch:
			_ = handle(e)
		}
	}
}

// Webhook header names of the signed events endpoint.
const (
	HeaderTimestamp = "X-Isfc-Timestamp" // unix seconds
	HeaderSignature = "X-Isfc-Signature" // "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body))
)

// Sign returns the signature header value for body at ts.
func Sign(secret []byte, ts time.Time, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(strconv.FormatInt(ts.Unix(), 10)))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// WebhookHandler is POST /internal/isfc/events on the admin listener: a signed webhook that turns
// an ISFC event into a reconcile trigger. It answers 202 and does nothing else with the event.
type WebhookHandler struct {
	// Secret signs the requests; it is never logged. An empty secret refuses everything.
	Secret []byte
	// Trigger is called once per accepted event (cheap, non-blocking), with the request's context.
	Trigger func(context.Context, Event)
	// Tolerance is how far a timestamp may be from now (default 5 minutes): old requests are not
	// replayable beyond it.
	Tolerance time.Duration
	Now       func() time.Time
}

const maxEventBody = 64 << 10

// ServeHTTP implements http.Handler.
func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	tol := h.Tolerance
	if tol <= 0 {
		tol = 5 * time.Minute
	}
	if len(h.Secret) == 0 {
		http.Error(w, "events are not enabled", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBody))
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderTimestamp)), 10, 64)
	if err != nil {
		http.Error(w, "missing or invalid timestamp", http.StatusUnauthorized)
		return
	}
	ts := time.Unix(sec, 0)
	if d := now().Sub(ts); d > tol || d < -tol {
		http.Error(w, "timestamp outside the tolerance", http.StatusUnauthorized)
		return
	}
	want := Sign(h.Secret, ts, body)
	if subtle.ConstantTimeCompare([]byte(want), []byte(strings.TrimSpace(r.Header.Get(HeaderSignature)))) != 1 {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	if h.Trigger != nil {
		h.Trigger(r.Context(), ev)
	}
	w.WriteHeader(http.StatusAccepted)
}
