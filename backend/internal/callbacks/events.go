package callbacks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// Outbound event payloads (contracts/schemas/events/*.json, DESIGN §12.1). The body never
// carries a vendor name, an account name or a version (D12): the caller fills Data with
// canonical results and canonical errors only.

// EventType is an outbound callback event type (envelope.json `type`, header X-Hub-Event).
type EventType string

const (
	EventRequestCompleted EventType = "request.completed"
	EventRequestFailed    EventType = "request.failed"
	EventRequestExpired   EventType = "request.expired"
	EventDeliveryStatus   EventType = "delivery.status"
)

// EventTypes lists every outbound event type, in contract order.
var EventTypes = []EventType{EventRequestCompleted, EventRequestFailed, EventRequestExpired, EventDeliveryStatus}

// SpecVersion is the envelope major version (envelope.json `spec_version`).
const SpecVersion = "1"

var (
	eventIDPattern    = regexp.MustCompile(`^evt_[0-9A-HJKMNP-TV-Z]{26}$`)
	deliveryIDPattern = regexp.MustCompile(`^dlv_[0-9A-HJKMNP-TV-Z]{26}$`)
)

// NewEventID mints an event id ("evt_" + ULID).
func NewEventID() string { return "evt_" + core.NewULID(time.Now()) }

// NewDeliveryID mints a delivery id ("dlv_" + ULID).
func NewDeliveryID() string { return "dlv_" + core.NewULID(time.Now()) }

// Event is one outbound event, as the request state machine hands it to Enqueue.
type Event struct {
	// EventID is "evt_" + ULID, stable across delivery retries. Empty: Enqueue mints one. Pass
	// a stable id when the same transition may be enqueued twice (a retried transaction):
	// (event_id, endpoint) is unique, so the second insert is a no-op.
	EventID string
	// OccurredAt is when the state change happened. Zero: now.
	OccurredAt time.Time
	// TenantID is the tenant that made the request. Callbacks go only to registrar endpoints
	// of this tenant.
	TenantID      string
	IntegrationID string
	// IntegrationVersion is the version the request ran on; its webhooks are the
	// subscriptions (registry.IntegrationVersion.Webhooks).
	IntegrationVersion int
	// Operation is the integration operation. Empty: "default".
	Operation string
	RequestID string
	// IdempotencyKey is the caller's Idempotency-Key. Empty: null.
	IdempotencyKey string
	// Data is the type-specific payload: *Completed, *Failed, *Expired or *DeliveryStatus.
	Data EventData
}

// EventData is the type-specific part of an event.
type EventData interface {
	Type() EventType
	wire(ev *Event) (any, error)
}

// Timings are the durations of a completed request in milliseconds (request.completed.json).
type Timings struct {
	TotalMS       int64 `json:"total_ms"`
	HubOverheadMS int64 `json:"hub_overhead_ms"`
	VendorMS      int64 `json:"vendor_ms"`
	QueueWaitMS   int64 `json:"queue_wait_ms"`
}

// Completed is the data of request.completed (status succeeded).
type Completed struct {
	// Result is the canonical response body (a JSON object), as GET /v1/requests/{id} returns it.
	Result      json.RawMessage
	Attempts    int
	CompletedAt time.Time
	Timings     *Timings
}

// Failed is the data of request.failed (status dead_lettered).
type Failed struct {
	Error    core.CanonicalError
	Attempts int
	FailedAt time.Time
}

// Expired is the data of request.expired. Error.Code is always EXPIRED; empty defaults to it.
type Expired struct {
	Error     core.CanonicalError
	Attempts  int
	ExpiresAt time.Time
	// LastState is accepted, retry_scheduled or parked; empty omits it.
	LastState string
}

// DeliveryStatus is the data of delivery.status (non-terminal transitions, DESIGN §10.9).
type DeliveryStatus struct {
	State         string // accepted | in_flight | retry_scheduled | parked
	PreviousState string // "" = null
	Sequence      int    // ≥ 1, monotonic per request
	Attempt       int
	NextAttemptAt *time.Time
	Reason        string // "" = null
	LastError     *core.CanonicalError
}

// Type implements EventData.
func (*Completed) Type() EventType { return EventRequestCompleted }

// Type implements EventData.
func (*Failed) Type() EventType { return EventRequestFailed }

// Type implements EventData.
func (*Expired) Type() EventType { return EventRequestExpired }

// Type implements EventData.
func (*DeliveryStatus) Type() EventType { return EventDeliveryStatus }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (d *Completed) wire(*Event) (any, error) {
	r := bytes.TrimSpace(d.Result)
	if len(r) == 0 || r[0] != '{' || !json.Valid(r) {
		return nil, errors.New("completed: result must be a JSON object")
	}
	if d.Attempts < 1 || d.CompletedAt.IsZero() {
		return nil, errors.New("completed: attempts ≥ 1 and completed_at are required")
	}
	return struct {
		Status      string          `json:"status"`
		Result      json.RawMessage `json:"result"`
		Attempts    int             `json:"attempts"`
		CompletedAt string          `json:"completed_at"`
		Timings     *Timings        `json:"timings,omitempty"`
	}{"succeeded", r, d.Attempts, ts(d.CompletedAt), d.Timings}, nil
}

