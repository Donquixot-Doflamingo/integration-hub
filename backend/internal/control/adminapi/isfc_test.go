package adminapi

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const (
	testHydra        = "https://hydra.test"
	testConsoleID    = "integration-hub-console"
	testPlatformUUID = "11111111-1111-4111-8111-111111111111"
	testTenantHL     = "22222222-2222-4222-8222-222222222222" // linked, Hub id "hl-home"
	testTenantUnk    = "33333333-3333-4333-8333-333333333333" // not synced
)

// gatewayHeaders are the x-isfc-* headers the ISFC gateway forwards for a signed-in user.
type gatewayHeaders struct {
	sub, client, email, tenant string
	roles                      []string
	rawAttrs                   string // overrides roles when set
}

func (g gatewayHeaders) apply(h http.Header) {
	h.Set(hdrISFCSubID, g.sub)
	h.Set(hdrISFCClientID, g.client)
	h.Set(hdrISFCSubType, "USER")
	if g.email != "" {
		h.Set(hdrISFCEmail, g.email)
	}
	if g.tenant != "" {
		h.Set(hdrISFCTenantID, g.tenant)
	}
	attrs := g.rawAttrs
	if attrs == "" {
		b, _ := json.Marshal(map[string]any{"roles": g.roles, "permissions": []string{}})
		attrs = base64.StdEncoding.EncodeToString(b)
	}
	h.Set(hdrISFCSubAttrs, attrs)
}

func testISFCAuth(t testing.TB, iss *rsIssuer) *ISFCAuth {
	t.Helper()
	keys := map[string]crypto.PublicKey{iss.kid: &iss.key.PublicKey}
	return &ISFCAuth{
		Tokens:           &JWTVerifier{Issuer: testHydra, Keys: keysFunc(keys)},
		ConsoleClientID:  testConsoleID,
		PlatformTenantID: testPlatformUUID,
		ResolveTenant: func(_ context.Context, id string) (string, bool) {
			if id == testTenantHL {
				return "hl-home", true
			}
			return "", false
		},
	}
}

func TestISFCAuthenticate(t *testing.T) {
	iss := newRSIssuer(t, testHydra, "rs-1", 2048)
	other := newRSIssuer(t, testHydra, "rs-1", 2048)
	a := testISFCAuth(t, iss)
	tok := func(claims map[string]any) string {
		c := map[string]any{"sub": "u-1", "client_id": testConsoleID}
		for k, v := range claims {
			c[k] = v
		}
		return iss.sign(t, c)
	}
	user := gatewayHeaders{sub: "u-1", client: testConsoleID, email: "priya@example.com", tenant: testPlatformUUID, roles: []string{"HUB_EDITOR"}}
	with := func(f func(*gatewayHeaders)) gatewayHeaders { g := user; f(&g); return g }

	tests := []struct {
		name       string
		token      string
		hdr        gatewayHeaders
		wantReason string // "" = authenticated
		wantRoles  []Role
		wantTenant []string
		wantScoped bool
	}{
		{"platform editor", tok(nil), user, "", []Role{RoleEditor}, []string{}, false},
		{"platform all roles", tok(nil), with(func(g *gatewayHeaders) { g.roles = []string{"HUB_VIEWER", "HUB_ADMIN"} }), "", []Role{RoleViewer, RoleAdmin}, []string{}, false},
		{"tenant viewer", tok(nil), with(func(g *gatewayHeaders) { g.tenant, g.roles = testTenantHL, []string{"HUB_TENANT_VIEWER"} }), "", []Role{RoleTenantViewer}, []string{"hl-home"}, true},
		{"tenant admin", tok(nil), with(func(g *gatewayHeaders) { g.tenant, g.roles = testTenantHL, []string{"HUB_TENANT_ADMIN"} }), "", []Role{RoleTenantAdmin}, []string{"hl-home"}, true},
		{"platform role in a linked tenant counts for nothing", tok(nil), with(func(g *gatewayHeaders) { g.tenant, g.roles = testTenantHL, []string{"HUB_ADMIN"} }), "", nil, []string{}, false},
		{"tenant role in the platform tenant counts for nothing", tok(nil), with(func(g *gatewayHeaders) { g.roles = []string{"HUB_TENANT_ADMIN"} }), "", nil, []string{}, false},
		{"ISFC_ADMIN is not a Hub admin (D6)", tok(nil), with(func(g *gatewayHeaders) { g.roles = []string{"ISFC_ADMIN", "ISFC_READ_ADMIN"} }), "", nil, []string{}, false},
		{"tenant not synced: no roles", tok(nil), with(func(g *gatewayHeaders) { g.tenant, g.roles = testTenantUnk, []string{"HUB_TENANT_VIEWER"} }), "", nil, []string{}, false},
		{"no tenant header: no roles", tok(nil), with(func(g *gatewayHeaders) { g.tenant = "" }), "", nil, []string{}, false},
		{"malformed attrs (not base64): no roles", tok(nil), with(func(g *gatewayHeaders) { g.rawAttrs = "%%%not base64%%%" }), "", nil, []string{}, false},
		{"malformed attrs (not JSON): no roles", tok(nil), with(func(g *gatewayHeaders) { g.rawAttrs = base64.StdEncoding.EncodeToString([]byte("[1,2")) }), "", nil, []string{}, false},
		{"roles of the wrong shape: no roles", tok(nil), with(func(g *gatewayHeaders) {
			g.rawAttrs = base64.StdEncoding.EncodeToString([]byte(`{"roles":[{"name":"HUB_ADMIN"}]}`))
		}), "", nil, []string{}, false},
		{"unpadded base64 attrs are read", tok(nil), with(func(g *gatewayHeaders) {
			g.rawAttrs = base64.RawStdEncoding.EncodeToString([]byte(`{"roles":["HUB_VIEWER"]}`))
		}), "", []Role{RoleViewer}, []string{}, false},
		{"no token", "", user, authnNoToken, nil, nil, false},
		{"foreign signature", other.sign(t, map[string]any{"sub": "u-1", "client_id": testConsoleID}), user, authnBadSignature, nil, nil, false},
		{"expired token", tok(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), user, authnBadSignature, nil, nil, false},
		{"sub header differs from the token", tok(nil), with(func(g *gatewayHeaders) { g.sub = "someone-else" }), authnHeaderMismatch, nil, nil, false},
		{"no sub header", tok(nil), with(func(g *gatewayHeaders) { g.sub = "" }), authnHeaderMismatch, nil, nil, false},
		{"client header differs from the token", tok(nil), with(func(g *gatewayHeaders) { g.client = "other-client" }), authnHeaderMismatch, nil, nil, false},
		{"token of another client with matching headers", tok(map[string]any{"client_id": "other-client"}), with(func(g *gatewayHeaders) { g.client = "other-client" }), authnHeaderMismatch, nil, nil, false},
		{"token without client_id", tok(map[string]any{"client_id": ""}), with(func(g *gatewayHeaders) { g.client = "" }), authnHeaderMismatch, nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+"/me", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			tt.hdr.apply(req.Header)
			p, aerr := a.authenticate(req)
			if tt.wantReason != "" {
				if aerr == nil || aerr.reason != tt.wantReason || aerr.err.Status != http.StatusUnauthorized {
					t.Fatalf("got %+v, want 401 reason %s", aerr, tt.wantReason)
				}
				return
			}
			if aerr != nil {
				t.Fatalf("unexpected %+v", aerr.err)
			}
			if !slices.Equal(p.Roles, tt.wantRoles) || !slices.Equal(p.Tenants, tt.wantTenant) || p.Scoped() != tt.wantScoped {
				t.Fatalf("principal %+v: roles %v tenants %v scoped %v, want %v %v %v", p, p.Roles, p.Tenants, p.Scoped(), tt.wantRoles, tt.wantTenant, tt.wantScoped)
			}
			if p.Subject != "u-1" || p.ClientID != testConsoleID {
				t.Fatalf("identity %+v", p)
			}
		})
	}
}

