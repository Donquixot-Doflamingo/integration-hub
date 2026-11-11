package adminapi

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// API keys (HUB-206, DESIGN §16.5): tenant keys stored only as sha256 hashes in the tenant's
// YAML. The plaintext exists only in the create and rotate responses. Rotation keeps the old
// key working (state expiring) until its overlap ends; the edge then refuses it by its
// expires_at, without another snapshot.

func init() { registrars = append(registrars, (*Server).registerAPIKeys) }

func (s *Server) registerAPIKeys() {
	s.handle("listApiKeys", RoleTenantViewer, false, "GET /api-keys", s.listAPIKeys)
	s.handle("createApiKey", RoleAdmin, true, "POST /api-keys", s.createAPIKey)
	s.handle("getApiKey", RoleTenantViewer, false, "GET /api-keys/{key_id}", s.getAPIKey)
	s.handle("revokeApiKey", RoleAdmin, true, "POST /api-keys/{key_id}/revoke", s.revokeAPIKey)
	s.handle("rotateApiKey", RoleAdmin, true, "POST /api-keys/{key_id}/rotate", s.rotateAPIKey)
}

var scopeRE = regexp.MustCompile(`^(invoke|dry_run|requests_read)(:[A-Za-z0-9][A-Za-z0-9_-]{2,63})?$`)

// maxOverlap is the contract's ApiKeyRotate.overlap_ms maximum (30 days).
const maxOverlap = 2_592_000_000 * time.Millisecond

const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// randomToken returns n characters of [A-Za-z0-9] from crypto/rand (rejection sampling, no bias).
func randomToken(n int) string {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, b := range buf {
			if int(b) < 248 && len(out) < n { // 248 = 4 × 62
				out = append(out, keyAlphabet[int(b)%len(keyAlphabet)])
			}
		}
	}
	return string(out)
}

// uniqueScopes drops repeated scopes anywhere in the list and keeps the caller's order
// (slices.Compact only removed neighbours).
func uniqueScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// mintKey returns a new key record and its plaintext (hk_live_ + 32 random characters).
func mintKey(now time.Time, scopes []string, description string, expiresAt *time.Time) (registry.APIKey, string) {
	secret := "hk_live_" + randomToken(32)
	created := now.UTC().Truncate(time.Second)
	return registry.APIKey{KeyID: "key_" + core.NewULID(now), Hash: registry.HashKey(secret), Scopes: scopes, State: registry.KeyActive,
		Prefix: secret[:12], Description: description, CreatedAt: &created, ExpiresAt: expiresAt}, secret
}

// keyState is the contract ApiKeyState at now: a key past its expiry is expired, or revoked
// when a rotation replaced it (the overlap ended).
func keyState(k registry.APIKey, now time.Time) string {
	switch {
	case k.State == registry.KeyRevoked:
		return "revoked"
	case k.ExpiresAt != nil && !now.Before(*k.ExpiresAt):
		if k.ReplacedBy != "" {
			return "revoked"
		}
		return "expired"
	case k.State == "":
		return "active"
	}
	return k.State
}

func (s *Server) keyView(snap *registry.Snapshot, tenant string, k registry.APIKey) map[string]any {
	created := snap.BuiltAt.UTC()
	if k.CreatedAt != nil {
		created = k.CreatedAt.UTC()
	}
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return map[string]any{"key_id": k.KeyID, "tenant_id": tenant, "prefix": k.Prefix, "description": nullable(k.Description),
		"scopes": scopes, "state": keyState(k, s.d.Now()), "created_at": created, "expires_at": k.ExpiresAt,
		"last_used_at": nil, "replaced_by": nullable(k.ReplacedBy)}
}

// keyAudit is what the change log records of a key: metadata, never the hash or the secret.
func keyAudit(k registry.APIKey) map[string]any {
	return map[string]any{"key_id": k.KeyID, "prefix": k.Prefix, "scopes": k.Scopes, "state": k.State, "expires_at": k.ExpiresAt, "replaced_by": k.ReplacedBy}
}

