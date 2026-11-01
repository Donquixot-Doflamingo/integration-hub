package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// TerminalEvent is a queued request reaching a terminal state. OnTerminal runs inside the
// transaction that commits it, so callbacks are enqueued exactly when the state change commits
// (no double callback on a lost race, none missing on a crash).
type TerminalEvent struct {
	Envelope core.Envelope
	State    core.State // succeeded, dead_lettered, expired
	Result   json.RawMessage
	Err      *core.CanonicalError
	Attempts int
	// Version is the integration version the final attempt ran on (its subscriptions apply).
	Version int
	At      time.Time
}

// OnTerminal is called in the terminal transaction (callbacks.Enqueue in the role wiring).
type OnTerminal func(ctx context.Context, tx pgx.Tx, ev TerminalEvent) error

// Transitions applies guarded state changes to the outbox and the requests row together.
type Transitions struct {
	DB         *pgxpool.Pool
	Queue      *queue.Outbox
	Ledger     audit.Ledger
	OnTerminal OnTerminal // may be nil
	Now        func() time.Time
}

func (t *Transitions) now() time.Time {
	if t.Now != nil {
		return t.Now().UTC()
	}
	return time.Now().UTC()
}

// ErrLost is returned when another actor already moved the envelope (queue.ErrLost).
var ErrLost = queue.ErrLost

// Outcome describes how an attempt run ended, for the terminal record.
type Outcome struct {
	Final  audit.Final
	Result json.RawMessage
	Err    *core.CanonicalError
	DL     queue.DeadLetter
}

// Succeed acks the envelope, finishes the request as succeeded, keeps the result and fires
// OnTerminal, all in one transaction.
func (t *Transitions) Succeed(ctx context.Context, env core.Envelope, worker string, o Outcome) error {
	o.Final.Status = core.StateSucceeded
	return t.terminal(ctx, env, o, func(tx pgx.Tx) error { return t.Queue.Ack(ctx, tx, env.RequestID, worker) })
}

// DeadLetter moves the envelope to the DLQ and finishes the request as dead_lettered.
func (t *Transitions) DeadLetter(ctx context.Context, env core.Envelope, worker string, o Outcome) error {
	o.Final.Status = core.StateDeadLettered
	return t.terminal(ctx, env, o, func(tx pgx.Tx) error { return t.Queue.DeadLetter(ctx, tx, env.RequestID, worker, o.DL) })
}

// Expire finishes a request whose TTL passed: a claimed envelope (worker found it expired) when
// worker is set, else a waiting one (scheduler).
func (t *Transitions) Expire(ctx context.Context, env core.Envelope, worker string, attempts int) error {
	o := Outcome{
		Final: audit.Final{Status: core.StateExpired, ErrorCode: string(core.CodeExpired), Attempts: attempts},
		Err:   &core.CanonicalError{Code: core.CodeExpired, Message: "The request expired before it could be delivered"},
	}
	return t.terminal(ctx, env, o, func(tx pgx.Tx) error {
		if worker != "" {
			return t.Queue.ExpireClaimed(ctx, tx, env.RequestID, worker)
		}
		return t.Queue.Expire(ctx, tx, env.RequestID)
	})
}

func (t *Transitions) terminal(ctx context.Context, env core.Envelope, o Outcome, queueOp func(pgx.Tx) error) error {
	now := t.now()
	o.Final.At = now
	return pgx.BeginFunc(ctx, t.DB, func(tx pgx.Tx) error {
		if err := queueOp(tx); err != nil {
			return err // ErrLost: another actor won; nothing else is written
		}
		if err := t.Ledger.Finish(ctx, tx, env.RequestID, o.Final); err != nil {
			return err
		}
		if err := t.Ledger.SaveResult(ctx, tx, env.RequestID, env.Tenant, o.Result, o.Err, now); err != nil {
			return err
		}
		if t.OnTerminal != nil {
			version := o.Final.IntegrationVersion
			if version == 0 {
				version = env.ResolvedVersion
			}
			ev := TerminalEvent{Envelope: env, State: o.Final.Status, Result: o.Result, Err: o.Err, Attempts: o.Final.Attempts, Version: version, At: now}
			if err := t.OnTerminal(ctx, tx, ev); err != nil {
				return fmt.Errorf("delivery: terminal hook: %w", err)
			}
		}
		return nil
	})
}

// Retry schedules the next attempt and records the progress.
func (t *Transitions) Retry(ctx context.Context, env core.Envelope, worker string, at time.Time, reason string, attempts int) error {
	return pgx.BeginFunc(ctx, t.DB, func(tx pgx.Tx) error {
		if err := t.Queue.Nack(ctx, tx, env.RequestID, worker, at, reason); err != nil {
			return err
		}
		return t.Ledger.Progress(ctx, tx, env.RequestID, core.StateRetryScheduled, attempts, t.now())
	})
}

// Hold puts a claimed envelope back unattempted (paused integration).
func (t *Transitions) Hold(ctx context.Context, env core.Envelope, worker string, at time.Time, reason string) error {
	return t.Queue.Defer(ctx, t.DB, env.RequestID, worker, at, reason)
}

// IsLost reports a lost race (the caller drops its result).
func IsLost(err error) bool { return errors.Is(err, queue.ErrLost) }
