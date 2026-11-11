package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer adds an Authorization header to every request.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// httpTestServer serves hubmcp over Streamable HTTP in front of hub. Keys are verified against
// the fake Hub, which accepts testKey only.
func httpTestServer(t *testing.T, hub *fakeHub, withAdmin bool) *httptest.Server {
	t.Helper()
	o := appOpts{transport: transportHTTP}
	if withAdmin {
		o.adminToken = testAdminToken // allowed on loopback (ADR-0018): read tools only
	}
	a := newTestApp(t, hub.srv.URL, o) // over HTTP every client brings its own key (a.hub is nil)
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newMCPServer(c, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	ha := &httpAuth{newClient: a.newHub, newISFC: a.newISFC, now: time.Now, verified: map[[32]byte]verifiedKey{}}
	return httpServe(t, httpHandler(srv, ha, a.log))
}

func httpServe(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func connectHTTP(t *testing.T, endpoint, token string) (*mcp.ClientSession, error) {
	t.Helper()
	if token == "" {
		return connectHTTPWith(t, endpoint, http.DefaultTransport)
	}
	return connectHTTPWith(t, endpoint, bearer{token: token, next: http.DefaultTransport})
}

// connectHTTPWith connects through rt, which sets the credential headers.
func connectHTTPWith(t *testing.T, endpoint string, rt http.RoundTripper) (*mcp.ClientSession, error) {
	t.Helper()
	hc := &http.Client{Transport: rt}
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: hc, DisableStandaloneSSE: true, MaxRetries: -1}
	return mcp.NewClient(&mcp.Implementation{Name: "http-test", Version: "1"}, nil).Connect(t.Context(), tr, nil)
}

// TestHTTPTransportServesSameToolsAsStdio: the Streamable HTTP endpoint advertises the stdio tool
// set minus the admin write tools, which are stdio-only (HUB-301, HUB-315).
func TestHTTPTransportServesSameToolsAsStdio(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	stdioOnly := map[string]bool{}
	for _, tool := range c.Tools {
		if !tool.allowedOn(transportHTTP) {
			stdioOnly[tool.Name] = true
		}
	}
	if len(stdioOnly) != 16 {
		t.Fatalf("%d stdio-only tools in the contract, want the 16 write tools", len(stdioOnly))
	}
	for _, withAdmin := range []bool{false, true} {
		hub := newFakeHub(t)
		hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
		ts := httpTestServer(t, hub, withAdmin)
		cs, err := connectHTTP(t, ts.URL+mcpPath, testKey)
		if err != nil {
			t.Fatal(err)
		}
		got := toolNames(t, cs)
		_ = cs.Close()
		var want []string
		for _, name := range toolNames(t, connect(t, testApp(t, "http://127.0.0.1:1", withAdmin, nil))) {
			if !stdioOnly[name] {
				want = append(want, name)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("admin=%v: http tools %v, stdio tools without write tools %v", withAdmin, got, want)
		}
		if n := len(got); (withAdmin && n != 25) || (!withAdmin && n != 5) {
			t.Errorf("admin=%v: %d tools", withAdmin, n)
		}
	}
}

// TestHTTPTransportRejectsUnauthenticated: requests without a tenant key the Hub accepts get 401
// and never reach the MCP layer (HUB-301).
func TestHTTPTransportRejectsUnauthenticated(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	ts := httpTestServer(t, hub, true)
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	tests := []struct {
		name, auth string
	}{
		{"no header", ""},
		{"key the Hub rejects", "Bearer hk_test_unknown_key_0000000000"},
		{"key prefix", "Bearer " + testKey[:20]},
		{"key with suffix", "Bearer " + testKey + "x"},
		{"basic scheme", "Basic " + testKey},
		{"admin token as tenant key", "Bearer " + testAdminToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+mcpPath, strings.NewReader(initialize))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401 (body %s)", resp.StatusCode, body)
			}
			if strings.Contains(string(body), "hubmcp") || strings.Contains(string(body), testKey) {
				t.Fatalf("401 body reveals server details: %s", body)
			}
		})
	}
	t.Run("MCP client without token cannot connect", func(t *testing.T) {
		if cs, err := connectHTTP(t, ts.URL+mcpPath, ""); err == nil {
			_ = cs.Close()
			t.Fatal("connected without a token")
		}
	})
	t.Run("cross-origin browser request", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+mcpPath, strings.NewReader(initialize))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testKey)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "https://evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status %d, want 403", resp.StatusCode)
		}
	})
}

