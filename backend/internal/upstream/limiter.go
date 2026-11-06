package upstream

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// AccountLimiter is the shared token bucket per vendor account (DESIGN §10.8): every node and
// worker draws from one Redis bucket, so the account's quota holds across the fleet. Live
// traffic may use the whole bucket; retries and drain only what is above a reserve, so live
// requests are served first when both compete. Workers wait for tokens instead of collecting
// 429s.
//
// Tokens are leased (HUB-408, ADR-0021): each node takes them from Redis in small blocks and
// serves calls from its local lease, refilling it in the background before it runs out, so a
// request makes no Redis round trip in steady state (DESIGN §18.5 rule 6). A node that holds no
// token and is not known to be at the limit lets one call through and pays it back from its
// next lease, so a cold or idle node never waits for Redis either. A request waits only when
// the account really is at its limit. Every token a node uses comes from the Redis bucket
// (borrowed ones included), so the fleet-wide rate holds; at any instant the fleet may be ahead
// by at most one borrowed token per node and class.
//
// With Redis unavailable the limiter lets traffic through (the vendor's own 429s still apply)
// and retries the lease after FailOpenFor.
type AccountLimiter struct {
	rdb    redis.Scripter
	log    *slog.Logger
	script *redis.Script
	// Reserve is the share of the bucket only live traffic may use (default 0.2).
	Reserve float64
	// LeaseShare caps one lease at this share of the account rate (default 0.1).
	LeaseShare float64
	// LeaseHorizon sizes a lease to the node's recent use over this long (default 200 ms).
	LeaseHorizon time.Duration
	// LeaseTTL drops leased tokens a node has not used within it (default 1 s), so an idle node
	// does not hoard quota.
	LeaseTTL time.Duration
	// LeaseTimeout bounds one Redis lease call (default 50 ms).
	LeaseTimeout time.Duration
	// FailOpenFor is how long a pool lets traffic through after a failed lease (default 1 s).
	FailOpenFor time.Duration

	pools sync.Map // poolKey → *pool

	// base is the parent of every lease call; Close cancels it.
	base context.Context
	stop context.CancelFunc

	// closeMu orders lease starts against Close (no wg.Add after Wait).
	closeMu sync.Mutex
	closed  bool
	wg      sync.WaitGroup

	waits      *prometheus.HistogramVec
	rejections *prometheus.CounterVec
	leases     *prometheus.CounterVec
	leaseDur   prometheus.Observer
}

// opLease is the redis_op_duration_seconds{op} value of one lease call.
const opLease = "limiter_lease"

// Lease results: account_limiter_leases_total{result}.
const (
	leaseGranted = "granted" // at least one token
	leaseEmpty   = "empty"   // the bucket had none above the class's reserve
	leaseError   = "error"   // Redis failed or timed out: fail open
)

// Token bucket lease: KEYS[1] bucket; ARGV rate/s, burst, reserve (tokens that must remain),
// now ms, want (tokens asked for). Reply {granted, wait}: granted ≤ want tokens, and when none
// was granted the ms until one would be.
const leaseSrc = `
local rate, burst, reserve, now, want = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4]), tonumber(ARGV[5])
local s = redis.call('HMGET', KEYS[1], 't', 'ts')
local tokens, ts = tonumber(s[1]), tonumber(s[2])
if not tokens then tokens, ts = burst, now end
if now > ts then
  tokens = math.min(burst, tokens + (now - ts) * rate / 1000)
else
  now = ts
end
local grant = math.max(0, math.min(want, math.floor(tokens - reserve)))
tokens = tokens - grant
local wait = 0
if grant == 0 then
  wait = math.max(1, math.ceil((reserve + 1 - tokens) * 1000 / rate))
end
redis.call('HSET', KEYS[1], 't', tostring(tokens), 'ts', string.format('%d', now))
redis.call('PEXPIRE', KEYS[1], 60000)
return {grant, wait}
`

// poolKey is one lease: an account's live traffic, or its retries and drain.
type poolKey struct {
	account string
	live    bool
}

// pool is a node's lease of one account bucket for one traffic class.
type pool struct {
	mu sync.Mutex
	// tokens leased and not yet used; -1 when one call was let through ahead of a lease.
	tokens  int
	expires time.Time // positive tokens are dropped after this
	leasing bool
	done    chan struct{} // closed when the lease in flight finishes
	// retryAt: the bucket had nothing above the reserve; no call is let through ahead of a
	// lease, and no new lease is asked for, before it.
	retryAt time.Time
	// openUntil: the last lease failed; traffic passes unlimited until then.
	openUntil time.Time
	rps       int
	// used counts calls served since the last lease started; rate is their smoothed rate.
	used      int
	lastLease time.Time
	rate      float64
	// target is the size of the last lease asked for; the next starts at half of it.
	target int
}

