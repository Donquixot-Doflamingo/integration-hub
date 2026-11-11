package adminapi

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// "Sync now" (HUB-484, AUTH_PLAN §6.2): syncTenants and syncTenant run the ISFC reconciler on
// demand and answer with what it did. The reconciler itself lives in internal/isfcsync; the admin
// package sees it only through TenantSyncer (cmd/hub wires it).

func init() { registrars = append(registrars, (*Server).registerTenantSync) }

func (s *Server) registerTenantSync() {
	s.handle("syncTenants", RoleAdmin, true, "POST /tenants/sync", s.syncTenants)
	s.handle("syncTenant", RoleAdmin, true, "POST /tenants/{tenant_id}/sync", s.syncTenant)
}

// TenantSyncer runs a reconcile on behalf of actor (the operator, who the change log names for a
// manual run). SyncOne names a Hub tenant id. An ISFC that cannot be reached is
// not an error: the result says failed and the Hub keeps its last good state.
type TenantSyncer interface {
	SyncAll(ctx context.Context, actor, reason string) (SyncResult, error)
	SyncOne(ctx context.Context, actor, tenant, reason string) (SyncResult, error)
}

// SyncResult is one run (hub-admin TenantSyncResult). Updated counts every tenant whose ISFC copy
// changed, re-enabled ones included.
type SyncResult struct {
	ChangeID  string
	Outcome   string // ok | partial | failed
	Error     string
	Started   time.Time
	Finished  time.Time
	Created   int
	Updated   int
	Disabled  int
	Unchanged int
}

// Errors a TenantSyncer returns for the 409 cases. Other control errors (control.ErrNotFound for an
// unknown tenant) map as everywhere else.
var (
	ErrSyncBusy      = errors.New("a sync is already running")
	ErrSyncNotLinked = errors.New("the tenant has no ISFC link")
)

// syncRequest reads the body of both sync operations: a reason.
func (s *Server) syncRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return "", false
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return "", false
	}
	if s.d.Sync == nil {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: "Tenants are not managed by ISFC on this deployment (HUB_TENANT_SOURCE is not isfc)"})
		return "", false
	}
	return reason, true
}

// syncError maps the syncer's refusals.
func syncError(err error) error {
	switch {
	case errors.Is(err, ErrSyncBusy):
		return &Error{Status: http.StatusConflict, Code: CodeConflict, Retryable: true, Details: []Detail{}, Message: "Another sync is running"}
	case errors.Is(err, ErrSyncNotLinked):
		return &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{}, Message: "The tenant has no ISFC link"}
	}
	return controlError(err)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) syncTenants(w http.ResponseWriter, r *http.Request) {
	reason, ok := s.syncRequest(w, r)
	if !ok {
		return
	}
	res, err := s.d.Sync.SyncAll(r.Context(), principal(r).Actor(), reason)
	if err != nil {
		s.fail(w, r, syncError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"change_id": res.ChangeID, "result": res.Outcome, "started_at": res.Started.UTC(), "finished_at": res.Finished.UTC(),
		"created": res.Created, "updated": res.Updated, "disabled": res.Disabled, "unchanged": res.Unchanged, "error": nullIfEmpty(res.Error),
	})
}

func (s *Server) syncTenant(w http.ResponseWriter, r *http.Request) {
	reason, ok := s.syncRequest(w, r)
	if !ok {
		return
	}
	id := r.PathValue("tenant_id")
	res, err := s.d.Sync.SyncOne(r.Context(), principal(r).Actor(), id, reason)
	if err != nil {
		s.fail(w, r, syncError(err))
		return
	}
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, ok := snap.Tenants[id]
	if !ok {
		s.fail(w, r, notFound("Tenant"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "result": res.Outcome, "error": nullIfEmpty(res.Error),
		"tenant": s.tenantView(r.Context(), snap, t)})
}
