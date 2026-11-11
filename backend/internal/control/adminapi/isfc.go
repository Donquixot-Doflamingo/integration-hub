package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
)

// Identity headers of the ISFC api-gateway (AUTH_PLAN §3.1). The gateway strips client-sent
// x-isfc-* headers; the Hub still verifies the bearer token and cross-checks the headers against it,
// because anything that reaches the admin listener directly could forge them (gap G5).
const (
	hdrISFCClientID = "X-Isfc-Client-Id"
	hdrISFCSubID    = "X-Isfc-Sub-Id"
	hdrISFCSubType  = "X-Isfc-Sub-Type"
	hdrISFCEmail    = "X-Isfc-Sub-Email-Id"
	hdrISFCTenantID = "X-Isfc-Tenant-Id"
	hdrISFCSubAttrs = "X-Isfc-Sub-Attrs"
	hdrISFCAPIKey   = "X-Isfc-Api-Key" //nolint:gosec // a header name, not a credential
)

// Auth modes (HUB_ADMIN_AUTH_MODE).
const (
	AuthModeJWT  = "jwt"
	AuthModeISFC = "isfc"
)

// Reasons of hub_admin_authn_failures_total.
const (
	authnNoToken        = "no_token"
	authnBadSignature   = "bad_signature"
	authnHeaderMismatch = "header_mismatch"
	authnNoRoles        = "no_roles"
)

// ISFC roles of the Hub console client (AUTH_PLAN §2.2). Platform roles count in the platform
// tenant only, tenant roles in a linked tenant only. ISFC_ADMIN maps to nothing (decision D6).
var (
	isfcPlatformRoles = map[string]Role{"HUB_ADMIN": RoleAdmin, "HUB_EDITOR": RoleEditor, "HUB_VIEWER": RoleViewer}
	isfcTenantRoles   = map[string]Role{"HUB_TENANT_ADMIN": RoleTenantAdmin, "HUB_TENANT_VIEWER": RoleTenantViewer}
)

// ISFCAuth turns a request that came through the ISFC api-gateway into a Principal
// (HUB_ADMIN_AUTH_MODE=isfc, AUTH_PLAN §7.1 steps 2 to 4). Wire it as Deps.ISFC.
type ISFCAuth struct {
	// Tokens verifies the Hydra access token (signature, iss, exp, nbf, aud).
	Tokens TokenVerifier
	// ConsoleClientID is the ISFC client id of the Hub console. The token's client_id and
	// x-isfc-client-id must both equal it.
	ConsoleClientID string
	// PlatformTenantID is the ISFC tenant UUID of the platform tenant (HUB_ISFC_PLATFORM_TENANT_ID):
	// a request in it has platform scope. Empty: no platform scope exists.
	PlatformTenantID string
	// ResolveTenant maps an ISFC tenant UUID to the Hub tenant id (the synced copy). nil, or a
	// tenant it does not know, gives a principal with no roles.
	ResolveTenant func(ctx context.Context, isfcTenantID string) (hubID string, ok bool)
}

// authnError is a refused authentication: the HTTP error and the metric reason.
type authnError struct {
	err    *Error
	reason string
}

func unauthorized(reason, msg string) *authnError {
	return &authnError{reason: reason, err: &Error{Status: http.StatusUnauthorized, Code: CodeUnauthorized, Message: msg}}
}

// machineRefused are the operations no machine principal may call, whatever its key holds (human
// only, decision D7).
var machineRefused = map[string]bool{"unsealRequest": true, "purgeDeadLetters": true}

