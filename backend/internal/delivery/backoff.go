package delivery

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// DefaultSchedule is DESIGN §10.3: the delay before attempt n+1 after attempt n failed.
// After the last step the envelope is dead-lettered (8 attempts in all).
var DefaultSchedule = []time.Duration{
	time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute,
	10 * time.Minute, 30 * time.Minute, time.Hour, time.Hour,
}

// Backoff computes retry delays with full jitter (a uniform delay in [0, step]) and never less
// than the vendor's Retry-After.
type Backoff struct {
	Schedule []time.Duration
	// Rand returns a float in [0, 1); nil means math/rand/v2.
	Rand func() float64
}

// MaxAttempts is the number of attempts before an envelope is dead-lettered.
func (b Backoff) MaxAttempts() int { return len(b.schedule()) }

func (b Backoff) schedule() []time.Duration {
	if len(b.Schedule) == 0 {
		return DefaultSchedule
	}
	return b.Schedule
}

// Delay returns the wait after failed attempt n (1-based), or false when attempts are
// exhausted. retryAfter (from a 429's Retry-After) is a floor: max(backoff, Retry-After).
func (b Backoff) Delay(n int, retryAfter time.Duration) (time.Duration, bool) {
	s := b.schedule()
	if n < 1 || n >= len(s) {
		return 0, false
	}
	r := rand.Float64 //nolint:gosec // jitter, not security
	if b.Rand != nil {
		r = b.Rand
	}
	d := time.Duration(r() * float64(s[n-1]))
	return max(d, retryAfter), true
}

// Retryable reports whether a queued attempt's error is worth another attempt (DESIGN §10.2
// async column): timeouts, unavailability, rate limits and transient Hub errors retry; vendor
// rejections, auth failures and transformer errors dead-letter at once, except a transformer
// time-budget overrun (wall clock: usually a stall), which retries on the schedule.
func Retryable(ce *core.CanonicalError) bool {
	if ce.Code == core.CodeInternalError {
		return ce.Retryable
	}
	if ce.Code == core.CodeTransformFailed { // only a time-budget overrun (a stall) retries
		return ce.Reason != nil && *ce.Reason == "time_budget"
	}
	return ce.Code == core.CodeUpstreamTimeout || ce.Code == core.CodeUpstreamUnavailable || ce.Code == core.CodeUpstreamRateLimited
}

// Budget limits retries to a share of each account's live traffic (DESIGN §10.3: retries ≤ 20 %
// of live traffic), so a recovering vendor is not flooded by its backlog. Every first attempt
// earns Ratio tokens (capped at Burst); every retry spends one. Without a token the retry is
// deferred, not dropped.
type Budget struct {
	Ratio float64 // default 0.2
	Burst float64 // default 20: retries a quiet account may still make
	mu    sync.Mutex
	acct  map[string]float64

	exhausted *prometheus.CounterVec
}

// NewBudget returns a budget that counts deferrals in retry_budget_exhausted_total{account}.
func NewBudget(reg prometheus.Registerer) *Budget {
	b := &Budget{Ratio: 0.2, Burst: 20, acct: map[string]float64{}}
	b.exhausted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "retry_budget_exhausted_total",
		Help: "Retries deferred because the account's retry budget (share of live traffic) was spent.",
	}, []string{"account"})
	if reg != nil {
		reg.MustRegister(b.exhausted)
	}
	return b
}

func (b *Budget) tokens(account string) float64 {
	t, ok := b.acct[account]
	if !ok {
		t = b.Burst
	}
	return t
}

// Live records a first attempt on account.
func (b *Budget) Live(account string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acct[account] = min(b.tokens(account)+b.Ratio, b.Burst)
}

// AllowRetry spends a token for a retry on account; false means defer it.
func (b *Budget) AllowRetry(account string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.tokens(account)
	if t < 1 {
		b.exhausted.WithLabelValues(account).Inc()
		return false
	}
	b.acct[account] = t - 1
	return true
}
