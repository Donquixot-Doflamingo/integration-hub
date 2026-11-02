package edge

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// Vendor sessions (DESIGN §3.1, §6.3 row 6, §14, HUB-175). Step 1: the response transformer maps
// session data, the edge opens a session pinned to the version and account that served it and
// answers X-Hub-Session. Step >= 2: X-Hub-Session loads that session; the request resolves to its
// exact version and account and the transformer reads its data as session.*. The lookup is one
// primary-key read, made only for requests that carry the header (the one documented exception
// to "no DB on the sync path", TECH_SPEC §6.3).

func sessionNotFound() *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeSessionNotFound, Message: "Unknown or expired session"}
}

// resolveSession loads the caller's session and pins the resolution to it. Unknown, expired,
// another tenant's or integration's session, or a session whose version or account is gone from
// the snapshot: 404 SESSION_NOT_FOUND.
func (s *Server) resolveSession(ctx context.Context, p *prepared, integration, id string) (resolve.Resolution, *core.CanonicalError) {
	if s.deps.Sessions == nil {
		return resolve.Resolution{}, sessionNotFound() // no session store: no session can be open
	}
	tenant := p.caller.Tenant.Tenant
	sess, err := sessions.Get(ctx, s.deps.Sessions, tenant, integration, id)
	if errors.Is(err, sessions.ErrNotFound) {
		return resolve.Resolution{}, sessionNotFound()
	}
	if err != nil {
		s.log.ErrorContext(ctx, "session lookup failed", slog.String("integration", integration), slog.String("error", err.Error()))
		return resolve.Resolution{}, internalError()
	}
	res, err := resolve.ResolveSession(p.caller.Snap, tenant, integration, sess.Version, sess.AccountID)
	if err != nil {
		s.log.WarnContext(ctx, "session target gone", slog.String("integration", integration), slog.String("error", err.Error()))
		return resolve.Resolution{}, sessionNotFound()
	}
	p.session = &sess
	return res, nil
}

// sessionData is the transformer's session.* of a later step (nil on step 1).
func (p *prepared) sessionData() map[string]any {
	if p.session == nil {
		return nil
	}
	return p.session.Data
}

// openSession opens a vendor session for a successful step 1 (call it only when res.Err is nil) whose response transformer mapped
// session data, and returns its id ("" when there is nothing to open). A later step never opens
// another session: its session: output is ignored. Data the store refuses (an Aadhaar-like
// number, too large) is a broken mapping: TRANSFORM_FAILED. The session open is recorded on
// response.transformed (meta only, never the data).
func (s *Server) openSession(ctx context.Context, p *prepared, trail *audit.Trail, res delivery.Result) (string, *core.CanonicalError) {
	if res.Session == nil || p.session != nil { // res.Session is only set on success
		return "", nil
	}
	integration := p.integration.Integration
	if s.deps.Sessions == nil {
		s.log.ErrorContext(ctx, "response maps a vendor session but no session store is configured", slog.String("integration", integration))
		return "", internalError()
	}
	sess, err := sessions.Create(ctx, s.deps.Sessions, sessions.Open{
		TenantID: p.caller.Tenant.Tenant, IntegrationID: integration, Version: p.res.VersionNumber, AccountID: res.Account, Data: res.Session,
	})
	switch {
	case errors.Is(err, sessions.ErrSensitiveData), errors.Is(err, sessions.ErrInvalid):
		s.log.WarnContext(ctx, "session data refused", slog.String("integration", integration), slog.String("error", err.Error()))
		trail.Annotate(audit.ResponseTransformed, "session", map[string]any{"event": "refused"})
		return "", &core.CanonicalError{Code: core.CodeTransformFailed, Message: "The upstream response could not be mapped"}
	case err != nil:
		s.log.ErrorContext(ctx, "session create failed", slog.String("integration", integration), slog.String("error", err.Error()))
		return "", internalError()
	}
	meta := delivery.SessionMeta(sess.ID, "opened", sess.Version, sess.AccountID)
	meta["expires_at"] = sess.ExpiresAt
	trail.Annotate(audit.ResponseTransformed, "session", meta)
	return sess.ID, nil
}

// setSessionHeader adds X-Hub-Session to a 200 that opened a session (contract InvokeOk).
func setSessionHeader(w http.ResponseWriter, id string) {
	if id != "" {
		w.Header().Set("X-Hub-Session", id)
	}
}
