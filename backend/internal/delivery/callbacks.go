package delivery

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// CallbackHook enqueues the terminal callback (request.completed, request.failed,
// request.expired) for every subscription of the request's integration version, inside the
// terminal transaction (DESIGN §12.1): the callback exists exactly when the state change
// committed. The event id is derived from the request id (one terminal event per request), so
// a retried transaction cannot enqueue it twice. A callback whose own payload is unusable is
// skipped and counted instead of failing the transition (callbacks.EnqueueTerminal).
func CallbackHook(enq *callbacks.Enqueuer) OnTerminal {
	return func(ctx context.Context, tx pgx.Tx, ev TerminalEvent) error {
		e := &callbacks.Event{
			EventID: "evt_" + strings.TrimPrefix(ev.Envelope.RequestID, "req_"), OccurredAt: ev.At,
			TenantID: ev.Envelope.Tenant, IntegrationID: ev.Envelope.IntegrationID, IntegrationVersion: ev.Version,
			Operation: ev.Envelope.Operation, RequestID: ev.Envelope.RequestID,
		}
		if ev.Envelope.IdempotencyKey != nil {
			e.IdempotencyKey = *ev.Envelope.IdempotencyKey
		}
		switch ev.State {
		case core.StateSucceeded:
			e.Data = &callbacks.Completed{Result: ev.Result, Attempts: ev.Attempts, CompletedAt: ev.At}
		case core.StateExpired:
			e.Data = &callbacks.Expired{Attempts: ev.Attempts, ExpiresAt: ev.Envelope.ExpiresAt}
		case core.StateDeadLettered, core.StateFailed:
			cerr := core.CanonicalError{Code: core.CodeInternalError, Message: "The request failed"}
			if ev.Err != nil {
				cerr = *ev.Err
			}
			e.Data = &callbacks.Failed{Error: cerr, Attempts: ev.Attempts, FailedAt: ev.At}
		case core.StateAccepted, core.StateInFlight, core.StateRetryScheduled, core.StateParked:
			return nil
		}
		_, err := enq.EnqueueTerminal(ctx, tx, e)
		return err
	}
}
