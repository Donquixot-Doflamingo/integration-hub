package edge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/resolve"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Key scopes (contracts: ApiKeyScope).
const (
	ScopeInvoke       = "invoke"
	ScopeDryRun       = "dry_run"
	ScopeRequestsRead = "requests_read"
)

// Caller is the authenticated tenant and key of a request.
type Caller struct {
	Tenant *registry.Tenant
	Key    registry.APIKey
	Snap   *registry.Snapshot // the snapshot the whole request uses (DESIGN §6.3)
}

type callerKey struct{}

// CallerFrom returns the authenticated caller.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// HashKey returns the stored form of an API key: "sha256:<hex>" (DESIGN §16.5).
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// authenticate checks the tenant key against the in-memory hashed key set of the current
// snapshot; no database access (DESIGN §5.1 step 1). The key is hashed and looked up, then the
// stored and computed hashes are compared in constant time. Keys of a disabled tenant are
// refused like unknown keys (401, hub-admin updateTenant). It records StageAuth.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return s.metrics.TimedMiddleware(telemetry.StageAuth, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			snap := s.deps.Snapshots.Snapshot()
			if snap == nil {
				WriteError(w, r, &core.CanonicalError{Code: core.CodeNotReady, Message: "Node is not ready", Retryable: true, RetryAfterMS: core.Ptr[int64](1000)})
				return
			}
			unauthorized := &core.CanonicalError{Code: core.CodeUnauthorized, Message: "Missing or invalid API key"}
			key, ok := bearer(r)
			if !ok {
				WriteError(w, r, unauthorized)
				return
			}
			hash := HashKey(key)
			entry, found := snap.KeyByHash(hash)
			if !found || subtle.ConstantTimeCompare([]byte(entry.Key.Hash), []byte(hash)) != 1 || entry.Key.State == "revoked" || entry.Tenant.Disabled {
				WriteError(w, r, unauthorized)
				return
			}
			if entry.Tenant.Status == "suspended" {
				WriteError(w, r, &core.CanonicalError{Code: core.CodeForbidden, Message: "Tenant is suspended", Reason: core.Ptr("tenant_suspended")})
				return
			}
			ctx := context.WithValue(r.Context(), callerKey{}, Caller{Tenant: entry.Tenant, Key: entry.Key, Snap: snap})
			f, _ := telemetry.RequestFieldsFrom(ctx)
			f.Tenant = entry.Tenant.Tenant
			ctx = telemetry.WithRequestFields(ctx, f)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})(next)
}

// hasScope reports whether the key carries scope, globally or for the integration
// ("<scope>:<integration_id>").
func hasScope(k registry.APIKey, scope, integrationID string) bool {
	return slices.Contains(k.Scopes, scope) || (integrationID != "" && slices.Contains(k.Scopes, scope+":"+integrationID))
}

// authorizeIntegration resolves the integration's visibility for the caller: unknown → 404, not
// owner or grantee → 403 not_granted, missing scope → 403 missing_scope ("" = any key may).
func authorizeIntegration(c Caller, integrationID, scope string) (*registry.Integration, *core.CanonicalError) {
	in, ok := c.Snap.Integrations[integrationID]
	if !ok {
		return nil, &core.CanonicalError{Code: core.CodeIntegrationNotFound, Message: "Unknown integration"}
	}
	if !resolve.Granted(in, c.Tenant.Tenant) {
		return nil, &core.CanonicalError{Code: core.CodeForbidden, Message: "The tenant may not use this integration", Reason: core.Ptr("not_granted")}
	}
	if scope != "" && !hasScope(c.Key, scope, integrationID) {
		return nil, &core.CanonicalError{Code: core.CodeForbidden, Message: "The API key lacks scope " + scope, Reason: core.Ptr("missing_scope")}
	}
	return in, nil
}
