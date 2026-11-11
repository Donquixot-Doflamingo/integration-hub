//go:build integration

package idempotency_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// budget is the idempotency stage budget of DESIGN §18.6 (Redis, if key).
const budget = time.Millisecond

func redisAddr() string {
	if a := os.Getenv("HUB_REDIS_ADDR"); a != "" {
		return a
	}
	return "localhost:6379"
}

// newRedis connects to the compose Redis. Every test uses its own key prefix, so tests never
// see each other's records or anyone else's; the prefix is deleted on cleanup.
func newRedis(t testing.TB) (*redis.Client, string) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr(), DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err := rdb.Ping(t.Context()).Err(); err != nil {
		_ = rdb.Close()
		t.Fatalf("redis at %s: %v (run make up)", redisAddr(), err)
	}
	prefix := "test:idem:" + strings.ToLower(core.NewULID(time.Now())) + ":"
	t.Cleanup(func() {
		ctx := context.Background() // t.Context is already cancelled when cleanups run
		var cursor uint64
		for {
			keys, next, err := rdb.Scan(ctx, cursor, prefix+"*", 500).Result()
			if err != nil {
				t.Errorf("cleanup scan: %v", err)
				break
			}
			if len(keys) > 0 {
				rdb.Del(ctx, keys...)
			}
			if cursor = next; cursor == 0 {
				break
			}
		}
		_ = rdb.Close()
	})
	return rdb, prefix
}

func newStore(t testing.TB, opts idempotency.Options) (*idempotency.Store, *redis.Client, string) {
	t.Helper()
	rdb, prefix := newRedis(t)
	opts.Prefix = prefix
	s := idempotency.New(rdb, telemetry.NewMetrics("gateway", "test"), opts)
	if err := s.Warm(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, rdb, prefix
}

// onlyKey returns the single Redis key under prefix.
func onlyKey(t *testing.T, rdb *redis.Client, prefix string) string {
	t.Helper()
	keys, err := rdb.Keys(t.Context(), prefix+"*").Result()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys under prefix = %v (err %v), want exactly one", keys, err)
	}
	return keys[0]
}

func key(k string) idempotency.Key {
	return idempotency.Key{Tenant: "ten_acme", Integration: "int_pay_upi", Key: k}
}

func replayHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Hub-Served-By", "primary")
	h.Set("Authorization", "Bearer canary-secret-7d1f")
	return h
}

func TestFreshCompletedReplayIdentical(t *testing.T) {
	s, rdb, prefix := newStore(t, idempotency.Options{})
	ctx := t.Context()
	k := key("order-8821-created")
	bh := idempotency.BodyHash("default", []byte(`{"amount":100}`))

	got, err := s.Begin(ctx, k, bh, "req_A")
	if err != nil || got.State != idempotency.Fresh {
		t.Fatalf("first Begin = %+v, %v; want Fresh", got, err)
	}
	if got, err := s.Begin(ctx, k, bh, "req_A"); err != nil || got.State != idempotency.Fresh {
		t.Fatalf("Begin retried by the holder = %+v, %v; want Fresh", got, err)
	}
	if got, err := s.Begin(ctx, k, bh, "req_B"); err != nil || got.State != idempotency.InProgress || got.RequestID != "req_A" {
		t.Fatalf("duplicate while running = %+v, %v; want InProgress req_A", got, err)
	}

	body := []byte(`{"status":"captured","amount":100}`)
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Header: replayHeader(), Body: body}); err != nil {
		t.Fatal(err)
	}
	for _, rid := range []string{"req_C", "req_D"} {
		got, err := s.Begin(ctx, k, bh, rid)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != idempotency.Completed || got.RequestID != "req_A" || got.Response.Status != 200 || !bytes.Equal(got.Response.Body, body) {
			t.Fatalf("replay = %+v, want Completed req_A 200 %s", got, body)
		}
		if got.Response.Header.Get("X-Hub-Served-By") != "primary" || got.Response.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("replay headers = %v", got.Response.Header)
		}
		if got.Response.Header.Get("Authorization") != "" {
			t.Fatal("a credential header was stored")
		}
	}

	// Nothing in Redis carries the raw key or the credential.
	rkey := onlyKey(t, rdb, prefix)
	if strings.Contains(rkey, "order-8821") {
		t.Fatalf("redis key %q contains the raw idempotency key", rkey)
	}
	fields, err := rdb.HGetAll(ctx, rkey).Result()
	if err != nil {
		t.Fatal(err)
	}
	for f, v := range fields {
		if strings.Contains(v, "canary-secret") || strings.Contains(v, "order-8821") {
			t.Fatalf("field %s = %q leaks a secret or the raw key", f, v)
		}
	}
}

