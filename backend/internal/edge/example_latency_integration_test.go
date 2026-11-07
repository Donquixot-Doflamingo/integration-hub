//go:build integration

package edge

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// HUB-353: int_latency_echo works through the gateway against mockvendor programmed with
// examples/latency-demo/mockvendor/echo.json (the scenario run.sh uses with LATENCY_TARGET=mock),
// with auth scheme none (no credential loaded or sent), and the echoed request headers never reach
// the caller. The public region is redirected to mockvendor: tests never call the internet.
func TestExampleLatencyEcho(t *testing.T) {
	mv := mockvendor.Start(t)
	snap := overlaySnapshot(t, latencyExampleDir)
	for region := range snap.Vendors["postman-echo"].Regions {
		snap.Vendors["postman-echo"].Regions[region] = mv.VendorURL
	}
	var scenario kycScenario
	raw, err := os.ReadFile(latencyExampleDir + "/mockvendor/echo.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	route := scenario.Routes["echo"]
	route.Behaviour.Latency = nil // the demo's fixed 50 ms is for run.sh, not for CI
	route.Behaviour.Body = json.RawMessage(`{"args": {}, "headers": {"host": "postman-echo.com"}, "json": {"message": "hello", "seq": 7}, "url": "https://postman-echo.com/post"}`)
	mv.SetRoute(t, "echo", route)

	store := &registry.Store{}
	store.Swap(snap)
	m := telemetry.NewMetrics("gateway", "latency-example")
	cache, _ := creds.NewCache(forbiddenCreds{t}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	ids := slices.DeleteFunc(creds.AccountIDs(snap), func(id string) bool { return snap.Accounts[id].Vendor != "postman-echo" })
	if len(ids) != 0 {
		t.Fatalf("auth none accounts need no credentials, got %v", ids)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	h := New(Deps{Snapshots: store, Creds: cache, Upstream: client, Metrics: m, Log: slog.New(slog.DiscardHandler), Audit: &recordingSink{}}).Handler()

	spec := contract.Load(t, "hub-public.v1.yaml")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_latency_echo/invoke", strings.NewReader(`{"message": "hello", "seq": 7}`))
	req.Header.Set("Authorization", "Bearer hk_test_example_latency_demo_local_only")
	req.Header.Set("Content-Type", "application/json")
	rec := spec.Serve(t, h, req)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"message":"hello","seq":7,"status":"echoed"}` {
		t.Fatalf("echo: %d %s", rec.Code, rec.Body)
	}
	for _, leak := range []string{"postman", "echo-public"} {
		if strings.Contains(strings.ToLower(rec.Body.String()), leak) {
			t.Fatalf("response leaks %q: %s", leak, rec.Body)
		}
	}
	calls := mv.Requests(t, nil)
	if len(calls) != 1 || calls[0].Path != "/post" || http.Header(calls[0].Headers).Get("Authorization") != "" {
		t.Fatalf("vendor calls %+v", calls)
	}
}

// forbiddenCreds is a credential source that must never be asked: auth none accounts have no credentials.
type forbiddenCreds struct{ t testing.TB }

func (s forbiddenCreds) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	s.t.Errorf("credentials loaded for %v", ids)
	return map[string]creds.AccountSecrets{}, nil
}
