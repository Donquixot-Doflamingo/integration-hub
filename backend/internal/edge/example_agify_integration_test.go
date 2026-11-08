//go:build integration

package edge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// HUB-435: int_age_by_name through the gateway against mockvendor programmed with each scenario
// file of examples/agify-age/mockvendor (the ones run.sh loads). Every region is redirected to
// mockvendor: tests never call agify.io. The caller sends {firstName, lastName} and gets only the
// age; no credential is loaded or sent (auth none), and nothing of the vendor reaches the caller.
func TestExampleAgifyAge(t *testing.T) {
	mv := mockvendor.Start(t)
	snap := overlaySnapshot(t, agifyExampleDir)
	for region := range snap.Vendors["agify"].Regions {
		snap.Vendors["agify"].Regions[region] = mv.VendorURL
	}
	store := &registry.Store{}
	store.Swap(snap)
	m := telemetry.NewMetrics("gateway", "agify-example")
	cache, _ := creds.NewCache(forbiddenCreds{t}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	h := New(Deps{Snapshots: store, Creds: cache, Upstream: client, Metrics: m, Log: slog.New(slog.DiscardHandler), Audit: &recordingSink{}}).Handler()
	spec := contract.Load(t, "hub-public.v1.yaml")

	invoke := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_age_by_name/invoke", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer hk_test_example_age_demo_local_only")
		req.Header.Set("Content-Type", "application/json")
		return spec.Serve(t, h, req)
	}
	scenario := func(t *testing.T, name string) {
		t.Helper()
		raw, err := os.ReadFile(agifyExampleDir + "/mockvendor/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		var s kycScenario
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		mv.Reset(t)
		for id, route := range s.Routes {
			mv.SetRoute(t, id, route)
		}
	}
	noLeak := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		for _, leak := range []string{"agify", "agify-public", "count", "harsh"} {
			if strings.Contains(strings.ToLower(rec.Body.String()), leak) {
				t.Errorf("response leaks %q: %s", leak, rec.Body)
			}
		}
		for name := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(name), "x-rate-limit") {
				t.Errorf("vendor header %s reached the caller", name)
			}
		}
	}

	cases := []struct {
		scenario string
		status   int
		body     string // exact body, or the error code
	}{
		{"found", 200, `{"age":29}`},
		{"unknown-name", 200, `{}`},
		{"rate-limited", 429, "UPSTREAM_RATE_LIMITED"},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			scenario(t, c.scenario)
			rec := invoke(t, `{"firstName": "harsh", "lastName": "Khandelwal"}`)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if c.status == 200 && strings.TrimSpace(rec.Body.String()) != c.body {
				t.Fatalf("body %s, want %s", rec.Body, c.body)
			}
			if c.status != 200 {
				var e struct {
					Error struct {
						Code         string `json:"code"`
						Retryable    bool   `json:"retryable"`
						RetryAfterMS int    `json:"retry_after_ms"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error.Code != c.body || !e.Error.Retryable || e.Error.RetryAfterMS != 30000 {
					t.Fatalf("error %+v (%v): %s", e, err, rec.Body)
				}
			}
			noLeak(t, rec)
			calls := mv.Requests(t, nil)
			if len(calls) == 0 {
				t.Fatal("no vendor call")
			}
			last := calls[len(calls)-1]
			if last.Method != http.MethodGet || last.Path != "/" || len(last.Query["name"]) != 1 || last.Query["name"][0] != "harsh Khandelwal" {
				t.Fatalf("vendor request %+v", last)
			}
			if http.Header(last.Headers).Get("Authorization") != "" || len(last.Query) != 1 {
				t.Fatalf("auth none: no credential may be sent: %+v", last)
			}
		})
	}

	t.Run("caller mistakes are refused without a vendor call", func(t *testing.T) {
		scenario(t, "found")
		for name, body := range map[string]string{
			"no lastName":   `{"firstName": "harsh"}`,
			"empty name":    `{"firstName": "", "lastName": "Khandelwal"}`,
			"unknown field": `{"firstName": "harsh", "lastName": "Khandelwal", "age": 3}`,
		} {
			rec := invoke(t, body)
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), "VALIDATION_FAILED") {
				t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			}
		}
		if calls := mv.Requests(t, nil); len(calls) != 0 {
			t.Fatalf("vendor called for refused requests: %+v", calls)
		}
	})
}
