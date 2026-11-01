//go:build integration

package edge

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// TestShadow is HUB-173: int_evt_brandA serves from ct-brandA-in (region in) and copies to
// ct-brandB-us (region us, served under /us by the mock). The copy fails at the vendor, which
// the caller never sees; its events are a separate audit block. A dry_run shadow (the only kind
// allowed for side-effect endpoints) never reaches the vendor.
func TestShadow(t *testing.T) {
	for _, mode := range []registry.ShadowMode{registry.ShadowSend, registry.ShadowDryRun} {
		t.Run(string(mode), func(t *testing.T) {
			mv := mockvendor.Start(t)
			mv.Reset(t)
			mv.SetRoute(t, "primary", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
				Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)}})
			mv.SetRoute(t, "shadow", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/us/1/upload"},
				Behaviour: mockvendor.Behaviour{Status: 500, Body: json.RawMessage(`{"status":"fail","error":"shadow broke"}`)}})

			snap := e2eSnapshot(t, mv.VendorURL)
			ct := *snap.Vendors["clevertap"]
			ct.Regions = map[string]string{"in": mv.VendorURL, "us": mv.VendorURL + "/us"}
			snap.Vendors["clevertap"] = &ct
			in := *snap.Integrations["int_evt_brandA"]
			v := *in.Active()
			v.Delivery.Mode = registry.ModeSync
			v.Shadow = &registry.Shadow{Mode: mode, Target: registry.Target{Vendor: "clevertap", Account: "ct-brandB-us",
				Transformer: v.Targets[0].Transformer}}
			in.Versions = map[int]*registry.IntegrationVersion{in.ActiveVersion: &v}
			snap.Integrations = maps.Clone(snap.Integrations)
			snap.Integrations["int_evt_brandA"] = &in

			m := telemetry.NewMetrics("gateway", "shadow")
			cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
			if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
				t.Fatal(err)
			}
			client, _ := upstream.New(m, upstream.Options{})
			t.Cleanup(client.Close)
			sink := &recordingSink{}
			log := slog.New(slog.DiscardHandler)
			shadow := delivery.NewShadower(&delivery.Attempter{Creds: cache, Upstream: client, Log: log}, sink, log, m.Registry)
			h := New(Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Audit: sink, Metrics: m, Log: log, Shadow: shadow}).Handler()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(trackBody))
			req.Header.Set("Authorization", "Bearer "+devKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			shadow.Wait()

			if rec.Code != 200 || rec.Header().Get("X-Hub-Served-By") != "primary" || strings.Contains(rec.Body.String(), "shadow") {
				t.Fatalf("caller got %d %s %s", rec.Code, rec.Header().Get("X-Hub-Served-By"), rec.Body)
			}
			var shadowSent int
			for _, r := range mv.Requests(t, nil) {
				if strings.HasPrefix(r.Path, "/us/") {
					shadowSent++
				}
			}
			var primary, copy audit.Batch
			for _, b := range sink.batches {
				if len(b.Events) > 0 && b.Events[0].Seq > delivery.ShadowSeqBase {
					copy = b
				} else {
					primary = b
				}
			}
			if strings.Contains(types(primary), "shadow") || len(primary.Records) != 1 || primary.Records[0].Status != "succeeded" {
				t.Fatalf("primary batch %s %+v", types(primary), primary.Records)
			}
			if len(copy.Records) != 0 || len(copy.Attempts) != 0 {
				t.Fatal("a shadow must not write request or attempt rows")
			}
			switch mode {
			case registry.ShadowSend:
				if shadowSent != 1 || types(copy) != "integration.resolved,request.transformed,vendor.sent,vendor.responded,response.transformed" {
					t.Fatalf("send shadow: %d vendor calls, events %s", shadowSent, types(copy))
				}
			case registry.ShadowDryRun:
				if shadowSent != 0 || types(copy) != "integration.resolved,request.transformed" || copy.Events[1].Meta["dry_run"] != true {
					t.Fatalf("dry-run shadow: %d vendor calls, events %s", shadowSent, types(copy))
				}
			}
		})
	}
}