func TestAcceptedReplay(t *testing.T) {
	s, _, _ := newStore(t, idempotency.Options{})
	ctx := t.Context()
	k := key("evt-1")
	if got, err := s.Begin(ctx, k, "bh", "req_A"); err != nil || got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v, %v", got, err)
	}
	h := http.Header{}
	h.Set("Location", "/v1/requests/req_A")
	body := []byte(`{"request_id":"req_A","status":"accepted","poll":"/v1/requests/req_A"}`)
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 202, Header: h, Body: body}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Begin(ctx, k, "bh", "req_B")
	if err != nil || got.State != idempotency.Completed || got.Response.Status != 202 ||
		got.Response.Header.Get("Location") != "/v1/requests/req_A" || !bytes.Equal(got.Response.Body, body) {
		t.Fatalf("replay = %+v, %v", got, err)
	}
}

func TestSameKeyDifferentBodyConflicts(t *testing.T) {
	s, _, _ := newStore(t, idempotency.Options{})
	ctx := t.Context()
	k := key("order-1")
	a := idempotency.BodyHash("default", []byte(`{"amount":100}`))
	b := idempotency.BodyHash("default", []byte(`{"amount":999}`))

	if got, _ := s.Begin(ctx, k, a, "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	if got, err := s.Begin(ctx, k, b, "req_B"); err != nil || got.State != idempotency.Conflict || got.RequestID != "req_A" {
		t.Fatalf("different body while running = %+v, %v; want Conflict", got, err)
	}
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Begin(ctx, k, b, "req_C"); err != nil || got.State != idempotency.Conflict {
		t.Fatalf("different body after completion = %+v, %v; want Conflict", got, err)
	}
	other := idempotency.BodyHash("verify", []byte(`{"amount":100}`))
	if got, err := s.Begin(ctx, k, other, "req_D"); err != nil || got.State != idempotency.Conflict {
		t.Fatalf("other operation = %+v, %v; want Conflict", got, err)
	}
	// Scope: the same key under another tenant or integration is independent.
	for _, sk := range []idempotency.Key{
		{Tenant: "ten_other", Integration: k.Integration, Key: k.Key},
		{Tenant: k.Tenant, Integration: "int_other", Key: k.Key},
	} {
		if got, err := s.Begin(ctx, sk, b, "req_E"); err != nil || got.State != idempotency.Fresh {
			t.Fatalf("scope %+v = %+v, %v; want Fresh", sk, got, err)
		}
	}
}

