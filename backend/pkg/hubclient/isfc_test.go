package hubclient_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

const (
	isfcCallerID     = "orders-backend"
	isfcCallerSecret = "orders-secret-canary-7d41"
	isfcAuthRoute    = "GET /api/v1/client/api-key"
)

// isfcFixture is a fake auth-service (mockisfc) plus a fake Hub that records the credential
// headers it receives and accepts only one key.
type isfcFixture struct {
	auth   *mockisfc.Server
	authTS *httptest.Server
	hubTS  *httptest.Server

	mu       sync.Mutex
	keyID    string // the mock's id of the first key
	accepted string // the only key the Hub accepts; empty accepts none
	status   int    // answer for a rejected key
	seen     []http.Header
	hits     atomic.Int32
}

func newISFCFixture(t *testing.T) (*isfcFixture, string) {
	t.Helper()
	f := &isfcFixture{auth: mockisfc.New(mockisfc.Options{}), status: http.StatusUnauthorized}
	f.auth.Seed(hubclient.ISFCHubClientID, "hub-secret", "integration-hub-console")
	f.auth.AddClient(isfcCallerID, isfcCallerSecret, "backend_services")
	id, key := f.auth.IssueKey(hubclient.ISFCHubClientID, isfcCallerID, "HUB:INVOKE")
	f.accepted, f.keyID = key, id
	f.authTS = httptest.NewServer(f.auth.Handler())
	f.hubTS = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.seen = append(f.seen, r.Header.Clone())
		ok := r.Header.Get("x-isfc-api-key") == f.accepted && r.Header.Get("x-isfc-client-id") == isfcCallerID
		status := f.status
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"no","retryable":false,"request_id":"req_1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[],"next_cursor":null}`))
	}))
	t.Cleanup(func() { f.hubTS.Close(); f.authTS.Close() })
	return f, key
}

func (f *isfcFixture) setAccepted(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted = key
}

func (f *isfcFixture) lastHeader() http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

func list(t *testing.T, c *hubclient.Client) error {
	t.Helper()
	_, err := c.ListIntegrations(context.Background(), hubclient.ListOptions{})
	return err
}

func TestWithISFC_FetchesOnceAndSendsTheCredential(t *testing.T) {
	f, key := newISFCFixture(t)
	c, err := hubclient.New(f.hubTS.URL, "", hubclient.WithISFC(isfcCallerID, isfcCallerSecret, f.authTS.URL),
		hubclient.WithTenantID("3f1c2a4e-0000-4000-8000-000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := list(t, c); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.auth.Calls(isfcAuthRoute); n != 1 {
		t.Errorf("key fetches = %d, want 1 (the key is cached)", n)
	}
	h := f.lastHeader()
	if h.Get("x-isfc-client-id") != isfcCallerID || h.Get("x-isfc-api-key") != key {
		t.Errorf("credential headers = %q / %q", h.Get("x-isfc-client-id"), h.Get("x-isfc-api-key"))
	}
	if h.Get("Authorization") != "" {
		t.Error("an Authorization header was sent next to the ISFC credential (the Hub answers 400)")
	}
	if got := h.Get("X-Tenant-Id"); got != "3f1c2a4e-0000-4000-8000-000000000001" {
		t.Errorf("X-Tenant-Id = %q", got)
	}
}

func TestWithISFC_RefreshesOnceOn401And403(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, _ := newISFCFixture(t)
			f.mu.Lock()
			f.status = status
			f.mu.Unlock()
			c, err := hubclient.New(f.hubTS.URL, "", hubclient.WithISFC(isfcCallerID, isfcCallerSecret, f.authTS.URL))
			if err != nil {
				t.Fatal(err)
			}
			if err := list(t, c); err != nil {
				t.Fatal(err)
			}
			// ISFC rotates the key: the old one is revoked, a new one issued.
			_, newKey := f.auth.IssueKey(hubclient.ISFCHubClientID, isfcCallerID, "HUB:INVOKE")
			f.auth.RevokeKey(f.keyID)
			f.setAccepted(newKey)
			f.hits.Store(0)
			if err := list(t, c); err != nil {
				t.Fatalf("call after rotation: %v", err)
			}
			if got := f.hits.Load(); got != 2 {
				t.Errorf("Hub requests = %d, want 2 (rejected, then retried with the new key)", got)
			}
			if n := f.auth.Calls(isfcAuthRoute); n != 2 {
				t.Errorf("key fetches = %d, want 2", n)
			}
			if f.lastHeader().Get("x-isfc-api-key") != newKey {
				t.Error("the retry did not use the new key")
			}
		})
	}
}

