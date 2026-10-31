package delivery

import (
	"context"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// Settler turns the result of a queued attempt into its state change: succeeded, a retry on
// the backoff schedule (within the retry budget), or a dead letter. The worker and the
// wait-mode gateway attempt use the same policy (DESIGN §10.2 async / wait-tail column).
type Settler struct {
	Transitions *Transitions
	Backoff     Backoff
	Budget      *Budget
	Now         func() time.Time
}

func (s Settler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Settle applies the outcome of attempt n of env and returns the state it moved to. A lost race
// returns ErrLost (IsLost) and changes nothing.
func (s Settler) Settle(ctx context.Context, env core.Envelope, worker string, n int, account string, out Result, final audit.Final) (core.State, error) {
	if out.Err == nil {
		final.HTTPStatus = 200
		return core.StateSucceeded, s.Transitions.Succeed(ctx, env, worker, Outcome{Final: final, Result: out.Body})
	}
	if Retryable(out.Err) {
		var retryAfter time.Duration
		if out.Err.RetryAfterMS != nil {
			retryAfter = time.Duration(*out.Err.RetryAfterMS) * time.Millisecond
		}
		if delay, more := s.Backoff.Delay(n, retryAfter); more {
			if s.Budget != nil && !s.Budget.AllowRetry(account) {
				delay = max(delay, 30*time.Second) // deferred: live traffic first
			}
			return core.StateRetryScheduled, s.Transitions.Retry(ctx, env, worker, s.now().Add(delay), string(out.Err.Code), n)
		}
	}
	final.ErrorCode, final.HTTPStatus = string(out.Err.Code), out.Err.Code.HTTPStatus()
	return core.StateDeadLettered, s.Transitions.DeadLetter(ctx, env, worker, Outcome{Final: final, Err: out.Err, DL: queue.DeadLetter{
		Reason: string(out.Err.Code), LastError: out.Err.Message, AccountID: account,
		LastVendorRequest: out.LastRequest, LastVendorResponse: out.LastResponse,
	}})
}
