package transform

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

// payload4KB is an events.track@v1 body of about 4 KB (DESIGN §7.6.5, §18.6).
func payload4KB(tb testing.TB) map[string]any {
	tb.Helper()
	props := map[string]any{}
	for i := range 60 {
		props[fmt.Sprintf("property_%02d", i)] = fmt.Sprintf("value-%02d-%s", i, "abcdefghijklmnopqrstuvwxyz0123456789")
	}
	req := map[string]any{
		"user":    map[string]any{"id": "u_48213"},
		"event":   map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z", "properties": props},
		"context": map[string]any{"campaign": "diwali_2026"},
	}
	b, _ := json.Marshal(req)
	if n := len(b); n < 3800 || n > 4400 {
		tb.Fatalf("payload is %d bytes, want ~4 KB", n)
	}
	// Decode as the edge would, so numbers and maps have JSON types.
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// BenchmarkTransform4KB measures one request transform (T1) plus one response mapping (R1) and
// reports the p99 per operation. Budget: p99 ≤ 0.5 ms (HUB-070).
func BenchmarkTransform4KB(b *testing.B) {
	reqProg := compileDev(b, "T1/7.yaml")
	respProg := devResponse(b, "R1/2.yaml")
	in := Input{Req: payload4KB(b), Account: map[string]any{"config": map[string]any{"brand_code": "A"}},
		Creds: map[string]string{"account_id": "fixture-account-id", "passcode": "fixture-passcode"}, Now: fixedNow}
	resp := vendor(200, map[string]any{"status": "success", "processed": 1.0, "unprocessed": []any{}})
	durations := make([]time.Duration, 0, 1<<16)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		start := time.Now()
		if _, err := reqProg.BuildRequest(in); err != nil {
			b.Fatal(err)
		}
		if _, ce, err := respProg.MapResponse(resp, Input{}); err != nil || ce != nil {
			b.Fatal(ce, err)
		}
		durations = append(durations, time.Since(start))
	}
	b.StopTimer()
	slices.Sort(durations)
	p99 := durations[len(durations)*99/100]
	b.ReportMetric(float64(p99.Microseconds()), "p99-µs")
	if p99 > 500*time.Microsecond {
		b.Errorf("p99 %s exceeds the 0.5 ms budget", p99)
	}
}