// Permission bundles of the Hub roles (AUTH_PLAN §7.4). A machine principal's Roles are the roles
// whose bundle its key fully covers; they describe the key in /me and the change log. What a key
// may do is decided by its permissions in OPA, not by these roles.
var (
	hubReadPermissions   = []string{"HUB_VENDOR:READ", "HUB_ACCOUNT:READ", "HUB_TRANSFORMER:READ", "HUB_INTEGRATION:READ", "HUB_TENANT:READ", "HUB_REQUEST:READ", "HUB_DLQ:READ", "HUB_BREAKER:READ", "HUB_PLATFORM:READ"}
	hubWritePermissions  = []string{"HUB_ACCOUNT:TEST", "HUB_TRANSFORMER:WRITE", "HUB_INTEGRATION:WRITE", "HUB_DLQ:REPLAY"}
	hubAdminPermissions  = []string{"HUB_VENDOR:ADMIN", "HUB_ACCOUNT:ADMIN", "HUB_TENANT:ADMIN", "HUB_TENANT:SETUP", "HUB_REQUEST:UNSEAL", "HUB_DLQ:PURGE", "HUB_BREAKER:ADMIN"}
	hubTenantReadBundle  = []string{"HUB_TENANT:READ", "HUB_INTEGRATION:READ", "HUB_REQUEST:READ", "HUB_DLQ:READ"}
	hubTenantSetupBundle = append(slices.Clone(hubTenantReadBundle), "HUB_TENANT:SETUP")
)

// rolesCovered returns the Hub roles whose permission bundle is a subset of perms.
func rolesCovered(perms []string) []Role {
	covers := func(bundle ...[]string) bool {
		return !slices.ContainsFunc(slices.Concat(bundle...), func(p string) bool { return !slices.Contains(perms, p) })
	}
	var roles []Role
	if covers(hubReadPermissions, hubWritePermissions, hubAdminPermissions) {
		roles = append(roles, RoleAdmin)
	}
	if covers(hubReadPermissions, hubWritePermissions) {
		roles = append(roles, RoleEditor)
	}
	if covers(hubReadPermissions) {
		roles = append(roles, RoleViewer)
	}
	if covers(hubTenantSetupBundle) {
		roles = append(roles, RoleTenantAdmin)
	}
	if covers(hubTenantReadBundle) {
		roles = append(roles, RoleTenantViewer)
	}
	return roles
}

// authenticate builds the principal of a request: a machine (x-isfc-api-key) or a human (a
// gateway-forwarded bearer token).
func (a *ISFCAuth) authenticate(r *http.Request) (Principal, *authnError) {
	if key := r.Header.Get(hdrISFCAPIKey); key != "" {
		return a.authenticateMachine(r, key)
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return Principal{}, unauthorized(authnNoToken, "Missing bearer token")
	}
	if a.Tokens == nil {
		return Principal{}, unauthorized(authnNoToken, "No identity provider is configured")
	}
	claims, err := a.Tokens.VerifyToken(r.Context(), tok)
	if err != nil {
		return Principal{}, unauthorized(authnBadSignature, "Invalid token: "+reasonOf401(err))
	}
	subID, clientID := r.Header.Get(hdrISFCSubID), r.Header.Get(hdrISFCClientID)
	switch {
	case !equalString(claims.Subject, subID):
		return Principal{}, unauthorized(authnHeaderMismatch, "Identity headers do not match the token")
	case clientID == "" || !equalString(claims.ClientID, clientID):
		return Principal{}, unauthorized(authnHeaderMismatch, "Identity headers do not match the token")
	case a.ConsoleClientID != "" && !equalString(clientID, a.ConsoleClientID):
		return Principal{}, unauthorized(authnHeaderMismatch, "Token was not issued to the Hub console")
	}
	tenantID := strings.TrimSpace(r.Header.Get(hdrISFCTenantID))
	attrs := decodeSubAttrs(r.Header.Get(hdrISFCSubAttrs))
	p := Principal{
		Subject: subID, Email: r.Header.Get(hdrISFCEmail), ExpiresAt: claims.ExpiresAt, Tenants: []string{},
		ISFCTenantID: tenantID, ClientID: clientID, SubType: r.Header.Get(hdrISFCSubType), SubAttrs: attrs,
	}
	if name, ok := attrs["name"].(string); ok {
		p.Name = name
	}
	p.Roles, p.Tenants = a.mapRoles(r.Context(), tenantID, stringList(attrs["roles"]))
	return p, nil
}

