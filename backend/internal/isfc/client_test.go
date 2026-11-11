package isfc_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

const (
	hubClient  = "integration-hub"
	hubSecret  = "hub-secret-value"
	consoleCli = "integration-hub-console"
)

func setup(t *testing.T) (*mockisfc.Server, *isfc.Client, *httptest.Server) {
	t.Helper()
	m := mockisfc.New(mockisfc.Options{})
	m.Seed(hubClient, hubSecret, consoleCli)
	ts := httptest.NewServer(m.Handler())
	t.Cleanup(ts.Close)
	c, err := isfc.New(isfc.Config{BaseURL: ts.URL, ClientID: hubClient, ClientSecret: hubSecret})
	if err != nil {
		t.Fatal(err)
	}
	return m, c, ts
}

func TestTenantsAndClientMetadata(t *testing.T) {
	m, c, _ := setup(t)
	hl := m.AddTenant("HL", "Home Loans")
	pl := m.AddTenant("PL", "Personal Loans")
	m.SetClientTenants(consoleCli, hl, pl)
	m.SetTenantStatus(pl, "INACTIVE")

	list, err := c.ListTenants(t.Context())
	if err != nil || len(list) != 2 {
		t.Fatalf("ListTenants: %v %v", list, err)
	}
	got, err := c.GetTenant(t.Context(), hl)
	if err != nil || got.Key != "HL" || got.Name != "Home Loans" || got.Status != isfc.StatusActive {
		t.Fatalf("GetTenant: %+v %v", got, err)
	}
	if _, err := c.GetTenant(t.Context(), "6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f"); !isfc.IsNotFound(err) {
		t.Fatalf("unknown tenant: %v", err)
	}
	info, err := c.ClientMetadata(t.Context(), consoleCli)
	if err != nil || info.Tenancy != "required" || len(info.Tenants) != 1 || info.Tenants[0] != hl {
		t.Fatalf("ClientMetadata (only ACTIVE tenants, like ISFC): %+v %v", info, err)
	}
}

func TestClientConfigurationHashesKeysOnRead(t *testing.T) {
	m, c, _ := setup(t)
	id, plain := m.IssueKey(hubClient, "billing-backend", "HUB:INVOKE", "HUB:REQUESTS_READ")
	revoked, _ := m.IssueKey(hubClient, "old-backend", "HUB:INVOKE")
	m.RevokeKey(revoked)

	cfg, err := c.ClientConfiguration(t.Context(), hubClient)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Keys) != 1 {
		t.Fatalf("a revoked key is not in the configuration: %+v", cfg.Keys)
	}
	k := cfg.Keys[0]
	if k.ID != id || k.ClientID != "billing-backend" || k.Status != "ACTIVE" || k.KeyHash != registry.HashKey(plain) ||
		strings.Join(k.Permissions, ",") != "HUB:INVOKE,HUB:REQUESTS_READ" {
		t.Fatalf("key %+v", k)
	}
	if s := fmt.Sprintf("%+v %#v", cfg, cfg); strings.Contains(s, plain) {
		t.Fatal("plaintext key reachable from the decoded configuration")
	}
}

