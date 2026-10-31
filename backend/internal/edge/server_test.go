package edge

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

func TestEveryResponseCarriesRequestID(t *testing.T) {
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !core.RequestIDPattern.MatchString(RequestID(r.Context())) {
			t.Errorf("context request id %q", RequestID(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/x", nil))
	if !core.RequestIDPattern.MatchString(rec.Header().Get("X-Hub-Request-Id")) {
		t.Fatalf("X-Hub-Request-Id %q", rec.Header().Get("X-Hub-Request-Id"))
	}
}

func TestPanicBecomesEnvelopeWithoutBody(t *testing.T) {
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/integrations/{integration_id}/invoke", func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := withRequestID(withRecover(slog.New(slog.NewJSONHandler(&logs, nil)), mux))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(`{"secret_body":"s3cr3t-value"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := contract.Load(t, "hub-public.v1.yaml").Serve(t, h, req)
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), `"panic":"boom"`) || strings.Contains(logs.String(), "s3cr3t-value") {
		t.Fatalf("log %s", logs.String())
	}
}

// TestErrorEnvelopeMatchesContract writes every canonical code through WriteError and validates
// the response (status, headers, body) against invokeIntegration, which declares all of them.
func TestErrorEnvelopeMatchesContract(t *testing.T) {
	spec := contract.Load(t, "hub-public.v1.yaml")
	codes := []core.Code{
		core.CodeValidationFailed, core.CodeIdempotencyKeyRequired, core.CodeUnauthorized, core.CodeForbidden,
		core.CodeIntegrationNotFound, core.CodeOperationNotFound, core.CodeIdempotencyConflict, core.CodePayloadTooLarge,
		core.CodeTransformFailed, core.CodeInternalError, core.CodeUpstreamTimeout, core.CodeUpstreamUnavailable,
		core.CodeUpstreamRateLimited, core.CodeUpstreamRejected, core.CodeUpstreamAuthFailed, core.CodeIntegrationPaused,
	}
	for _, code := range codes {
		t.Run(string(code), func(t *testing.T) {
			h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ce := &core.CanonicalError{Code: code, Message: "x", Retryable: code == core.CodeUpstreamUnavailable || code == core.CodeUpstreamRateLimited}
				if code == core.CodeUpstreamRateLimited || code == core.CodeUpstreamUnavailable {
					ce.RetryAfterMS = core.Ptr[int64](1500)
				}
				if code == core.CodeUpstreamUnavailable {
					ce.Circuit, ce.Reason = core.Ptr("open"), core.Ptr("circuit_open")
				}
				WriteError(w, r, ce)
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer hk_test_x")
			rec := spec.Serve(t, h, req)
			if rec.Code != code.HTTPStatus() {
				t.Fatalf("status %d, want %d", rec.Code, code.HTTPStatus())
			}
			if (code == core.CodeUpstreamRateLimited || code == core.CodeUpstreamUnavailable) && rec.Header().Get("Retry-After") != "2" {
				t.Fatalf("Retry-After %q, want 2 (1500 ms rounded up)", rec.Header().Get("Retry-After"))
			}
		})
	}
}
