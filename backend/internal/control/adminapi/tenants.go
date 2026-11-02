package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Tenants (HUB-206, DESIGN §16.1): calling services. Each is tenants/<id>.yaml of the config
// tree; its API keys (hashed) live in the same file and are managed by the api-keys routes.

func init() { registrars = append(registrars, (*Server).registerTenants) }

func (s *Server) registerTenants() {
	s.handle("listTenants", RoleViewer, false, "GET /tenants", s.listTenants)
	s.handle("createTenant", RoleAdmin, true, "POST /tenants", s.createTenant)
	s.handle("getTenant", RoleTenantViewer, false, "GET /tenants/{tenant_id}", s.getTenant)
	s.handle("updateTenant", RoleAdmin, true, "PATCH /tenants/{tenant_id}", s.updateTenant)
}

func tenantPath(id string) string { return "tenants/" + id + ".yaml" }

func tenantName(t *registry.Tenant) string {
	if t.DisplayName != "" {
		return t.DisplayName
	}
	return t.Tenant
}

// tenantCounts are integrations_owned and active_api_keys.
func (s *Server) tenantCounts(snap *registry.Snapshot, t *registry.Tenant) (owned, keys int) {
	for _, in := range snap.Integrations {
		if in.OwnerTenant == t.Tenant {
			owned++
		}
	}
	now := s.d.Now()
	for _, k := range t.APIKeys {
		if st := keyState(k, now); st == "active" || st == "expiring" {
			keys++
		}
	}
	return owned, keys
}

func (s *Server) tenantView(ctx context.Context, snap *registry.Snapshot, t *registry.Tenant) map[string]any {
	created, updated := s.objectTimes(ctx, "tenant", t.Tenant, snap.BuiltAt)
	owned, keys := s.tenantCounts(snap, t)
	return map[string]any{
		"tenant_id": t.Tenant, "display_name": tenantName(t), "description": nullable(t.Description), "contact": nullable(t.Contact),
		"rate_limit": map[string]any{"rps": t.RateLimit.RPS, "burst": t.RateLimit.Burst}, "disabled": t.Disabled,
		"integrations_owned": owned, "active_api_keys": keys, "created_at": created, "updated_at": updated,
	}
}

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sub := strings.ToLower(r.URL.Query().Get("q"))
	var ids []string
	for id, t := range snap.Tenants {
		if sub == "" || strings.Contains(strings.ToLower(id), sub) || strings.Contains(strings.ToLower(tenantName(t)), sub) {
			ids = append(ids, id)
		}
	}
	ctx := r.Context()
	items, next, err := accessPage(r, ids, func(id string) map[string]any {
		t := snap.Tenants[id]
		_, updated := s.objectTimes(ctx, "tenant", id, snap.BuiltAt)
		owned, keys := s.tenantCounts(snap, t)
		return map[string]any{"tenant_id": id, "display_name": tenantName(t), "disabled": t.Disabled,
			"integrations_owned": owned, "active_api_keys": keys, "updated_at": updated}
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, ok := snap.Tenants[r.PathValue("tenant_id")]
	if !ok || !principal(r).SeesTenant(t.Tenant) {
		s.fail(w, r, notFound("Tenant"))
		return
	}
	w.Header().Set("ETag", accessETag(t))
	writeJSON(w, http.StatusOK, s.tenantView(r.Context(), snap, t))
}

// tenantLimit merge-patches base with a JSON RateLimit.
func tenantLimit(m map[string]any, base registry.TenantLimit) (registry.TenantLimit, error) {
	for k, v := range m {
		n, err := accessInt(v, "/rate_limit/"+k)
		if err != nil {
			return base, err
		}
		switch k {
		case "rps":
			base.RPS = n
		case "burst":
			base.Burst = n
		default:
			return base, badRequest("/rate_limit/"+k, "unknown rate limit field")
		}
	}
	return base, nil
}

// tenantText reads an optional nullable string field of a merge-patch body.
func tenantText(m map[string]any, key string, base string) (string, error) {
	v, ok := m[key]
	if !ok {
		return base, nil
	}
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	}
	return base, badRequest("/"+key, key+" must be a string or null")
}

