package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// admission holds the breakers a call was admitted by; record feeds them its outcome.
type admission struct {
	shared, view *upstream.Breaker
	sharedProbe  bool
	viewProbe    bool
}

func (a admission) probe() bool { return a.sharedProbe || a.viewProbe }

func (a admission) record(now time.Time, o upstream.Outcome) {
	if a.shared != nil {
		a.shared.Record(now, o, a.sharedProbe)
	}
	if a.view != nil {
		a.view.Record(now, o, a.viewProbe)
	}
}

// again re-admits for the sync retry (a closed breaker always admits; a breaker the first
// attempt tripped refuses, and the retry still goes: DESIGN §10.2 allows one immediate retry).
func (a admission) again(now time.Time) admission {
	out := a
	if a.shared != nil {
		_, out.sharedProbe, _ = a.shared.Allow(now)
	}
	if a.view != nil {
		_, out.viewProbe, _ = a.view.Allow(now)
	}
	return out
}

// admitBreakers applies DESIGN §10.4: the shared (account, endpoint) breaker and the
// integration's tightened view. A refusal is a circuit-open error.
func (a *Attempter) admitBreakers(j Job, t upstream.Target, endpointRef string) (admission, *core.CanonicalError) {
	var adm admission
	if a.Breakers == nil {
		return adm, nil
	}
	now := a.now()
	policy := upstream.PolicyFrom(t.Vendor.Defaults.Breaker)
	adm.shared = a.Breakers.Get(upstream.BreakerKey{Account: t.AccountID, Endpoint: endpointRef}, policy)
	ok, probe, retry := adm.shared.Allow(now)
	if !ok {
		return admission{}, circuitOpen(retry)
	}
	adm.sharedProbe = probe
	if n := j.Res.Version.Breaker.Override.OpenAfterConsecutiveTimeouts; n > 0 {
		view := policy
		view.ConsecutiveTimeouts, view.RatioDisabled = n, true
		adm.view = a.Breakers.Get(upstream.BreakerKey{Account: t.AccountID, Endpoint: endpointRef, Integration: j.Res.Integration.Integration}, view)
		ok, probe, retry := adm.view.Allow(now)
		if !ok {
			adm.shared.Record(now, upstream.OutcomeIgnore, adm.sharedProbe)
			return admission{}, circuitOpen(retry)
		}
		adm.viewProbe = probe
	}
	return adm, nil
}

// waitLimiter takes a token of the account's shared bucket (DESIGN §10.8), waiting up to the
// job's limit; the wait is excluded from hub_overhead_ms.
func (a *Attempter) waitLimiter(ctx context.Context, j Job, t upstream.Target) *core.CanonicalError {
	if a.Limiter == nil {
		return nil
	}
	maxWait := j.LimiterWait
	if maxWait <= 0 {
		maxWait = 5 * time.Second
		if j.Live {
			maxWait = 250 * time.Millisecond
		}
	}
	rps := 0
	if acct := j.Snap.Accounts[t.AccountID]; acct != nil {
		rps = acct.Quota.RPS
	}
	if rps <= 0 {
		rps = t.Vendor.Defaults.RateLimit.PerAccountRPS
	}
	ok, waited := a.Limiter.Wait(ctx, t.AccountID, rps, j.Live, maxWait)
	j.Timings.Add(telemetry.StageWait, waited) // a deliberate wait: excluded from overhead
	if !ok {
		return &core.CanonicalError{Code: core.CodeUpstreamRateLimited, Message: "The upstream account's rate limit is reached",
			Retryable: true, RetryAfterMS: core.Ptr[int64](1000), Reason: core.Ptr("account_quota"), Upstream: &core.Upstream{}}
	}
	return nil
}

func circuitOpen(retry time.Duration) *core.CanonicalError {
	ms := max(retry.Milliseconds(), 1)
	return &core.CanonicalError{Code: core.CodeUpstreamUnavailable, Message: "The upstream is unavailable", Retryable: true,
		RetryAfterMS: &ms, Circuit: core.Ptr("open"), Reason: core.Ptr("circuit_open"), Upstream: &core.Upstream{}}
}

// transportOutcome maps a failed attempt to its breaker weight (TECH_SPEC §10).
func transportOutcome(err error) upstream.Outcome {
	var te *upstream.TransportError
	if !errors.As(err, &te) {
		return upstream.OutcomeFailure
	}
	switch te.Kind {
	case upstream.KindTimeout:
		if te.CallerDeadline {
			return upstream.OutcomeIgnore // the caller's budget, not the vendor's health
		}
		return upstream.OutcomeTimeout
	case upstream.KindCanceled, upstream.KindEgressDenied:
		return upstream.OutcomeIgnore
	case upstream.KindConnect, upstream.KindReset, upstream.KindProtocol:
	}
	return upstream.OutcomeFailure
}

// responseOutcome maps a vendor answer, as classified by the response transformer, to its
// breaker weight: unavailable 1, rate limited 0.5; auth failures, rejections and transformer
// errors are not outages (0).
func responseOutcome(status int, ce *core.CanonicalError) upstream.Outcome {
	if ce != nil {
		if ce.Code == core.CodeUpstreamUnavailable {
			return upstream.OutcomeFailure
		}
		if ce.Code == core.CodeUpstreamRateLimited {
			return upstream.OutcomeRateLimited
		}
		if ce.Code == core.CodeUpstreamTimeout {
			return upstream.OutcomeTimeout
		}
		return upstream.OutcomeSuccess
	}
	if status >= 500 {
		return upstream.OutcomeFailure
	}
	return upstream.OutcomeSuccess
}
