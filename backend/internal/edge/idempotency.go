package edge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Idempotency is the Redis store (idempotency.Store).
type Idempotency interface {
	Begin(ctx context.Context, k idempotency.Key, bodyHash, requestID string) (idempotency.Outcome, error)
	Complete(ctx context.Context, k idempotency.Key, requestID string, r idempotency.Response) error
	MarkCompleted(ctx context.Context, k idempotency.Key, requestID string) error
	Release(ctx context.Context, k idempotency.Key, requestID string) error
}

// idemClaim is a fresh claim on an Idempotency-Key, settled once the response is known.
type idemClaim struct {
	key       idempotency.Key
	requestID string
}

// idempotencyBegin runs DESIGN §5.1 step 4. It returns a fresh claim to settle later, or
// handled = true when it already answered: a replay of the stored response, a 409 for a body
// mismatch or a sync request in progress, the original 202 for a queued request in progress,
// or an error when Redis is down and the integration moves money or quota (fail closed). With
// Redis down otherwise the request passes through without deduplication.
func (s *Server) idempotencyBegin(w http.ResponseWriter, r *http.Request, p *prepared) (*idemClaim, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" || s.deps.Idempotency == nil {
		return nil, false
	}
	k := idempotency.Key{Tenant: p.caller.Tenant.Tenant, Integration: p.integration.Integration, Key: key}
	start := time.Now()
	out, err := s.deps.Idempotency.Begin(r.Context(), k, idempotency.BodyHash(p.operation, p.raw), RequestID(r.Context()))
	p.timings.Observe(telemetry.StageIdempotency, start)
	switch {
	case errors.Is(err, idempotency.ErrInvalidInput):
		s.fail(w, r, p, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Idempotency-Key must be 1–255 visible ASCII characters",
			Reason: core.Ptr("header"), Details: []core.ErrorDetail{{Pointer: "", Message: "invalid Idempotency-Key"}}})
		return nil, true
	case err != nil:
		s.log.WarnContext(r.Context(), "idempotency store unavailable", slog.String("integration", p.integration.Integration), slog.String("error", err.Error()))
		if p.integration.MovesMoneyOrQuota {
			s.fail(w, r, p, &core.CanonicalError{Code: core.CodeInternalError, Message: "Duplicate protection is unavailable; retry later",
				Retryable: true, RetryAfterMS: core.Ptr[int64](1000), Reason: core.Ptr("idempotency_unavailable")})
			return nil, true
		}
		return nil, false // pass through, no dedup: never Complete or Release
	}
	switch out.State {
	case idempotency.Fresh:
		return &idemClaim{key: k, requestID: RequestID(r.Context())}, false
	case idempotency.Completed:
		for name, v := range out.Response.Header {
			w.Header()[name] = v
		}
		w.Header().Set("X-Hub-Idempotent-Replay", "true")
		w.Header().Set("X-Hub-Request-Id", out.RequestID)
		w.WriteHeader(out.Response.Status)
		_, _ = w.Write(out.Response.Body)
		p.trail.Add(audit.RequestCompleted, 0, nil, map[string]any{"replay_of": out.RequestID, "status": out.Response.Status})
		s.record(r, p, "succeeded", out.Response.Status, string(core.CodeIdempotentReplay))
		return nil, true
	case idempotency.CompletedNotStored:
		// The first request succeeded and the vendor acted, but its response was too large to
		// keep (or the store failed). Running it again could act twice, so the retry is refused
		// with the contract's IDEMPOTENCY_CONFLICT until the key's 24 h TTL ends.
		s.fail(w, r, p, &core.CanonicalError{Code: core.CodeIdempotencyConflict,
			Message: "A request with this Idempotency-Key already completed (" + out.RequestID + "); its response was not stored for replay",
			Reason:  core.Ptr("completed_not_stored")})
		return nil, true
	case idempotency.InProgress:
		if s.queued() && p.integration.Active().Delivery.Mode != registry.ModeSync && !s.interactive(p) {
			w.Header().Set("X-Hub-Idempotent-Replay", "true")
			writeAccepted(w, out.RequestID, "in_flight", "idempotent_in_progress")
			p.trail.Add(audit.RequestCompleted, 0, nil, map[string]any{"replay_of": out.RequestID, "status": http.StatusAccepted})
			s.record(r, p, "succeeded", http.StatusAccepted, string(core.CodeIdempotentReplay))
			return nil, true
		}
		s.fail(w, r, p, &core.CanonicalError{Code: core.CodeIdempotencyConflict, Message: "A request with this Idempotency-Key is still in progress",
			Retryable: true, RetryAfterMS: core.Ptr[int64](1000), Reason: core.Ptr("in_progress")})
		return nil, true
	default: // Conflict
		s.fail(w, r, p, &core.CanonicalError{Code: core.CodeIdempotencyConflict, Message: "This Idempotency-Key was used with a different request body",
			Reason: core.Ptr("body_mismatch")})
		return nil, true
	}
}

// settle stores a replayable answer (200, 202). If that cannot be stored (the response is over
// the replay limit, or the store failed) the request still succeeded and the vendor acted, so
// the key is kept as "completed, response not stored" and never released: a retry must not run
// the vendor call again. Any other status releases the key so a retry runs fresh.
func (c *idemClaim) settle(ctx context.Context, s *Server, bw *bufferedWriter) {
	if c == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if bw.status == http.StatusOK || bw.status == http.StatusAccepted {
		err := s.deps.Idempotency.Complete(ctx, c.key, c.requestID, idempotency.Response{Status: bw.status, Header: bw.header, Body: bw.body.Bytes()})
		if err == nil || errors.Is(err, idempotency.ErrNotOwner) {
			return
		}
		s.log.WarnContext(ctx, "idempotency result not stored", slog.String("error", err.Error()))
		if err := s.deps.Idempotency.MarkCompleted(ctx, c.key, c.requestID); err != nil && !errors.Is(err, idempotency.ErrNotOwner) {
			// The lease still holds the key (in progress) until it expires; never Release here.
			s.log.WarnContext(ctx, "idempotency key not marked completed", slog.String("error", err.Error()))
		}
		return
	}
	if err := s.deps.Idempotency.Release(ctx, c.key, c.requestID); err != nil && !errors.Is(err, idempotency.ErrNotOwner) {
		s.log.WarnContext(ctx, "idempotency key not released", slog.String("error", err.Error()))
	}
}
