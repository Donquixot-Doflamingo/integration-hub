//go:build integration

package upstream

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// TestAccountRateAcrossWorkers: three workers (three limiters, one Redis bucket) together stay
// within 5 % of the account rate (HUB-147).
func TestAccountRateAcrossWorkers(t *testing.T) {
	account := "acct-rate-" + core.NewULID(time.Now())
	const rps = 50
	var granted atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	measureFrom := start.Add(time.Second) // after the initial burst (one second of tokens)
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	var counted atomic.Int64
	for range 3 {
		l := NewAccountLimiter(redisClient(t, redisAddr()), telemetry.NewMetrics("itest", "limiter"), nil)
		wg.Go(func() {
			for ctx.Err() == nil {
				if ok, _ := l.Wait(ctx, account, rps, true, time.Second); ok {
					granted.Add(1)
					if time.Now().After(measureFrom) {
						counted.Add(1)
					}
				}
			}
		})
	}
	wg.Wait()
	window := time.Since(measureFrom).Seconds()
	rate := float64(counted.Load()) / window
	t.Logf("3 workers: %.1f grants/s over %.1f s (account rate %d)", rate, window, rps)
	if rate < rps*0.95 || rate > rps*1.05 {
		t.Fatalf("aggregate rate %.1f/s, want %d ± 5 %%", rate, rps)
	}
}

// TestLiveTrafficFirst: when live traffic and retries/drain compete for one account, live
// requests get their tokens; the others only what is above the reserve (HUB-147).
func TestLiveTrafficFirst(t *testing.T) {
	account := "acct-prio-" + core.NewULID(time.Now())
	l := NewAccountLimiter(redisClient(t, redisAddr()), telemetry.NewMetrics("itest", "limiter"), nil)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	var live, drain atomic.Int64
	var wg sync.WaitGroup
	for _, isLive := range []bool{true, false} {
		wg.Go(func() {
			for ctx.Err() == nil {
				if ok, _ := l.Wait(ctx, account, 20, isLive, 200*time.Millisecond); ok {
					if isLive {
						live.Add(1)
					} else {
						drain.Add(1)
					}
				}
			}
		})
	}
	wg.Wait()
	t.Logf("live %d, drain %d", live.Load(), drain.Load())
	if live.Load() <= drain.Load() {
		t.Fatalf("live traffic got %d tokens, drain %d: live must come first", live.Load(), drain.Load())
	}
}

// TestBulkheadPerAccount: the 3rd concurrent call to an account with a bulkhead of 2 waits for
// a free slot; another account is not slowed down (HUB-148).
func TestBulkheadPerAccount(t *testing.T) {
	mv := mockvendor.Start(t)
	mv.SetRoute(t, "slow", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 300}}})
	c, err := New(telemetry.NewMetrics("itest", "bulkhead"), Options{Bulkhead: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	vendor := &registry.Vendor{Vendor: "clevertap", Regions: map[string]string{"in": mv.VendorURL}}
	target := func(acct string) Target {
		return Target{AccountID: acct, Region: "in", Vendor: vendor, EndpointName: "upload@1",
			Endpoint: registry.Endpoint{Method: http.MethodPost, Path: "/1/upload", TimeoutMS: 3000}, BaseURL: mv.VendorURL}
	}
	req := core.VendorRequest{Method: http.MethodPost, Path: "/1/upload", Body: map[string]any{}}
	durations := make([]time.Duration, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		acct := "acct-a"
		if i == 3 {
			acct = "acct-b"
		}
		wg.Go(func() {
			start := time.Now()
			if _, err := c.Do(t.Context(), target(acct), req); err != nil {
				t.Error(err)
			}
			durations[i] = time.Since(start)
		})
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	slowest := max(durations[0], durations[1], durations[2])
	if slowest < 550*time.Millisecond {
		t.Fatalf("3 concurrent calls with a bulkhead of 2 took %v: the 3rd did not wait", durations[:3])
	}
	if durations[3] > 450*time.Millisecond {
		t.Fatalf("another account waited %s", durations[3])
	}
}
