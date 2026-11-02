package upstream

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBreakerHolds(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		run   func(b *Breaker) (ok bool, state BreakerState, forced Force)
		ok    bool
		state BreakerState
		force Force
	}{
		{"force-open refuses until cleared", func(b *Breaker) (bool, BreakerState, Force) {
			b.Hold(now, ForceOpen, time.Time{})
			ok, _, _ := b.Allow(now.Add(time.Hour)) // no end: still held after any open window
			return ok, b.Snapshot().State, b.Snapshot().Forced
		}, false, Open, ForceOpen},
		{"force-open ends at its time with a probe", func(b *Breaker) (bool, BreakerState, Force) {
			b.Hold(now, ForceOpen, now.Add(time.Minute))
			if ok, _, _ := b.Allow(now.Add(30 * time.Second)); ok {
				return true, 0, 0
			}
			ok, probe, _ := b.Allow(now.Add(2 * time.Minute))
			if !probe {
				return false, 0, 0
			}
			return ok, b.Snapshot().State, b.Snapshot().Forced
		}, true, HalfOpen, ForceNone},
		{"force-close never trips", func(b *Breaker) (bool, BreakerState, Force) {
			b.Hold(now, ForceClosed, time.Time{})
			record(b, now, OutcomeTimeout, 50)
			ok, _, _ := b.Allow(now)
			return ok, b.Snapshot().State, b.Snapshot().Forced
		}, true, Closed, ForceClosed},
		{"force-close ends, then trips again", func(b *Breaker) (bool, BreakerState, Force) {
			b.Hold(now, ForceClosed, now.Add(time.Second))
			record(b, now.Add(2*time.Second), OutcomeTimeout, 5)
			ok, _, _ := b.Allow(now.Add(2 * time.Second))
			return ok, b.Snapshot().State, b.Snapshot().Forced
		}, false, Open, ForceNone},
		{"clear a force-open: half-open at once", func(b *Breaker) (bool, BreakerState, Force) {
			b.Hold(now, ForceOpen, time.Time{})
			b.Hold(now, ForceNone, time.Time{})
			ok, probe, _ := b.Allow(now)
			return ok && probe, b.Snapshot().State, b.Snapshot().Forced
		}, true, HalfOpen, ForceNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var changes int
			b := NewBreaker(policy(), func(_, _ BreakerSnapshot, local bool) { changes++ })
			ok, state, forced := c.run(b)
			if ok != c.ok || state != c.state || forced != c.force || changes == 0 {
				t.Fatalf("ok %v state %s forced %s changes %d", ok, state, forced, changes)
			}
		})
	}
}

func TestHeldVersionAndWireForm(t *testing.T) {
	now := time.Now()
	prev := BreakerSnapshot{State: Open, Version: now.UnixMilli() + 5, Reopens: 2}
	next := Held(prev, ForceOpen, now.Add(time.Minute), now)
	if next.Version <= prev.Version || next.Reopens != 2 || !next.Held(now) || next.Held(now.Add(2*time.Minute)) {
		t.Fatalf("held %+v", next)
	}
	b, _ := json.Marshal(next)
	var back BreakerSnapshot
	if err := json.Unmarshal(b, &back); err != nil || back.Forced != ForceOpen || !back.ForcedUntil.Equal(next.ForcedUntil) || back.State != Open {
		t.Fatalf("round trip %s %+v %v", b, back, err)
	}
	// Old records (no forced field) read as not held; unforced records stay compact.
	var old BreakerSnapshot
	_ = json.Unmarshal([]byte(`{"state":"open","open_until":"2026-10-31T09:41:00Z","reopens":1,"version":1}`), &old)
	if old.Forced != ForceNone {
		t.Fatal("old record forced")
	}
	if b, _ := json.Marshal(BreakerSnapshot{State: Closed}); string(b) != `{"state":"closed","open_until":"0001-01-01T00:00:00Z","reopens":0,"version":0}` {
		t.Fatalf("wire form changed: %s", b)
	}
}
