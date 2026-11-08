//go:build integration

package delivery

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// TestDrainHoldsWhileBreakerStateIsUnknown is HUB-453: with the shared breaker state unreadable
// (Redis down) the drain used to assume "long closed" and release parked envelopes at the full
// account rate, against a vendor whose circuit may be open. It now holds, and resumes once the
// state is readable again.
func TestDrainHoldsWhileBreakerStateIsUnknown(t *testing.T) {
	r, snap := emptyRig(t)
	r.snaps.s.Store(snap)
	addr := os.Getenv("HUB_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	var down atomic.Bool
	down.Store(true)
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 13, MaxRetries: -1, DialTimeout: 200 * time.Millisecond,
		ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond,
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			if down.Load() {
				return nil, errors.New("redis is down")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}})
	t.Cleanup(func() { _ = rdb.Close() })
	env := r.enqueueWith("int_evt_brandA", 6*time.Hour, nil, true)
	reg := prometheus.NewRegistry()
	res := &Resilience{Breakers: upstream.NewBreakers(rdb, telemetry.NewMetrics("scheduler", "iso"), r.w.Log), Snapshots: r.snaps,
		Queue: r.w.Queue, Log: r.w.Log, Now: r.now}
	res.RegisterMetrics(reg)

	for range 3 {
		res.Tick(t.Context(), r.db, time.Second)
		r.advance(time.Second)
	}
	if status, _, _, _ := r.outboxRow(env.RequestID); status != "parked" {
		t.Fatalf("released while the breaker state was unreadable: %s", status)
	}
	if got := testutil.ToFloat64(res.held.WithLabelValues(heldBreakerStateUnknown)); got != 1 {
		t.Fatalf("drain_held = %v while held, want 1", got)
	}

	down.Store(false)
	res.Tick(t.Context(), r.db, time.Second)
	if status, _, _, _ := r.outboxRow(env.RequestID); status != "retry_scheduled" {
		t.Fatalf("after the state became readable: %s, want released", status)
	}
	if got := testutil.ToFloat64(res.held.WithLabelValues(heldBreakerStateUnknown)); got != 0 {
		t.Fatalf("drain_held = %v after recovery, want 0", got)
	}
}
