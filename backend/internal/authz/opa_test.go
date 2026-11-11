package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func sampleInput() Input {
	return Input{
		Sub:    map[string]any{"id": "u-1", "type": "USER", "roles": []any{"HUB_VIEWER"}, "permissions": []any{"HUB_VENDOR:READ"}},
		HTTP:   HTTP{Method: "GET", Path: "/admin/v1/vendors/v_1"},
		Client: Caller{ClientID: "integration-hub-console"},
		Tenant: &Tenant{ID: "t-platform"},
	}
}

// fakeOPA answers every decision request with handler and records the last body.
func fakeOPA(t *testing.T, handler func(w http.ResponseWriter, body []byte)) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var last atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		last.Store(string(b))
		if r.Method != http.MethodPost || r.URL.Path != "/v1/data/integrationhub/decision" {
			http.Error(w, "wrong endpoint", http.StatusNotFound)
			return
		}
		handler(w, b)
	}))
	t.Cleanup(srv.Close)
	return srv, &last
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name      string
		handler   func(w http.ResponseWriter, body []byte)
		wantAllow bool
		wantErr   bool
		label     string
	}{
		{"allow", func(w http.ResponseWriter, _ []byte) {
			_, _ = io.WriteString(w, `{"result":{"allow":true,"reason":"authorized by panel","meta":{"permissions":["HUB_VENDOR:READ"]}}}`)
		}, true, false, DecisionAllow},
		{"deny", func(w http.ResponseWriter, _ []byte) {
			_, _ = io.WriteString(w, `{"result":{"allow":false,"reason":"denied by panel","meta":{}}}`)
		}, false, false, DecisionDeny},
		{"OPA 500", func(w http.ResponseWriter, _ []byte) { http.Error(w, "boom", http.StatusInternalServerError) }, false, true, DecisionError},
		{"OPA 404", func(w http.ResponseWriter, _ []byte) { http.NotFound(w, nil) }, false, true, DecisionError},
		{"package not loaded: empty answer", func(w http.ResponseWriter, _ []byte) { _, _ = io.WriteString(w, `{}`) }, false, true, DecisionError},
		{"malformed JSON", func(w http.ResponseWriter, _ []byte) { _, _ = io.WriteString(w, `{"result":`) }, false, true, DecisionError},
		{"result of the wrong type", func(w http.ResponseWriter, _ []byte) { _, _ = io.WriteString(w, `{"result":"allow"}`) }, false, true, DecisionError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeOPA(t, tt.handler)
			reg := prometheus.NewRegistry()
			c, err := New(Config{URL: srv.URL, Metrics: NewMetrics(reg)})
			if err != nil {
				t.Fatal(err)
			}
			d, err := c.Decide(t.Context(), sampleInput())
			if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrUnavailable)) {
				t.Fatalf("err %v, wantErr %v", err, tt.wantErr)
			}
			if d.Allow != tt.wantAllow {
				t.Fatalf("allow %v", d.Allow)
			}
			if n, _ := testutil.GatherAndCount(reg, "hub_admin_authz_duration_ms"); n == 0 {
				t.Fatal("no authz duration series")
			}
			if got := histCount(t, reg, tt.label); got != 1 {
				t.Fatalf("decision=%s count %d, want 1", tt.label, got)
			}
		})
	}
}