func TestConcurrentBeginHasOneWinner(t *testing.T) {
	s, _, _ := newStore(t, idempotency.Options{})
	const rounds, racers = 20, 32
	for round := range rounds {
		k := key("race-" + strconv.Itoa(round))
		var (
			wg      sync.WaitGroup
			start   = make(chan struct{})
			mu      sync.Mutex
			winners []string
			seen    = map[string]int{}
		)
		for i := range racers {
			rid := "req_" + strconv.Itoa(round) + "_" + strconv.Itoa(i)
			wg.Go(func() {
				<-start
				got, err := s.Begin(t.Context(), k, "bh", rid)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					t.Errorf("Begin: %v", err)
				case got.State == idempotency.Fresh:
					winners = append(winners, rid)
				case got.State == idempotency.InProgress:
					seen[got.RequestID]++
				default:
					t.Errorf("Begin = %+v, want Fresh or InProgress", got)
				}
			})
		}
		close(start)
		wg.Wait()
		if len(winners) != 1 {
			t.Fatalf("round %d: %d Fresh results (%v), want exactly 1", round, len(winners), winners)
		}
		if seen[winners[0]] != racers-1 || len(seen) != 1 {
			t.Fatalf("round %d: losers saw %v, want %d × %s", round, seen, racers-1, winners[0])
		}
	}
}

func TestTTLRunsFromFirstClaim(t *testing.T) {
	s, rdb, prefix := newStore(t, idempotency.Options{LeaseTTL: 30 * time.Second})
	ctx := t.Context()
	k := key("ttl-1")
	before, err := rdb.Time(ctx).Result()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Begin(ctx, k, "bh", "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	rkey := onlyKey(t, rdb, prefix)
	if ttl := rdb.PTTL(ctx, rkey).Val(); ttl <= 29*time.Second || ttl > 30*time.Second {
		t.Fatalf("in-progress PTTL = %s, want the 30s lease", ttl)
	}
	exp, err := rdb.HGet(ctx, rkey, "exp").Int64()
	if err != nil {
		t.Fatal(err)
	}
	claimedPlusTTL := time.UnixMilli(exp).Sub(before.Truncate(time.Millisecond))
	if claimedPlusTTL < 24*time.Hour || claimedPlusTTL > 24*time.Hour+2*time.Second {
		t.Fatalf("exp - claim time = %s, want 24h", claimedPlusTTL)
	}

	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	expireAt, err := rdb.PExpireTime(ctx, rkey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if expireAt.Milliseconds() != exp {
		t.Fatalf("completed PEXPIRETIME = %d, want the first-claim deadline %d", expireAt.Milliseconds(), exp)
	}
	if ttl := rdb.PTTL(ctx, rkey).Val(); ttl <= 24*time.Hour-5*time.Second || ttl > 24*time.Hour {
		t.Fatalf("completed PTTL = %s, want about 24h", ttl)
	}
	// A second Complete by the owner keeps the deadline.
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Body: []byte(`{"v":2}`)}); err != nil {
		t.Fatal(err)
	}
	if again := rdb.PExpireTime(ctx, rkey).Val(); again.Milliseconds() != exp {
		t.Fatalf("deadline moved to %d on a second Complete", again.Milliseconds())
	}
}