func TestOwnKeyIsFetchedOnceAndRefreshedOnceOn401(t *testing.T) {
	m, c, _ := setup(t)
	for range 3 {
		if _, err := c.ListTenants(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.Calls("GET /api/v1/client/api-key"); n != 1 {
		t.Fatalf("key fetched %d times for 3 calls, want 1 (cached)", n)
	}
	m.RotateAuthKeys() // the cached key now gets 401
	m.IssueKey("auth-service", hubClient, "TENANT:READ")
	if _, err := c.ListTenants(t.Context()); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if n := m.Calls("GET /api/v1/client/api-key"); n != 2 {
		t.Fatalf("key fetched %d times, want 2 (one refresh)", n)
	}
	if n := m.Calls("GET /api/v1/tenant"); n != 5 {
		t.Fatalf("tenant calls %d, want 5 (3 + the 401 + its retry)", n)
	}
}

func TestRefreshHappensOnlyOnceWhenNoNewKeyExists(t *testing.T) {
	m, c, _ := setup(t)
	if _, err := c.ListTenants(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.RotateAuthKeys() // no replacement key: the key fetch itself now fails with 404
	if _, err := c.ListTenants(t.Context()); err == nil {
		t.Fatal("want an error")
	}
	if n := m.Calls("GET /api/v1/client/api-key"); n != 2 {
		t.Fatalf("key fetches %d, want 2", n)
	}
}

func TestForbiddenAlsoTriggersOneRefresh(t *testing.T) {
	var tenantCalls, keyCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/client/api-key":
			keyCalls++
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"key":"k%d"}}`, keyCalls)
		case "/api/v1/tenant":
			tenantCalls++
			if r.Header.Get("x-isfc-api-key") != "k2" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error_code":"AM_FB_01","error":"forbidden"}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
		}
	}))
	defer srv.Close()
	c, _ := isfc.New(isfc.Config{BaseURL: srv.URL, ClientID: hubClient, ClientSecret: hubSecret})
	if _, err := c.ListTenants(t.Context()); err != nil || tenantCalls != 2 || keyCalls != 2 {
		t.Fatalf("err %v tenant calls %d key calls %d", err, tenantCalls, keyCalls)
	}
}

func TestExpiredKeyIsFetchedAgain(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	keyCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/client/api-key" {
			keyCalls++
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"key":"k","expires_at":%q}}`, now.Add(time.Hour).Format(time.RFC3339))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	}))
	defer srv.Close()
	c, _ := isfc.New(isfc.Config{BaseURL: srv.URL, ClientID: hubClient, ClientSecret: hubSecret, Now: func() time.Time { return now }})
	_, _ = c.ListTenants(t.Context())
	_, _ = c.ListTenants(t.Context())
	now = now.Add(2 * time.Hour)
	_, _ = c.ListTenants(t.Context())
	if keyCalls != 2 {
		t.Fatalf("key fetched %d times, want 2 (cached, then expired)", keyCalls)
	}
}

func TestISFCDownAndTimeoutsAreErrors(t *testing.T) {
	m, c, _ := setup(t)
	m.SetDown(true)
	if _, err := c.ListTenants(t.Context()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("down: %v", err)
	}
	m.SetDown(false)

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	sc, _ := isfc.New(isfc.Config{BaseURL: slow.URL, ClientID: hubClient, ClientSecret: hubSecret, Timeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := sc.ListTenants(t.Context()); err == nil || time.Since(start) > time.Second {
		t.Fatalf("timeout: err %v after %s", err, time.Since(start))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := sc.ListTenants(ctx); err == nil {
		t.Fatal("context deadline ignored")
	}
}

func TestBadCredentialsAndNoSecretInErrorsOrLogs(t *testing.T) {
	m := mockisfc.New(mockisfc.Options{})
	m.Seed(hubClient, hubSecret, consoleCli)
	ts := httptest.NewServer(m.Handler())
	defer ts.Close()
	c, _ := isfc.New(isfc.Config{BaseURL: ts.URL, ClientID: hubClient, ClientSecret: "wrong-secret-xyz"})
	_, err := c.ListTenants(t.Context())
	if err == nil || strings.Contains(err.Error(), "wrong-secret-xyz") {
		t.Fatalf("err %v", err)
	}
	var logs bytes.Buffer
	secret := isfc.Secret("super-secret-value")
	slog.New(slog.NewTextHandler(&logs, nil)).InfoContext(t.Context(), "cfg", slog.Any("secret", secret), slog.String("cfg", fmt.Sprintf("%v %+v %#v", secret, secret, secret)))
	if strings.Contains(logs.String(), "super-secret-value") {
		t.Fatalf("secret logged: %s", logs.String())
	}
}

func TestNewValidatesConfig(t *testing.T) {
	for _, cfg := range []isfc.Config{{}, {BaseURL: "ftp://x", ClientID: "a", ClientSecret: "b"}, {BaseURL: "http://x"}, {BaseURL: "http://x", ClientID: "a"}} {
		if _, err := isfc.New(cfg); err == nil {
			t.Errorf("accepted %+v", cfg)
		}
	}
}

func TestMockTokenAndJWKS(t *testing.T) {
	_, _, ts := setup(t)
	treq, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/oauth2/token",
		strings.NewReader(`{"client_id":"`+consoleCli+`","client_secret":"console-secret","grant_type":"client_credentials"}`))
	treq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(treq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("token status %d", resp.StatusCode)
	}
	jreq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/.well-known/jwks.json", nil)
	jr, err := http.DefaultClient.Do(jreq)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jr.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(jr.Body)
	if !strings.Contains(buf.String(), `"RS256"`) || !strings.Contains(buf.String(), `"kid":"mockisfc-1"`) {
		t.Fatalf("jwks %s", buf.String())
	}
}
