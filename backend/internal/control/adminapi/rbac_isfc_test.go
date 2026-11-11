package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/authz"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// The permission catalogue of AUTH_PLAN §7.4 as roles bundle it. It is deliberately written out
// here (not derived from the contract) so a contract that asks for a permission no role holds, or
// a role matrix that drifts from the permissions, fails TestRBACMatrix.
var (
	hubReadPerms = []string{"HUB_VENDOR:READ", "HUB_ACCOUNT:READ", "HUB_TRANSFORMER:READ", "HUB_INTEGRATION:READ", "HUB_TENANT:READ",
		"HUB_REQUEST:READ", "HUB_DLQ:READ", "HUB_BREAKER:READ", "HUB_PLATFORM:READ"}
	hubWritePerms  = []string{"HUB_ACCOUNT:TEST", "HUB_TRANSFORMER:WRITE", "HUB_INTEGRATION:WRITE", "HUB_DLQ:REPLAY"}
	hubAdminPerms  = []string{"HUB_VENDOR:ADMIN", "HUB_ACCOUNT:ADMIN", "HUB_TENANT:ADMIN", "HUB_TENANT:SETUP", "HUB_REQUEST:UNSEAL", "HUB_DLQ:PURGE", "HUB_BREAKER:ADMIN"}
	hubTenantRead  = []string{"HUB_TENANT:READ", "HUB_INTEGRATION:READ", "HUB_REQUEST:READ", "HUB_DLQ:READ"}
	hubTenantSetup = []string{"HUB_TENANT:SETUP"}
)

// isfcRole is a Hub role as ISFC models it: the role name, its permissions, and the tenant it
// is held in.
type isfcRole struct {
	name   string
	perms  []string
	tenant string // ISFC tenant UUID
}

func isfcRoleOf(role Role) isfcRole {
	cat := slices.Concat[[]string]
	switch role {
	case RoleAdmin:
		return isfcRole{"HUB_ADMIN", cat(hubReadPerms, hubWritePerms, hubAdminPerms), testPlatformUUID}
	case RoleEditor:
		return isfcRole{"HUB_EDITOR", cat(hubReadPerms, hubWritePerms), testPlatformUUID}
	case RoleViewer:
		return isfcRole{"HUB_VIEWER", cat(hubReadPerms), testPlatformUUID}
	case RoleTenantAdmin:
		return isfcRole{"HUB_TENANT_ADMIN", cat(hubTenantRead, hubTenantSetup), testTenantHL}
	case RoleTenantViewer:
	}
	return isfcRole{"HUB_TENANT_VIEWER", cat(hubTenantRead), testTenantHL}
}

// catalogOPA is a fake OPA that evaluates the permission catalogue the way the generated Rego
// does (AUTH_PLAN §7.3, §7.4): the operation is found by method and path, its
// x-required-permission must be held, platform-only operations need platform scope and
// tenant-capable ones (x-required-role tenant_viewer or tenant_admin) accept tenant scope too.
type catalogOPA struct {
	rules []catalogRule
	down  atomic.Bool
	calls atomic.Int64
}

type catalogRule struct {
	op, method, perm string
	re               *regexp.Regexp
	tenantCapable    bool
}

var platformRoleNames = []string{"HUB_ADMIN", "HUB_EDITOR", "HUB_VIEWER"}
var tenantRoleNames = []string{"HUB_TENANT_ADMIN", "HUB_TENANT_VIEWER"}

func newCatalogOPA(spec *contract.Spec) *catalogOPA {
	f := &catalogOPA{}
	for _, op := range spec.Operations() {
		f.rules = append(f.rules, catalogRule{op: op.ID, method: op.Method, perm: op.Extensions["x-required-permission"],
			re: regexp.MustCompile(buildPathRegexp(op.Path)), tenantCapable: slices.Contains([]string{"tenant_viewer", "tenant_admin"}, op.Extensions["x-required-role"])})
	}
	return f
}

// buildPathRegexp turns /vendors/{vendor_id} into ^/admin/v1/vendors/[^/]+$.
func buildPathRegexp(tmpl string) string {
	parts := strings.Split(tmpl, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") {
			parts[i] = "[^/]+"
		} else {
			parts[i] = regexp.QuoteMeta(p)
		}
	}
	return "^" + regexp.QuoteMeta(Base) + strings.Join(parts, "/") + "$"
}

