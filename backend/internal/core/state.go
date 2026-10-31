package core

import (
	"encoding/json"
	"strings"
	"time"
)

// State is a request state (DESIGN §10.9, contracts RequestState).
type State string

const (
	StateAccepted       State = "accepted"
	StateInFlight       State = "in_flight"
	StateSucceeded      State = "succeeded"
	StateRetryScheduled State = "retry_scheduled"
	StateParked         State = "parked"
	StateDeadLettered   State = "dead_lettered"
	StateExpired        State = "expired"
	// StateFailed is the terminal state of a sync request that returned an error; no queue.
	StateFailed State = "failed"
)

// transitions is DESIGN §10.9 as a table: from → allowed targets.
var transitions = map[State][]State{
	StateAccepted:       {StateInFlight, StateExpired},
	StateInFlight:       {StateSucceeded, StateRetryScheduled, StateParked, StateDeadLettered, StateExpired},
	StateRetryScheduled: {StateInFlight, StateExpired},
	StateParked:         {StateRetryScheduled, StateExpired},
	StateDeadLettered:   {StateAccepted}, // operator replay
}

// CanTransition reports whether from → to is allowed. Terminal states (succeeded, expired,
// failed) have no way out; dead_lettered only goes back to accepted on a replay.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Terminal reports whether s ends the request (a callback fires; the outbox row is gone).
func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateExpired || s == StateFailed || s == StateDeadLettered
}

// AllStates lists every state, for tables and tests.
func AllStates() []State {
	return []State{StateAccepted, StateInFlight, StateSucceeded, StateRetryScheduled, StateParked, StateDeadLettered, StateExpired, StateFailed}
}

// Envelope is one queued request (DESIGN §11.3). It holds the canonical body and the
// integration id, never the vendor payload, so the worker re-resolves and re-transforms at send
// time (cutover, transformer fixes, failover).
type Envelope struct {
	RequestID       string            `json:"request_id"`
	Tenant          string            `json:"tenant"`
	IntegrationID   string            `json:"integration_id"`
	ResolvedVersion int               `json:"resolved_version"`
	Pinned          bool              `json:"pinned"`
	SessionID       *string           `json:"session_id"`
	Capability      string            `json:"capability"`
	Operation       string            `json:"operation"`
	Canonical       json.RawMessage   `json:"canonical"`
	Mode            string            `json:"mode"`
	IdempotencyKey  *string           `json:"idempotency_key"`
	OrderingKey     *string           `json:"ordering_key"`
	Attempt         int               `json:"attempt"`
	NextAttemptAt   time.Time         `json:"next_attempt_at"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Trace           map[string]string `json:"trace,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
}

// RequestIDTime returns the creation time encoded in a request id's ULID (millisecond
// precision). It finds the day partition of a request without an index lookup.
func RequestIDTime(id string) (time.Time, bool) {
	u, ok := strings.CutPrefix(id, "req_")
	if !ok || len(u) != 26 {
		return time.Time{}, false
	}
	var ms uint64
	for i := range 10 { // the first 10 chars hold the 48-bit timestamp (50 bits, top 2 zero)
		c := strings.IndexByte(crockford, u[i])
		if c < 0 {
			return time.Time{}, false
		}
		ms = ms<<5 | uint64(c)
	}
	return time.UnixMilli(int64(ms)).UTC(), true //nolint:gosec // 48-bit value
}
