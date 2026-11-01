package upstream

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func policy() BreakerPolicy {
	return BreakerPolicy{Window: 30 * time.Second, MinCalls: 20, FailureRatio: 0.5, ConsecutiveTimeouts: 5,
		Open: 30 * time.Second, OpenMax: 10 * time.Minute, HalfOpenProbes: 3}
}

func record(b *Breaker, now time.Time, o Outcome, n int) {
	for range n {
		ok, probe, _ := b.Allow(now)
		if !ok {
			return
		}
		b.Record(now, o, probe)
	}
}

func TestBreakerTrips(t *testing.T) {
	cases := []struct {
		name  string
		feed  func(b *Breaker)
		state BreakerState
	}{
		{"below min calls stays closed", func(b *Breaker) { record(b, t0, OutcomeFailure, 19) }, Closed},
		{"exactly 50 % failures stays closed", func(b *Breaker) {
			record(b, t0, OutcomeFailure, 10)
			record(b, t0, OutcomeSuccess, 10)
		}, Closed},
		{"over 50 % with 20 calls opens", func(b *Breaker) {
			record(b, t0, OutcomeSuccess, 9)
			record(b, t0, OutcomeFailure, 11)
		}, Open},
		{"429 counts half: 20 rate limits = 10 weighted failures stays closed", func(b *Breaker) {
			record(b, t0, OutcomeRateLimited, 20)
		}, Closed},
		{"429 half weight plus failures trips", func(b *Breaker) {
			record(b, t0, OutcomeRateLimited, 12) // 6
			record(b, t0, OutcomeFailure, 5)      // 11 of 20 calls (weighted 11)
			record(b, t0, OutcomeSuccess, 3)
		}, Open},
		{"5 consecutive timeouts open with few calls", func(b *Breaker) { record(b, t0, OutcomeTimeout, 5) }, Open},
		{"a success resets the timeout run", func(b *Breaker) {
			record(b, t0, OutcomeTimeout, 4)
			record(b, t0, OutcomeSuccess, 1)
			record(b, t0, OutcomeTimeout, 4)
		}, Closed},
		{"failures older than the window fall out", func(b *Breaker) {
			record(b, t0, OutcomeFailure, 15)
			record(b, t0.Add(31*time.Second), OutcomeSuccess, 10)
		}, Closed},
		{"ignored outcomes do not count", func(b *Breaker) { record(b, t0, OutcomeIgnore, 50) }, Closed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBreaker(policy(), nil)
			tc.feed(b)
			if got := b.Snapshot().State; got != tc.state {
				t.Fatalf("state %s, want %s", got, tc.state)
			}
		})
	}
}

func TestOpenHalfOpenClosed(t *testing.T) {
	var transitions []string
	b := NewBreaker(policy(), func(from, to BreakerSnapshot, _ bool) {
		transitions = append(transitions, from.State.String()+"→"+to.State.String())
	})
	record(b, t0, OutcomeTimeout, 5)
	if ok, _, retry := b.Allow(t0.Add(10 * time.Second)); ok || retry != 20*time.Second {
		t.Fatalf("open must refuse with the rest of the window: %v %s", ok, retry)
	}
	at := t0.Add(30 * time.Second)
	var probes []bool
	for range 4 {
		ok, probe, _ := b.Allow(at)
		probes = append(probes, ok && probe)
	}
	if probes[0] != true || probes[2] != true || probes[3] != false {
		t.Fatalf("half-open admits 3 probes at a time: %v", probes)
	}
	for range 3 {
		b.Record(at, OutcomeSuccess, true)
	}
	if s := b.Snapshot(); s.State != Closed || s.Reopens != 0 {
		t.Fatalf("3 probe successes close: %+v", s)
	}
	want := "closed→open,open→half_open,half_open→closed"
	if got := join(transitions); got != want {
		t.Fatalf("transitions %s", got)
	}
}

func TestReopenDoublesAndCaps(t *testing.T) {
	b := NewBreaker(policy(), nil)
	record(b, t0, OutcomeTimeout, 5)
	var now time.Time
	wants := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 480 * time.Second, 10 * time.Minute, 10 * time.Minute}
	for i, want := range wants {
		now = b.Snapshot().OpenUntil
		ok, probe, _ := b.Allow(now)
		if !ok || !probe {
			t.Fatalf("round %d: no probe", i)
		}
		b.Record(now, OutcomeFailure, true) // any probe failure re-opens
		s := b.Snapshot()
		if s.State != Open || s.OpenUntil.Sub(now) != want {
			t.Fatalf("round %d: %s open for %s, want %s", i, s.State, s.OpenUntil.Sub(now), want)
		}
	}
}

func TestAdoptNewerOnly(t *testing.T) {
	b := NewBreaker(policy(), nil)
	if !b.Adopt(BreakerSnapshot{State: Open, OpenUntil: t0.Add(time.Minute), Version: 10}) {
		t.Fatal("newer state refused")
	}
	if b.Adopt(BreakerSnapshot{State: Closed, Version: 9}) {
		t.Fatal("older state adopted")
	}
	if b.Snapshot().State != Open {
		t.Fatal("state")
	}
}

func join(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}
