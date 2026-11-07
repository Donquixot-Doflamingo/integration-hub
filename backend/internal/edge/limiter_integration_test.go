//go:build integration

package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// redisCounter counts Redis commands and can delay each one (HUB-408).
type redisCounter struct {
	n     atomic.Int64
	delay time.Duration
}

func (h *redisCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *redisCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.n.Add(1)
		if h.delay > 0 {
			time.Sleep(h.delay)
		}
		return next(ctx, cmd)
	}
}

func (h *redisCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.n.Add(int64(len(cmds)))
		return next(ctx, cmds)
	}
}

// limiterRig is sync gateway nodes, each with its own Redis client and account limiter, in
// front of one mockvendor; every account gets quota rps.
type limiterRig struct {
	mv    *mockvendor.Instance
	nodes []http.Handler
	hooks []*redisCounter
}

func newLimiterRig(t *testing.T, nodes, rps int, delay time.Duration, tune func(*upstream.AccountLimiter)) *limiterRig {
	t.Helper()
	mv := mockvendor.Start(t)
	mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Body: okVendor}})
	snap := e2eSnapshot(t, mv.VendorURL)
	accts := map[string]*registry.Account{}
	for id, a := range snap.Accounts {
		ac := *a
		ac.Quota.RPS = rps
		accts[id] = &ac
	}
	snap.Accounts = accts
	snap = withTenantRate(snap)
	m := telemetry.NewMetrics("itest", "limiter")
	cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	clean := redis.NewClient(&redis.Options{Addr: redisAddr()})
	defer func() { _ = clean.Close() }()
	if keys, _ := clean.Keys(t.Context(), "hub:rl:acct:*").Result(); len(keys) > 0 {
		clean.Del(t.Context(), keys...)
	}
	r := &limiterRig{mv: mv}
	for range nodes {
		rdb := redis.NewClient(&redis.Options{Addr: redisAddr()})
		hook := &redisCounter{delay: delay}
		rdb.AddHook(hook)
		nm := telemetry.NewMetrics("itest", "node")
		l := upstream.NewAccountLimiter(rdb, nm, nil)
		if tune != nil {
			tune(l)
		}
		t.Cleanup(func() { l.Close(); _ = rdb.Close() })
		r.hooks = append(r.hooks, hook)
		r.nodes = append(r.nodes, New(Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Limiter: l, Metrics: nm}).Handler())
	}
	return r
}

// withTenantRate lifts the dev tenant's own quota so only the account limiter refuses.
func withTenantRate(snap *registry.Snapshot) *registry.Snapshot {
	cp := *snap
	cp.Tenants = map[string]*registry.Tenant{}
	for id, tn := range snap.Tenants {
		c := *tn
		c.RateLimit = registry.TenantLimit{RPS: 1_000_000, Burst: 1_000_000}
		cp.Tenants[id] = &c
	}
	return registry.ReindexKeys(&cp)
}

// TestAccountLimitAcrossTwoGateways is HUB-408: two gateway nodes, each serving sync calls
// from its own token lease, together send the vendor the account rate (± 10 %) and refuse the
// rest with UPSTREAM_RATE_LIMITED.
func TestAccountLimitAcrossTwoGateways(t *testing.T) {
	const rps = 40
	r := newLimiterRig(t, 2, rps, 0, nil)
	start := time.Now()
	measureFrom, end := start.Add(time.Second), start.Add(4*time.Second)
	var mu sync.Mutex
	var oks []time.Time
	var limited, other atomic.Int64
	var wg sync.WaitGroup
	for _, h := range r.nodes {
		for range 3 {
			wg.Go(func() {
				for time.Now().Before(end) {
					rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody)
					switch rec.Code {
					case http.StatusOK:
						mu.Lock()
						oks = append(oks, time.Now())
						mu.Unlock()
					case http.StatusTooManyRequests:
						var body map[string]map[string]any
						_ = json.Unmarshal(rec.Body.Bytes(), &body)
						if body["error"]["code"] != "UPSTREAM_RATE_LIMITED" || body["error"]["reason"] != "account_quota" {
							t.Errorf("429 %s", rec.Body)
						}
						limited.Add(1)
						time.Sleep(5 * time.Millisecond)
					default:
						other.Add(1)
					}
				}
			})
		}
	}
	wg.Wait()
	counted := 0
	for _, at := range oks {
		if at.After(measureFrom) && !at.After(end) {
			counted++
		}
	}
	rate := float64(counted) / end.Sub(measureFrom).Seconds()
	t.Logf("2 gateways: %.1f vendor calls/s (account rate %d), %d refused, Redis commands per node %d / %d",
		rate, rps, limited.Load(), r.hooks[0].n.Load(), r.hooks[1].n.Load())
	if other.Load() > 0 {
		t.Fatalf("%d unexpected responses", other.Load())
	}
	if limited.Load() == 0 {
		t.Fatal("no call was refused: the account limit was never reached")
	}
	if rate < rps*0.9 || rate > rps*1.1 {
		t.Fatalf("fleet rate %.1f/s, want %d ± 10 %%", rate, rps)
	}
	if vendor := len(r.mv.Requests(t, nil)); vendor != len(oks) {
		t.Fatalf("vendor saw %d calls, callers got %d answers", vendor, len(oks))
	}
}

// TestSyncPathMakesNoRedisCallPerRequest is HUB-408: with every Redis command slowed to
// 100 ms, warm sync calls on a gateway with a real account limiter never take 100 ms (none
// waited for Redis), and the limiter talks to Redis far less than once per call.
func TestSyncPathMakesNoRedisCallPerRequest(t *testing.T) {
	r := newLimiterRig(t, 1, 5000, 100*time.Millisecond, func(l *upstream.AccountLimiter) {
		l.LeaseHorizon = time.Second // a lease covers more than one 100 ms Redis round trip
		l.LeaseTimeout = time.Second
	})
	const calls, warmup = 400, 50
	var hub []time.Duration
	for i := range calls {
		start := time.Now()
		// During warm-up a call may find its node one token ahead and wait for the first
		// (200 ms: EVALSHA, then EVAL) lease past its 250 ms limit.
		if rec := post(t, r.nodes[0], devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody); rec.Code != http.StatusOK && i >= warmup {
			t.Fatalf("call %d: %d %s", i, rec.Code, rec.Body)
		}
		if i >= warmup {
			hub = append(hub, time.Since(start))
		}
		time.Sleep(2 * time.Millisecond)
	}
	slices.Sort(hub)
	n := r.hooks[0].n.Load()
	t.Logf("%d sync calls, %d Redis commands; warm calls p50 %s, max %s", calls, n, hub[len(hub)/2], hub[len(hub)-1])
	if worst := hub[len(hub)-1]; worst >= 100*time.Millisecond {
		t.Fatalf("a warm sync call took %s: it waited for a 100 ms Redis command", worst)
	}
	if n > calls/10 {
		t.Fatalf("%d Redis commands for %d sync calls", n, calls)
	}
}
