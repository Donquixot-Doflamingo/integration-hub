package adminapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Registrar endpoints (HUB-206, DESIGN §12.1, §16.1): tenant webhook destinations in
// registrar/<id>.yaml. URLs pass the callbacks SSRF guard at registration (delivery re-checks
// at dial time). Signing secrets are stored like account secrets (secret_ref
// localfile://<id>) and shown exactly once, at create and rotate.

func init() { registrars = append(registrars, (*Server).registerRegistrar) }

func (s *Server) registerRegistrar() {
	s.handle("listRegistrarEndpoints", RoleTenantViewer, false, "GET /registrar-endpoints", s.listRegistrar)
	s.handle("createRegistrarEndpoint", RoleAdmin, true, "POST /registrar-endpoints", s.createRegistrar)
	s.handle("getRegistrarEndpoint", RoleTenantViewer, false, "GET /registrar-endpoints/{endpoint_id}", s.getRegistrar)
	s.handle("updateRegistrarEndpoint", RoleAdmin, true, "PATCH /registrar-endpoints/{endpoint_id}", s.updateRegistrar)
	s.handle("deleteRegistrarEndpoint", RoleAdmin, true, "DELETE /registrar-endpoints/{endpoint_id}", s.deleteRegistrar)
	s.handle("rotateRegistrarSecret", RoleAdmin, true, "POST /registrar-endpoints/{endpoint_id}/rotate-secret", s.rotateRegistrarSecret)
}

var defaultRegistrarGuard = sync.OnceValue(func() *callbacks.Guard { return callbacks.NewGuard(callbacks.GuardOptions{}) })

// checkRegistrarURL vets a registrar URL with the SSRF guard (tests replace it to inject a
// resolver). Any public host is accepted: Deps carries no callback host allowlist yet.
var checkRegistrarURL = func(ctx context.Context, raw string) error { return defaultRegistrarGuard().CheckURL(ctx, raw) }

func (s *Server) vetRegistrarURL(ctx context.Context, raw string) error {
	if !strings.HasPrefix(raw, "https://") {
		return badRequest("/url", "url must be https")
	}
	err := checkRegistrarURL(ctx, raw)
	var blocked *callbacks.BlockedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &blocked):
		return badRequest("/url", fmt.Sprintf("url refused by the SSRF guard (%s): %s", blocked.Class, blocked.Detail))
	}
	return badRequest("/url", "url host cannot be resolved")
}

func registrarPath(id string) string { return "registrar/" + id + ".yaml" }

// subscribers counts the integration versions that subscribe to endpoint (active only, or any).
func subscribers(snap *registry.Snapshot, endpoint string, activeOnly bool) int {
	n := 0
	for _, in := range snap.Integrations {
		for num, v := range in.Versions {
			if activeOnly && num != in.ActiveVersion {
				continue
			}
			if slices.ContainsFunc(v.Webhooks, func(w registry.Subscription) bool { return w.Endpoint == endpoint }) {
				n++
			}
		}
	}
	return n
}

func (s *Server) registrarView(ctx context.Context, snap *registry.Snapshot, e *registry.RegistrarEndpoint) map[string]any {
	created, updated := s.objectTimes(ctx, "registrar_endpoint", e.EndpointID, snap.BuiltAt)
	version := max(e.SecretVersion, 1)
	var overlap *time.Time
	if e.SecretOverlapUntil != nil && s.d.Now().Before(*e.SecretOverlapUntil) {
		overlap = e.SecretOverlapUntil
	}
	return map[string]any{"endpoint_id": e.EndpointID, "tenant_id": e.Tenant, "url": e.URL, "description": nullable(e.Description),
		"disabled": e.Disabled, "secret_version": version, "secret_overlap_until": overlap,
		"subscribed_by": subscribers(snap, e.EndpointID, true), "created_at": created, "updated_at": updated}
}

// registrarAudit is the change-log form of an endpoint (no secret, only its version).
func registrarAudit(e registry.RegistrarEndpoint) map[string]any {
	return map[string]any{"tenant": e.Tenant, "url": e.URL, "description": e.Description, "disabled": e.Disabled,
		"secret_version": max(e.SecretVersion, 1), "secret_overlap_until": e.SecretOverlapUntil}
}

func (s *Server) listRegistrar(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, tenant := principal(r), r.URL.Query().Get("tenant_id")
	if tenant != "" && !p.SeesTenant(tenant) {
		s.fail(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "tenant_id is outside the caller's tenants"})
		return
	}
	var ids []string
	for id, e := range snap.Registrar {
		if (tenant == "" || e.Tenant == tenant) && p.SeesTenant(e.Tenant) {
			ids = append(ids, id)
		}
	}
	ctx := r.Context()
	items, next, err := accessPage(r, ids, func(id string) map[string]any { return s.registrarView(ctx, snap, snap.Registrar[id]) })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getRegistrar(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	e, ok := snap.Registrar[r.PathValue("endpoint_id")]
	if !ok || !principal(r).SeesTenant(e.Tenant) {
		s.fail(w, r, notFound("Registrar endpoint"))
		return
	}
	w.Header().Set("ETag", accessETag(e))
	writeJSON(w, http.StatusOK, s.registrarView(r.Context(), snap, e))
}