// authenticateMachine builds the principal of an automation client that calls the admin directly
// with x-isfc-client-id and x-isfc-api-key (AUTH_PLAN §7.5). The key itself is checked by OPA
// against the bundle's allowed_api_keys; the principal gets its roles from the key's permissions
// once OPA has answered (applyKeyPermissions). Machine principals have platform scope: ISFC keys
// carry no tenant (gap G1).
func (a *ISFCAuth) authenticateMachine(r *http.Request, key string) (Principal, *authnError) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return Principal{}, unauthorized(authnHeaderMismatch, "Send either a bearer token or an API key, not both")
	}
	clientID := strings.TrimSpace(r.Header.Get(hdrISFCClientID))
	if clientID == "" {
		return Principal{}, unauthorized(authnNoToken, "x-isfc-client-id is required with an API key")
	}
	_ = key // checked by OPA; never stored in the principal
	return Principal{Subject: "client:" + clientID, ClientID: clientID, SubType: "INTERNAL", Machine: true, Tenants: []string{}}, nil
}

// applyKeyPermissions sets a machine principal's roles from the key permissions OPA reports in
// the decision meta (`key_permissions`).
func applyKeyPermissions(p *Principal, meta map[string]any) {
	p.Roles = rolesCovered(stringList(meta["key_permissions"]))
}

// mapRoles applies the AUTH_PLAN §2.2 table. A request without a tenant, in an unknown tenant, or
// with roles that do not count in its tenant gets no roles (fail closed: the caller is 403).
func (a *ISFCAuth) mapRoles(ctx context.Context, tenantID string, isfcRoles []string) ([]Role, []string) {
	if tenantID == "" {
		return nil, []string{}
	}
	var roles []Role
	if a.PlatformTenantID != "" && equalString(tenantID, a.PlatformTenantID) {
		for _, name := range isfcRoles {
			if r, ok := isfcPlatformRoles[name]; ok {
				roles = append(roles, r)
			}
		}
		return roles, []string{}
	}
	for _, name := range isfcRoles {
		if r, ok := isfcTenantRoles[name]; ok {
			roles = append(roles, r)
		}
	}
	if len(roles) == 0 || a.ResolveTenant == nil {
		return nil, []string{}
	}
	hubID, ok := a.ResolveTenant(ctx, tenantID)
	if !ok || hubID == "" {
		return nil, []string{}
	}
	return roles, []string{hubID}
}

// reasonOf401 is the token problem for the 401 message, never the token itself.
func reasonOf401(err error) string {
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, ErrBadToken.Error()+": "); ok {
		return rest
	}
	return "could not verify"
}

func equalString(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// decodeSubAttrs reads x-isfc-sub-attrs (base64 JSON). The gateway uses standard base64; the other
// alphabets are tolerated. Anything malformed gives no attributes, so no roles (fail closed).
func decodeSubAttrs(h string) map[string]any {
	h = strings.TrimSpace(h)
	if h == "" {
		return map[string]any{}
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(h); err == nil {
			break
		}
	}
	attrs := map[string]any{}
	if err != nil || json.Unmarshal(raw, &attrs) != nil || attrs == nil {
		return map[string]any{}
	}
	return attrs
}

// stringList reads a JSON array of strings; any other shape (or element) yields nothing.
func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		s, ok := x.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// authnMetrics counts refused authentications (hub_admin_authn_failures_total{reason}).
type authnMetrics struct{ failures *prometheus.CounterVec }

func newAuthnMetrics(reg prometheus.Registerer) *authnMetrics {
	if reg == nil {
		return nil
	}
	m := &authnMetrics{failures: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hub_admin_authn_failures_total",
		Help: "Admin API requests refused at authentication in ISFC mode, by reason (no_token, bad_signature, header_mismatch, no_roles).",
	}, []string{"reason"})}
	for _, reason := range []string{authnNoToken, authnBadSignature, authnHeaderMismatch, authnNoRoles} {
		m.failures.WithLabelValues(reason)
	}
	reg.MustRegister(m.failures)
	return m
}

func (m *authnMetrics) fail(reason string) {
	if m != nil {
		m.failures.WithLabelValues(reason).Inc()
	}
}
