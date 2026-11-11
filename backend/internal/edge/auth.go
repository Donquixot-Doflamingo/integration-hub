package edge

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
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

// Credential kinds (hub_edge_auth_total{method}).
const (
	MethodHubKey = "hub_key"
	MethodISFC   = "isfc"
)

// Caller is the authenticated tenant and key of a request.
type Caller struct {
	Tenant *registry.Tenant
	Key    registry.APIKey
	Snap   *registry.Snapshot // the snapshot the whole request uses (DESIGN §6.3)
	// Method is how the caller authenticated: MethodHubKey or MethodISFC. For an ISFC credential
	// ClientID is the ISFC caller client and Key is the ISFC key (KeyID = the ISFC key id, Hash =
	// its hash, Scopes from its permissions); both are empty for a Hub key.
	Method   string
	ClientID string
}

type callerKey struct{}

// CallerFrom returns the authenticated caller.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// HashKey returns the stored form of an API key: "sha256:<hex>" (DESIGN §16.5).
func HashKey(key string) string { return registry.HashKey(key) }

func bearer(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// Headers of an ISFC S2S credential (contract isfcS2S) and the tenant selector.
const (
	headerISFCKey      = "X-Isfc-Api-Key"
	headerISFCClientID = "X-Isfc-Client-Id"
	headerTenantID     = "X-Tenant-Id"
)

// CallerKeys is the in-memory map of ISFC caller keys (*isfc.KeyMap). Lookup must not block or
// touch a database: it runs on the request path (DESIGN §18.5).
type CallerKeys interface {
	Lookup(hash string) (*isfc.CallerEntry, bool)
	Loaded() bool
}

// Auth results (hub_edge_auth_total{result}).
const (
	resOK = iota
	resUnauthorized
	resForbidden
	resBadRequest
	resNotReady
	nResults
)

var resultNames = [nResults]string{"ok", "unauthorized", "forbidden", "bad_request", "not_ready"}

// authCounters is hub_edge_auth_total, one prepared counter per (method, result) so counting
// allocates nothing.
type authCounters [2][nResults]prometheus.Counter

func newAuthCounters(reg prometheus.Registerer) *authCounters {
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "hub_edge_auth_total",
		Help: "Edge authentications by credential kind (hub_key, isfc) and result: tracks the retirement of legacy Hub keys (AUTH_PLAN §11)."}, []string{"method", "result"})
	if err := reg.Register(vec); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
		vec = are.ExistingCollector.(*prometheus.CounterVec)
	}
	var c authCounters
	for mi, m := range []string{MethodHubKey, MethodISFC} {
		for ri, r := range resultNames {
			c[mi][ri] = vec.WithLabelValues(m, r)
		}
	}
	return &c
}

func (c *authCounters) inc(method, result int) {
	if c != nil {
		c[method][result].Inc()
	}
}

const (
	mHubKey = iota
	mISFC
)

// authenticate checks the caller's credential against in-memory data only; no database access
// (DESIGN §5.1 step 1). Two credential kinds:
//
//   - a Hub tenant key (Authorization: Bearer): hashed, looked up in the snapshot's key set, hashes
//     compared in constant time. Keys of a disabled tenant are refused like unknown keys (401), and
//     so are revoked keys and keys past their expiry (a rotated key stops when its overlap ends);
//   - with HUB_EDGE_ISFC_AUTH=on, an ISFC backend-client key (x-isfc-client-id + x-isfc-api-key):
//     see authenticateISFC (AUTH_PLAN §8.2). Sending both kinds is a 400.
//
// It records StageAuth.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return s.metrics.TimedMiddleware(telemetry.StageAuth, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tagRoute(r)
			snap := s.deps.Snapshots.Snapshot()
			if snap == nil {
				WriteError(w, r, &core.CanonicalError{Code: core.CodeNotReady, Message: "Node is not ready", Retryable: true, RetryAfterMS: core.Ptr[int64](1000)})
				return
			}
			method, apiKey := mHubKey, ""
			if s.deps.ISFCKeys != nil {
				if apiKey = r.Header.Get(headerISFCKey); apiKey != "" {
					method = mISFC
					if _, legacy := bearer(r); legacy {
						s.counters.inc(mISFC, resBadRequest)
						WriteError(w, r, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "Send one credential: either a Hub API key (Authorization) or an ISFC key (x-isfc-api-key), not both"})
						return
					}
				}
			}
			var c Caller
			var ce *core.CanonicalError
			var res int
			if method == mISFC {
				c, res, ce = s.authenticateISFC(r, snap, apiKey)
			} else {
				c, res, ce = s.authenticateHubKey(r, snap)
			}
			s.counters.inc(method, res)
			if ce != nil {
				WriteError(w, r, ce)
				return
			}
			ctx := context.WithValue(r.Context(), callerKey{}, c)
			f, _ := telemetry.RequestFieldsFrom(ctx)
			f.Tenant = c.Tenant.Tenant
			ctx = telemetry.WithRequestFields(ctx, f)
			tagTenant(ctx, f.Tenant)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})(next)
}

var errUnauthorized = &core.CanonicalError{Code: core.CodeUnauthorized, Message: "Missing or invalid API key"}

