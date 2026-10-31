package core

import (
	"testing"
	"time"
)

// TestTransitions: every allowed transition succeeds and every other pair is refused
// (HUB-111, DESIGN §10.9).
func TestTransitions(t *testing.T) {
	allowed := map[[2]State]bool{
		{StateAccepted, StateInFlight}: true, {StateAccepted, StateExpired}: true,
		{StateInFlight, StateSucceeded}: true, {StateInFlight, StateRetryScheduled}: true, {StateInFlight, StateParked}: true,
		{StateInFlight, StateDeadLettered}: true, {StateInFlight, StateExpired}: true,
		{StateRetryScheduled, StateInFlight}: true, {StateRetryScheduled, StateExpired}: true,
		{StateParked, StateRetryScheduled}: true, {StateParked, StateExpired}: true,
		{StateDeadLettered, StateAccepted}: true,
	}
	n := 0
	for _, from := range AllStates() {
		for _, to := range AllStates() {
			if got := CanTransition(from, to); got != allowed[[2]State{from, to}] {
				t.Errorf("%s → %s: %v", from, to, got)
			}
			n++
		}
	}
	if n != 64 {
		t.Fatalf("checked %d pairs", n)
	}
	for _, s := range []State{StateSucceeded, StateExpired, StateFailed} {
		if !s.Terminal() {
			t.Errorf("%s must be terminal", s)
		}
		for _, to := range AllStates() {
			if CanTransition(s, to) {
				t.Errorf("terminal %s leaves to %s", s, to)
			}
		}
	}
}

func TestRequestIDTime(t *testing.T) {
	at := time.Date(2026, 10, 31, 9, 15, 0, 123e6, time.UTC)
	got, ok := RequestIDTime("req_" + NewULID(at))
	if !ok || !got.Equal(at) {
		t.Fatalf("%v %v", got, ok)
	}
	for _, bad := range []string{"", "req_short", "evt_" + NewULID(at), "req_" + "U" + NewULID(at)[1:]} {
		if _, ok := RequestIDTime(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