func TestWithISFC_DoesNotRetryWhenTheKeyDidNotChange(t *testing.T) {
	f, _ := newISFCFixture(t)
	f.setAccepted("some-other-key") // the Hub rejects ISFC's key: refreshing returns the same key
	c, err := hubclient.New(f.hubTS.URL, "", hubclient.WithISFC(isfcCallerID, isfcCallerSecret, f.authTS.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = list(t, c)
	var he *hubclient.Error
	if !errors.As(err, &he) || he.Code != hubclient.CodeUnauthorized {
		t.Fatalf("err = %v, want the Hub's UNAUTHORIZED", err)
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("Hub requests = %d, want 1", got)
	}
	if n := f.auth.Calls(isfcAuthRoute); n != 2 {
		t.Errorf("key fetches = %d, want 2 (one refresh attempt)", n)
	}
}

func TestWithISFC_FetchFailureNeverLeaksTheSecret(t *testing.T) {
	f, _ := newISFCFixture(t)
	c, err := hubclient.New(f.hubTS.URL, "", hubclient.WithISFC(isfcCallerID, "wrong-secret-canary", f.authTS.URL))
	if err != nil {
		t.Fatal(err)
	}
	err = list(t, c)
	var ie *hubclient.ISFCError
	if !errors.As(err, &ie) || ie.Code != "AS_VE_09" || ie.Status != http.StatusBadRequest {
		t.Fatalf("err = %v, want an *ISFCError AS_VE_09", err)
	}
	if f.hits.Load() != 0 {
		t.Error("the Hub was called without a key")
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	c2, _ := hubclient.New(f.hubTS.URL, "", hubclient.WithISFC(isfcCallerID, "wrong-secret-canary", closedURL))
	err2 := list(t, c2)
	for _, s := range []string{err.Error(), fmt.Sprintf("%+v", err), err2.Error(), fmt.Sprintf("%#v", c), fmt.Sprint(c)} {
		if strings.Contains(s, "wrong-secret-canary") || strings.Contains(s, closedURL) {
			t.Errorf("leak in %q", s)
		}
	}
}

func TestWithISFC_NewValidation(t *testing.T) {
	tests := []struct {
		name    string
		apiKey  string
		opts    []hubclient.Option
		wantErr string
	}{
		{name: "both credentials", apiKey: testKey, opts: []hubclient.Option{hubclient.WithISFC("a", "b", "http://auth")}, wantErr: "not both"},
		{name: "no secret", opts: []hubclient.Option{hubclient.WithISFC("a", "", "http://auth")}, wantErr: "required"},
		{name: "bad auth url", opts: []hubclient.Option{hubclient.WithISFC("a", "b", "auth")}, wantErr: "absolute"},
		{name: "static key without id", opts: []hubclient.Option{hubclient.WithISFCKey("", "k")}, wantErr: "required"},
		{name: "ok", opts: []hubclient.Option{hubclient.WithISFC("a", "b", "http://auth")}},
		{name: "ok static", opts: []hubclient.Option{hubclient.WithISFCKey("a", "k")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := hubclient.New("https://hub.example", tt.apiKey, tt.opts...)
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestWithISFCKey_SendsTheKeyAsIsAndNeverRefreshes(t *testing.T) {
	f, _ := newISFCFixture(t)
	c, err := hubclient.New(f.hubTS.URL, "", hubclient.WithISFCKey(isfcCallerID, "static-key-canary-123"))
	if err != nil {
		t.Fatal(err)
	}
	if err := list(t, c); err == nil {
		t.Fatal("the Hub should reject the static key")
	}
	if f.hits.Load() != 1 || f.lastHeader().Get("x-isfc-api-key") != "static-key-canary-123" {
		t.Errorf("hits %d, header %q", f.hits.Load(), f.lastHeader().Get("x-isfc-api-key"))
	}
	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "client", slog.Any("hub", c))
	for _, s := range []string{c.String(), fmt.Sprintf("%#v", c), logs.String()} {
		if strings.Contains(s, "static-key-canary-123") {
			t.Errorf("key leaked: %s", s)
		}
	}
}