func (s *Server) writeTenant(w http.ResponseWriter, r *http.Request, status int, res control.Result, id string) {
	t := res.Snapshot.Tenants[id]
	w.Header().Set("ETag", accessETag(t))
	writeJSON(w, status, map[string]any{"change_id": res.ChangeID, "tenant": s.tenantView(r.Context(), res.Snapshot, t)})
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantID    string         `json:"tenant_id"`
		DisplayName string         `json:"display_name"`
		Description *string        `json:"description"`
		Contact     *string        `json:"contact"`
		RateLimit   map[string]any `json:"rate_limit"`
		Reason      string         `json:"reason"`
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
	id := body.TenantID
	if !accessSlugRE.MatchString(id) {
		s.fail(w, r, badRequest("/tenant_id", "tenant_id must match "+accessSlugRE.String()))
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" || len(body.DisplayName) > 128 {
		s.fail(w, r, badRequest("/display_name", "display_name must be 1 to 128 characters"))
		return
	}
	limit, err := tenantLimit(body.RateLimit, registry.TenantLimit{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t := registry.Tenant{Tenant: id, DisplayName: body.DisplayName, APIKeys: []registry.APIKey{}, RateLimit: limit}
	if body.Description != nil {
		t.Description = *body.Description
	}
	if body.Contact != nil {
		t.Contact = *body.Contact
	}
	res, err := pub.Edit(r.Context(), func(tr control.Tree, snap *registry.Snapshot) (control.Change, error) {
		if _, ok := snap.Tenants[id]; ok {
			return control.Change{}, fmt.Errorf("%w: tenant %q exists", control.ErrConflict, id)
		}
		if _, ok := tr.Get(tenantPath(id)); ok {
			return control.Change{}, fmt.Errorf("%w: %s exists", control.ErrConflict, tenantPath(id))
		}
		doc, err := yaml.Marshal(t)
		if err != nil {
			return control.Change{}, err
		}
		tr.Put(tenantPath(id), append([]byte("# Created through the admin API (createTenant). API keys are stored only as sha256 hashes (DESIGN §16.5).\n"), doc...))
		return control.Change{Actor: principal(r).Actor(), Action: "createTenant", ObjectType: "tenant", ObjectID: id, Reason: reason,
			After: map[string]any{"display_name": t.DisplayName, "description": t.Description, "contact": t.Contact, "rate_limit": t.RateLimit}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeTenant(w, r, http.StatusCreated, res, id)
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request) {
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
		switch k {
		case "display_name", "description", "contact", "rate_limit", "disabled", "reason":
		default:
			s.fail(w, r, badRequest("/"+k, "unknown field "+k))
			return
		}
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("tenant_id")
	res, err := pub.Edit(r.Context(), func(tr control.Tree, snap *registry.Snapshot) (control.Change, error) {
		t, ok := snap.Tenants[id]
		if !ok {
			return control.Change{}, notFound("Tenant")
		}
		if err := accessIfMatch(r, accessETag(t)); err != nil {
			return control.Change{}, err
		}
		next := *t
		if v, ok := body["display_name"]; ok {
			name, isStr := v.(string)
			if !isStr || strings.TrimSpace(name) == "" || len(name) > 128 {
				return control.Change{}, badRequest("/display_name", "display_name must be 1 to 128 characters")
			}
			next.DisplayName = name
		}
		var err error
		if next.Description, err = tenantText(body, "description", t.Description); err != nil {
			return control.Change{}, err
		}
		if next.Contact, err = tenantText(body, "contact", t.Contact); err != nil {
			return control.Change{}, err
		}
		if v, ok := body["rate_limit"]; ok {
			m, isMap := v.(map[string]any)
			if !isMap {
				return control.Change{}, badRequest("/rate_limit", "rate_limit must be an object")
			}
			if next.RateLimit, err = tenantLimit(m, t.RateLimit); err != nil {
				return control.Change{}, err
			}
		}
		if v, ok := body["disabled"]; ok {
			b, isBool := v.(bool)
			if !isBool {
				return control.Change{}, badRequest("/disabled", "disabled must be a boolean")
			}
			next.Disabled = b
		}
		kv := map[string]any{"display_name": nil, "description": nil, "contact": nil, "disabled": nil, "rate_limit": next.RateLimit}
		if next.DisplayName != "" {
			kv["display_name"] = next.DisplayName
		}
		if next.Description != "" {
			kv["description"] = next.Description
		}
		if next.Contact != "" {
			kv["contact"] = next.Contact
		}
		if next.Disabled {
			kv["disabled"] = true
		}
		if err := accessSetKeys(tr, tenantPath(id), kv); err != nil {
			return control.Change{}, err
		}
		view := func(t registry.Tenant) map[string]any {
			return map[string]any{"display_name": t.DisplayName, "description": t.Description, "contact": t.Contact, "rate_limit": t.RateLimit, "disabled": t.Disabled}
		}
		return control.Change{Actor: principal(r).Actor(), Action: "updateTenant", ObjectType: "tenant", ObjectID: id, Reason: reason,
			Before: view(*t), After: view(next)}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	s.writeTenant(w, r, http.StatusOK, res, id)
}