// TestISFCModeServer: the server in ISFC mode answers 401 without a token, 403 without a Hub
// role or below the operation's role, and counts the refusals; the jwt mode is not involved.
func TestISFCModeServer(t *testing.T) {
	iss := newRSIssuer(t, testHydra, "rs-1", 2048)
	reg := prometheus.NewRegistry()
	s := New(Deps{ISFC: testISFCAuth(t, iss), Metrics: reg})
	call := func(tok string, g gatewayHeaders) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+"/snapshots", nil) // needs viewer
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		g.apply(req.Header)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	tok := iss.sign(t, map[string]any{"sub": "u-1", "client_id": testConsoleID})
	g := gatewayHeaders{sub: "u-1", client: testConsoleID, tenant: testPlatformUUID, roles: []string{"HUB_TENANT_VIEWER"}}
	if rec := call("", g); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := call(tok, g); rec.Code != http.StatusForbidden { // tenant role in the platform tenant: no roles
		t.Fatalf("no role: %d %s", rec.Code, rec.Body)
	}
	g.tenant, g.roles = testTenantHL, []string{"HUB_TENANT_VIEWER"}
	if rec := call(tok, g); rec.Code != http.StatusForbidden { // snapshots need viewer
		t.Fatalf("tenant viewer on a platform route: %d", rec.Code)
	}
	if got := testutil.ToFloat64(s.authn.failures.WithLabelValues(authnNoToken)); got != 1 {
		t.Fatalf("no_token count %v", got)
	}
	if got := testutil.ToFloat64(s.authn.failures.WithLabelValues(authnNoRoles)); got != 1 {
		t.Fatalf("no_roles count %v", got)
	}
}

// TestRoleHierarchy: admin ⊃ editor ⊃ viewer ⊃ tenant_viewer and admin ⊃ tenant_admin ⊃
// tenant_viewer; editor and viewer do not satisfy tenant_admin.
func TestRoleHierarchy(t *testing.T) {
	want := map[Role][]Role{
		RoleAdmin:        {RoleAdmin, RoleEditor, RoleViewer, RoleTenantAdmin, RoleTenantViewer},
		RoleEditor:       {RoleEditor, RoleViewer, RoleTenantViewer},
		RoleViewer:       {RoleViewer, RoleTenantViewer},
		RoleTenantAdmin:  {RoleTenantAdmin, RoleTenantViewer},
		RoleTenantViewer: {RoleTenantViewer},
	}
	for have, satisfied := range want {
		for _, need := range []Role{RoleAdmin, RoleEditor, RoleViewer, RoleTenantAdmin, RoleTenantViewer} {
			if got := (Principal{Roles: []Role{have}}).Allows(need); got != slices.Contains(satisfied, need) {
				t.Errorf("%s holding %s: %v", need, have, got)
			}
		}
	}
	if (Principal{}).Allows(RoleTenantViewer) {
		t.Error("a principal without roles is allowed")
	}
	if !(Principal{Roles: []Role{RoleTenantAdmin}}).Scoped() || (Principal{Roles: []Role{RoleViewer, RoleTenantViewer}}).Scoped() {
		t.Error("scope: tenant_admin is scoped, viewer+tenant_viewer is not")
	}
}
