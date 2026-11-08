package delivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

const (
	expiryBatch      = 100
	expiryBackoffMin = time.Second
	expiryBackoffMax = 10 * time.Minute
)

// stuckExpiry is a waiting envelope past its TTL whose expiry keeps failing (for example the
// request is no longer pending in the audit ledger, or a terminal hook fails).
type stuckExpiry struct {
	fails   int
	retryAt time.Time
}

// expiryTracker runs the scheduler's expiry sweep and remembers, per request id and in memory,
// the rows that failed: they are skipped until their backoff (1 s doubling to 10 min) passes, so
// they neither keep a sweep spinning nor hide newer expiries behind them (HUB-453). The zero
// value is ready; the scheduler leader is the only user, so it needs no lock.
type expiryTracker struct {
	stuck map[string]*stuckExpiry
}

// run expires every waiting envelope past its TTL. A pass that neither expires a row nor finds a
// new stuck one ends the sweep (the rest will be tried at the next tick), and so does a cancelled ctx. A row that fails is
// logged once, when it first gets stuck.
func (t *expiryTracker) run(ctx context.Context, log *slog.Logger, now func() time.Time,
	due func(ctx context.Context, n int) ([]core.Envelope, error), expire func(ctx context.Context, env core.Envelope) error) {
	if t.stuck == nil {
		t.stuck = map[string]*stuckExpiry{}
	}
	for ctx.Err() == nil {
		// Stuck rows sort first (oldest TTL): ask for enough that they cannot fill the whole page.
		limit := expiryBatch + len(t.stuck)
		envs, err := due(ctx, limit)
		if err != nil {
			log.ErrorContext(ctx, "expiry sweep failed", slog.String("error", err.Error()))
			return
		}
		progress, before := 0, len(t.stuck)
		seen := make(map[string]bool, len(envs))
		for _, env := range envs {
			if ctx.Err() != nil {
				return
			}
			seen[env.RequestID] = true
			at := now()
			if s := t.stuck[env.RequestID]; s != nil && at.Before(s.retryAt) {
				continue
			}
			err := expire(ctx, env)
			if err == nil || IsLost(err) {
				delete(t.stuck, env.RequestID)
				progress++
				continue
			}
			s := t.stuck[env.RequestID]
			if s == nil {
				s = &stuckExpiry{}
				t.stuck[env.RequestID] = s
				log.ErrorContext(ctx, "expire failed; the envelope is skipped with a growing backoff",
					slog.String("request_id", env.RequestID), slog.String("error", err.Error()))
			}
			s.fails++
			s.retryAt = at.Add(min(expiryBackoffMin<<min(s.fails-1, 20), expiryBackoffMax))
		}
		if len(envs) < limit || (progress == 0 && len(t.stuck) == before) {
			if len(envs) < limit { // the whole backlog was seen: forget rows that went away
				for id := range t.stuck {
					if !seen[id] {
						delete(t.stuck, id)
					}
				}
			}
			return
		}
	}
}
