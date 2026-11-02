package adminapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func init() { registrars = append(registrars, (*Server).registerIntegrations) }

func (s *Server) registerIntegrations() {
	s.handle("pauseIntegration", RoleEditor, true, "POST /integrations/{integration_id}/pause", s.setPaused(true))
	s.handle("resumeIntegration", RoleEditor, true, "POST /integrations/{integration_id}/resume", s.setPaused(false))
}

// objectTimes reads an object's created_at and updated_at from the change log; objects that
// predate it (the bootstrap config) report the snapshot build time for both.
func (s *Server) objectTimes(ctx context.Context, objectType, id string, fallback time.Time) (created, updated time.Time) {
	created, updated = fallback.UTC(), fallback.UTC()
	if s.d.Pool == nil {
		return created, updated
	}
	var lo, hi *time.Time
	if err := s.d.Pool.QueryRow(ctx, `SELECT min(at), max(at) FROM hub.change_log WHERE object_type = $1 AND object_id = $2`, objectType, id).
		Scan(&lo, &hi); err == nil && lo != nil {
		created, updated = lo.UTC(), hi.UTC()
	}
	return created, updated
}

// integrationView is the contract's Integration (without the active version in full).
func (s *Server) integrationView(ctx context.Context, snap *registry.Snapshot, in *registry.Integration) map[string]any {
	latest := 0
	for v := range in.Versions {
		latest = max(latest, v)
	}
	created, updated := s.objectTimes(ctx, "integration", in.Integration, snap.BuiltAt)
	var active, latestV *int
	if in.ActiveVersion > 0 {
		active = &in.ActiveVersion
	}
	if latest > 0 {
		latestV = &latest
	}
	grants := in.Grants
	if grants == nil {
		grants = []string{}
	}
	return map[string]any{
		"integration_id": in.Integration, "owner_tenant": in.OwnerTenant, "capability": in.Capability,
		"description": nullable(in.Description), "grants": grants, "moves_money_or_quota": in.MovesMoneyOrQuota,
		"active_version": active, "latest_version": latestV, "has_draft": false,
		"paused": in.Paused, "paused_reason": nullable(in.PausedReason), "active": nil,
		"created_at": created, "updated_at": updated,
	}
}

func (s *Server) setPaused(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		if err := decode(w, r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
		reason, err := reasonOf(r, body.Reason)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if s.d.Publisher == nil {
			s.fail(w, r, errors.New("no publisher"))
			return
		}
		res, err := s.d.Publisher.SetPaused(r.Context(), r.PathValue("integration_id"), paused, principal(r).Actor(), reason)
		if err != nil {
			s.fail(w, r, controlError(err))
			return
		}
		in := res.Snapshot.Integrations[r.PathValue("integration_id")]
		writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "integration": s.integrationView(r.Context(), res.Snapshot, in)})
	}
}