func (s *Server) authenticateHubKey(r *http.Request, snap *registry.Snapshot) (Caller, int, *core.CanonicalError) {
	unauthorized := *errUnauthorized
	key, ok := bearer(r)
	if !ok {
		return Caller{}, resUnauthorized, &unauthorized
	}
	hash := HashKey(key)
	entry, found := snap.KeyByHash(hash)
	if !found || subtle.ConstantTimeCompare([]byte(entry.Key.Hash), []byte(hash)) != 1 || !entry.Key.Usable(s.deps.Now()) || entry.Tenant.Disabled {
		return Caller{}, resUnauthorized, &unauthorized
	}
	if s.deps.TenantSourceISFC && entry.Tenant.SetupState() == registry.SetupPending {
		return Caller{}, resUnauthorized, &unauthorized // setup not finished: calls are refused (HUB-480)
	}
	if entry.Tenant.Status == "suspended" {
		return Caller{}, resForbidden, &core.CanonicalError{Code: core.CodeForbidden, Message: "Tenant is suspended", Reason: core.Ptr("tenant_suspended")}
	}
	return Caller{Tenant: entry.Tenant, Key: entry.Key, Snap: snap, Method: MethodHubKey}, resOK, nil
}

// authenticateISFC is AUTH_PLAN §8.2 for an ISFC credential, all from memory: the hash of
// x-isfc-api-key in the caller-key map (constant-time compare), the key must be ACTIVE (only
// active keys are in the map) and issued to the client in x-isfc-client-id; the tenant is the one
// ISFC tenant the key covers, or the one named by X-Tenant-Id when it covers several; a pending,
// disabled or ISFC-INACTIVE tenant is 401, a suspended one 403. Scopes come from the key's ISFC
// permissions.
func (s *Server) authenticateISFC(r *http.Request, snap *registry.Snapshot, apiKey string) (Caller, int, *core.CanonicalError) {
	keys := s.deps.ISFCKeys
	unauthorized := *errUnauthorized
	if !keys.Loaded() { // this node has not read the key table yet: say so instead of guessing
		return Caller{}, resNotReady, &core.CanonicalError{Code: core.CodeNotReady, Message: "Node is not ready", Retryable: true, RetryAfterMS: core.Ptr[int64](1000)}
	}
	clientID := r.Header.Get(headerISFCClientID)
	hash := HashKey(apiKey)
	e, found := keys.Lookup(hash)
	if clientID == "" || !found || subtle.ConstantTimeCompare([]byte(e.Hash), []byte(hash)) != 1 || subtle.ConstantTimeCompare([]byte(e.ClientID), []byte(clientID)) != 1 {
		return Caller{}, resUnauthorized, &unauthorized
	}
	var tenant *registry.Tenant
	if want := r.Header.Get(headerTenantID); want != "" {
		want = strings.ToLower(strings.TrimSpace(want))
		if !isUUID(want) {
			return Caller{}, resBadRequest, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "X-Tenant-Id must be an ISFC tenant UUID"}
		}
		t, known := snap.TenantByISFC(want)
		if !slices.Contains(e.TenantIDs, want) || !known {
			return Caller{}, resForbidden, &core.CanonicalError{Code: core.CodeForbidden, Message: "The credential does not cover this tenant", Reason: core.Ptr("tenant_not_covered")}
		}
		tenant = t
	} else {
		n := 0
		for _, id := range e.TenantIDs {
			if t, known := snap.TenantByISFC(id); known {
				tenant, n = t, n+1
			}
		}
		switch {
		case n == 0:
			return Caller{}, resForbidden, &core.CanonicalError{Code: core.CodeForbidden, Message: "The credential covers no tenant of this Hub", Reason: core.Ptr("tenant_not_covered")}
		case n > 1:
			return Caller{}, resBadRequest, &core.CanonicalError{Code: core.CodeValidationFailed, Message: "The credential covers several tenants: send X-Tenant-Id"}
		}
	}
	if tenant.SetupState() == registry.SetupPending || tenant.Disabled || tenant.ISFC == nil || tenant.ISFC.Status != registry.ISFCStatusActive || !tenant.ISFC.Linked {
		return Caller{}, resUnauthorized, &unauthorized
	}
	if tenant.Status == "suspended" {
		return Caller{}, resForbidden, &core.CanonicalError{Code: core.CodeForbidden, Message: "Tenant is suspended", Reason: core.Ptr("tenant_suspended")}
	}
	return Caller{Tenant: tenant, Key: e.Key, Snap: snap, Method: MethodISFC, ClientID: e.ClientID}, resOK, nil
}

// isUUID reports whether s has the shape of a lower-case UUID (8-4-4-4-12 hex).
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		switch c := s[i]; {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// hasScope reports whether the key carries scope, globally or for the integration
// ("<scope>:<integration_id>").
func hasScope(k registry.APIKey, scope, integrationID string) bool {
	return slices.Contains(k.Scopes, scope) || (integrationID != "" && slices.Contains(k.Scopes, scope+":"+integrationID))
}

// hasAnyScope reports whether the key carries scope for at least one integration or globally.
func hasAnyScope(k registry.APIKey, scope string) bool {
	return slices.ContainsFunc(k.Scopes, func(s string) bool { return s == scope || strings.HasPrefix(s, scope+":") })
}

func missingScope(scope string) *core.CanonicalError {
	return &core.CanonicalError{Code: core.CodeForbidden, Message: "The API key lacks scope " + scope, Reason: core.Ptr("missing_scope")}
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
		return nil, missingScope(scope)
	}
	return in, nil
}
