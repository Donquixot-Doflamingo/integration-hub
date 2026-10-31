package hubclient_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		baseURL string
		key     string
		wantErr string
	}{
		{name: "https", baseURL: "https://hub.prod.internal", key: "k"},
		{name: "trailing slash", baseURL: "http://localhost:8080/", key: "k"},
		{name: "path prefix", baseURL: "https://gw.internal/hub", key: "k"},
		{name: "empty key", baseURL: "https://hub.prod.internal", key: " ", wantErr: "API key is empty"},
		{name: "relative URL", baseURL: "/v1", key: "k", wantErr: "absolute http or https"},
		{name: "other scheme", baseURL: "ftp://hub", key: "k", wantErr: "absolute http or https"},
		{name: "query", baseURL: "https://hub/?x=1", key: "k", wantErr: "query"},
		{name: "user info", baseURL: "https://user:pw@hub", key: "k", wantErr: "user info"},
		{name: "unparsable", baseURL: "http://[::1", key: "k", wantErr: "parse base URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := hubclient.New(tt.baseURL, tt.key)
			if tt.wantErr == "" {
				if err != nil || c == nil {
					t.Fatalf("New: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestClient_PathPrefixAndUserAgent(t *testing.T) {
	t.Parallel()
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUA = r.URL.EscapedPath(), r.Header.Get("User-Agent")
		w.Header().Set("X-Hub-Request-Id", testRequestID)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, srv.URL+"/hub/", false, hubclient.WithUserAgent("orders-service/2.3.1"))
	if _, err := c.Invoke(t.Context(), "int_evt_brandA", trackEvent(), hubclient.WithOperation("verify")); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/hub/v1/integrations/int_evt_brandA/invoke/verify" {
		t.Errorf("path = %q", gotPath)
	}
	if want := "hubclient-go/" + hubclient.Version + " orders-service/2.3.1"; gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

func TestClient_ContextCancellation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctx  func(t *testing.T, received <-chan struct{}) context.Context
		want error
	}{
		{
			name: "cancelled while waiting for the Hub",
			ctx: func(t *testing.T, received <-chan struct{}) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				go func() { <-received; cancel() }()
				return ctx
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			ctx: func(t *testing.T, _ <-chan struct{}) context.Context {
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				t.Cleanup(cancel)
				return ctx
			},
			want: context.DeadlineExceeded,
		},
		{
			name: "already cancelled",
			ctx: func(t *testing.T, _ <-chan struct{}) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx
			},
			want: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			received := make(chan struct{}, 1)
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			t.Cleanup(func() { close(release); srv.Close() })
			c := newClient(t, srv.URL, false)
			out, err := c.Invoke(tt.ctx(t, received), "int_evt_brandA", trackEvent())
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want errors.Is %v", err, tt.want)
			}
			if out != nil || hubclient.IsRetryable(err) {
				t.Errorf("out = %v, retryable = %v; want nil, false", out, hubclient.IsRetryable(err))
			}
		})
	}
}

// TestClient_KeyNeverLeaks fails if the API key shows up in any error or formatted value.
func TestClient_KeyNeverLeaks(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name string
		rp   *reply // nil: use the closed server (transport error)
		call func(c *hubclient.Client) error
	}{
		{name: "401 envelope", rp: &reply{status: 401, body: componentExample(t, "Unauthorized", "")}},
		{name: "proxy 502 HTML", rp: &reply{status: 502, body: []byte("<html>" + testKey + "?</html>"), header: http.Header{"Content-Type": {"text/html"}}}},
		{name: "malformed 200", rp: &reply{status: 200, body: []byte("{")}, call: func(c *hubclient.Client) error {
			_, err := c.GetIntegration(context.Background(), "int_evt_brandA")
			return err
		}},
		{name: "malformed 202", rp: &reply{status: 202, body: []byte("[]")}},
		{name: "transport error"},
		{name: "client-side validation", rp: &reply{status: 200, body: []byte("{}")}, call: func(c *hubclient.Client) error {
			_, err := c.Invoke(context.Background(), "int_evt_brandA", trackEvent(), hubclient.WithIdempotencyKey("bad key"))
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var c *hubclient.Client
			if tt.rp == nil {
				c = newClient(t, closedURL, false)
			} else {
				c = newHub(t, false, *tt.rp).client
			}
			call := tt.call
			if call == nil {
				call = func(c *hubclient.Client) error {
					_, err := c.Invoke(context.Background(), "int_evt_brandA", trackEvent())
					return err
				}
			}
			err := call(c)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, s := range []string{err.Error(), fmt.Sprintf("%v %+v %#v", err, err, err)} {
				if strings.Contains(s, testKey) {
					t.Errorf("API key leaked: %s", s)
				}
			}
		})
	}

	t.Run("formatted client", func(t *testing.T) {
		t.Parallel()
		c := newClient(t, "https://hub.prod.internal", false)
		var logs bytes.Buffer
		slog.New(slog.NewJSONHandler(&logs, nil)).InfoContext(t.Context(), "client", slog.Any("hub", c))
		for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c), c.String(), logs.String()} {
			if strings.Contains(s, testKey) {
				t.Errorf("API key leaked: %s", s)
			}
			if !strings.Contains(s, "redacted") {
				t.Errorf("formatted client does not say the key is redacted: %s", s)
			}
		}
	})
}

func TestNewIdempotencyKey(t *testing.T) {
	t.Parallel()
	shape := regexp.MustCompile(`^idem_[A-Za-z0-9_-]{22}$`)
	seenKeys := map[string]bool{}
	for range 1000 {
		k := hubclient.NewIdempotencyKey()
		if !shape.MatchString(k) {
			t.Fatalf("key %q is not idem_ + 22 base64url characters", k)
		}
		if seenKeys[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seenKeys[k] = true
	}

	t.Run("accepted by the contract", func(t *testing.T) {
		t.Parallel()
		hub := newHub(t, true, reply{status: 200, body: componentExample(t, "InvokeOk", "events.track@v1")})
		k := hubclient.NewIdempotencyKey()
		if _, err := hub.client.Invoke(t.Context(), "int_evt_brandA", trackEvent(), hubclient.WithIdempotencyKey(k)); err != nil {
			t.Fatal(err)
		}
		if got := hub.last(t).Header.Get("Idempotency-Key"); got != k {
			t.Errorf("Idempotency-Key = %q, want %q", got, k)
		}
	})
}