func subList(in authz.Input, key string) []string {
	arr, _ := in.Sub[key].([]any)
	var out []string
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (f *catalogOPA) Decide(_ context.Context, in authz.Input) (authz.Decision, error) {
	f.calls.Add(1)
	if f.down.Load() {
		return authz.Decision{}, fmt.Errorf("%w: down", authz.ErrUnavailable)
	}
	hasTenant := in.Tenant != nil && in.Tenant.ID != ""
	roles, perms := subList(in, "roles"), subList(in, "permissions")
	anyOf := func(names []string) bool {
		return slices.ContainsFunc(roles, func(r string) bool { return slices.Contains(names, r) })
	}
	platform, tenant := hasTenant && anyOf(platformRoleNames), hasTenant && anyOf(tenantRoleNames)
	for _, r := range f.rules {
		if r.method != in.HTTP.Method || !r.re.MatchString(in.HTTP.Path) {
			continue
		}
		switch {
		case r.perm == "none":
			return authz.Decision{Allow: platform || tenant}, nil
		case !slices.Contains(perms, r.perm):
			return authz.Decision{}, nil
		}
		return authz.Decision{Allow: platform || (tenant && r.tenantCapable)}, nil
	}
	return authz.Decision{}, nil
}

// isfcMatrixMode is the ISFC-mode harness: RS256 tokens signed like Hydra's, gateway headers
// generated from the role catalogue, and the catalogue OPA.
func isfcMatrixMode(t *testing.T, spec *contract.Spec) matrixMode {
	m, _ := isfcMatrixModeWith(t, newCatalogOPA(spec))
	return m
}

func isfcMatrixModeWith(t *testing.T, opa authz.Decider) (matrixMode, *Server) {
	t.Helper()
	iss := newRSIssuer(t, testHydra, "rs-1", 2048)
	foreignKey := newRSIssuer(t, testHydra, "rs-1", 2048)
	s := New(Deps{ISFC: testISFCAuth(t, iss), Authz: opa})

	type cred struct {
		token string
		hdr   gatewayHeaders
	}
	creds := map[Role]cred{}
	for _, role := range allRoles {
		r := isfcRoleOf(role)
		sub := "sub-" + string(role)
		b, _ := json.Marshal(map[string]any{"roles": []string{r.name}, "permissions": r.perms})
		creds[role] = cred{
			token: iss.sign(t, map[string]any{"sub": sub, "client_id": testConsoleID}),
			hdr:   gatewayHeaders{sub: sub, client: testConsoleID, email: string(role) + "@example.com", tenant: r.tenant, rawAttrs: base64.StdEncoding.EncodeToString(b)},
		}
	}
	admin := creds[RoleAdmin]
	forge := func(token string, hdr gatewayHeaders) func(*testing.T, string, string) *http.Request {
		return func(t *testing.T, method, path string) *http.Request {
			req := newReq(t, method, path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			hdr.apply(req.Header)
			return req
		}
	}
	expired := iss.sign(t, map[string]any{"sub": admin.hdr.sub, "client_id": testConsoleID, "exp": time.Now().Add(-time.Hour).Unix()})
	foreign := foreignKey.sign(t, map[string]any{"sub": admin.hdr.sub, "client_id": testConsoleID})
	wrongSub := admin.hdr
	wrongSub.sub = "sub-viewer" // another user's identity headers on the admin's token
	wrongClient := admin.hdr
	wrongClient.client = "some-other-client"
	return matrixMode{name: "isfc", server: s,
		as: func(t *testing.T, role Role, method, path string) *http.Request {
			c := creds[role]
			return forge(c.token, c.hdr)(t, method, path)
		},
		bad: map[string]func(*testing.T, string, string) *http.Request{
			"no token":                      forge("", admin.hdr),
			"headers without a valid token": forge("a.b.c", admin.hdr),
			"expired":                       forge(expired, admin.hdr),
			"foreign key":                   forge(foreign, admin.hdr),
			"sub header of someone else":    forge(admin.token, wrongSub),
			"client header of someone else": forge(admin.token, wrongClient),
			"token without gateway headers": forge(admin.token, gatewayHeaders{}),
		}}, s
}

// TestISFCModeFailsClosed: when OPA cannot decide, every route answers 503 authz_unavailable and
// never runs the handler, whatever the role; a missing policy engine does the same.
func TestISFCModeFailsClosed(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	opa := newCatalogOPA(spec)
	opa.down.Store(true)
	mode, s := isfcMatrixModeWith(t, opa)
	noEngine, _ := isfcMatrixModeWith(t, nil)
	for name, m := range map[string]matrixMode{"OPA down": mode, "no engine": noEngine} {
		for _, rt := range s.Routes() {
			req := m.as(t, RoleAdmin, rt.Method, Base+samplePath(rt.Path))
			rec := httptest.NewRecorder()
			m.server.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeAuthzUnavailable) {
				t.Fatalf("%s: %s: %d %s", name, rt.Op, rec.Code, rec.Body)
			}
			checkResponse(t, spec, req, rec)
			if rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%s: %s: no Retry-After on 503", name, rt.Op)
			}
		}
	}
}

