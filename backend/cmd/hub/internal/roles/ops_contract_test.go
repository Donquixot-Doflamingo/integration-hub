package roles

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// TestOpsEndpointsMatchContract validates /healthz and /readyz (200 and 503) against
// getHealthz / getReadyz in hub-public.v1.yaml. /metrics is Prometheus text, not in the
// contract, and is excluded on purpose.
func TestOpsEndpointsMatchContract(t *testing.T) {
	pub := contract.Load(t, "hub-public.v1.yaml")
	for _, tt := range []struct {
		name   string
		ready  bool
		checks []Check
		path   string
		want   int
	}{
		{"healthz", true, nil, "/healthz", 200},
		{"readyz ok", true, []Check{{"postgres", okCheck}, {"redis", okCheck}}, "/readyz", 200},
		{"readyz failing", true, []Check{{"postgres", okCheck}, {"redis", downCheck}}, "/readyz", 503},
		{"readyz shutting down", false, nil, "/readyz", 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var ready atomic.Bool
			ready.Store(tt.ready)
			h := opsHandler(Gateway, &ready, tt.checks, telemetry.NewMetrics("gateway", "test"), slog.New(slog.DiscardHandler))
			rec := pub.Serve(t, h, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}
