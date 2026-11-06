package upstream

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// fakeBucket is a redis.Scripter that runs the lease script in Go against an in-memory bucket.
// It counts calls and can delay or fail them, so tests can tell whether a request waited for
// Redis (HUB-408).
type fakeBucket struct {
	mu      sync.Mutex
	buckets map[string]*[2]float64 // tokens, ts ms
	calls   atomic.Int64
	delay   time.Duration
	fail    atomic.Bool
}

func newFakeBucket(delay time.Duration) *fakeBucket {
	return &fakeBucket{buckets: map[string]*[2]float64{}, delay: delay}
}

func (f *fakeBucket) run(ctx context.Context, keys []string, args ...any) *redis.Cmd {
	f.calls.Add(1)
	if f.delay > 0 {
		t := time.NewTimer(f.delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return redis.NewCmdResult(nil, ctx.Err())
		case <-t.C:
		}
	}
	if f.fail.Load() {
		return redis.NewCmdResult(nil, errors.New("redis: connection refused"))
	}
	num := func(i int) float64 {
		switch v := args[i].(type) {
		case string:
			n, _ := strconv.ParseFloat(v, 64)
			return n
		case int64:
			return float64(v)
		}
		panic("unexpected arg type")
	}
	rate, burst, reserve, want := num(0), num(1), num(2), num(4)
	now := float64(time.Now().UnixMilli()) // the script's ARGV[4] is the caller's clock; the fake uses its own
	f.mu.Lock()
	defer f.mu.Unlock()
	b := f.buckets[keys[0]]
	if b == nil {
		b = &[2]float64{burst, now}
		f.buckets[keys[0]] = b
	}
	if now > b[1] {
		b[0] = math.Min(burst, b[0]+(now-b[1])*rate/1000)
		b[1] = now
	}
	grant := math.Max(0, math.Min(want, math.Floor(b[0]-reserve)))
	b[0] -= grant
	wait := 0.0
	if grant == 0 {
		wait = math.Max(1, math.Ceil((reserve+1-b[0])*1000/rate))
	}
	return redis.NewCmdResult([]any{int64(grant), int64(wait)}, nil)
}

func (f *fakeBucket) Eval(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.run(ctx, keys, args...)
}

func (f *fakeBucket) EvalSha(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.run(ctx, keys, args...)
}

func (f *fakeBucket) EvalRO(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.run(ctx, keys, args...)
}

func (f *fakeBucket) EvalShaRO(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return f.run(ctx, keys, args...)
}

func (f *fakeBucket) ScriptExists(context.Context, ...string) *redis.BoolSliceCmd {
	return redis.NewBoolSliceResult([]bool{true}, nil)
}

func (f *fakeBucket) ScriptLoad(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("sha", nil)
}

func newTestLimiter(f *fakeBucket) (*AccountLimiter, *telemetry.Metrics) {
	m := telemetry.NewMetrics("test", "limiter")
	return NewAccountLimiter(f, m, nil), m
}

// TestLimiterSteadyStateMakesNoRedisCallOnTheRequest: with every Redis call taking 20 ms, a
// node serving 1,000 calls/s on a 2,000 rps account answers each of them without waiting (the
// fake clock does not advance inside Wait) once its lease has sized itself to the traffic (the
// first 100 calls), and leases from Redis every few dozen calls, never per call (HUB-408).
func TestLimiterSteadyStateMakesNoRedisCallOnTheRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(20 * time.Millisecond)
		l, _ := newTestLimiter(f)
		defer l.Close()
		const calls, warmup = 2000, 100
		blocked := 0
		for i := range calls {
			start := time.Now()
			ok, waited := l.Wait(t.Context(), "acct", 2000, true, 250*time.Millisecond)
			if !ok || waited != 0 {
				t.Fatalf("call %d: ok %v waited %s", i, ok, waited)
			}
			if time.Since(start) > 0 && i >= warmup {
				blocked++
			}
			time.Sleep(time.Millisecond)
		}
		synctest.Wait()
		if blocked != 0 {
			t.Fatalf("%d of %d calls waited for Redis", blocked, calls)
		}
		if n := f.calls.Load(); n > calls/20 {
			t.Fatalf("%d Redis calls for %d requests: the lease is not reused", n, calls)
		}
		t.Logf("%d requests, %d Redis lease calls, none on a request", calls, f.calls.Load())
	})
}