func (s *Server) writeRegistrar(w http.ResponseWriter, r *http.Request, status int, res control.Result, id, secret string) {
	e := res.Snapshot.Registrar[id]
	w.Header().Set("ETag", accessETag(e))
	out := map[string]any{"change_id": res.ChangeID, "endpoint": s.registrarView(r.Context(), res.Snapshot, e)}
	if secret != "" {
		out["signing_secret"] = secret
	}
	writeJSON(w, status, out)
}

func newSigningSecret() string { return "whsec_" + randomToken(32) }

func (s *Server) createRegistrar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EndpointID  string  `json:"endpoint_id"`
		TenantID    string  `json:"tenant_id"`
		URL         string  `json:"url"`
		Description *string `json:"description"`
		Reason      string  `json:"reason"`
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
	id := body.EndpointID
	if !accessSlugRE.MatchString(id) {
		s.fail(w, r, badRequest("/endpoint_id", "endpoint_id must match "+accessSlugRE.String()))
		return
	}
	if err := s.vetRegistrarURL(r.Context(), body.URL); err != nil {
		s.fail(w, r, err)
		return
	}
	rot, err := s.accessRotator()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	e := registry.RegistrarEndpoint{EndpointID: id, Tenant: body.TenantID, URL: body.URL, SecretRef: "localfile://" + id, SecretVersion: 1}
	if body.Description != nil {
		e.Description = *body.Description
	}
	secret := newSigningSecret()
	stored := false
	ctx := r.Context()
	res, err := pub.Edit(ctx, func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		if _, ok := snap.Tenants[e.Tenant]; !ok {
			return control.Change{}, badRequest("/tenant_id", fmt.Sprintf("unknown tenant %q", e.Tenant))
		}
		if _, ok := snap.Registrar[id]; ok {
			return control.Change{}, fmt.Errorf("%w: registrar endpoint %q exists", control.ErrConflict, id)
		}
		if _, ok := t.Get(registrarPath(id)); ok {
			return control.Change{}, fmt.Errorf("%w: %s exists", control.ErrConflict, registrarPath(id))
		}
		doc, err := yaml.Marshal(e)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(registrarPath(id), append([]byte("# Created through the admin API (createRegistrarEndpoint). The signing secret lives only in the credential store.\n"), doc...))
		sec := creds.AccountSecrets{Active: creds.NewSlot(creds.AccountSecrets{}, map[string]string{creds.SigningSecretKey: secret}, nil, s.d.Now())}
		if err := rot.PutCredentials(ctx, id, &sec, true); err != nil {
			return control.Change{}, err
		}
		stored = true
		return control.Change{Actor: principal(r).Actor(), Action: "createRegistrarEndpoint", ObjectType: "registrar_endpoint", ObjectID: id,
			Reason: reason, After: registrarAudit(e)}, nil
	})
	if err != nil {
		if stored {
			if rerr := rot.PutCredentials(context.WithoutCancel(ctx), id, nil, false); rerr != nil {
				s.d.Log.ErrorContext(ctx, "removing the secret of a failed registrar create failed", slog.String("endpoint", id), slog.String("error", rerr.Error()))
			}
		}
		s.fail(w, r, controlError(err))
		return
	}
	s.writeRegistrar(w, r, http.StatusCreated, res, id, secret)
}

func (s *Server) updateRegistrar(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	rs, _ := body["reason"].(string)
	reason, err := reasonOf(r, rs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for k := range body {
		if !slices.Contains([]string{"url", "description", "disabled", "reason"}, k) {
			s.fail(w, r, badRequest("/"+k, "unknown field "+k))
			return
		}
	}
	if v, ok := body["url"]; ok {
		u, isStr := v.(string)
		if !isStr {
			s.fail(w, r, badRequest("/url", "url must be a string"))
			return
		}
		if err := s.vetRegistrarURL(r.Context(), u); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("endpoint_id")
	res, err := pub.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		e, ok := snap.Registrar[id]
		if !ok {
			return control.Change{}, notFound("Registrar endpoint")
		}
		if err := accessIfMatch(r, accessETag(e)); err != nil {
			return control.Change{}, err
		}
		next := *e
		if u, ok := body["url"].(string); ok {
			next.URL = u
		}
		var err error
		if next.Description, err = tenantText(body, "description", e.Description); err != nil {
			return control.Change{}, err
		}
		if v, ok := body["disabled"]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return control.Change{}, badRequest("/disabled", "disabled must be a boolean")
			}
			next.Disabled = b
		}
		kv := map[string]any{"url": next.URL, "description": nil, "disabled": nil}
		if next.Description != "" {
			kv["description"] = next.Description
		}
		if next.Disabled {
			kv["disabled"] = true
		}
		if err := accessSetKeys(t, registrarPath(id), kv); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "updateRegistrarEndpoint", ObjectType: "registrar_endpoint", ObjectID: id,
			Reason: reason, Before: registrarAudit(*e), After: registrarAudit(next)}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeRegistrar(w, r, http.StatusOK, res, id, "")
}

