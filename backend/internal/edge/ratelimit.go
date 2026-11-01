package edge

import (
	"math"
	"sync"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// tenantLimiter is the per-node token bucket per tenant + integration (DESIGN §10.8: tenant
// quotas at the edge). The bucket allows the tenant's rps with its burst on each gateway node;
// the fleet-wide account quota is the upstream limiter's job.
type tenantLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// allow takes a token, or returns how long until one is available.
func (l *tenantLimiter) allow(tenant *registry.Tenant, integration string, now time.Time) (bool, time.Duration) {
	rps, burst := float64(tenant.RateLimit.RPS), float64(tenant.RateLimit.Burst)
	if rps <= 0 {
		return true, 0
	}
	if burst < 1 {
		burst = math.Max(rps, 1)
	}
	key := tenant.Tenant + "|" + integration
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buckets == nil {
		l.buckets = map[string]*tokenBucket{}
	}
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.last).Seconds()*rps)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / rps * float64(time.Second))
}

func tenantQuotaError(wait time.Duration) *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeUpstreamRateLimited, Message: "The tenant's request rate limit for this integration is reached",
		Retryable: true, RetryAfterMS: core.Ptr(max(wait.Milliseconds(), 1)), Reason: core.Ptr("tenant_quota")}
}
