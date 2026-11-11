package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/authz"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
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
	keys  map[string]catalogKey // API keys of the bundle (allowed_api_keys)
	down  atomic.Bool
	calls atomic.Int64
	// failWith, when set, is returned as the OPA error (a test that checks the log is clean of it).
	failWith error
}

// catalogKey is one entry of the bundle's allowed_api_keys.
type catalogKey struct {
	client string // allowed_client_id
	status string
	perms  []string
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
	if f.failWith != nil {
		return authz.Decision{}, f.failWith
	}
	if in.Client.APIKey != "" {
		return f.decideMachine(in), nil
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

// decideMachine mirrors has_required_permissions_on_api_key: the key must be ACTIVE, issued to
// the calling client and hold the operation's permission; a key has platform scope, no tenant.
func (f *catalogOPA) decideMachine(in authz.Input) authz.Decision {
	key, ok := f.keys[string(in.Client.APIKey)]
	if !ok || key.status != "ACTIVE" || key.client != in.Client.ClientID {
		return authz.Decision{}
	}
	for _, r := range f.rules {
		if r.method != in.HTTP.Method || !r.re.MatchString(in.HTTP.Path) {
			continue
		}
		if r.perm == "none" || slices.Contains(key.perms, r.perm) {
			return authz.Decision{Allow: true, Meta: map[string]any{"key_permissions": toAny(key.perms)}}
		}
	}
	return authz.Decision{}
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
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

// machine keys of the machine tests: key -> bundle entry.
func machineKeys() map[string]catalogKey {
	return map[string]catalogKey{
		"isfck_admin_key_0001":  {"ci-admin", "ACTIVE", slices.Concat(hubReadPerms, hubWritePerms, hubAdminPerms)},
		"isfck_ci_key_00000002": {"integration-hub-ci", "ACTIVE", []string{"HUB_INTEGRATION:WRITE"}},
		"isfck_viewer_key_0003": {"viewer-bot", "ACTIVE", hubReadPerms},
		"isfck_revoked_key_004": {"ci-admin", "INACTIVE", slices.Concat(hubReadPerms, hubWritePerms, hubAdminPerms)},
	}
}

func machineReq(t *testing.T, method, path, client, key string) *http.Request {
	t.Helper()
	req := newReq(t, method, path, strings.NewReader(`{}`))
	if client != "" {
		req.Header.Set(hdrISFCClientID, client)
	}
	if key != "" {
		req.Header.Set(hdrISFCAPIKey, key)
	}
	return req
}

// TestISFCMachineMatrix: automation clients call the admin API directly with x-isfc-client-id and
// x-isfc-api-key (HUB-476). OPA decides by the key's permissions; unsealRequest and
// purgeDeadLetters are refused whatever the key holds; a revoked key, a key of another client and an
// unknown key are refused.
func TestISFCMachineMatrix(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	opa := newCatalogOPA(spec)
	opa.keys = machineKeys()
	m, s := isfcMatrixModeWith(t, opa)
	runMachineMatrix(t, spec, m, s)
	// The refusal is not an OPA deny: it never even asks.
	before := opa.calls.Load()
	rec := httptest.NewRecorder()
	m.server.Handler().ServeHTTP(rec, machineReq(t, http.MethodPost, Base+"/dlq/purge", "ci-admin", "isfck_admin_key_0001"))
	if rec.Code != http.StatusForbidden || opa.calls.Load() != before || !strings.Contains(rec.Body.String(), "API keys cannot call") {
		t.Errorf("purgeDeadLetters by a machine: %d %s (OPA calls %d -> %d)", rec.Code, rec.Body, before, opa.calls.Load())
	}
}

// runMachineMatrix sends every route with every kind of key (machineKeys) and checks the answer.
func runMachineMatrix(t *testing.T, spec *contract.Spec, m matrixMode, s *Server) {
	t.Helper()
	h := m.server.Handler()
	perms := map[string]string{}
	for _, op := range spec.Operations() {
		perms[op.ID] = op.Extensions["x-required-permission"]
	}
	type want struct {
		name, client, key string
		allowed           func(op, perm string) bool
		code              int // when not allowed
	}
	always := func(string, string) bool { return true }
	cases := []want{
		{"all permissions", "ci-admin", "isfck_admin_key_0001", always, http.StatusForbidden},
		{"integration writer only", "integration-hub-ci", "isfck_ci_key_00000002", func(_, p string) bool { return p == "HUB_INTEGRATION:WRITE" || p == "none" }, http.StatusForbidden},
		{"read only", "viewer-bot", "isfck_viewer_key_0003", func(_, p string) bool { return strings.HasSuffix(p, ":READ") || p == "none" }, http.StatusForbidden},
		{"revoked key", "ci-admin", "isfck_revoked_key_004", func(string, string) bool { return false }, http.StatusForbidden},
		{"key of another client", "intruder", "isfck_admin_key_0001", func(string, string) bool { return false }, http.StatusForbidden},
		{"unknown key", "ci-admin", "isfck_made_up_key_0005", func(string, string) bool { return false }, http.StatusForbidden},
		{"no client id", "", "isfck_admin_key_0001", func(string, string) bool { return false }, http.StatusUnauthorized},
	}
	for _, c := range cases {
		for _, rt := range s.Routes() {
			req := machineReq(t, rt.Method, Base+samplePath(rt.Path), c.client, c.key)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			allowed := c.allowed(rt.Op, perms[rt.Op]) && !machineRefused[rt.Op]
			switch {
			case allowed && (rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized || rec.Code == http.StatusServiceUnavailable):
				t.Errorf("%s: %s: %d, want past authorization", c.name, rt.Op, rec.Code)
			case !allowed && rec.Code != c.code:
				t.Errorf("%s: %s: %d, want %d", c.name, rt.Op, rec.Code, c.code)
			case !allowed && rt.Op == "getMe":
				// The contract lists no 403 for getMe (every role may call it); a refused key or a user
				// without a Hub role still gets the standard envelope. Contract gap, reported to the steward.
			case !allowed:
				checkResponse(t, spec, req, rec)
			}
		}
	}
	// A bearer token together with an API key is ambiguous.
	req := machineReq(t, http.MethodGet, Base+"/me", "ci-admin", "isfck_admin_key_0001")
	req.Header.Set("Authorization", "Bearer a.b.c")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bearer + key: %d", rec.Code)
	}
}

// TestISFCMachinePrincipal: the principal is client:<id>, Machine, with the roles its key covers;
// the change log actor is client:<id>.
func TestISFCMachinePrincipal(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	opa := newCatalogOPA(spec)
	opa.keys = machineKeys()
	_, s := isfcMatrixModeWith(t, opa)
	for key, wantRoles := range map[string][]Role{
		"isfck_admin_key_0001":  {RoleAdmin, RoleEditor, RoleViewer, RoleTenantAdmin, RoleTenantViewer},
		"isfck_viewer_key_0003": {RoleViewer, RoleTenantViewer},
		"isfck_ci_key_00000002": nil,
	} {
		req := machineReq(t, http.MethodGet, Base+"/me", opa.keys[key].client, key)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: /me %d %s", key, rec.Code, rec.Body)
		}
		var me struct {
			Subject string   `json:"subject"`
			Roles   []string `json:"roles"`
			Tenants []string `json:"tenants"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
		wantNames := make([]string, len(wantRoles))
		for i, r := range wantRoles {
			wantNames[i] = string(r)
		}
		if me.Subject != "client:"+opa.keys[key].client || !slices.Equal(me.Roles, wantNames) || len(me.Tenants) != 0 {
			t.Fatalf("%s: /me %+v, want roles %v", key, me, wantNames)
		}
	}
	p := Principal{Subject: "client:ci-admin", Machine: true}
	if p.Actor() != "client:ci-admin" || p.Scoped() {
		t.Fatalf("actor %q scoped %v", p.Actor(), p.Scoped())
	}
}

// TestMachineKeyIsRedactedFromLogs: the logger redacts x-isfc-api-key by value (HUB-476, AGENTS
// §3.5). Even a policy engine error that echoes the key cannot put it in a log line.
func TestMachineKeyIsRedactedFromLogs(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	const key = "isfck_admin_key_0001"
	opa := newCatalogOPA(spec)
	opa.keys = machineKeys()
	opa.failWith = fmt.Errorf("%w: upstream said key %s is bad", authz.ErrUnavailable, key)
	var buf strings.Builder
	secrets := telemetry.NewSecretSet()
	log := telemetry.NewLogger(&buf, slog.LevelDebug, secrets, "admin")
	iss := newRSIssuer(t, testHydra, "rs-1", 2048)
	s := New(Deps{ISFC: testISFCAuth(t, iss), Authz: opa, Log: log, Secrets: secrets})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, machineReq(t, http.MethodGet, Base+"/vendors", "ci-admin", key))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	out := buf.String()
	if !strings.Contains(out, "admin.authz") || strings.Contains(out, key) || !strings.Contains(out, "***") {
		t.Fatalf("the key must be redacted by value:\n%s", out)
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Fatalf("the key is in the response: %s", rec.Body)
	}
	// Without a Secrets set nothing breaks (and nothing is registered).
	s2 := New(Deps{ISFC: testISFCAuth(t, iss), Authz: opa})
	s2.Handler().ServeHTTP(httptest.NewRecorder(), machineReq(t, http.MethodGet, Base+"/vendors", "ci-admin", key))
}

// TestRedactedKeysAreBounded: endless distinct keys cannot grow the redaction set.
func TestRedactedKeysAreBounded(t *testing.T) {
	secrets := telemetry.NewSecretSet()
	s := New(Deps{Secrets: secrets})
	for i := range 2 * maxRedactedKeys {
		s.redactKey(fmt.Sprintf("isfck_flood_key_%06d", i))
	}
	if secrets.Len() != maxRedactedKeys {
		t.Fatalf("%d keys registered, want %d", secrets.Len(), maxRedactedKeys)
	}
}
