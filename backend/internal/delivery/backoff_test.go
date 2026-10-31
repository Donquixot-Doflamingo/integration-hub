package delivery

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// TestJitterWithinSchedule is a property test: for many random draws, every delay lies in
// [0, step] (full jitter) and attempts stop after the schedule.
func TestJitterWithinSchedule(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test
	b := Backoff{Rand: rng.Float64}
	for range 10_000 {
		n := 1 + rng.IntN(len(DefaultSchedule)-1)
		d, ok := b.Delay(n, 0)
		if !ok || d < 0 || d > DefaultSchedule[n-1] {
			t.Fatalf("attempt %d: %s (bound %s)", n, d, DefaultSchedule[n-1])
		}
	}
	if _, ok := b.Delay(len(DefaultSchedule), 0); ok {
		t.Fatal("attempt 8 must be the last")
	}
	if b.MaxAttempts() != 8 {
		t.Fatal("8 attempts (DESIGN §10.3)")
	}
	// Edges of the jitter range.
	if d, _ := (Backoff{Rand: func() float64 { return 0 }}).Delay(3, 0); d != 0 {
		t.Fatal(d)
	}
	if d, _ := (Backoff{Rand: func() float64 { return 0.999999 }}).Delay(1, 0); d > time.Second {
		t.Fatal(d)
	}
}

func TestRetryAfterIsAFloor(t *testing.T) {
	b := Backoff{Rand: func() float64 { return 0.1 }}
	d, ok := b.Delay(1, 10*time.Second) // schedule step 1 s; vendor says 10 s
	if !ok || d < 10*time.Second {
		t.Fatalf("429 Retry-After 10 s waited %s", d)
	}
}

func TestRetryable(t *testing.T) {
	for code, want := range map[core.Code]bool{
		core.CodeUpstreamTimeout: true, core.CodeUpstreamUnavailable: true, core.CodeUpstreamRateLimited: true,
		core.CodeUpstreamAuthFailed: false, core.CodeUpstreamRejected: false, core.CodeTransformFailed: false,
	} {
		if Retryable(&core.CanonicalError{Code: code}) != want {
			t.Errorf("%s", code)
		}
	}
	if !Retryable(&core.CanonicalError{Code: core.CodeInternalError, Retryable: true}) || Retryable(&core.CanonicalError{Code: core.CodeInternalError}) {
		t.Error("internal errors follow their retryable flag")
	}
}

func TestBudget(t *testing.T) {
	reg := prometheus.NewRegistry()
	b := NewBudget(reg)
	b.Burst = 2
	for range 2 {
		if !b.AllowRetry("a") {
			t.Fatal("burst")
		}
	}
	if b.AllowRetry("a") {
		t.Fatal("budget spent: retry must be deferred")
	}
	for range 5 { // 5 live attempts earn 1 retry at 20 %
		b.Live("a")
	}
	if !b.AllowRetry("a") || b.AllowRetry("a") {
		t.Fatal("20 % of live traffic")
	}
	if !b.AllowRetry("b") {
		t.Fatal("accounts are independent")
	}
	if got := testutil.ToFloat64(b.exhausted.WithLabelValues("a")); got != 2 {
		t.Fatalf("retry_budget_exhausted_total %v", got)
	}
}