// TestLimiterColdNodeDoesNotWaitForRedis: the first call on a node, and the first after its
// lease expired, pass at once; the lease that pays them back runs in the background.
func TestLimiterColdNodeDoesNotWaitForRedis(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(20 * time.Millisecond)
		l, _ := newTestLimiter(f)
		defer l.Close()
		for _, idle := range []time.Duration{0, 5 * time.Second} {
			time.Sleep(idle)
			start := time.Now()
			if ok, _ := l.Wait(t.Context(), "acct", 100, true, 250*time.Millisecond); !ok || time.Since(start) != 0 {
				t.Fatalf("after %s idle: ok %v, took %s", idle, ok, time.Since(start))
			}
			time.Sleep(f.delay + time.Millisecond) // the background lease returns
			p := l.pool("acct", true)
			p.mu.Lock()
			tokens := p.tokens
			p.mu.Unlock()
			if tokens != 1 { // two leased: one paid the borrowed token back, one is for the next call
				t.Fatalf("after %s idle: %d tokens left, want 1", idle, tokens)
			}
		}
	})
}

// TestLimiterWaitsOnlyAtTheLimit: once the account bucket is empty, calls wait for tokens (a
// deliberate wait, reported) or are refused when the wait would exceed maxWait.
func TestLimiterWaitsOnlyAtTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(0)
		l, _ := newTestLimiter(f)
		defer l.Close()
		const rps = 10
		granted := 0
		for range rps + 2 { // the burst (rps tokens), then one borrowed
			if ok, _ := l.Wait(t.Context(), "acct", rps, true, 0); ok {
				granted++
			}
			synctest.Wait()
		}
		if granted < rps || granted > rps+1 {
			t.Fatalf("granted %d of a %d-token burst", granted, rps)
		}
		ok, waited := l.Wait(t.Context(), "acct", rps, true, time.Millisecond)
		if ok {
			t.Fatal("granted with an empty bucket and 1 ms to wait")
		}
		if waited != 0 {
			t.Fatalf("a refusal waited %s", waited)
		}
		ok, waited = l.Wait(t.Context(), "acct", rps, true, time.Second)
		if !ok || waited < 50*time.Millisecond || waited > 300*time.Millisecond {
			t.Fatalf("ok %v waited %s, want about one token interval (100 ms) or two", ok, waited)
		}
	})
}

// TestLimiterLeaseReplyIsNotAWait: a call that has to wait for a lease reply reports no
// deliberate wait; the attempter counts that time as admission overhead.
func TestLimiterLeaseReplyIsNotAWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(20 * time.Millisecond)
		l, _ := newTestLimiter(f)
		defer l.Close()
		if ok, _ := l.Wait(t.Context(), "acct", 100, true, time.Second); !ok { // borrows, starts a lease
			t.Fatal("first call refused")
		}
		start := time.Now()
		ok, waited := l.Wait(t.Context(), "acct", 100, true, time.Second) // waits for that lease
		if !ok || waited != 0 || time.Since(start) != 20*time.Millisecond {
			t.Fatalf("ok %v waited %s took %s; want ok, 0, 20ms", ok, waited, time.Since(start))
		}
	})
}

// TestLimiterRedisDownFailsOpen: a failed lease lets traffic through for FailOpenFor without
// another Redis call per request, then leases again.
func TestLimiterRedisDownFailsOpen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(0)
		f.fail.Store(true)
		l, m := newTestLimiter(f)
		defer l.Close()
		for range 100 {
			if ok, _ := l.Wait(t.Context(), "acct", 5, true, 0); !ok {
				t.Fatal("refused while Redis is down")
			}
			synctest.Wait()
		}
		if n := f.calls.Load(); n != 1 {
			t.Fatalf("%d Redis calls within the fail-open window, want 1", n)
		}
		if v := testutil.ToFloat64(l.leases.WithLabelValues(leaseError)); v != 1 {
			t.Fatalf("leases{error} = %v", v)
		}
		if v := testutil.CollectAndCount(m.RedisOpDuration); v != 1 {
			t.Fatalf("redis_op_duration_seconds series %d", v)
		}
		f.fail.Store(false)
		time.Sleep(l.FailOpenFor)
		l.Wait(t.Context(), "acct", 5, true, 0)
		synctest.Wait()
		if v := testutil.ToFloat64(l.leases.WithLabelValues(leaseGranted)); v != 1 {
			t.Fatalf("leases{granted} = %v after Redis came back", v)
		}
	})
}

