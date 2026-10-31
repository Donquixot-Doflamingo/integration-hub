package telemetry

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status %d", rec.Code)
	}
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}

// TestExportedNamesArePinned fails if a library upgrade or refactor changes the names that
// dashboards and alerts depend on (go-observability skill).
func TestExportedNamesArePinned(t *testing.T) {
	m := NewMetrics("gateway", "test")
	tm := NewTimings(time.Now().Add(-3 * time.Millisecond))
	tm.Add(StageValidate, 400*time.Microsecond)
	tm.Add(StageUpstream, 2*time.Millisecond)
	m.RecordTimings(&tm, time.Now(), "int_evt_brandA", "sync")
	m.RedisOpDuration.WithLabelValues("idempotency_begin").Observe(0.0004)

	out := scrape(t, m)
	for _, want := range []string{
		"hub_overhead_ms_bucket{integration=\"int_evt_brandA\",mode=\"sync\",le=\"5\"}",
		"hub_stage_duration_ms_bucket{stage=\"validate\",le=\"0.5\"}",
		"hub_build_info{role=\"gateway\",version=\"test\"} 1",
		"redis_op_duration_seconds_bucket{op=\"idempotency_begin\",le=\"0.001\"}",
		"go_goroutines",
		"go_memstats_heap_inuse_bytes",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics is missing %s", want)
		}
	}
	if strings.Contains(out, `stage="upstream"`) {
		t.Error("vendor time must not be recorded as a Hub stage")
	}
}

func TestTimingsOverheadExcludesVendorTime(t *testing.T) {
	start := time.Unix(0, 0)
	tm := NewTimings(start)
	tm.Add(StageUpstream, 40*time.Millisecond)
	tm.Add(StageTransform, time.Millisecond)
	tm.Add(StageTransform, time.Millisecond)
	if got := tm.Overhead(start.Add(45 * time.Millisecond)); got != 5*time.Millisecond {
		t.Fatalf("Overhead = %s, want 5ms", got)
	}
	if got := tm.Stage(StageTransform); got != 2*time.Millisecond {
		t.Fatalf("transform = %s, want 2ms (accumulated)", got)
	}
}

func TestTimedMiddlewareRecordsOnlyOwnTime(t *testing.T) {
	m := NewMetrics("gateway", "test")
	passThrough := func(next http.Handler) http.Handler { return next }
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(30 * time.Millisecond)
		<-timer.C
		w.WriteHeader(http.StatusNoContent)
	})
	h := m.TimedMiddleware(StageAuth, passThrough)(slow)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if n := testutil.CollectAndCount(m.StageDuration, "hub_stage_duration_ms"); n != 1 {
		t.Fatalf("series = %d, want 1", n)
	}
	// The 30 ms handler is downstream, so the auth stage must land well under the 5 ms bucket.
	out := scrape(t, m)
	if !strings.Contains(out, `hub_stage_duration_ms_bucket{stage="auth",le="5"} 1`) {
		t.Fatalf("auth stage counted downstream time:\n%s", grep(out, "stage=\"auth\""))
	}
}

func grep(s, sub string) string {
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.Contains(l, sub) {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func BenchmarkRecordTimings(b *testing.B) {
	m := NewMetrics("gateway", "bench")
	b.ReportAllocs()
	for b.Loop() {
		tm := NewTimings(time.Now())
		tm.Add(StageAuth, 10*time.Microsecond)
		tm.Add(StageValidate, 300*time.Microsecond)
		tm.Add(StageTransform, 200*time.Microsecond)
		tm.Add(StageUpstream, time.Millisecond)
		m.RecordTimings(&tm, time.Now(), "int_evt_brandA", "sync")
	}
}
