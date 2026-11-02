package upstream

import (
	"sync"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// BreakerState is a circuit state (metric breaker_state: 0 closed, 1 half_open, 2 open).
type BreakerState int8

const (
	Closed BreakerState = iota
	HalfOpen
	Open
)

func (s BreakerState) String() string {
	switch s {
	case HalfOpen:
		return "half_open"
	case Open:
		return "open"
	case Closed:
	}
	return "closed"
}

// ParseBreakerState is the inverse of String.
func ParseBreakerState(s string) BreakerState {
	switch s {
	case "half_open":
		return HalfOpen
	case "open":
		return Open
	}
	return Closed
}

// Outcome is what one vendor call means for its breaker (TECH_SPEC §10 weights).
type Outcome int8

const (
	OutcomeSuccess     Outcome = iota // 2xx, and outcomes that are not outages (4xx, auth, transform)
	OutcomeFailure                    // 5xx, refused, reset, DNS, TLS: weight 1
	OutcomeTimeout                    // weight 1, and counts toward the consecutive-timeout trip
	OutcomeRateLimited                // 429: weight 0.5
	OutcomeIgnore                     // Hub-side outcomes (no vendor call, caller canceled)
)

// BreakerPolicy is DESIGN §10.4 with the vendor's defaults (registry.BreakerConfig).
type BreakerPolicy struct {
	Window              time.Duration // 30 s
	MinCalls            int           // 20
	FailureRatio        float64       // 0.5 (strictly greater trips)
	ConsecutiveTimeouts int           // 5; 0 disables
	Open                time.Duration // 30 s, doubling per re-open
	OpenMax             time.Duration // 10 min
	HalfOpenProbes      int           // 3
	// RatioDisabled leaves only the consecutive-timeout trip (integration overrides).
	RatioDisabled bool
}

// PolicyFrom converts a vendor's breaker config, filling DESIGN §10.4 defaults.
func PolicyFrom(c registry.BreakerConfig) BreakerPolicy {
	p := BreakerPolicy{
		Window: time.Duration(c.WindowS) * time.Second, MinCalls: c.MinCalls, FailureRatio: c.FailureRatio,
		ConsecutiveTimeouts: c.ConsecutiveTimeouts, Open: time.Duration(c.OpenS) * time.Second,
		OpenMax: time.Duration(c.OpenMaxS) * time.Second, HalfOpenProbes: c.HalfOpenProbes,
	}
	if p.Window <= 0 {
		p.Window = 30 * time.Second
	}
	if p.MinCalls <= 0 {
		p.MinCalls = 20
	}
	if p.FailureRatio <= 0 {
		p.FailureRatio = 0.5
	}
	if p.Open <= 0 {
		p.Open = 30 * time.Second
	}
	if p.OpenMax <= 0 {
		p.OpenMax = 10 * time.Minute
	}
	if p.HalfOpenProbes <= 0 {
		p.HalfOpenProbes = 3
	}
	return p
}

// BreakerSnapshot is the shareable state of one breaker (Redis, metrics, admin views).
type BreakerSnapshot struct {
	State     BreakerState
	OpenUntil time.Time // when an open breaker lets probes through
	Reopens   int       // re-opens since the last close (doubles the open window)
	Version   int64     // unix ms of the last transition; the newest wins across nodes
	// Forced is an admin hold (HUB-209): open refuses every call, closed admits every call and
	// never trips, until ForcedUntil (zero: until cleared). It travels with the state.
	Forced      Force
	ForcedUntil time.Time
}

// Breaker is the local state machine of one (account, endpoint) — or one integration's
// tightened view of it. Safe for concurrent use; the hot path only takes its mutex.
type Breaker struct {
	mu     sync.Mutex
	policy BreakerPolicy
	snap   BreakerSnapshot
	// rolling window: one bucket per second
	buckets        []bucket
	consecTimeouts int
	probesInFlight int
	probeSuccesses int
	onChange       func(from, to BreakerSnapshot, local bool)
}

type bucket struct {
	sec      int64
	calls    float64
	failures float64
}

// NewBreaker returns a closed breaker. onChange (may be nil) is called outside the lock after
// every transition; local is false for states adopted from another node.
func NewBreaker(p BreakerPolicy, onChange func(from, to BreakerSnapshot, local bool)) *Breaker {
	n := int(p.Window / time.Second)
	if n < 1 {
		n = 1
	}
	return &Breaker{policy: p, buckets: make([]bucket, n), onChange: onChange}
}

// Snapshot returns the current state.
func (b *Breaker) Snapshot() BreakerSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snap
}

// Allow decides whether a call may go out now. An open breaker past its window turns half-open
// and admits up to HalfOpenProbes calls at a time (probe = true: the caller must Record it).
// When refused, retryAfter is the rest of the open window.
func (b *Breaker) Allow(now time.Time) (ok bool, probe bool, retryAfter time.Duration) {
	b.mu.Lock()
	var change *[2]BreakerSnapshot
	defer func() {
		b.mu.Unlock()
		if change != nil && b.onChange != nil {
			b.onChange(change[0], change[1], true)
		}
	}()
	if b.snap.Forced != ForceNone {
		if b.forceActive(now) {
			if b.snap.Forced == ForceClosed {
				return true, false, 0
			}
			retry := 30 * time.Second
			if !b.snap.ForcedUntil.IsZero() {
				retry = b.snap.ForcedUntil.Sub(now)
			}
			return false, false, retry
		}
		b.expireForce()
	}
	switch b.snap.State {
	case Closed:
		return true, false, 0
	case Open:
		if now.Before(b.snap.OpenUntil) {
			return false, false, b.snap.OpenUntil.Sub(now)
		}
		from := b.snap
		b.snap = BreakerSnapshot{State: HalfOpen, Reopens: from.Reopens, Version: now.UnixMilli()}
		b.probesInFlight, b.probeSuccesses = 0, 0
		change = &[2]BreakerSnapshot{from, b.snap}
	case HalfOpen:
	}
	if b.probesInFlight >= b.policy.HalfOpenProbes {
		return false, false, time.Second
	}
	b.probesInFlight++
	return true, true, 0
}