func TestExpiredLeaseFreesTheKey(t *testing.T) {
	s, _, _ := newStore(t, idempotency.Options{LeaseTTL: 100 * time.Millisecond})
	ctx := t.Context()
	k := key("crash-1")
	if got, _ := s.Begin(ctx, k, "bh", "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	// The holder "crashes": no Complete, no Release. The lease frees the key.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := s.Begin(ctx, k, "bh", "req_B")
		if err != nil {
			t.Fatal(err)
		}
		if got.State == idempotency.Fresh {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("key still held after the lease: %+v", got)
		}
		<-time.After(20 * time.Millisecond)
	}
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200}); !errors.Is(err, idempotency.ErrNotOwner) {
		t.Fatalf("late Complete by the old holder = %v, want ErrNotOwner", err)
	}
	if err := s.Release(ctx, k, "req_A"); !errors.Is(err, idempotency.ErrNotOwner) {
		t.Fatalf("late Release by the old holder = %v, want ErrNotOwner", err)
	}
	if err := s.Complete(ctx, k, "req_B", idempotency.Response{Status: 200, Body: []byte(`{"by":"B"}`)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Begin(ctx, k, "bh", "req_C"); got.State != idempotency.Completed || got.RequestID != "req_B" {
		t.Fatalf("replay = %+v, want Completed by req_B", got)
	}
}

func TestReleaseLetsTheNextRequestRunFresh(t *testing.T) {
	s, rdb, prefix := newStore(t, idempotency.Options{})
	ctx := t.Context()
	k := key("fail-1")
	if got, _ := s.Begin(ctx, k, "bh", "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	if err := s.Release(ctx, k, "req_B"); !errors.Is(err, idempotency.ErrNotOwner) {
		t.Fatalf("Release by a non-owner = %v, want ErrNotOwner", err)
	}
	if err := s.Release(ctx, k, "req_A"); err != nil {
		t.Fatal(err)
	}
	if keys := rdb.Keys(ctx, prefix+"*").Val(); len(keys) != 0 {
		t.Fatalf("record still exists after Release: %v", keys)
	}
	if err := s.Release(ctx, k, "req_A"); err != nil {
		t.Fatalf("Release of a missing record = %v, want nil", err)
	}
	// A failed request is not replayed: the retry runs fresh (DESIGN §6.3), even with another body.
	if got, _ := s.Begin(ctx, k, "other-bh", "req_C"); got.State != idempotency.Fresh {
		t.Fatalf("Begin after Release = %+v, want Fresh", got)
	}
	// A completed 202 can be released by its request id (terminal failure in the worker).
	if err := s.Complete(ctx, k, "req_C", idempotency.Response{Status: 202, Body: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, k, "req_C"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Begin(ctx, k, "bh", "req_D"); got.State != idempotency.Fresh {
		t.Fatalf("Begin after releasing a completed 202 = %+v, want Fresh", got)
	}
}

func TestLargeResponseIsRefusedWithoutWriting(t *testing.T) {
	s, rdb, prefix := newStore(t, idempotency.Options{MaxBytes: 1024})
	ctx := t.Context()
	k := key("big-1")
	if got, _ := s.Begin(ctx, k, "bh", "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Body: bytes.Repeat([]byte("x"), 2048)})
	if !errors.Is(err, idempotency.ErrTooLarge) {
		t.Fatalf("Complete = %v, want ErrTooLarge", err)
	}
	if st := rdb.HGet(ctx, onlyKey(t, rdb, prefix), "st").Val(); st != "p" {
		t.Fatalf("record state = %q after a refused Complete, want still in progress", st)
	}
}

func TestRedisUnavailableIsTyped(t *testing.T) {
	// Nothing listens on port 1: connection refused. The client options are the fail-fast
	// settings the gateway should use (TECH_SPEC §8.1): go-redis otherwise retries a failed
	// dial 5 times with 100 ms backoff, about 0.4 s per request while Redis is down.
	rdb := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1,
		DialerRetries: 1, DialerRetryTimeout: time.Millisecond, ContextTimeoutEnabled: true,
	})
	defer func() { _ = rdb.Close() }()
	s := idempotency.New(rdb, telemetry.NewMetrics("gateway", "test"), idempotency.Options{})
	ctx := t.Context()
	k := key("down-1")

	start := time.Now()
	_, err := s.Begin(ctx, k, "bh", "req_A")
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Errorf("Begin with Redis down took %s, want a fast failure", took)
	}
	var ue *idempotency.UnavailableError
	if !errors.Is(err, idempotency.ErrUnavailable) || !errors.As(err, &ue) || ue.Op != "begin" {
		t.Fatalf("Begin = %v, want *UnavailableError{Op: begin}", err)
	}
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200}); !errors.Is(err, idempotency.ErrUnavailable) {
		t.Fatalf("Complete = %v, want ErrUnavailable", err)
	}
	if err := s.Release(ctx, k, "req_A"); !errors.Is(err, idempotency.ErrUnavailable) {
		t.Fatalf("Release = %v, want ErrUnavailable", err)
	}
	if err := s.Warm(ctx); !errors.Is(err, idempotency.ErrUnavailable) {
		t.Fatalf("Warm = %v, want ErrUnavailable", err)
	}
}