// NewAccountLimiter registers hub_wait_ms{kind="account_limiter"},
// ratelimit_rejections_total{layer="account"}, account_limiter_leases_total{result} and
// redis_op_duration_seconds{op="limiter_lease"} on m. rdb may be nil (no limiting).
func NewAccountLimiter(rdb redis.Scripter, m *telemetry.Metrics, log *slog.Logger) *AccountLimiter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &AccountLimiter{rdb: rdb, log: log, script: redis.NewScript(leaseSrc), Reserve: 0.2,
		LeaseShare: 0.1, LeaseHorizon: 200 * time.Millisecond, LeaseTTL: time.Second, LeaseTimeout: 50 * time.Millisecond, FailOpenFor: time.Second}
	l.base, l.stop = context.WithCancel(context.Background())
	l.waits = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "hub_wait_ms", Help: "Deliberate waits excluded from hub_overhead_ms (account limiter, bulkhead).",
		Buckets: []float64{1, 5, 10, 50, 100, 250, 500, 1000, 5000},
	}, []string{"kind"})
	l.rejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_rejections_total", Help: "Requests refused or deferred by a Hub-side rate limit.",
	}, []string{"layer"})
	l.leases = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "account_limiter_leases_total", Help: "Account limiter token leases taken from Redis, off the request path, by result.",
	}, []string{"result"})
	m.Registry.MustRegister(l.waits, l.rejections, l.leases)
	for _, r := range []string{leaseGranted, leaseEmpty, leaseError} {
		l.leases.WithLabelValues(r)
	}
	l.leaseDur = m.RedisOpDuration.WithLabelValues(opLease)
	return l
}

// Rejections exposes the rejection counter so the edge's tenant limiter shares the metric.
func (l *AccountLimiter) Rejections() *prometheus.CounterVec { return l.rejections }

// Close cancels the leases in flight, waits for them and stops new ones. Calls after Close
// pass unlimited.
func (l *AccountLimiter) Close() {
	if l == nil || l.stop == nil {
		return
	}
	l.closeMu.Lock()
	l.closed = true
	l.closeMu.Unlock()
	l.stop()
	l.wg.Wait()
}

func (l *AccountLimiter) pool(account string, live bool) *pool {
	k := poolKey{account, live}
	if p, ok := l.pools.Load(k); ok {
		return p.(*pool)
	}
	p, _ := l.pools.LoadOrStore(k, &pool{})
	return p.(*pool)
}

// maxBlock is the largest lease: LeaseShare of the account rate, at least one token.
func (l *AccountLimiter) maxBlock(rps int) int {
	return max(1, int(math.Ceil(float64(rps)*l.LeaseShare)))
}

// want is the size of the next lease: the node's recent use over LeaseHorizon, between one
// token and maxBlock. Caller holds p.mu.
func (l *AccountLimiter) want(p *pool, now time.Time) int {
	if !p.lastLease.IsZero() {
		if dt := now.Sub(p.lastLease).Seconds(); dt > 0 {
			sample := float64(p.used) / dt
			if p.rate == 0 {
				p.rate = sample
			} else {
				p.rate = (p.rate + sample) / 2
			}
		}
	}
	p.used, p.lastLease = 0, now
	return min(l.maxBlock(p.rps), max(1, int(math.Ceil(p.rate*l.LeaseHorizon.Seconds()))))
}

// startLease asks Redis for tokens in the background, unless a lease is in flight, the bucket
// is known to be empty or the limiter is closed. Caller holds p.mu.
func (l *AccountLimiter) startLease(p *pool, k poolKey, now time.Time) {
	if p.leasing || now.Before(p.retryAt) || now.Before(p.openUntil) {
		return
	}
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	if l.closed {
		return
	}
	p.target = l.want(p, now)
	n := max(1, p.target-p.tokens) // pays a borrowed token back
	reserve := 0.0
	if !k.live {
		reserve = float64(p.rps) * l.Reserve
	}
	p.leasing, p.done = true, make(chan struct{})
	rps := p.rps
	l.wg.Go(func() { l.lease(l.base, p, k, rps, reserve, n) })
}

