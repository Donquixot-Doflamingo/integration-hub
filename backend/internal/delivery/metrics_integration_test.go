//go:build integration

package delivery

import (
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

// requestSeries returns the hub_requests_total series of integration, keyed by code, with the
// labels of the last series seen per code.
func requestSeries(t *testing.T, m *telemetry.Metrics, integration string) map[string]*dto.Metric {
	t.Helper()
	fams, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.Metric{}
	for _, f := range fams {
		if f.GetName() != "hub_requests_total" {
			continue
		}
		for _, s := range f.GetMetric() {
			l := map[string]string{}
			for _, p := range s.GetLabel() {
				l[p.GetName()] = p.GetValue()
			}
			if l["integration"] == integration {
				out[l["code"]] = s
			}
		}
	}
	return out
}

func label(s *dto.Metric, name string) string {
	for _, p := range s.GetLabel() {
		if p.GetName() == name {
			return p.GetValue()
		}
	}
	return ""
}

// TestQueuedRequestsAreCountedOnce (HUB-230): a queued request is counted in hub_requests_total
// when it reaches a terminal state, once, with the served vendor and account and its final
// code; a scheduled retry counts nothing.
func TestQueuedRequestsAreCountedOnce(t *testing.T) {
	r := newRig(t)
	m := telemetry.NewMetrics("worker", "test")
	r.w.Transitions.Metrics = m

	r.route(mockvendor.Behaviour{Status: 503, Body: json.RawMessage(`{"status":"fail","error":"down"}`)})
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.process()
	if got := requestSeries(t, m, "int_evt_brandA"); len(got) != 0 {
		t.Fatalf("a retry_scheduled request was counted: %v", got)
	}
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	r.advance(2 * time.Second)
	r.process()
	if s, _ := r.status(env.RequestID); s != "succeeded" {
		t.Fatalf("status %s", s)
	}
	ok := requestSeries(t, m, "int_evt_brandA")["ok"]
	if ok == nil || ok.GetCounter().GetValue() != 1 {
		t.Fatalf("ok series = %v, want 1", ok)
	}
	if v, a, mode, ten := label(ok, "vendor"), label(ok, "account"), label(ok, "mode"), label(ok, "tenant"); v != "clevertap" || a != "ct-brandA-in" || mode != "async" || ten != "orders-service" {
		t.Fatalf("labels vendor=%q account=%q mode=%q tenant=%q", v, a, mode, ten)
	}
	if label(ok, "version") == "" {
		t.Fatal("version label empty for a delivered request")
	}

	r.route(mockvendor.Behaviour{Status: 400, Body: json.RawMessage(`{"status":"fail","error":"Invalid event name"}`)})
	r.enqueue("int_evt_brandA", 6*time.Hour)
	r.process()
	rej := requestSeries(t, m, "int_evt_brandA")["UPSTREAM_REJECTED"]
	if rej == nil || rej.GetCounter().GetValue() != 1 || label(rej, "account") != "ct-brandA-in" {
		t.Fatalf("UPSTREAM_REJECTED series = %v", rej)
	}
}

// TestQueueSampler (HUB-230): queue_depth, queue_oldest_age_s and dlq_count reflect the outbox
// and the dead letters; dead letters past the series cap fold into one __overflow__ series.
func TestQueueSampler(t *testing.T) {
	r := newRig(t)
	m := telemetry.NewMetrics("scheduler", "test")
	qs := &QueueSampler{Queue: r.w.Queue, Log: slog.New(slog.DiscardHandler), Now: r.now}
	qs.RegisterMetrics(m.Registry)

	qs.Sample(t.Context(), r.db)
	baseAccepted := testutil.ToFloat64(qs.depth.WithLabelValues("messaging", "accepted"))

	r.enqueue("int_evt_brandA", 6*time.Hour)
	r.enqueue("int_evt_brandB", 6*time.Hour)
	r.advance(90 * time.Second) // both have been due, unclaimed, for 90 s
	qs.Sample(t.Context(), r.db)
	if got := testutil.ToFloat64(qs.depth.WithLabelValues("messaging", "accepted")); got != baseAccepted+2 {
		t.Fatalf("queue_depth{messaging,accepted} = %v, want %v", got, baseAccepted+2)
	}
	if got := testutil.ToFloat64(qs.depth.WithLabelValues("llm", "in_flight")); got != 0 {
		t.Fatalf("empty pool/status must read 0, got %v", got)
	}
	if got := testutil.ToFloat64(qs.oldest.WithLabelValues("messaging")); got < 89 {
		t.Fatalf("queue_oldest_age_s{messaging} = %v, want ≥ 90", got)
	}

	// Two dead letters for different integrations.
	r.route(mockvendor.Behaviour{Status: 400, Body: json.RawMessage(`{"status":"fail","error":"Invalid event name"}`)})
	for r.process() > 0 {
	}
	qs.Sample(t.Context(), r.db)
	for _, in := range []string{"int_evt_brandA", "int_evt_brandB"} {
		if got := testutil.ToFloat64(qs.dlq.WithLabelValues("orders-service", in, accountOf(in), "UPSTREAM_REJECTED")); got < 1 {
			t.Fatalf("dlq_count for %s = %v, want ≥ 1", in, got)
		}
	}
	if got := testutil.ToFloat64(qs.depth.WithLabelValues("messaging", "accepted")); got != baseAccepted {
		t.Fatalf("queue_depth after dead-lettering = %v, want %v", got, baseAccepted)
	}

	qs.DLQCap = 1
	qs.Sample(t.Context(), r.db)
	o := telemetry.Overflow
	if got := testutil.ToFloat64(qs.dlq.WithLabelValues(o, o, o, o)); got < 1 {
		t.Fatalf("dlq_count overflow = %v, want ≥ 1 with a cap of 1", got)
	}
	if got := testutil.CollectAndCount(qs.dlq); got != 2 {
		t.Fatalf("dlq_count series = %d, want 2 (1 group + overflow)", got)
	}

	qs.Clear()
	if got := testutil.CollectAndCount(qs.depth); got != 0 {
		t.Fatalf("Clear left %d queue_depth series", got)
	}
}

func accountOf(integration string) string {
	if integration == "int_evt_brandB" {
		return "ct-brandB-us"
	}
	return "ct-brandA-in"
}
