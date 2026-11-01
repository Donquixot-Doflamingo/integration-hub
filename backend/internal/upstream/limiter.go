package upstream

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// AccountLimiter is the shared token bucket per vendor account (DESIGN §10.8): every node and
// worker draws from one Redis bucket, so the account's quota holds across the fleet. Live
// traffic may use the whole bucket; retries and drain only what is above a reserve, so live
// requests are served first when both compete. Workers wait for tokens instead of collecting
// 429s. With Redis unavailable it lets traffic through (the vendor's own 429s still apply).
type AccountLimiter struct {
	rdb    redis.Scripter
	log    *slog.Logger
	script *redis.Script
	// Reserve is the share of the bucket only live traffic may use (default 0.2).
	Reserve float64

	waits      *prometheus.HistogramVec
	rejections *prometheus.CounterVec
}

// Token bucket: KEYS[1] bucket; ARGV rate/s, burst, need-above (tokens that must remain), now
// ms. Reply: 0 granted, or the ms to wait before the request could be granted.
const bucketSrc = `
local rate, burst, floor, now = tonumber(ARGV[1]), tonumber(ARGV[2]), tonumber(ARGV[3]), tonumber(ARGV[4])
local s = redis.call('HMGET', KEYS[1], 't', 'ts')
local tokens, ts = tonumber(s[1]), tonumber(s[2])
if not tokens then tokens, ts = burst, now end
tokens = math.min(burst, tokens + (now - ts) * rate / 1000)
local wait = 0
if tokens - 1 >= floor then
  tokens = tokens - 1
else
  wait = math.ceil((floor + 1 - tokens) * 1000 / rate)
end
redis.call('HSET', KEYS[1], 't', tostring(tokens), 'ts', string.format('%d', now))
redis.call('PEXPIRE', KEYS[1], 60000)
return wait
`

// NewAccountLimiter registers hub_wait_ms{kind="account_limiter"} and
// ratelimit_rejections_total{layer="account"} on m. rdb may be nil (no limiting).
func NewAccountLimiter(rdb redis.Scripter, m *telemetry.Metrics, log *slog.Logger) *AccountLimiter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	l := &AccountLimiter{rdb: rdb, log: log, script: redis.NewScript(bucketSrc), Reserve: 0.2}
	l.waits = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "hub_wait_ms", Help: "Deliberate waits excluded from hub_overhead_ms (account limiter, bulkhead).",
		Buckets: []float64{1, 5, 10, 50, 100, 250, 500, 1000, 5000},
	}, []string{"kind"})
	l.rejections = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_rejections_total", Help: "Requests refused or deferred by a Hub-side rate limit.",
	}, []string{"layer"})
	m.Registry.MustRegister(l.waits, l.rejections)
	return l
}

// Rejections exposes the rejection counter so the edge's tenant limiter shares the metric.
func (l *AccountLimiter) Rejections() *prometheus.CounterVec { return l.rejections }

// Wait takes one token of account, waiting at most maxWait. live marks caller traffic (sync,
// wait and first worker attempts); others leave the reserve alone. It returns false when no
// token came within maxWait.
func (l *AccountLimiter) Wait(ctx context.Context, account string, rps int, live bool, maxWait time.Duration) (bool, time.Duration) {
	if l == nil || l.rdb == nil || rps <= 0 {
		return true, 0
	}
	burst := float64(rps)
	floor := 0.0
	if !live {
		floor = burst * l.Reserve
	}
	start := time.Now()
	for {
		cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		wait, err := l.script.Run(cctx, l.rdb, []string{"hub:rl:acct:" + account},
			strconv.Itoa(rps), strconv.FormatFloat(burst, 'f', -1, 64), strconv.FormatFloat(floor, 'f', -1, 64), time.Now().UnixMilli()).Int64()
		cancel()
		if err != nil {
			l.log.WarnContext(ctx, "account limiter unavailable: letting traffic through", slog.String("account", account), slog.String("error", err.Error()))
			return true, time.Since(start)
		}
		if wait == 0 {
			waited := time.Since(start)
			if waited > 0 {
				l.waits.WithLabelValues("account_limiter").Observe(telemetry.Millis(waited))
			}
			return true, waited
		}
		d := time.Duration(wait) * time.Millisecond
		if time.Since(start)+d > maxWait {
			l.rejections.WithLabelValues("account").Inc()
			return false, time.Since(start)
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return false, time.Since(start)
		case <-t.C:
		}
	}
}
