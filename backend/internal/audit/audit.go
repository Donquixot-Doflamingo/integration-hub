package audit

import (
	"time"
)

// Event types (DESIGN §13.1).
const (
	RequestReceived         = "request.received"
	RequestValidated        = "request.validated"
	IntegrationResolved     = "integration.resolved"
	RequestTransformed      = "request.transformed"
	VendorSent              = "vendor.sent"
	VendorResponded         = "vendor.responded"
	ResponseTransformed     = "response.transformed"
	ResponseTransformFailed = "response.transform_failed"
	RequestCompleted        = "request.completed"
	RequestFailed           = "request.failed"
	// Queued requests (DESIGN §13.1 "plus"): persisted in the outbox, accepted (202 sent),
	// parked on an open circuit, the payload policy applied, the TTL passed, dead-lettered, and
	// an attempt scheduled for later.
	RequestPersisted    = "request.persisted"
	RequestAccepted     = "request.accepted"
	RequestParked       = "request.parked"
	PolicyApplied       = "policy.applied"
	RequestExpired      = "request.expired"
	RequestDeadLettered = "request.dead_lettered"
	RetryScheduled      = "retry.scheduled"
	// CallbackDelivered records one outbound callback attempt (internal/callbacks, HUB-120):
	// outcome and status in meta, payload hash and size only, never the signature or secret.
	CallbackDelivered = "callback.delivered"
)

// PayloadState says what request_events.payload holds.
type PayloadState string

const (
	PayloadNone      PayloadState = "none"      // the event has no payload
	PayloadFull      PayloadState = "full"      // raw bytes as seen
	PayloadMasked    PayloadState = "masked"    // x-pii values masked
	PayloadSealed    PayloadState = "sealed"    // field-level encrypted (HUB-208)
	PayloadOmitted   PayloadState = "omitted"   // metadata_only: hash and size only
	PayloadTruncated PayloadState = "truncated" // first MaxInlinePayload bytes; the rest at PayloadRef
)

// Event is one request_events row. SHA256 and Bytes describe the payload exactly as the Hub saw
// it, whatever Payload stores (DESIGN §13.3).
type Event struct {
	At           time.Time
	RequestID    string
	Seq          int
	Type         string
	AttemptNo    int // 0 = not attempt-scoped
	SHA256       string
	Bytes        int
	Payload      []byte
	PayloadState PayloadState
	PayloadRef   string
	Meta         map[string]any
}

// Record is the requests row: the final record of one request (DESIGN §13.1).
type Record struct {
	RequestID          string
	CreatedAt          time.Time
	TenantID           string
	IntegrationID      string
	IntegrationVersion int
	Operation          string
	Mode               string
	DryRun             bool
	Status             string
	PayloadPolicy      string
	SnapshotVersion    uint64
	IdempotencyKey     string
	TraceID            string
	HTTPStatus         int
	ErrorCode          string
	Attempts           int
	HubOverheadMS      float64
	VendorMS           float64
	QueueWaitMS        float64
	CompletedAt        time.Time
}

// Attempt is one request_attempts row.
type Attempt struct {
	StartedAt  time.Time
	RequestID  string
	AttemptNo  int
	AccountID  string
	EndpointID string
	// IntegrationVersion is the version this attempt used (re-resolved per attempt, DESIGN §6.3).
	IntegrationVersion int
	Host               string
	DurationMS         float64
	HTTPStatus         int
	ErrorClass         string
}

// Sink receives audit rows. Emit must never block the request path (DESIGN §18.5 rule 3).
type Sink interface {
	Emit(Batch)
}

// Batch is the rows of one request (or one stage of it), written together.
type Batch struct {
	Events   []Event
	Records  []Record
	Attempts []Attempt
}

func (b *Batch) len() int { return len(b.Events) + len(b.Records) + len(b.Attempts) }

func (b *Batch) append(o Batch) {
	b.Events = append(b.Events, o.Events...)
	b.Records = append(b.Records, o.Records...)
	b.Attempts = append(b.Attempts, o.Attempts...)
}

// Discard is a Sink that drops everything (tests, tools).
type Discard struct{}

// Emit drops b.
func (Discard) Emit(Batch) {}