// TestHTTPKeyVerificationIsCachedAndFailsClosed: an accepted key is re-checked at most once per
// keyCheckTTL, and an unreachable Hub never lets a request through.
func TestHTTPKeyVerificationIsCachedAndFailsClosed(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	a := testApp(t, hub.srv.URL, false, nil)
	now := time.Unix(1_800_000_000, 0)
	ha := &httpAuth{newClient: a.newHub, now: func() time.Time { return now }, verified: map[[32]byte]verifiedKey{}}
	for range 3 {
		if _, err := ha.verify(t.Context(), testKey, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(hub.callsTo("listIntegrations")); n != 1 {
		t.Fatalf("%d key checks within the TTL, want 1", n)
	}
	now = now.Add(keyCheckTTL + time.Second)
	if _, err := ha.verify(t.Context(), testKey, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(hub.callsTo("listIntegrations")); n != 2 {
		t.Fatalf("%d key checks after the TTL, want 2", n)
	}
	if c := hub.callsTo("listIntegrations")[0]; c.Query != "limit=1" || !strings.Contains(c.Header.Get("User-Agent"), "hubmcp/") {
		t.Errorf("key check query %q, UA %q", c.Query, c.Header.Get("User-Agent"))
	}

	down := testApp(t, "http://127.0.0.1:1", false, nil)
	hd := &httpAuth{newClient: down.newHub, now: time.Now, verified: map[[32]byte]verifiedKey{}}
	if _, err := hd.verify(t.Context(), testKey, nil); err == nil {
		t.Fatal("verified a key while the Hub is unreachable")
	}
}

// TestRunServesHTTPUntilCancelled runs the real entry point with -transport http.
func TestRunServesHTTPUntilCancelled(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var logs syncBuffer
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	env := map[string]string{"HUB_BASE_URL": hub.srv.URL}
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-transport", "http", "-addr", addr}, func(k string) string { return env[k] }, strings.NewReader(""), io.Discard, &logs)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(logs.String(), `"msg":"hubmcp listening"`) {
		if time.Now().After(deadline) {
			t.Fatalf("not listening: %s", logs.String())
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-timer.C:
		case code := <-done:
			t.Fatalf("exited early with %d: %s", code, logs.String())
		}
	}
	cs, err := connectHTTP(t, "http://"+addr+mcpPath, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(toolNames(t, cs)); n != 5 {
		t.Errorf("%d tools over http, want 5", n)
	}
	_ = cs.Close()
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, logs.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop after cancel")
	}
	if s := logs.String(); strings.Contains(s, testKey) {
		t.Fatalf("secret in logs: %s", s)
	}
}

func TestHTTPConfig(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{name: "loopback, no HUB_API_KEY needed", args: []string{"-transport", "http"}},
		{name: "ipv6 loopback", args: []string{"-transport", "http", "-addr", "[::1]:9000"}},
		{name: "remote without flag", args: []string{"-transport", "http", "-addr", "0.0.0.0:8090"}, wantErr: "-allow-remote"},
		{name: "remote with flag", args: []string{"-transport", "http", "-addr", "0.0.0.0:8090", "-allow-remote"}},
		{name: "admin token on loopback", args: []string{"-transport", "http"}, env: map[string]string{"HUB_ADMIN_TOKEN": testAdminToken}},
		{name: "admin token on a remote listener", args: []string{"-transport", "http", "-addr", "0.0.0.0:8090", "-allow-remote"}, env: map[string]string{"HUB_ADMIN_TOKEN": testAdminToken}, wantErr: "HUB_ADMIN_TOKEN is refused"},
		{name: "stdio still needs HUB_API_KEY", args: []string{"-transport", "stdio"}, wantErr: "HUB_API_KEY is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{"HUB_BASE_URL": "http://hub"}
			for k, v := range tt.env {
				env[k] = v
			}
			_, err := loadConfig(tt.args, func(k string) string { return env[k] }, io.Discard)
			if tt.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("error %v, want %q", err, tt.wantErr)
			}
		})
	}
}
