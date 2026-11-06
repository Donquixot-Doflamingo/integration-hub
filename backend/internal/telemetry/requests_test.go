package telemetry

import (
	"fmt"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCountRequest(t *testing.T) {
	tests := []struct {
		name   string
		labels RequestLabels
		want   []string
	}{
		{"success", RequestLabels{Tenant: "orders", Integration: "int_a", Vendor: "clevertap", Account: "ct-a", Version: 3, Mode: "sync"},
			[]string{"orders", "int_a", "clevertap", "ct-a", "3", "sync", "ok"}},
		{"error before any target", RequestLabels{Tenant: "orders", Integration: "int_a", Mode: "wait", Code: "VALIDATION_FAILED"},
			[]string{"orders", "int_a", "", "", "", "wait", "VALIDATION_FAILED"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMetrics("test", "dev")
			m.CountRequest(tt.labels)
			m.CountRequest(tt.labels)
			if got := testutil.ToFloat64(m.Requests.WithLabelValues(tt.want...)); got != 2 {
				t.Fatalf("hub_requests_total%v = %v, want 2", tt.want, got)
			}
		})
	}
}

// TestRequestsOverflow proves the cardinality guard: past the cap, new combinations land in
// the __overflow__ series (mode and code kept), already-admitted ones keep their own labels,
// and every overflowed observation is counted.
func TestRequestsOverflow(t *testing.T) {
	m := NewMetrics("test", "dev")
	m.SetSeriesCapsForTest(3, 3, 3)
	for i := range 5 {
		m.CountRequest(RequestLabels{Tenant: "t", Integration: fmt.Sprintf("int_%d", i), Mode: "async", Code: "UPSTREAM_TIMEOUT"})
	}
	m.CountRequest(RequestLabels{Tenant: "t", Integration: "int_0", Mode: "async", Code: "UPSTREAM_TIMEOUT"})

	if got := testutil.ToFloat64(m.Requests.WithLabelValues("t", "int_0", "", "", "", "async", "UPSTREAM_TIMEOUT")); got != 2 {
		t.Errorf("admitted series = %v, want 2 (admitted combinations stay admitted)", got)
	}
	o := Overflow
	if got := testutil.ToFloat64(m.Requests.WithLabelValues(o, o, o, o, o, "async", "UPSTREAM_TIMEOUT")); got != 2 {
		t.Errorf("overflow series = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.SeriesOverflow.WithLabelValues("hub_requests_total")); got != 2 {
		t.Errorf("metric_series_overflow_total = %v, want 2", got)
	}
	if got := testutil.CollectAndCount(m.Requests); got != 4 {
		t.Errorf("series = %d, want 4 (3 admitted + 1 overflow)", got)
	}
}

func TestLabelGuardConcurrent(t *testing.T) {
	m := NewMetrics("test", "dev")
	g := NewLabelGuard("x", 100, m.SeriesOverflow)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := map[string]bool{}
	for w := range 8 {
		wg.Go(func() {
			for i := range 200 {
				k := fmt.Sprint(i)
				if g.Admit(k) {
					mu.Lock()
					admitted[k] = true
					mu.Unlock()
				}
				_ = w
			}
		})
	}
	wg.Wait()
	if len(admitted) != 100 {
		t.Fatalf("admitted %d combinations, want exactly the cap of 100", len(admitted))
	}
}

func TestRecordLLMUsage(t *testing.T) {
	m := NewMetrics("test", "dev")
	m.RecordLLMUsage(LLMUsage{Tenant: "support", Integration: "int_chat", Account: "openai-a", Model: "m1", InputTokens: 120, OutputTokens: 30, CostUSD: 0.004})
	if got := testutil.ToFloat64(m.LLMTokens.WithLabelValues("support", "int_chat", "openai-a", "m1", "input")); got != 120 {
		t.Errorf("input tokens = %v", got)
	}
	if got := testutil.ToFloat64(m.LLMTokens.WithLabelValues("support", "int_chat", "openai-a", "m1", "output")); got != 30 {
		t.Errorf("output tokens = %v", got)
	}
	if got := testutil.ToFloat64(m.LLMCost.WithLabelValues("support", "int_chat", "openai-a", "m1")); got != 0.004 {
		t.Errorf("cost = %v", got)
	}
}