// Record adds the outcome of a call admitted by Allow (probe as Allow returned it).
func (b *Breaker) Record(now time.Time, o Outcome, probe bool) {
	if o == OutcomeIgnore {
		if probe {
			b.mu.Lock()
			b.probesInFlight = max(b.probesInFlight-1, 0)
			b.mu.Unlock()
		}
		return
	}
	b.mu.Lock()
	var change *[2]BreakerSnapshot
	defer func() {
		b.mu.Unlock()
		if change != nil && b.onChange != nil {
			b.onChange(change[0], change[1], true)
		}
	}()
	failed := o != OutcomeSuccess
	if probe {
		b.probesInFlight = max(b.probesInFlight-1, 0)
	}
	if b.snap.State == HalfOpen {
		if failed && o != OutcomeRateLimited { // any probe failure re-opens
			change = b.open(now, true)
			return
		}
		if !failed {
			b.probeSuccesses++
			if b.probeSuccesses >= b.policy.HalfOpenProbes {
				from := b.snap
				// Reopens is kept: a trip soon after this recovery is a re-open (flapping vendor).
				b.snap = BreakerSnapshot{State: Closed, Reopens: from.Reopens, Version: now.UnixMilli()}
				b.reset()
				change = &[2]BreakerSnapshot{from, b.snap}
			}
		}
		return
	}
	if b.snap.State == Open {
		return // late result of a call admitted before the trip
	}
	bk := b.bucket(now)
	bk.calls++
	switch o {
	case OutcomeTimeout:
		bk.failures++
		b.consecTimeouts++
	case OutcomeFailure:
		bk.failures++
		b.consecTimeouts = 0
	case OutcomeRateLimited:
		bk.failures += 0.5
		b.consecTimeouts = 0
	case OutcomeSuccess, OutcomeIgnore:
		b.consecTimeouts = 0
	}
	if b.snap.Forced == ForceClosed && b.forceActive(now) {
		return // counted, never tripped while forced closed
	}
	if b.policy.ConsecutiveTimeouts > 0 && b.consecTimeouts >= b.policy.ConsecutiveTimeouts {
		change = b.open(now, false)
		return
	}
	if b.policy.RatioDisabled {
		return
	}
	calls, fails := b.totals(now)
	if calls >= float64(b.policy.MinCalls) && fails/calls > b.policy.FailureRatio {
		change = b.open(now, false)
	}
}

// open trips the breaker. A re-open doubles the window (capped): a failed half-open probe, or a
// trip within OpenMax of the last recovery (re-open mid-drain, DESIGN §10.4/§10.5).
func (b *Breaker) open(now time.Time, reopen bool) *[2]BreakerSnapshot {
	from := b.snap
	recovered := from.State == Closed && from.Version > 0 && now.Sub(time.UnixMilli(from.Version)) < b.policy.OpenMax
	reopens := 0
	if reopen || recovered {
		reopens = from.Reopens + 1
	}
	d := b.policy.Open
	for range reopens {
		d *= 2
		if d >= b.policy.OpenMax {
			d = b.policy.OpenMax
			break
		}
	}
	b.snap = BreakerSnapshot{State: Open, OpenUntil: now.Add(d), Reopens: reopens, Version: now.UnixMilli()}
	b.reset()
	return &[2]BreakerSnapshot{from, b.snap}
}

func (b *Breaker) reset() {
	for i := range b.buckets {
		b.buckets[i] = bucket{}
	}
	b.consecTimeouts, b.probesInFlight, b.probeSuccesses = 0, 0, 0
}

func (b *Breaker) bucket(now time.Time) *bucket {
	sec := now.Unix()
	bk := &b.buckets[int(sec%int64(len(b.buckets)))]
	if bk.sec != sec {
		*bk = bucket{sec: sec}
	}
	return bk
}

func (b *Breaker) totals(now time.Time) (calls, fails float64) {
	from := now.Unix() - int64(len(b.buckets)) + 1
	for _, bk := range b.buckets {
		if bk.sec >= from {
			calls += bk.calls
			fails += bk.failures
		}
	}
	return calls, fails
}

// Adopt applies a newer state from another node (Redis). Returns false when s is not newer.
func (b *Breaker) Adopt(s BreakerSnapshot) bool {
	b.mu.Lock()
	if s.Version <= b.snap.Version {
		b.mu.Unlock()
		return false
	}
	from := b.snap
	b.snap = s
	b.reset()
	b.mu.Unlock()
	if b.onChange != nil {
		b.onChange(from, s, false)
	}
	return true
}

// Force sets a state (admin force-open/close, probe results): a closed or open breaker with the
// given window.
func (b *Breaker) Force(now time.Time, state BreakerState, openFor time.Duration) {
	b.mu.Lock()
	from := b.snap
	b.snap = BreakerSnapshot{State: state, Version: now.UnixMilli(), Reopens: from.Reopens}
	if state == Open {
		b.snap.OpenUntil = now.Add(openFor)
	}
	if state == Closed {
		b.snap.Reopens = 0
	}
	b.reset()
	b.mu.Unlock()
	if b.onChange != nil {
		b.onChange(from, b.snap, true)
	}
}
