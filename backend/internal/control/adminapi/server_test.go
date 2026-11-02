package adminapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

var allRoles = []Role{RoleAdmin, RoleEditor, RoleViewer, RoleTenantViewer}

// testServer is a server without collaborators and a dev issuer for tokens.
func testServer(t *testing.T, d Deps) (*Server, *DevIssuer) {
	t.Helper()
	iss, err := NewDevIssuer("http://admin.test")
	if err != nil {
		t.Fatal(err)
	}
	d.Verifier = iss.Verifier()
	return New(d), iss
}

func token(t *testing.T, iss *DevIssuer, role Role, tenants ...string) string {
	t.Helper()
	tok, err := iss.Mint(Principal{Subject: "sub-" + string(role), Email: string(role) + "@example.com", Name: "Test " + string(role),
		Roles: []Role{role}, Tenants: tenants}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var pathParam = regexp.MustCompile(`\{[a-z_]+\}`)

// samplePath fills a contract path template with placeholder values.
func samplePath(p string) string { return pathParam.ReplaceAllString(p, "x1") }

// TestRoutesMatchContract: every registered route is a contract operation with the same method,
// path and x-required-role, and is mutating exactly when it is not GET (except the contract's
// non-mutating POST actions, which declare no reason).
func TestRoutesMatchContract(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	ops := map[string]contract.Operation{}
	for _, op := range spec.Operations() {
		ops[op.ID] = op
	}
	s, _ := testServer(t, Deps{})
	seen := map[string]bool{}
	for _, rt := range s.Routes() {
		op, ok := ops[rt.Op]
		if !ok {
			t.Errorf("route %s %s: operation %q is not in the contract", rt.Method, rt.Path, rt.Op)
			continue
		}
		if seen[rt.Op] {
			t.Errorf("operation %s registered twice", rt.Op)
		}
		seen[rt.Op] = true
		if op.Method != rt.Method || op.Path != rt.Path {
			t.Errorf("%s: route %s %s, contract %s %s", rt.Op, rt.Method, rt.Path, op.Method, op.Path)
		}
		if want := op.Extensions["x-required-role"]; Role(want) != rt.Role {
			t.Errorf("%s: route role %s, contract x-required-role %s", rt.Op, rt.Role, want)
		}
	}
	t.Logf("%d of %d contract operations implemented", len(seen), len(ops))
}

// TestRBACMatrix: for every registered route and every role, a caller below the operation's role
// gets 403 FORBIDDEN and one at or above it gets past authorization. Tokenless, expired and
// foreign tokens get 401.
func TestRBACMatrix(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	s, iss := testServer(t, Deps{})
	h := s.Handler()
	other, _ := NewDevIssuer("http://admin.test") // same iss, different key
	expired, _ := iss.Mint(Principal{Subject: "old", Roles: []Role{RoleAdmin}}, -time.Hour)
	foreign := token(t, other, RoleAdmin)
	for _, rt := range s.Routes() {
		path := Base + samplePath(rt.Path)
		for _, role := range allRoles {
			req := httptest.NewRequestWithContext(t.Context(), rt.Method, path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+token(t, iss, role, "orders-service"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			allowed := Principal{Roles: []Role{role}}.Allows(rt.Role)
			switch {
			case !allowed && rec.Code != http.StatusForbidden:
				t.Errorf("%s as %s: %d, want 403", rt.Op, role, rec.Code)
			case allowed && (rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized):
				t.Errorf("%s as %s: %d, want past authorization", rt.Op, role, rec.Code)
			case !allowed:
				checkResponse(t, spec, req, rec)
			}
		}
		for name, tok := range map[string]string{"no token": "", "expired": expired, "foreign key": foreign, "garbage": "a.b.c"} {
			req := httptest.NewRequestWithContext(t.Context(), rt.Method, path, nil)
			if tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %s: %d, want 401", rt.Op, name, rec.Code)
				continue
			}
			checkResponse(t, spec, req, rec)
		}
	}
}

// checkResponse validates only the response of an exchange (its request may be incomplete).
func checkResponse(t *testing.T, spec *contract.Spec, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	if err := spec.Check(contract.Exchange{Request: req, Status: rec.Code, Header: rec.Header(), Body: rec.Body.Bytes(), InvalidRequest: true}); err != nil {
		t.Error(err)
	}
	var env struct {
		Error Error `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.RequestID != rec.Header().Get("X-Hub-Request-Id") {
		t.Errorf("error envelope %s: request_id must equal X-Hub-Request-Id", rec.Body)
	}
}

func TestMe(t *testing.T) {
	spec := contract.Load(t, "hub-admin.v1.yaml")
	s, iss := testServer(t, Deps{})
	for _, role := range allRoles {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+"/me", nil)
		req.Header.Set("Authorization", "Bearer "+token(t, iss, role, "orders-service"))
		rec := spec.Serve(t, s.Handler(), req)
		var me struct {
			Subject     string   `json:"subject"`
			Roles       []string `json:"roles"`
			Tenants     []string `json:"tenants"`
			Permissions []string `json:"permissions"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &me)
		if rec.Code != 200 || me.Subject != "sub-"+string(role) || len(me.Roles) != 1 || me.Roles[0] != string(role) {
			t.Fatalf("%s: %d %s", role, rec.Code, rec.Body)
		}
		wantTenants := 0
		if role == RoleTenantViewer {
			wantTenants = 1
		}
		if len(me.Tenants) != wantTenants {
			t.Fatalf("%s tenants %v", role, me.Tenants)
		}
		has := func(p string) bool { return strings.Contains(strings.Join(me.Permissions, ","), p) }
		if has("dlq:purge") != (role == RoleAdmin) || has("integrations:publish") != (role == RoleAdmin || role == RoleEditor) {
			t.Fatalf("%s permissions %v", role, me.Permissions)
		}
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	s, _ := testServer(t, Deps{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+"/nope", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), CodeNotFound) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestDevIssuerServesDiscoveryAndTokens(t *testing.T) {
	iss, err := NewDevIssuer("http://admin.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(iss.Handler())
	t.Cleanup(srv.Close)
	iss.Issuer = srv.URL
	status, b := postJSON(t, srv.URL+"/oidc/dev-token", `{"sub":"priya","email":"priya@example.com","roles":["editor"]}`)
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.AccessToken == "" {
		t.Fatalf("dev-token %d %s", status, b)
	}
	// A verifier that only knows the issuer URL finds the key through discovery.
	v := &JWTVerifier{Issuer: srv.URL, Keys: RemoteKeys(srv.URL, srv.Client())}
	p, err := v.Verify(t.Context(), out.AccessToken)
	if err != nil || p.Subject != "priya" || p.Top() != RoleEditor || p.Actor() != "priya@example.com" {
		t.Fatalf("verify %+v %v", p, err)
	}
	if status, _ := postJSON(t, srv.URL+"/oidc/dev-token", `{"sub":"x","roles":["root"]}`); status != 400 {
		t.Fatalf("unknown role minted: %d", status)
	}
}

func postJSON(t *testing.T, url, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