// TestISFCModeAuthorizationLayers: OPA and the in-process role check are independent layers. A
// permissive OPA does not lift the role check, and a role that holds the permission but sits in the
// wrong scope is refused by the policy.
func TestISFCModeAuthorizationLayers(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	allowAll, _ := isfcMatrixModeWith(t, authz.Disabled{})
	req := allowAll.as(t, RoleViewer, http.MethodPost, Base+"/vendors")
	rec := httptest.NewRecorder()
	allowAll.server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Requires role") {
		t.Fatalf("a viewer behind an allow-all OPA: %d %s", rec.Code, rec.Body)
	}
	// A tenant viewer holds HUB_TENANT:READ, which listTenants also asks for, but listTenants is
	// a platform operation: the policy refuses the tenant scope before the role check runs.
	m := isfcMatrixMode(t, spec)
	rec = httptest.NewRecorder()
	m.server.Handler().ServeHTTP(rec, m.as(t, RoleTenantViewer, http.MethodGet, Base+"/tenants"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "authorization policy") {
		t.Fatalf("tenant viewer on a platform route: %d %s", rec.Code, rec.Body)
	}
}

// TestISFCModeLogsAuthorization: the admin.authz log names subject, tenant, operation and decision
// and carries neither the token nor the attributes.
func TestISFCModeLogsAuthorization(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	var buf strings.Builder
	log := newTestLogger(&buf)
	iss := newRSIssuer(t, testHydra, "rs-1", 2048)
	s := New(Deps{ISFC: testISFCAuth(t, iss), Authz: newCatalogOPA(spec), Log: log})
	tok := iss.sign(t, map[string]any{"sub": "u-1", "client_id": testConsoleID})
	b, _ := json.Marshal(map[string]any{"roles": []string{"HUB_VIEWER"}, "permissions": hubReadPerms, "secret_attr": "do-not-log"})
	g := gatewayHeaders{sub: "u-1", client: testConsoleID, tenant: testPlatformUUID, rawAttrs: base64.StdEncoding.EncodeToString(b)}
	for _, path := range []string{"/snapshots", "/vendors"} { // allowed, then denied (createVendor needs admin)
		req := newReq(t, http.MethodGet, Base+path, nil)
		if path == "/vendors" {
			req.Method = http.MethodPost
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		g.apply(req.Header)
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	out := buf.String()
	for _, want := range []string{`msg=admin.authz`, `operation=listSnapshots`, `decision=allow`, `operation=createVendor`, `decision=deny`, `subject=u-1`, `tenant=` + testPlatformUUID} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, leak := range []string{tok, "do-not-log", "Bearer"} {
		if strings.Contains(out, leak) {
			t.Errorf("log leaks %q", leak)
		}
	}
}

// TestISFCMe: /me in ISFC mode keeps its shape (HUB-475, AUTH_PLAN §7.2): roles are the mapped
// Hub roles, tenants the Hub id of the current tenant for tenant scope (empty on the platform), and
// the actor of the change log is the email.
func TestISFCMe(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	m := isfcMatrixMode(t, spec)
	for _, role := range allRoles {
		req := m.as(t, role, http.MethodGet, Base+"/me")
		req.Body = nil
		rec := spec.Serve(t, m.server.Handler(), req)
		var me struct {
			Subject     string   `json:"subject"`
			Email       string   `json:"email"`
			Roles       []string `json:"roles"`
			Tenants     []string `json:"tenants"`
			Permissions []string `json:"permissions"`
			ExpiresAt   *string  `json:"token_expires_at"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", role, rec.Code, rec.Body)
		}
		wantTenants := []string{}
		if role == RoleTenantAdmin || role == RoleTenantViewer {
			wantTenants = []string{"hl-home"}
		}
		if me.Subject != "sub-"+string(role) || me.Email != string(role)+"@example.com" || !slices.Equal(me.Roles, []string{string(role)}) ||
			!slices.Equal(me.Tenants, wantTenants) || me.ExpiresAt == nil {
			t.Fatalf("%s: %+v", role, me)
		}
		has := func(p string) bool { return slices.Contains(me.Permissions, p) }
		if has("tenants:setup") != (role == RoleAdmin || role == RoleTenantAdmin) || has("vendors:write") != (role == RoleAdmin) {
			t.Fatalf("%s permissions %v", role, me.Permissions)
		}
		p := Principal{Subject: me.Subject, Email: me.Email}
		if p.Actor() != me.Email {
			t.Fatalf("actor %q", p.Actor())
		}
	}
}
