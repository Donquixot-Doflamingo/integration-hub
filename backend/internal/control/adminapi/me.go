package adminapi

import (
	"net/http"
	"time"
)

// permissions are the UI gates of /me (`<area>:<action>`) with the least role that holds each.
// The server never consults them: every operation checks its own x-required-role.
var permissions = []struct {
	name string
	min  Role
}{
	{"requests:read", RoleTenantViewer},
	{"registry:read", RoleViewer},
	{"change_log:read", RoleViewer},
	{"accounts:test", RoleEditor},
	{"transformers:write", RoleEditor},
	{"integrations:write", RoleEditor},
	{"integrations:publish", RoleEditor},
	{"integrations:pause", RoleEditor},
	{"dlq:replay", RoleEditor},
	{"vendors:write", RoleAdmin},
	{"accounts:write", RoleAdmin},
	{"credentials:rotate", RoleAdmin},
	{"tenants:write", RoleAdmin},
	{"tenants:setup", RoleTenantAdmin},
	{"tenants:sync", RoleAdmin},
	{"api_keys:write", RoleAdmin},
	{"registrar:write", RoleTenantAdmin},
	{"requests:unseal", RoleAdmin},
	{"dlq:purge", RoleAdmin},
	{"breakers:force", RoleAdmin},
}

func (s *Server) registerMe() {
	s.handle("getMe", RoleTenantViewer, false, "GET /me", func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		perms := []string{}
		for _, perm := range permissions {
			if p.Allows(perm.min) {
				perms = append(perms, perm.name)
			}
		}
		tenants := p.Tenants
		if !p.Scoped() {
			tenants = []string{}
		}
		var exp *string
		if !p.ExpiresAt.IsZero() {
			e := p.ExpiresAt.UTC().Format(time.RFC3339)
			exp = &e
		}
		writeJSON(w, http.StatusOK, map[string]any{"subject": p.Subject, "email": nullable(p.Email), "name": nullable(p.Name),
			"roles": p.RoleNames(), "tenants": tenants, "permissions": perms, "token_expires_at": exp})
	})
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
