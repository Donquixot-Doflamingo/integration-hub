//go:build integration

package upstream

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func redisClient(t *testing.T, addr string) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func redisAddr() string {
	if a := os.Getenv("HUB_REDIS_ADDR"); a != "" {
		return a
	}
	return "localhost:6379"
}

func node(t *testing.T, rdb redis.UniversalClient) *Breakers {
	bs := NewBreakers(rdb, telemetry.NewMetrics("itest", "breakers"), nil)
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { bs.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return bs
}

// TestNodesConverge: a breaker opened on one gateway is open on another within 1 s, and its
// close propagates too (HUB-141).
func TestNodesConverge(t *testing.T) {
	a, b := node(t, redisClient(t, redisAddr())), node(t, redisClient(t, redisAddr()))
	key := BreakerKey{Account: "acct-" + core.NewULID(time.Now()), Endpoint: "clevertap.upload@1"}
	ba, bb := a.Get(key, policy()), b.Get(key, policy())
	time.Sleep(200 * time.Millisecond) // subscriptions up
	start := time.Now()
	record(ba, time.Now(), OutcomeTimeout, 5)
	if ba.Snapshot().State != Open {
		t.Fatal("node A did not open")
	}
	waitState(t, bb, Open, time.Second)
	t.Logf("open reached node B in %s", time.Since(start).Round(time.Millisecond))
	start = time.Now()
	ba.Force(time.Now(), Closed, 0)
	waitState(t, bb, Closed, time.Second)
	t.Logf("close reached node B in %s", time.Since(start).Round(time.Millisecond))
	v, ep := endpointLabels(key)
	if testutil.ToFloat64(b.state.WithLabelValues(v, key.Account, ep)) != 0 || testutil.ToFloat64(a.transitions.WithLabelValues(v, key.Account, ep, "closed", "open")) != 1 {
		t.Fatal("metrics")
	}
}

// TestLearnsBreakersItNeverUsed: a node that has not called the account yet gets the open state
// on its first call (pending state, adopted with the vendor's own policy).
func TestLearnsBreakersItNeverUsed(t *testing.T) {
	a, b := node(t, redisClient(t, redisAddr())), node(t, redisClient(t, redisAddr()))
	key := BreakerKey{Account: "acct-" + core.NewULID(time.Now()), Endpoint: "clevertap.upload@1"}
	time.Sleep(200 * time.Millisecond)
	record(a.Get(key, policy()), time.Now(), OutcomeTimeout, 5)
	time.Sleep(300 * time.Millisecond)
	if ok, _, _ := b.Get(key, policy()).Allow(time.Now()); ok {
		t.Fatal("first call on node B passed an open breaker")
	}
}

// TestRedisDownNeverBlocks: with Redis unreachable, the request path keeps working on local
// state and never waits on Redis (HUB-141).
func TestRedisDownNeverBlocks(t *testing.T) {
	bs := node(t, redisClient(t, "127.0.0.1:1"))
	key := BreakerKey{Account: "acct-down", Endpoint: "clevertap.upload@1"}
	b := bs.Get(key, policy())
	start := time.Now()
	for range 1000 {
		ok, probe, _ := b.Allow(time.Now())
		if ok {
			b.Record(time.Now(), OutcomeSuccess, probe)
		}
	}
	record(b, time.Now(), OutcomeTimeout, 5)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("1005 calls took %s with Redis down", d)
	}
	if b.Snapshot().State != Open {
		t.Fatal("local state must still trip")
	}
}

func waitState(t *testing.T, b *Breaker, want BreakerState, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for b.Snapshot().State != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s within %s", b.Snapshot().State, want, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestForceReachesEveryNodeWithinOneSecond is HUB-209: an admin hold written to Redis (the
// admin role has no breakers of its own) is in force on two gateway nodes within 1 s, one of
// which never used the breaker, and clearing it returns both to automatic control.
func TestForceReachesEveryNodeWithinOneSecond(t *testing.T) {
	admin := redisClient(t, redisAddr())
	a, b := node(t, redisClient(t, redisAddr())), node(t, redisClient(t, redisAddr()))
	key := BreakerKey{Account: "acct-" + core.NewULID(time.Now()), Endpoint: "clevertap.upload@1"}
	ba := a.Get(key, policy())
	time.Sleep(200 * time.Millisecond) // subscriptions up
	start := time.Now()
	if _, next, err := ForceShared(t.Context(), admin, key, ForceOpen, time.Time{}, time.Now()); err != nil || next.Forced != ForceOpen {
		t.Fatalf("force %+v %v", next, err)
	}
	waitState(t, ba, Open, time.Second)
	// B hears the hold on its own Redis subscription, which can land a few ms after A's: the
	// guarantee is "in force on every node within 1 s of the write", so B gets the same window
	// (HUB-416). It was checked once, right after A, which raced with B's message.
	nodeB := b.Get(key, policy())
	for {
		ok, _, _ := nodeB.Allow(time.Now())
		if !ok {
			break
		}
		if time.Since(start) > time.Second {
			t.Fatal("node B (never used the breaker) still passed a forced-open breaker 1 s after the force")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("force-open in force on both nodes after %s", time.Since(start).Round(time.Millisecond))
	if s, ok, _ := ReadShared(t.Context(), admin, key); !ok || s.Forced != ForceOpen {
		t.Fatalf("shared %+v", s)
	}
	if open, _ := a.SharedOpen(t.Context()); open != nil {
		if _, listed := open[key]; listed {
			t.Fatalf("a held breaker must not be probed: %+v", open[key])
		}
	}
	all, err := SharedStates(t.Context(), admin)
	if err != nil || all[key].Forced != ForceOpen {
		t.Fatalf("states %v %v", all[key], err)
	}
	if ttl := admin.TTL(t.Context(), breakerPrefix+key.String()).Val(); ttl != -1 {
		t.Fatalf("an endless hold must not expire: ttl %s", ttl)
	}

	start = time.Now()
	if _, _, err := ForceShared(t.Context(), admin, key, ForceNone, time.Time{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitState(t, ba, HalfOpen, time.Second)
	waitState(t, b.Get(key, policy()), HalfOpen, time.Second)
	t.Logf("clear reached both nodes after %s", time.Since(start).Round(time.Millisecond))

	// Force-close from a node holds closed everywhere: timeouts on node B do not trip it.
	if _, err := a.Force(t.Context(), key, ForceClosed, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	bb := b.Get(key, policy())
	waitState(t, bb, Closed, time.Second)
	record(bb, time.Now(), OutcomeTimeout, 10)
	if s := bb.Snapshot(); s.State != Closed || s.Forced != ForceClosed {
		t.Fatalf("forced closed tripped %+v", s)
	}
	_ = admin.Del(context.Background(), breakerPrefix+key.String())
}