// TestLimiterFleetRate: two nodes hammering one account together get the account rate (± 5 %)
// after the first second's burst, although each serves from its own lease.
func TestLimiterFleetRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(time.Millisecond)
		const rps = 200
		var counted atomic.Int64
		measureFrom := time.Now().Add(time.Second)
		ctx, cancel := context.WithTimeout(t.Context(), 11*time.Second)
		defer cancel()
		var wg sync.WaitGroup
		for range 2 {
			l, _ := newTestLimiter(f)
			defer l.Close()
			for range 4 { // four callers per node
				wg.Go(func() {
					for ctx.Err() == nil {
						if ok, _ := l.Wait(ctx, "acct", rps, true, time.Second); ok && time.Now().After(measureFrom) {
							counted.Add(1)
						}
					}
				})
			}
		}
		wg.Wait()
		rate := float64(counted.Load()) / 10
		t.Logf("2 nodes: %.1f grants/s (account rate %d), %d Redis calls", rate, rps, f.calls.Load())
		if rate < rps*0.95 || rate > rps*1.05 {
			t.Fatalf("fleet rate %.1f/s, want %d ± 5 %%", rate, rps)
		}
	})
}

// TestLimiterLiveTrafficFirst: retries and drain lease only what is above the reserve, so live
// traffic gets most of a contended account.
func TestLimiterLiveTrafficFirst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(time.Millisecond)
		l, _ := newTestLimiter(f)
		defer l.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var live, drain atomic.Int64
		var wg sync.WaitGroup
		for _, isLive := range []bool{true, false} {
			wg.Go(func() {
				for ctx.Err() == nil {
					ok, _ := l.Wait(ctx, "acct", 50, isLive, 200*time.Millisecond)
					switch {
					case !ok: // a refusal returns at once; a worker would nack and come back later
						time.Sleep(10 * time.Millisecond)
					case isLive:
						live.Add(1)
					default:
						drain.Add(1)
					}
				}
			})
		}
		wg.Wait()
		t.Logf("live %d, drain %d", live.Load(), drain.Load())
		if live.Load() <= 2*drain.Load() {
			t.Fatalf("live %d, drain %d: live traffic must come first", live.Load(), drain.Load())
		}
	})
}

// TestLimiterIdleLeaseExpires: tokens a node leased and did not use within LeaseTTL are
// dropped, so an idle node does not hoard the account's quota.
func TestLimiterIdleLeaseExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeBucket(0)
		l, _ := newTestLimiter(f)
		defer l.Close()
		for range 50 { // builds a lease of several tokens
			l.Wait(t.Context(), "acct", 1000, true, 0)
			synctest.Wait()
		}
		before := f.calls.Load()
		time.Sleep(l.LeaseTTL + time.Millisecond)
		l.Wait(t.Context(), "acct", 1000, true, 0)
		p := l.pool("acct", true)
		p.mu.Lock()
		tokens := p.tokens
		p.mu.Unlock()
		if tokens != -1 {
			t.Fatalf("after an idle LeaseTTL the node had %d tokens, want -1 (dropped, then one borrowed)", tokens)
		}
		synctest.Wait()
		if f.calls.Load() != before+1 {
			t.Fatal("no new lease after the old one expired")
		}
	})
}

func TestLimiterDisabled(t *testing.T) {
	var nilLimiter *AccountLimiter
	l, _ := newTestLimiter(newFakeBucket(0))
	defer l.Close()
	noRedis := NewAccountLimiter(nil, telemetry.NewMetrics("test", "nil"), nil)
	for name, tc := range map[string]struct {
		l   *AccountLimiter
		rps int
	}{"nil limiter": {nilLimiter, 10}, "no redis": {noRedis, 10}, "no quota": {l, 0}} {
		if ok, waited := tc.l.Wait(t.Context(), "acct", tc.rps, true, 0); !ok || waited != 0 {
			t.Errorf("%s: ok %v waited %s", name, ok, waited)
		}
	}
	noRedis.Close()
	nilLimiter.Close()
}