func (s *Server) deleteRegistrar(w http.ResponseWriter, r *http.Request) {
	reason, err := reasonOf(r, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("endpoint_id")
	var secretID string
	res, err := pub.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		e, ok := snap.Registrar[id]
		if !ok {
			return control.Change{}, notFound("Registrar endpoint")
		}
		// Every version that names it would fail the snapshot build, so any subscriber blocks.
		if n := subscribers(snap, id, false); n > 0 {
			return control.Change{}, fmt.Errorf("%w: %d integration version(s) subscribe to registrar endpoint %s", control.ErrConflict, n, id)
		}
		secretID, _ = creds.SecretID(e.SecretRef)
		t.Delete(registrarPath(id))
		return control.Change{Actor: principal(r).Actor(), Action: "deleteRegistrarEndpoint", ObjectType: "registrar_endpoint", ObjectID: id,
			Reason: reason, Before: registrarAudit(*e)}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	// Pending callback deliveries to the endpoint can never be sent: dead-letter them.
	if s.d.Pool != nil {
		if _, err := s.d.Pool.Exec(r.Context(), `UPDATE hub.callback_deliveries SET status = 'dead_lettered', last_error = 'endpoint_deleted',
			claimed_by = NULL, claimed_until = NULL, updated_at = now() WHERE endpoint_id = $1 AND status IN ('pending', 'retry_scheduled')`, id); err != nil {
			s.d.Log.ErrorContext(r.Context(), "dead-lettering deliveries of a deleted registrar endpoint failed", slog.String("endpoint", id), slog.String("error", err.Error()))
		}
	}
	if secretID != "" && s.d.Rotator != nil {
		if err := s.d.Rotator.PutCredentials(r.Context(), secretID, nil, false); err != nil {
			s.d.Log.ErrorContext(r.Context(), "deleting the signing secret of a deleted registrar endpoint failed", slog.String("endpoint", id), slog.String("error", err.Error()))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID})
}

func (s *Server) rotateRegistrarSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OverlapMS *int64 `json:"overlap_ms"`
		Reason    string `json:"reason"`
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
	if body.OverlapMS == nil || *body.OverlapMS < 0 || time.Duration(*body.OverlapMS)*time.Millisecond > maxOverlap {
		s.fail(w, r, badRequest("/overlap_ms", "overlap_ms must be 0 to 2592000000"))
		return
	}
	rot, err := s.accessRotator()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("endpoint_id")
	now := s.d.Now()
	secret := newSigningSecret()
	var secretID string
	var prev *creds.AccountSecrets
	written := false
	ctx := r.Context()
	res, err := pub.Edit(ctx, func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		e, ok := snap.Registrar[id]
		if !ok {
			return control.Change{}, notFound("Registrar endpoint")
		}
		sid, err := creds.SecretID(e.SecretRef)
		if err != nil {
			return control.Change{}, err
		}
		secretID = sid
		cur, found, err := rot.ReadCredentials(secretID)
		if err != nil {
			return control.Change{}, err
		}
		next := creds.AccountSecrets{Active: creds.NewSlot(cur, map[string]string{creds.SigningSecretKey: secret}, nil, now)}
		var until *time.Time
		if found {
			prev = &cur
			if *body.OverlapMS > 0 { // the old secret keeps signing (second v1=) until the overlap ends
				end := now.Add(time.Duration(*body.OverlapMS) * time.Millisecond).UTC().Truncate(time.Millisecond)
				old := cur.Active
				old.ExpiresAt, until = &end, &end
				next.Next = &old
			}
		}
		var overlap any
		if until != nil {
			overlap = *until
		}
		if err := accessSetKeys(t, registrarPath(id), map[string]any{"secret_version": next.Active.Version, "secret_overlap_until": overlap}); err != nil {
			return control.Change{}, err
		}
		if err := rot.PutCredentials(ctx, secretID, &next, false); err != nil {
			return control.Change{}, err
		}
		written = true
		return control.Change{Actor: principal(r).Actor(), Action: "rotateRegistrarSecret", ObjectType: "registrar_endpoint", ObjectID: id, Reason: reason,
			Before: map[string]any{"secret": cur.Views()}, After: map[string]any{"secret": next.Views(), "overlap_ms": *body.OverlapMS}}, nil
	})
	if err != nil {
		if written { // the config edit did not land: put the previous secrets back
			if rerr := rot.PutCredentials(context.WithoutCancel(ctx), secretID, prev, false); rerr != nil {
				s.d.Log.ErrorContext(ctx, "restoring the signing secret of a failed rotation failed", slog.String("endpoint", id), slog.String("error", rerr.Error()))
			}
		}
		s.fail(w, r, controlError(err))
		return
	}
	s.writeRegistrar(w, r, http.StatusOK, res, id, secret)
}
