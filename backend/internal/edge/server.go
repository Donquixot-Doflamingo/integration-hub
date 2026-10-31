package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

type requestIDKey struct{}

// RequestID returns the Hub request id minted for this request ("" outside the edge).
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID mints a req_ ULID for every request, sets X-Hub-Request-Id on every response
// and adds the id to the logging context (DESIGN §4.3).
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := core.NewRequestID()
		w.Header().Set("X-Hub-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		ctx = telemetry.WithRequestFields(ctx, telemetry.RequestFields{RequestID: id})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRecover turns a panic into a 500 INTERNAL_ERROR envelope. The log carries the panic value
// and stack but never the request body.
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { //nolint:contextcheck // uses the request's own context via r
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared as panic value, per net/http
					panic(v)
				}
				log.ErrorContext(r.Context(), "handler panic",
					slog.String("panic", fmt.Sprint(v)), slog.String("stack", string(debug.Stack())),
					slog.String("route", r.Pattern))
				WriteError(w, r, &core.CanonicalError{Code: core.CodeInternalError, Message: "Internal error", Retryable: true})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// WriteError writes the canonical error envelope (DESIGN §4.3) with the status of its code. It
// fills request_id from the context and mirrors retry_after_ms into Retry-After (seconds, rounded
// up), as the contract declares for 429 and 503.
func WriteError(w http.ResponseWriter, r *http.Request, ce *core.CanonicalError) {
	if ce.RequestID == "" {
		ce.RequestID = RequestID(r.Context())
	}
	status := ce.Code.HTTPStatus()
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if ce.RetryAfterMS != nil && (status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable) {
		w.Header().Set("Retry-After", strconv.FormatInt((*ce.RetryAfterMS+999)/1000, 10))
	}
	writeJSON(w, status, core.ErrorEnvelope{Error: ce})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
