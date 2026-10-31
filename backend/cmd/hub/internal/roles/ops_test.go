package roles

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func serveOps(t *testing.T, ready bool, checks []Check, path string) *httptest.ResponseRecorder {
	t.Helper()
	var r atomic.Bool
	r.Store(ready)
	h := opsHandler(Gateway, &r, checks, telemetry.NewMetrics("gateway", "test"), slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

var (
	okCheck   = func(context.Context) error { return nil }
	downCheck = func(context.Context) error { return errors.New("dial tcp 10.0.0.7:6379: connection refused") }
)

func TestHealthzIgnoresDependencies(t *testing.T) {
	rec := serveOps(t, false, []Check{{"postgres", downCheck}}, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 even when not ready", rec.Code)
	}
	var h health
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil || h.Status != "ok" || h.Role != Gateway {
		t.Fatalf("body %s (%v)", rec.Body, err)
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name        string
		ready       bool
		checks      []Check
		wantStatus  int
		wantDetails []string
	}{
		{"all ok", true, []Check{{"postgres", okCheck}, {"redis", okCheck}}, http.StatusOK, nil},
		{"redis down", true, []Check{{"postgres", okCheck}, {"redis", downCheck}}, http.StatusServiceUnavailable, []string{"redis unreachable"}},
		{"both down", true, []Check{{"postgres", downCheck}, {"redis", downCheck}}, http.StatusServiceUnavailable, []string{"postgres unreachable", "redis unreachable"}},
		{"shutting down", false, []Check{{"postgres", okCheck}}, http.StatusServiceUnavailable, []string{"role is starting or shutting down"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveOps(t, tt.ready, tt.checks, "/readyz")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus == http.StatusOK {
				var h health
				if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
					t.Fatal(err)
				}
				for _, c := range tt.checks {
					if h.Checks[c.Name] != "ok" {
						t.Errorf("check %s = %q", c.Name, h.Checks[c.Name])
					}
				}
				return
			}
			var env core.ErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			e := env.Error
			if e.Code != core.CodeNotReady || !e.Retryable || !core.RequestIDPattern.MatchString(e.RequestID) {
				t.Fatalf("bad envelope: %s", rec.Body)
			}
			if rec.Header().Get("X-Hub-Request-Id") != e.RequestID {
				t.Errorf("X-Hub-Request-Id %q != body request_id %q", rec.Header().Get("X-Hub-Request-Id"), e.RequestID)
			}
			var got []string
			for _, d := range e.Details {
				got = append(got, d.Message)
			}
			if len(got) != len(tt.wantDetails) {
				t.Fatalf("details %v, want %v", got, tt.wantDetails)
			}
			for i := range got {
				if got[i] != tt.wantDetails[i] {
					t.Errorf("detail %d = %q, want %q", i, got[i], tt.wantDetails[i])
				}
			}
			if strings.Contains(rec.Body.String(), "10.0.0.7") {
				t.Error("readiness response leaked the dependency address")
			}
		})
	}
}
