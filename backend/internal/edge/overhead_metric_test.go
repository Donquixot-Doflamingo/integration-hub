package edge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// TestOverheadMetricExcludesVendorTime (HUB-405): with a vendor that takes 20 ms, every
// hub_overhead_ms observation is the Hub's own time: not negative, and well under the vendor
// time. Before the fix the clock was read when the defer statement ran (right after prepare),
// so each observation was "prepare time minus 20 ms" and negative.
func TestOverheadMetricExcludesVendorTime(t *testing.T) {
	const vendorMS = 20
	mv := mockvendor.Start(t)
	mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: vendorMS},
			Body: json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)}})
	snap := devSnapshot(t)
	cp := *snap
	cp.Vendors = map[string]*registry.Vendor{}
	for k, v := range snap.Vendors {
		vc := *v
		vc.Regions = map[string]string{}
		for r := range v.Regions {
			vc.Regions[r] = mv.VendorURL
		}
		cp.Vendors[k] = &vc
	}
	m := telemetry.NewMetrics("gateway", "test")
	cache, err := creds.NewCache(staticLoaderBench(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), creds.AccountIDs(&cp)); err != nil {
		t.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	writer := audit.NewWriter(nopStore{}, m, audit.WriterOptions{})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { writer.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	h := New(Deps{Snapshots: staticSnapshots{&cp}, Creds: cache, Upstream: client, Audit: writer,
		Masker: audit.NewMasker([]byte("test-mask-key-0123456789abcdef0123")), Metrics: m}).Handler()

	const calls = 5
	for range calls {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke",
			strings.NewReader(`{"user":{"id":"u_1"},"event":{"name":"order_placed","timestamp":"2026-10-31T09:15:00Z","properties":{"amount":499}}}`))
		req.Header.Set("Authorization", "Bearer "+devKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		if f.GetName() != "hub_overhead_ms" {
			continue
		}
		for _, metric := range f.GetMetric() {
			hist := metric.GetHistogram()
			if hist.GetSampleCount() != calls {
				t.Fatalf("hub_overhead_ms has %d samples, want %d", hist.GetSampleCount(), calls)
			}
			found = true
			mean := hist.GetSampleSum() / float64(calls)
			if mean < 0 || mean >= vendorMS {
				t.Fatalf("mean overhead %.3f ms, want in [0, %d) ms: the vendor's %d ms must be excluded, the Hub's own time included", mean, vendorMS, vendorMS)
			}
			t.Logf("hub_overhead_ms mean %.3f ms over %d calls (vendor %d ms)", mean, calls, vendorMS)
		}
	}
	if !found {
		t.Fatal("hub_overhead_ms not exported")
	}
}
