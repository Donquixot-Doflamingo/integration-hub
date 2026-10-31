package roles

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Check is one readiness dependency. It must return quickly; it runs with checkTimeout.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
	// Failing is the detail shown in /readyz when Fn fails; default "<name> unreachable". The
	// underlying error is only logged (it may carry host names).
	Failing string
}

const checkTimeout = time.Second

type health struct {
	Status string            `json:"status"`
	Role   Role              `json:"role,omitempty"`
	Checks map[string]string `json:"checks"`
}

// opsHandler serves the ops port (TECH_SPEC §2): /healthz (process alive, no dependencies),
// /readyz (dependencies reachable and the role not shutting down), /metrics.
// Shapes follow getHealthz / getReadyz in contracts/openapi/hub-public.v1.yaml.
func opsHandler(role Role, ready *atomic.Bool, checks []Check, m *telemetry.Metrics, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, health{Status: "ok", Role: role, Checks: map[string]string{}})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		results, failing := runChecks(r.Context(), checks, log)
		if !ready.Load() {
			failing = append(failing, core.ErrorDetail{Message: "role is starting or shutting down"})
		}
		if len(failing) > 0 {
			e := &core.CanonicalError{
				Code: core.CodeNotReady, Message: "Node is not ready", Retryable: true,
				RetryAfterMS: core.Ptr[int64](1000), Details: failing, RequestID: core.NewRequestID(),
			}
			w.Header().Set("X-Hub-Request-Id", e.RequestID)
			writeJSON(w, http.StatusServiceUnavailable, core.ErrorEnvelope{Error: e})
			return
		}
		writeJSON(w, http.StatusOK, health{Status: "ok", Role: role, Checks: results})
	})
	mux.Handle("GET /metrics", m.Handler())
	return mux
}

// runChecks runs every check concurrently. The response names only the failing check, never
// the underlying error (it may carry host names); the error is logged instead.
func runChecks(ctx context.Context, checks []Check, log *slog.Logger) (map[string]string, []core.ErrorDetail) {
	results := make(map[string]string, len(checks))
	errs := make([]error, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()
			errs[i] = c.Fn(cctx)
		})
	}
	wg.Wait()
	var failing []core.ErrorDetail
	for i, c := range checks {
		if errs[i] != nil {
			results[c.Name] = "failing"
			msg := c.Failing
			if msg == "" {
				msg = c.Name + " unreachable"
			}
			failing = append(failing, core.ErrorDetail{Message: msg})
			log.WarnContext(ctx, "readiness check failed", slog.String("check", c.Name), slog.String("error", errs[i].Error()))
			continue
		}
		results[c.Name] = "ok"
	}
	sort.Slice(failing, func(i, j int) bool { return failing[i].Message < failing[j].Message })
	return results, failing
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
