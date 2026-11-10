package callbacks

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// TestCompletedCarriesAnyJSONResult: a pass-through result is any JSON value (ADR-0023; the
// request.completed schema puts no type on data.result). An empty body (204) is null.
func TestCompletedCarriesAnyJSONResult(t *testing.T) {
	v := validator(t)
	for _, c := range []struct {
		name, result, want string
	}{
		{"object", `{"ok":true}`, `{"ok":true}`},
		{"array", `[1,{"a":2}]`, `[1,{"a":2}]`},
		{"string", `"plain text from the vendor"`, `"plain text from the vendor"`},
		{"number", `42`, `42`},
		{"null", `null`, `null`},
		{"empty (204)", ``, `null`},
		{"whitespace", "  \n", `null`},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev := baseEvent(&Completed{Result: json.RawMessage(c.result), Attempts: 1, CompletedAt: t0})
			body, err := ev.Marshal(t0)
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Validate(EventRequestCompleted, body); err != nil {
				t.Fatalf("schema: %v\n%s", err, body)
			}
			var got struct {
				Data struct {
					Result json.RawMessage `json:"result"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if string(bytes.TrimSpace(got.Data.Result)) != c.want {
				t.Fatalf("result %s, want %s", got.Data.Result, c.want)
			}
		})
	}
}

// TestEnqueueTerminalNeverFailsOnPayload: an event whose own payload is unusable is skipped,
// counted and logged without the payload, and does not fail the caller's transaction (HUB-449).
// The payload check runs before the transaction is touched, so a nil tx proves it.
func TestEnqueueTerminalNeverFailsOnPayload(t *testing.T) {
	var logs bytes.Buffer
	m := telemetry.NewMetrics("test", "dev")
	e, err := NewEnqueuer(staticSnapshots{testSnapshot("https://example.com/cb")}, m,
		EnqueueOptions{Validator: validator(t), Log: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "VENDOR-BODY-SECRET-xyz"
	ev := baseEvent(&Completed{Result: json.RawMessage(`{"leak":"` + secret + `"`), Attempts: 1, CompletedAt: t0}) // truncated JSON

	if _, err := e.Enqueue(t.Context(), nil, ev); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Enqueue = %v, want ErrInvalidPayload", err)
	}
	n, err := e.EnqueueTerminal(t.Context(), nil, ev)
	if n != 0 || err != nil {
		t.Fatalf("EnqueueTerminal = %d, %v; want 0, nil", n, err)
	}
	if got := testutil.ToFloat64(e.skipped.WithLabelValues("invalid_payload")); got != 1 {
		t.Fatalf("callback_enqueue_skipped_total{reason=invalid_payload} = %v, want 1", got)
	}
	if !strings.Contains(logs.String(), "invalid_payload") || strings.Contains(logs.String(), secret) {
		t.Fatalf("log must name the reason and never the payload:\n%s", logs.String())
	}
}