// BenchmarkBegin measures one Begin round trip against the compose Redis and reports p50/p99
// against the 1 ms idempotency budget (DESIGN §18.6). "replay" is the hit path with a 4 KB
// stored body; "fresh" claims a new key each iteration.
func BenchmarkBegin(b *testing.B) {
	body := bytes.Repeat([]byte("a"), 4096)
	for _, mode := range []string{"replay", "fresh"} {
		b.Run(mode, func(b *testing.B) {
			s, _, _ := newStore(b, idempotency.Options{LeaseTTL: time.Minute})
			ctx := b.Context()
			k := key("bench-replay")
			if _, err := s.Begin(ctx, k, "bh", "req_seed"); err != nil {
				b.Fatal(err)
			}
			if err := s.Complete(ctx, k, "req_seed", idempotency.Response{Status: 200, Header: replayHeader(), Body: body}); err != nil {
				b.Fatal(err)
			}
			durs := make([]time.Duration, 0, 1<<16)
			i := 0
			b.ReportAllocs()
			for b.Loop() {
				kk, want := k, idempotency.Completed
				if mode == "fresh" {
					kk, want = key("bench-"+strconv.Itoa(i)), idempotency.Fresh
				}
				i++
				start := time.Now()
				got, err := s.Begin(ctx, kk, "bh", "req_bench")
				durs = append(durs, time.Since(start))
				if err != nil || got.State != want {
					b.Fatalf("Begin = %+v, %v", got, err)
				}
			}
			slices.Sort(durs)
			p50, p99 := durs[len(durs)/2], durs[len(durs)*99/100]
			b.ReportMetric(float64(p50.Nanoseconds())/1e6, "p50-ms")
			b.ReportMetric(float64(p99.Nanoseconds())/1e6, "p99-ms")
			if p99 > budget {
				b.Logf("p99 %s is over the %s idempotency budget (DESIGN §18.6)", p99, budget)
			}
		})
	}
}

// A response too large to store must not free the key: a retry would call the vendor again.
func TestMarkCompletedKeepsTheKeyAfterAnOversizeResponse(t *testing.T) {
	s, rdb, prefix := newStore(t, idempotency.Options{MaxBytes: 1024})
	ctx := t.Context()
	k := key("tomb-1")
	if got, _ := s.Begin(ctx, k, "bh", "req_A"); got.State != idempotency.Fresh {
		t.Fatalf("Begin = %+v", got)
	}
	if err := s.Complete(ctx, k, "req_A", idempotency.Response{Status: 200, Body: bytes.Repeat([]byte("x"), 2048)}); !errors.Is(err, idempotency.ErrTooLarge) {
		t.Fatalf("Complete = %v", err)
	}
	if err := s.MarkCompleted(ctx, k, "req_A"); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	if err := s.MarkCompleted(ctx, k, "req_B"); !errors.Is(err, idempotency.ErrNotOwner) {
		t.Fatalf("MarkCompleted by another request = %v, want ErrNotOwner", err)
	}
	got, err := s.Begin(ctx, k, "bh", "req_B")
	if err != nil || got.State != idempotency.CompletedNotStored || got.RequestID != "req_A" {
		t.Fatalf("retry Begin = %+v, %v; want CompletedNotStored of req_A", got, err)
	}
	if got, _ := s.Begin(ctx, k, "other", "req_C"); got.State != idempotency.Conflict {
		t.Fatalf("different body = %+v, want Conflict", got)
	}
	if ttl := rdb.PTTL(ctx, onlyKey(t, rdb, prefix)).Val(); ttl < time.Hour {
		t.Fatalf("tombstone ttl = %v, want the rest of the 24 h record TTL, not the lease", ttl)
	}
}