func canonicalError(e core.CanonicalError, ev *Event) (core.CanonicalError, error) {
	if e.RequestID == "" {
		e.RequestID = ev.RequestID
	}
	if e.Code == "" || e.Message == "" {
		return e, errors.New("error: code and message are required")
	}
	return e, nil
}

func (d *Failed) wire(ev *Event) (any, error) {
	e, err := canonicalError(d.Error, ev)
	if err != nil {
		return nil, fmt.Errorf("failed: %w", err)
	}
	if d.Attempts < 1 || d.FailedAt.IsZero() {
		return nil, errors.New("failed: attempts ≥ 1 and failed_at are required")
	}
	return struct {
		Status   string              `json:"status"`
		Error    core.CanonicalError `json:"error"`
		Attempts int                 `json:"attempts"`
		FailedAt string              `json:"failed_at"`
	}{"dead_lettered", e, d.Attempts, ts(d.FailedAt)}, nil
}

func (d *Expired) wire(ev *Event) (any, error) {
	in := d.Error
	if in.Code == "" {
		in.Code = core.CodeExpired
	}
	if in.Message == "" {
		in.Message = "Request passed its TTL before delivery"
	}
	if in.Reason == nil {
		in.Reason = core.Ptr("ttl_elapsed")
	}
	e, err := canonicalError(in, ev)
	if err != nil {
		return nil, fmt.Errorf("expired: %w", err)
	}
	if e.Code != core.CodeExpired || d.Attempts < 0 || d.ExpiresAt.IsZero() {
		return nil, errors.New("expired: code EXPIRED, attempts ≥ 0 and expires_at are required")
	}
	return struct {
		Status    string              `json:"status"`
		Error     core.CanonicalError `json:"error"`
		Attempts  int                 `json:"attempts"`
		ExpiresAt string              `json:"expires_at"`
		LastState string              `json:"last_state,omitempty"`
	}{"expired", e, d.Attempts, ts(d.ExpiresAt), d.LastState}, nil
}

func (d *DeliveryStatus) wire(ev *Event) (any, error) {
	if d.State == "" || d.Sequence < 1 || d.Attempt < 0 {
		return nil, errors.New("delivery.status: state, sequence ≥ 1 and attempt ≥ 0 are required")
	}
	var next *string
	if d.NextAttemptAt != nil {
		next = core.Ptr(ts(*d.NextAttemptAt))
	}
	var lastErr *core.CanonicalError
	if d.LastError != nil {
		e, err := canonicalError(*d.LastError, ev)
		if err != nil {
			return nil, fmt.Errorf("delivery.status last_error: %w", err)
		}
		lastErr = &e
	}
	return struct {
		State         string               `json:"state"`
		PreviousState *string              `json:"previous_state"`
		Sequence      int                  `json:"sequence"`
		Attempt       int                  `json:"attempt"`
		NextAttemptAt *string              `json:"next_attempt_at"`
		Reason        *string              `json:"reason"`
		LastError     *core.CanonicalError `json:"last_error"`
	}{d.State, nonEmpty(d.PreviousState), d.Sequence, d.Attempt, next, nonEmpty(d.Reason), lastErr}, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Marshal returns the exact callback body of ev (the bytes that are signed and sent). It fills
// EventID and OccurredAt when they are empty, so call it once per event.
func (ev *Event) Marshal(now time.Time) ([]byte, error) {
	if ev.Data == nil {
		return nil, errors.New("callbacks: event has no data")
	}
	if !core.RequestIDPattern.MatchString(ev.RequestID) {
		return nil, fmt.Errorf("callbacks: request_id %q is not req_<ULID>", ev.RequestID)
	}
	if ev.TenantID == "" || ev.IntegrationID == "" || ev.IntegrationVersion < 1 {
		return nil, errors.New("callbacks: tenant, integration and integration version are required")
	}
	if len(ev.IdempotencyKey) > 255 {
		return nil, errors.New("callbacks: idempotency_key longer than 255 bytes")
	}
	if ev.EventID == "" {
		ev.EventID = NewEventID()
	} else if !eventIDPattern.MatchString(ev.EventID) {
		return nil, fmt.Errorf("callbacks: event_id %q is not evt_<ULID>", ev.EventID)
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = now
	}
	op := ev.Operation
	if op == "" {
		op = "default"
	}
	data, err := ev.Data.wire(ev)
	if err != nil {
		return nil, fmt.Errorf("callbacks: %s: %w", ev.Data.Type(), err)
	}
	b, err := json.Marshal(struct {
		EventID        string    `json:"event_id"`
		Type           EventType `json:"type"`
		SpecVersion    string    `json:"spec_version"`
		OccurredAt     string    `json:"occurred_at"`
		TenantID       string    `json:"tenant_id"`
		IntegrationID  string    `json:"integration_id"`
		Operation      string    `json:"operation"`
		RequestID      string    `json:"request_id"`
		IdempotencyKey *string   `json:"idempotency_key"`
		Data           any       `json:"data"`
	}{ev.EventID, ev.Data.Type(), SpecVersion, ts(ev.OccurredAt), ev.TenantID, ev.IntegrationID, op, ev.RequestID, nonEmpty(ev.IdempotencyKey), data})
	if err != nil {
		return nil, fmt.Errorf("callbacks: encode %s: %w", ev.Data.Type(), err)
	}
	return b, nil
}
