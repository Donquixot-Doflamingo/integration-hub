package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// timedUpstream records the vendor time of the request in flight (benchmarks are sequential).
type timedUpstream struct {
	next   Upstream
	mu     sync.Mutex
	vendor time.Duration
}

func (u *timedUpstream) Attempt(ctx context.Context, t upstream.Target, req core.VendorRequest) (upstream.Attempt, error) {
	a, err := u.next.Attempt(ctx, t, req)
	u.mu.Lock()
	u.vendor += a.Latency
	u.mu.Unlock()
	return a, err
}

func (u *timedUpstream) take() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := u.vendor
	u.vendor = 0
	return v
}

type nopStore struct{}

func (nopStore) Write(context.Context, audit.Batch) error               { return nil }
func (nopStore) EnsurePartitions(context.Context, time.Time, int) error { return nil }
func (s staticLoaderBench) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: s}}
	}
	return out, nil
}

type staticLoaderBench map[string]string

// body4KB is an events.track body of about 4 KB (DESIGN §18.6 budget size).
func body4KB(b *testing.B) string {
	props := map[string]any{}
	for i := range 60 {
		props[fmt.Sprintf("property_%02d", i)] = fmt.Sprintf("value-%02d-abcdefghijklmnopqrstuvwxyz0123456789", i)
	}
	raw, _ := json.Marshal(map[string]any{"user": map[string]any{"id": "u_48213"},
		"event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": props}})
	if len(raw) < 3500 || len(raw) > 4600 {
		b.Fatalf("body is %d bytes", len(raw))
	}
	return string(raw)
}

// BenchmarkInvokeOverhead is DESIGN §19 M3 Accept: Hub overhead (handler time minus vendor
// time) ≤ 5 ms p99 with mockvendor answering at 0 ms, through the real credential cache,
// upstream client, audit writer and metrics. It also reports each stage against §18.6.
func BenchmarkInvokeOverhead(b *testing.B) {
	mv := mockvendor.Start(b)
	mv.SetRoute(b, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Body: json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)}})
	snap := devSnapshot(b)
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
	m := telemetry.NewMetrics("gateway", "bench")
	cache, err := creds.NewCache(staticLoaderBench(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err != nil {
		b.Fatal(err)
	}
	if err := cache.Warm(b.Context(), creds.AccountIDs(&cp)); err != nil {
		b.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()
	writer := audit.NewWriter(nopStore{}, m, audit.WriterOptions{})
	ctx, cancel := context.WithCancel(context.WithoutCancel(b.Context()))
	done := make(chan struct{})
	go func() { writer.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	up := &timedUpstream{next: client}
	h := New(Deps{Snapshots: staticSnapshots{&cp}, Creds: cache, Upstream: up, Audit: writer,
		Masker: audit.NewMasker([]byte("bench-mask-key-0123456789abcdef01")), Metrics: m}).Handler()
	body := body4KB(b)
	call := func() time.Duration {
		req := httptest.NewRequestWithContext(b.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+devKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		start := time.Now()
		h.ServeHTTP(rec, req)
		total := time.Since(start)
		if rec.Code != 200 {
			b.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return total - up.take()
	}
	for range 200 { // warm connections and caches; their stage samples stay in the histogram
		call()
	}

	overhead := make([]time.Duration, 0, 1<<16)
	b.ReportAllocs()
	for b.Loop() {
		overhead = append(overhead, call())
	}
	slices.Sort(overhead)
	p99 := overhead[len(overhead)*99/100]
	b.ReportMetric(float64(p99.Microseconds()), "overhead-p99-µs")
	b.ReportMetric(float64(overhead[len(overhead)/2].Microseconds()), "overhead-p50-µs")
	if p99 > 5*time.Millisecond {
		b.Errorf("overhead p99 %s over the 5 ms budget", p99)
	}
	reportStages(b, m)
}

// stageBudgetMS is DESIGN §18.6 per stage, mapped to the stage names the edge records.
var stageBudgetMS = map[telemetry.Stage]float64{
	telemetry.StageAuth: 0.1, telemetry.StageBodyLimit: 0.3, telemetry.StageValidate: 0.8, telemetry.StageResolve: 0.05,
	telemetry.StageTransform: 0.5, telemetry.StageResponseMap: 0.4, telemetry.StageAudit: 0.1,
}

// reportStages logs mean and the p99 histogram bucket of every stage against its budget.
func reportStages(b *testing.B, m *telemetry.Metrics) {
	families, err := m.Registry.Gather()
	if err != nil {
		b.Fatal(err)
	}
	var lines []string
	for _, f := range families {
		if f.GetName() != "hub_stage_duration_ms" {
			continue
		}
		for _, metric := range f.GetMetric() {
			stage := ""
			for _, l := range metric.GetLabel() {
				if l.GetName() == "stage" {
					stage = l.GetValue()
				}
			}
			hist := metric.GetHistogram()
			n := hist.GetSampleCount()
			if n == 0 {
				continue
			}
			p99 := math.Inf(1)
			for _, bk := range hist.GetBucket() {
				if float64(bk.GetCumulativeCount()) >= 0.99*float64(n) {
					p99 = bk.GetUpperBound()
					break
				}
			}
			budget := stageBudgetMS[telemetry.Stage(stage)]
			verdict := "within budget"
			if budget > 0 && p99 > budget {
				verdict = "OVER BUDGET"
			}
			if budget == 0 {
				verdict = "no §18.6 line"
			}
			lines = append(lines, fmt.Sprintf("%-13s mean %7.4f ms  p99 ≤ %5.3f ms  budget %5.3f ms  %s",
				stage, hist.GetSampleSum()/float64(n), p99, budget, verdict))
		}
	}
	slices.Sort(lines)
	b.Logf("per-stage durations (%d requests):\n%s", b.N, strings.Join(lines, "\n"))
}