// lease runs one Redis lease call and applies its result to p.
func (l *AccountLimiter) lease(ctx context.Context, p *pool, k poolKey, rps int, reserve float64, n int) {
	ctx, cancel := context.WithTimeout(ctx, l.LeaseTimeout)
	start := time.Now()
	reply, err := l.script.Run(ctx, l.rdb, []string{"hub:rl:acct:" + k.account},
		strconv.Itoa(rps), strconv.Itoa(rps), strconv.FormatFloat(reserve, 'f', -1, 64), start.UnixMilli(), strconv.Itoa(n)).Int64Slice()
	cancel()
	l.leaseDur.Observe(time.Since(start).Seconds())
	if err == nil && len(reply) != 2 {
		err = errUnexpectedReply
	}
	now := time.Now()
	result := leaseGranted
	p.mu.Lock()
	switch {
	case err != nil:
		result = leaseError
		p.tokens = max(p.tokens, 0) // a borrowed token is forgiven: nothing to pay it from
		p.openUntil = now.Add(l.FailOpenFor)
	case reply[0] > 0:
		p.tokens += int(reply[0])
		p.expires = now.Add(l.LeaseTTL)
	default:
		result = leaseEmpty
		p.retryAt = now.Add(time.Duration(reply[1]) * time.Millisecond)
	}
	p.leasing = false
	close(p.done)
	p.done = nil
	p.mu.Unlock()
	l.leases.WithLabelValues(result).Inc()
	if err != nil && l.base.Err() == nil { // not when Close cancelled it
		l.log.WarnContext(ctx, "account limiter unavailable: letting traffic through", slog.String("account", k.account),
			slog.Duration("for", l.FailOpenFor), slog.String("error", err.Error()))
	}
}

type limiterError string

func (e limiterError) Error() string { return string(e) }

const errUnexpectedReply = limiterError("account limiter: unexpected script reply")

// Wait takes one token of account, waiting at most maxWait. live marks caller traffic (sync,
// wait and first worker attempts); others leave the reserve alone. It returns false when no
// token came within maxWait, and the time spent waiting for the account's tokens: only that is
// a deliberate wait (hub_wait_ms, excluded from hub_overhead_ms). Time spent waiting for a
// lease reply is not included; the caller counts it as admission overhead.
func (l *AccountLimiter) Wait(ctx context.Context, account string, rps int, live bool, maxWait time.Duration) (bool, time.Duration) {
	if l == nil || l.rdb == nil || rps <= 0 {
		return true, 0
	}
	k := poolKey{account, live}
	p := l.pool(account, live)
	start := time.Now()
	deadline := start.Add(maxWait)
	var waited time.Duration
	for {
		now := time.Now()
		p.mu.Lock()
		p.rps = rps
		if p.tokens > 0 && now.After(p.expires) {
			p.tokens = 0
		}
		switch {
		case p.tokens > 0: // the steady state: no Redis call
			p.tokens--
			p.used++
			if 2*p.tokens <= p.target { // refill before the lease runs out
				l.startLease(p, k, now)
			}
			p.mu.Unlock()
			return true, l.waited(waited)
		case now.Before(p.openUntil): // Redis failed: degraded, never block requests
			p.used++
			p.mu.Unlock()
			return true, l.waited(waited)
		case p.tokens == 0 && !now.Before(p.retryAt): // ahead of a lease; the lease pays it back
			p.tokens = -1
			p.used++
			l.startLease(p, k, now)
			p.mu.Unlock()
			return true, l.waited(waited)
		}
		// One borrowed token is outstanding, or the bucket is empty: wait for the lease in
		// flight (admission overhead) or until a token is due (a deliberate wait).
		l.startLease(p, k, now)
		done, retryAt, closed := p.done, p.retryAt, l.isClosed()
		p.mu.Unlock()
		if closed {
			return true, l.waited(waited)
		}
		if done != nil {
			t := time.NewTimer(time.Until(deadline))
			select {
			case <-done:
				t.Stop()
				continue
			case <-ctx.Done():
				t.Stop()
				return false, waited
			case <-t.C:
				l.rejections.WithLabelValues("account").Inc()
				return false, waited
			}
		}
		d := max(retryAt.Sub(now), time.Millisecond)
		if now.Add(d).After(deadline) {
			l.rejections.WithLabelValues("account").Inc()
			return false, waited
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return false, waited + time.Since(now)
		case <-t.C:
			waited += time.Since(now)
		}
	}
}

func (l *AccountLimiter) isClosed() bool {
	l.closeMu.Lock()
	defer l.closeMu.Unlock()
	return l.closed
}

// waited records a deliberate wait in hub_wait_ms and returns it.
func (l *AccountLimiter) waited(d time.Duration) time.Duration {
	if d > 0 {
		l.waits.WithLabelValues("account_limiter").Observe(telemetry.Millis(d))
	}
	return d
}