func histCount(t *testing.T, reg *prometheus.Registry, label string) uint64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "hub_admin_authz_duration_ms" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "decision" && l.GetValue() == label {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

func TestDecideTimeoutIsUnavailable(t *testing.T) {
	release := make(chan struct{})
	srv, _ := fakeOPA(t, func(w http.ResponseWriter, _ []byte) { <-release })
	t.Cleanup(func() { close(release) })
	reg := prometheus.NewRegistry()
	c, err := New(Config{URL: srv.URL, Timeout: 30 * time.Millisecond, Metrics: NewMetrics(reg)})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = c.Decide(t.Context(), sampleInput())
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if histCount(t, reg, DecisionError) != 1 {
		t.Fatal("a timeout is an error decision")
	}
}

func TestDecideConnectionRefusedIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c, err := New(Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decide(t.Context(), sampleInput()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}

// TestInputIsSidecarCompatible pins the JSON the ISFC sidecar sends, because the Rego is written
// and tested against it: {"input": {sub, http{method,path,queryParams,metadata}, client, tenant}}.
func TestInputIsSidecarCompatible(t *testing.T) {
	srv, last := fakeOPA(t, func(w http.ResponseWriter, _ []byte) { _, _ = io.WriteString(w, `{"result":{"allow":true}}`) })
	c, _ := New(Config{URL: srv.URL})
	in := sampleInput()
	in.Client.APIKey = "isfck_secret_value"
	if _, err := c.Decide(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(last.Load().(string)), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"sub", "http", "client", "tenant"} {
		if _, ok := body.Input[k]; !ok {
			t.Errorf("input has no %q: %v", k, body.Input)
		}
	}
	h := body.Input["http"].(map[string]any)
	if h["method"] != "GET" || h["path"] != "/admin/v1/vendors/v_1" || h["queryParams"] == nil || h["metadata"] == nil {
		t.Errorf("http %v", h)
	}
	cl := body.Input["client"].(map[string]any)
	if cl["clientId"] != "integration-hub-console" || cl["apiKey"] != "isfck_secret_value" {
		t.Errorf("client %v", cl)
	}
	if body.Input["tenant"].(map[string]any)["id"] != "t-platform" {
		t.Errorf("tenant %v", body.Input["tenant"])
	}
	// No tenant: the key is omitted, not null (the sidecar's omitempty).
	in.Tenant, in.Client.APIKey = nil, ""
	if _, err := c.Decide(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	if s := last.Load().(string); strings.Contains(s, `"tenant"`) || strings.Contains(s, "apiKey") {
		t.Errorf("tenant and apiKey must be omitted when empty: %s", s)
	}
}

func TestAPIKeyNeverPrints(t *testing.T) {
	in := sampleInput()
	in.Client.APIKey = "isfck_secret_value"
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).InfoContext(t.Context(), "input", slog.Any("client", in.Client), slog.Any("key", in.Client.APIKey))
	for _, s := range []string{b.String(), fmt.Sprintf("%v %+v %#v %s", in.Client.APIKey, in.Client, in.Client.APIKey, in.Client.APIKey)} {
		if strings.Contains(s, "isfck_secret_value") {
			t.Fatalf("API key leaked: %s", s)
		}
	}
}

func TestDecideSpan(t *testing.T) {
	srv, _ := fakeOPA(t, func(w http.ResponseWriter, _ []byte) { _, _ = io.WriteString(w, `{"result":{"allow":false}}`) })
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	c, _ := New(Config{URL: srv.URL, Tracer: tp.Tracer("test")})
	if _, err := c.Decide(t.Context(), sampleInput()); err != nil {
		t.Fatal(err)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != SpanName {
		t.Fatalf("spans %v", spans)
	}
	found := false
	for _, a := range spans[0].Attributes() {
		if string(a.Key) == "authz.decision" && a.Value.AsString() == DecisionDeny {
			found = true
		}
	}
	if !found {
		t.Fatalf("attributes %v", spans[0].Attributes())
	}
}

func TestNewValidates(t *testing.T) {
	for _, bad := range []string{"", "localhost:8181", "ftp://x", "http://"} {
		if _, err := New(Config{URL: bad}); err == nil {
			t.Errorf("URL %q accepted", bad)
		}
	}
	if _, err := New(Config{URL: "http://localhost:8181", Package: "a/b"}); err == nil {
		t.Error("package with a slash accepted")
	}
	if c, err := New(Config{URL: "http://localhost:8181/"}); err != nil || c.endpoint != "http://localhost:8181/v1/data/integrationhub/decision" || c.timeout != DefaultTimeout {
		t.Errorf("%+v %v", c, err)
	}
}

func TestPackageOf(t *testing.T) {
	if got := PackageOf("integration-hub"); got != DefaultPackage {
		t.Fatalf("%q", got)
	}
	if got := PackageOf("client_ab-cd-ef"); got != "client_abcdef" {
		t.Fatalf("%q", got)
	}
}

func TestCheckDisabled(t *testing.T) {
	tests := []struct {
		disabled bool
		env      string
		wantErr  bool
	}{{false, "", false}, {false, "prod", false}, {true, "test", false}, {true, "", true}, {true, "prod", true}, {true, "dev", true}}
	for _, tt := range tests {
		if err := CheckDisabled(tt.disabled, tt.env); (err != nil) != tt.wantErr {
			t.Errorf("disabled=%v env=%q: %v", tt.disabled, tt.env, err)
		}
	}
	d, err := Disabled{}.Decide(context.Background(), Input{})
	if err != nil || !d.Allow {
		t.Fatalf("%+v %v", d, err)
	}
}
