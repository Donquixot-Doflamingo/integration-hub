package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Fake ISFC credential the fake Hub accepts (never a real key).
const (
	testISFCClient = "orders-backend"
	testISFCKey    = "isfc_test_canary_key_5b7e19c2d4"
	testTenantUUID = "3f1c2a4e-0000-4000-8000-000000000001"
)

func TestLoadConfigISFC(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{name: "stdio with the ISFC credential", env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey}},
		{name: "with a tenant", env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey, "HUB_TENANT_ID": testTenantUUID}},
		{name: "id without key", env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient}, wantErr: "must be set together"},
		{name: "key without id", env: map[string]string{"HUB_ISFC_API_KEY": testISFCKey}, wantErr: "must be set together"},
		{name: "both credentials", env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey, "HUB_API_KEY": testKey}, wantErr: "use one"},
		{name: "http loopback accepts the environment credential", args: []string{"-transport", "http"}, env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey}},
		{name: "http remote refuses it", args: []string{"-transport", "http", "-addr", "0.0.0.0:8090", "-allow-remote"}, env: map[string]string{"HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey}, wantErr: "HUB_ISFC_API_KEY is refused"},
		{name: "http remote without it is fine", args: []string{"-transport", "http", "-addr", "0.0.0.0:8090", "-allow-remote"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"HUB_BASE_URL": "http://hub"}
			for k, v := range tt.env {
				env[k] = v
			}
			c, err := loadConfig(tt.args, func(k string) string { return env[k] }, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s := c.String(); strings.Contains(s, testISFCKey) {
				t.Errorf("config string shows the ISFC key: %s", s)
			}
		})
	}
}

