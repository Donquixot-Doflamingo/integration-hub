package hubclient

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
)

// RequestState is the lifecycle state of a request (DESIGN §10.9).
type RequestState string

// Values of RequestStatus.Status. The contract may add values.
const (
	StateAccepted       RequestState = "accepted"
	StateInFlight       RequestState = "in_flight"
	StateSucceeded      RequestState = "succeeded"
	StateRetryScheduled RequestState = "retry_scheduled"
	StateParked         RequestState = "parked"
	StateDeadLettered   RequestState = "dead_lettered"
	StateExpired        RequestState = "expired"
	StateFailed         RequestState = "failed"
)

// Terminal reports whether no further state change is expected (an operator replay may still
// move dead_lettered back to accepted).
func (s RequestState) Terminal() bool {
	switch s {
	case StateSucceeded, StateDeadLettered, StateExpired, StateFailed:
		return true
	default:
		return false
	}
}

// RequestStatus is the state and final outcome of a request (operation getRequestStatus).
type RequestStatus struct {
	RequestID      string
	IntegrationID  string
	Capability     string
	Operation      string
	Mode           string // delivery mode: sync, wait, async, async_callback
	Status         RequestState
	IdempotencyKey string // empty when none was sent
	Attempts       int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    *time.Time // set when terminal
	NextAttemptAt  *time.Time
	ExpiresAt      *time.Time
	// Result is the canonical response when Status is succeeded, else nil.
	Result json.RawMessage
	// Error is the canonical error when Status is failed, dead_lettered or expired, else nil.
	// Its Status field is 0: it did not come with an HTTP error status.
	Error *Error
}

// UnmarshalJSON decodes the contract's RequestStatus object.
func (r *RequestStatus) UnmarshalJSON(b []byte) error {
	var w struct {
		RequestID      string          `json:"request_id"`
		IntegrationID  string          `json:"integration_id"`
		Capability     string          `json:"capability"`
		Operation      string          `json:"operation"`
		Mode           string          `json:"mode"`
		Status         RequestState    `json:"status"`
		IdempotencyKey *string         `json:"idempotency_key"`
		Attempts       int             `json:"attempts"`
		CreatedAt      time.Time       `json:"created_at"`
		UpdatedAt      time.Time       `json:"updated_at"`
		CompletedAt    *time.Time      `json:"completed_at"`
		NextAttemptAt  *time.Time      `json:"next_attempt_at"`
		ExpiresAt      *time.Time      `json:"expires_at"`
		Result         json.RawMessage `json:"result"`
		Error          *wireError      `json:"error"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = RequestStatus{
		RequestID:      w.RequestID,
		IntegrationID:  w.IntegrationID,
		Capability:     w.Capability,
		Operation:      w.Operation,
		Mode:           w.Mode,
		Status:         w.Status,
		IdempotencyKey: deref(w.IdempotencyKey),
		Attempts:       w.Attempts,
		CreatedAt:      w.CreatedAt,
		UpdatedAt:      w.UpdatedAt,
		CompletedAt:    w.CompletedAt,
		NextAttemptAt:  w.NextAttemptAt,
		ExpiresAt:      w.ExpiresAt,
	}
	if len(w.Result) > 0 && !bytes.Equal(w.Result, []byte("null")) {
		r.Result = w.Result
	}
	if w.Error != nil {
		r.Error = w.Error.toError(0)
	}
	return nil
}

// GetRequest returns the status of a request made by the calling tenant (operation
// getRequestStatus), typically the RequestID of a *Queued. Poll with backoff (start at 500 ms,
// cap at 30 s) or use callbacks. An unknown request, or one of another tenant, is an *Error with
// CodeRequestNotFound.
func (c *Client) GetRequest(ctx context.Context, requestID string) (*RequestStatus, error) {
	id, err := segment("request id", requestID)
	if err != nil {
		return nil, err
	}
	var st RequestStatus
	if err := c.getJSON(ctx, "/v1/requests/"+id, nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}
