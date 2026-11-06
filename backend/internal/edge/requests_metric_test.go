package edge

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// TestRequestsCountedAtTheEdge (HUB-230): requests that end in the gateway are counted once in
// hub_requests_total with their final code; dry-runs and unauthorized calls are not counted.
func TestRequestsCountedAtTheEdge(t *testing.T) {
	m := telemetry.NewMetrics("gateway", "test")
	up := &fakeUpstream{steps: []step{ok200()}}
	h, _ := invokeServer(t, devSnapshot(t), up, func(d *Deps) { d.Metrics = m })

	if rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", `{"user": {}}`)
	post(t, h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody)
	post(t, h, "hk_wrong", "/v1/integrations/int_evt_brandA/invoke", trackBody)

	mode := string(devSnapshot(t).Integrations["int_evt_brandA"].Active().Delivery.Mode)
	// Counted before the lookups below, which would create series of their own.
	if got := testutil.CollectAndCount(m.Requests); got != 2 {
		t.Fatalf("series = %d, want 2 (dry-run and unauthorized calls are not counted)", got)
	}
	tests := []struct {
		name   string
		labels []string
		want   float64
	}{
		{"delivered", []string{"orders-service", "int_evt_brandA", "clevertap", "ct-brandA-in", "2", mode, "ok"}, 1},
		// Refused before resolve: no target was chosen, so vendor, account and version are empty.
		{"refused by schema", []string{"orders-service", "int_evt_brandA", "", "", "", mode, "VALIDATION_FAILED"}, 1},
	}
	for _, tt := range tests {
		if got := testutil.ToFloat64(m.Requests.WithLabelValues(tt.labels...)); got != tt.want {
			t.Errorf("%s: hub_requests_total%v = %v, want %v", tt.name, tt.labels, got, tt.want)
		}
	}
}