func isfcTestApp(t *testing.T, baseURL string, o appOpts, extraEnv map[string]string) *app {
	t.Helper()
	env := map[string]string{"HUB_BASE_URL": baseURL, "HUB_ISFC_CLIENT_ID": testISFCClient, "HUB_ISFC_API_KEY": testISFCKey, "HUB_TENANT_ID": testTenantUUID}
	for k, v := range extraEnv {
		env[k] = v
	}
	var args []string
	if o.transport != "" {
		args = []string{"-transport", o.transport}
	}
	cfg, err := loadConfig(args, func(k string) string { return env[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newSecretSet(cfg.apiKey, cfg.adminToken, cfg.isfcAPIKey)
	a, err := newApp(cfg, c, newLogger(io.Discard, 0, secrets), secrets, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestStdioActsAsTheISFCBackendClient(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	cs := connect(t, isfcTestApp(t, hub.srv.URL, appOpts{}, nil))
	out := callTool(t, cs, toolListIntegrations, map[string]any{})
	if out.res.IsError {
		t.Fatalf("list_integrations failed: %s", out.json)
	}
	calls := hub.callsTo("listIntegrations")
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	h := calls[0].Header
	if h.Get("x-isfc-client-id") != testISFCClient || h.Get("x-isfc-api-key") != testISFCKey || h.Get("X-Tenant-Id") != testTenantUUID {
		t.Errorf("headers: client %q key %q tenant %q", h.Get("x-isfc-client-id"), h.Get("x-isfc-api-key"), h.Get("X-Tenant-Id"))
	}
	if h.Get("Authorization") != "" {
		t.Error("an Authorization header was sent with the ISFC credential")
	}
}

// isfcHeaders adds ISFC credential headers to every request.
type isfcHeaders struct {
	client, key, tenant string
	next                http.RoundTripper
}

func (b isfcHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.client != "" {
		r.Header.Set("x-isfc-client-id", b.client)
	}
	if b.key != "" {
		r.Header.Set("x-isfc-api-key", b.key)
	}
	if b.tenant != "" {
		r.Header.Set("X-Tenant-Id", b.tenant)
	}
	return b.next.RoundTrip(r)
}

func TestHTTPAcceptsISFCCredentialHeaders(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	ts := httpTestServer(t, hub, false)

	connectWith := func(rt http.RoundTripper) error {
		cs, err := connectHTTPWith(t, ts.URL+mcpPath, rt)
		if err == nil {
			_ = cs.Close()
		}
		return err
	}
	if err := connectWith(isfcHeaders{client: testISFCClient, key: testISFCKey, tenant: testTenantUUID, next: http.DefaultTransport}); err != nil {
		t.Fatalf("valid ISFC credential: %v", err)
	}
	for _, tt := range []struct {
		name string
		rt   http.RoundTripper
	}{
		{"wrong key", isfcHeaders{client: testISFCClient, key: testISFCKey + "x", next: http.DefaultTransport}},
		{"wrong client", isfcHeaders{client: "other-backend", key: testISFCKey, next: http.DefaultTransport}},
		{"id without key", isfcHeaders{client: testISFCClient, next: http.DefaultTransport}},
		{"key without id", isfcHeaders{key: testISFCKey, next: http.DefaultTransport}},
	} {
		if err := connectWith(tt.rt); err == nil {
			t.Errorf("%s: connected", tt.name)
		}
	}
	// Tenant key and ISFC headers together are refused, as at the Hub.
	if err := connectWith(isfcHeaders{client: testISFCClient, key: testISFCKey, next: bearer{token: testKey, next: http.DefaultTransport}}); err == nil {
		t.Error("mixed credentials: connected")
	}
	// The credential reached the Hub as ISFC headers, with the tenant, and no bearer.
	var sawTenant bool
	for _, c := range hub.callsTo("listIntegrations") {
		if c.Header.Get("x-isfc-api-key") == testISFCKey && c.Header.Get("Authorization") == "" && c.Header.Get("X-Tenant-Id") == testTenantUUID {
			sawTenant = true
		}
	}
	if !sawTenant {
		t.Error("the key check did not reach the Hub with ISFC headers and the tenant")
	}
}

func TestHTTPLoopbackServerUsesItsEnvironmentISFCCredential(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	a := isfcTestApp(t, hub.srv.URL, appOpts{transport: transportHTTP}, nil)
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newMCPServer(c, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	ha := &httpAuth{newClient: a.newHub, newISFC: a.newISFC, now: time.Now, verified: map[[32]byte]verifiedKey{},
		defaultISFC: &isfcDefault{clientID: testISFCClient, key: testISFCKey, tenant: testTenantUUID}}
	ts := httpServe(t, httpHandler(srv, ha, a.log))
	cs, err := connectHTTPWith(t, ts.URL+mcpPath, http.DefaultTransport)
	if err != nil {
		t.Fatalf("no credential, server has one: %v", err)
	}
	_ = cs.Close()
	// A client that brings a bad key of its own is not rescued by the default.
	if cs, err := connectHTTPWith(t, ts.URL+mcpPath, isfcHeaders{client: testISFCClient, key: "nope", next: http.DefaultTransport}); err == nil {
		_ = cs.Close()
		t.Error("bad own credential connected")
	}
}

func TestISFCTokenRoundTrip(t *testing.T) {
	for _, tenant := range []string{"", testTenantUUID} {
		id, tn, key, ok := parseISFCToken(isfcToken("a.b-backend", tenant, "k.e.y"))
		if !ok || id != "a.b-backend" || tn != tenant || key != "k.e.y" {
			t.Errorf("tenant %q: got %q %q %q %v", tenant, id, tn, key, ok)
		}
	}
	for _, bad := range []string{"", "hk_live_x", "isfc.", "isfc.a.b", "isfc.!!.b.c"} {
		if _, _, _, ok := parseISFCToken(bad); ok {
			t.Errorf("parsed %q", bad)
		}
	}
}

// HUB_TENANT_ID picks the ISFC tenant the signed-in user acts in on hub_admin_* calls; the
// admin token stays the user's own bearer token.
func TestAdminCallsCarryTheTenantHeader(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("searchRequests", hub.adm("searchRequests", 200, ""))
	env := map[string]string{"HUB_ADMIN_TOKEN": testAdminToken, "HUB_ADMIN_BASE_URL": hub.srv.URL + "/admin/v1"}
	cs := connect(t, isfcTestApp(t, hub.srv.URL, appOpts{}, env))
	if out := callTool(t, cs, toolAdminSearch, map[string]any{}); out.res.IsError {
		t.Fatalf("search failed: %s", out.json)
	}
	h := hub.callsTo("searchRequests")[0].Header
	if h.Get("X-Tenant-Id") != testTenantUUID || h.Get("Authorization") != "Bearer "+testAdminToken {
		t.Errorf("tenant %q, authorization %q", h.Get("X-Tenant-Id"), h.Get("Authorization"))
	}
	if h.Get("x-isfc-api-key") != "" {
		t.Error("the ISFC backend key was sent to the admin API: admin tools act as the user")
	}
}