// findKey returns the tenant holding key id and the key's index.
func findKey(snap *registry.Snapshot, id string) (*registry.Tenant, int) {
	for _, t := range snap.Tenants {
		if i := slices.IndexFunc(t.APIKeys, func(k registry.APIKey) bool { return k.KeyID == id }); i >= 0 {
			return t, i
		}
	}
	return nil, -1
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := principal(r)
	tenant, state := r.URL.Query().Get("tenant_id"), r.URL.Query().Get("state")
	if tenant != "" && !p.SeesTenant(tenant) {
		s.fail(w, r, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "tenant_id is outside the caller's tenants"})
		return
	}
	owner := map[string]*registry.Tenant{}
	keys := map[string]registry.APIKey{}
	var ids []string
	for _, t := range snap.Tenants {
		if (tenant != "" && t.Tenant != tenant) || !p.SeesTenant(t.Tenant) {
			continue
		}
		for _, k := range t.APIKeys {
			if state == "" || keyState(k, s.d.Now()) == state {
				ids, owner[k.KeyID], keys[k.KeyID] = append(ids, k.KeyID), t, k
			}
		}
	}
	items, next, err := accessPage(r, ids, func(id string) map[string]any { return s.keyView(snap, owner[id].Tenant, keys[id]) })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getAPIKey(w http.ResponseWriter, r *http.Request) {
	snap, err := s.accessSnapshot(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	t, i := findKey(snap, r.PathValue("key_id"))
	if t == nil || !principal(r).SeesTenant(t.Tenant) {
		s.fail(w, r, notFound("API key"))
		return
	}
	writeJSON(w, http.StatusOK, s.keyView(snap, t.Tenant, t.APIKeys[i]))
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TenantID    string     `json:"tenant_id"`
		Description *string    `json:"description"`
		Scopes      []string   `json:"scopes"`
		ExpiresAt   *time.Time `json:"expires_at"`
		Reason      string     `json:"reason"`
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
	if len(body.Scopes) == 0 {
		s.fail(w, r, badRequest("/scopes", "a key needs at least one scope"))
		return
	}
	for i, sc := range body.Scopes {
		if !scopeRE.MatchString(sc) {
			s.fail(w, r, badRequest(fmt.Sprintf("/scopes/%d", i), "scopes are invoke, dry_run or requests_read, optionally :<integration_id>"))
			return
		}
	}
	if body.ExpiresAt != nil && !body.ExpiresAt.After(s.d.Now()) {
		s.fail(w, r, badRequest("/expires_at", "expires_at must be in the future"))
		return
	}
	pub, err := s.accessPublisher()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	desc := ""
	if body.Description != nil {
		desc = *body.Description
	}
	key, secret := mintKey(s.d.Now(), uniqueScopes(body.Scopes), desc, body.ExpiresAt)
	res, err := pub.Edit(r.Context(), func(tr control.Tree, snap *registry.Snapshot) (control.Change, error) {
		t, ok := snap.Tenants[body.TenantID]
		if !ok {
			return control.Change{}, notFound("Tenant")
		}
		for i, sc := range key.Scopes {
			if _, in, ok := strings.Cut(sc, ":"); ok {
				if _, known := snap.Integrations[in]; !known {
					return control.Change{}, badRequest(fmt.Sprintf("/scopes/%d", i), fmt.Sprintf("unknown integration %q", in))
				}
			}
		}
		if err := accessSetKeys(tr, tenantPath(t.Tenant), map[string]any{"api_keys": append(slices.Clone(t.APIKeys), key)}); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: principal(r).Actor(), Action: "createApiKey", ObjectType: "api_key", ObjectID: key.KeyID, Reason: reason,
			After: map[string]any{"tenant_id": t.Tenant, "key": keyAudit(key)}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "api_key": s.keyView(res.Snapshot, body.TenantID, key), "secret": secret})
}

// editKey runs fn on the key's record under the publisher lock and writes the tenant file back.
// fn returns the keys to store (it may append) and the change.
func (s *Server) editKey(r *http.Request, fn func(t *registry.Tenant, keys []registry.APIKey, i int) ([]registry.APIKey, control.Change, error)) (control.Result, string, error) {
	pub, err := s.accessPublisher()
	if err != nil {
		return control.Result{}, "", err
	}
	var tenant string
	res, err := pub.Edit(r.Context(), func(tr control.Tree, snap *registry.Snapshot) (control.Change, error) {
		t, i := findKey(snap, r.PathValue("key_id"))
		if t == nil {
			return control.Change{}, notFound("API key")
		}
		tenant = t.Tenant
		keys, ch, err := fn(t, slices.Clone(t.APIKeys), i)
		if err != nil {
			return control.Change{}, err
		}
		return ch, accessSetKeys(tr, tenantPath(t.Tenant), map[string]any{"api_keys": keys})
	})
	return res, tenant, err
}

func reasonBody(w http.ResponseWriter, r *http.Request) (string, error) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		return "", err
	}
	return reasonOf(r, body.Reason)
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	reason, err := reasonBody(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.d.Now().UTC().Truncate(time.Second)
	var revoked registry.APIKey
	res, tenant, err := s.editKey(r, func(_ *registry.Tenant, keys []registry.APIKey, i int) ([]registry.APIKey, control.Change, error) {
		before := keys[i]
		if keyState(before, now) == "revoked" {
			return nil, control.Change{}, fmt.Errorf("%w: key %s is already revoked", control.ErrConflict, before.KeyID)
		}
		keys[i].State = registry.KeyRevoked
		if keys[i].ExpiresAt == nil || keys[i].ExpiresAt.After(now) {
			keys[i].ExpiresAt = &now
		}
		revoked = keys[i]
		return keys, control.Change{Actor: principal(r).Actor(), Action: "revokeApiKey", ObjectType: "api_key", ObjectID: before.KeyID, Reason: reason,
			Before: keyAudit(before), After: keyAudit(keys[i])}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "api_key": s.keyView(res.Snapshot, tenant, revoked)})
}

func (s *Server) rotateAPIKey(w http.ResponseWriter, r *http.Request) {
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
	now := s.d.Now()
	end := now.Add(time.Duration(*body.OverlapMS) * time.Millisecond).UTC()
	var old, minted registry.APIKey
	var secret string
	res, tenant, err := s.editKey(r, func(_ *registry.Tenant, keys []registry.APIKey, i int) ([]registry.APIKey, control.Change, error) {
		before := keys[i]
		if st := keyState(before, now); st != "active" {
			return nil, control.Change{}, fmt.Errorf("%w: key %s is %s; only an active key can be rotated", control.ErrConflict, before.KeyID, st)
		}
		minted, secret = mintKey(now, slices.Clone(before.Scopes), before.Description, nil)
		keys[i].State, keys[i].ReplacedBy = registry.KeyExpiring, minted.KeyID
		if keys[i].ExpiresAt == nil || keys[i].ExpiresAt.After(end) {
			keys[i].ExpiresAt = &end
		}
		old = keys[i]
		return append(keys, minted), control.Change{Actor: principal(r).Actor(), Action: "rotateApiKey", ObjectType: "api_key", ObjectID: before.KeyID,
			Reason: reason, Before: keyAudit(before), After: map[string]any{"old": keyAudit(old), "new": keyAudit(minted), "overlap_ms": *body.OverlapMS}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": res.ChangeID, "old_key": s.keyView(res.Snapshot, tenant, old),
		"api_key": s.keyView(res.Snapshot, tenant, minted), "secret": secret})
}
