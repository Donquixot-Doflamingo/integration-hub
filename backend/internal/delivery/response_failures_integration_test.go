//go:build integration

package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// TestWorkerRedirectIsAFailedAttempt (HUB-449): the upstream client never follows redirects, so
// a 3xx is a vendor answer the Hub cannot use. With or without a response transformer it is a
// non-retryable UPSTREAM_REJECTED: the request dead-letters after one vendor call instead of
// succeeding with an empty or garbage result.
func TestWorkerRedirectIsAFailedAttempt(t *testing.T) {
	const location = "https://moved.example.net/v2/upload"
	for _, c := range []struct {
		name        string
		passThrough bool
		status      int
	}{
		{"pass-through 301", true, 301},
		{"pass-through 302", true, 302},
		{"pass-through 307", true, 307},
		{"pass-through 308", true, 308},
		{"transformer 302", false, 302},
		{"transformer 307", false, 307},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			if c.passThrough {
				r.snaps.s.Store(passThroughSnap(r.snaps.Snapshot()))
			}
			r.route(mockvendor.Behaviour{Status: c.status, Headers: map[string]string{"Location": location}, Body: json.RawMessage(`{"moved":"` + location + `"}`)})
			env := r.enqueue("int_evt_brandA", 6*time.Hour)
			if r.process() != 1 {
				t.Fatal("not claimed")
			}
			if s, n := r.status(env.RequestID); s != "dead_lettered" || n != 1 {
				t.Fatalf("status %s attempts %d, want dead_lettered after 1", s, n)
			}
			if reqs := r.mv.Requests(t, nil); len(reqs) != 1 {
				t.Fatalf("vendor called %d times, want once (a redirect is not retried)", len(reqs))
			}
			var reason, lastError string
			if err := r.db.QueryRow(t.Context(), `SELECT reason, coalesce(last_error, '') FROM hub.dead_letters WHERE request_id = $1`, env.RequestID).Scan(&reason, &lastError); err != nil {
				t.Fatal(err)
			}
			if reason != "UPSTREAM_REJECTED" || strings.Contains(lastError, "moved.example") {
				t.Fatalf("dead letter reason %q last_error %q", reason, lastError)
			}
		})
	}
}

// TestWorkerTruncatedResponseIsAFailedAttempt (HUB-449): a vendor body over the response cap is
// cut, and a cut body is not a result. With or without a response transformer the attempt fails
// as UPSTREAM_REJECTED, not retryable (the same call returns the same oversized body), and
// the request dead-letters after one vendor call.
func TestWorkerTruncatedResponseIsAFailedAttempt(t *testing.T) {
	const limit = 4 << 10                                      // the response cap of this test's client (the default 4 MiB is beyond the mock's route body size)
	huge := `{"items":"` + strings.Repeat("x", 3*limit) + `"}` // valid JSON when whole
	for _, c := range []struct {
		name        string
		passThrough bool
	}{{"pass-through", true}, {"transformer", false}} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			client, err := upstream.New(telemetry.NewMetrics("worker", "test"), upstream.Options{MaxResponseBytes: limit})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			r.w.Attempter.Upstream = client
			if c.passThrough {
				r.snaps.s.Store(passThroughSnap(r.snaps.Snapshot()))
			}
			r.route(mockvendor.Behaviour{Status: 200, BodyRaw: &huge, Headers: map[string]string{"Content-Type": "application/json"}})
			env := r.enqueue("int_evt_brandA", 6*time.Hour)
			if r.process() != 1 {
				t.Fatal("not claimed")
			}
			if s, n := r.status(env.RequestID); s != "dead_lettered" || n != 1 {
				t.Fatalf("status %s attempts %d, want dead_lettered after 1", s, n)
			}
			if reqs := r.mv.Requests(t, nil); len(reqs) != 1 {
				t.Fatalf("vendor called %d times, want once", len(reqs))
			}
			var reason string
			if err := r.db.QueryRow(t.Context(), `SELECT reason FROM hub.dead_letters WHERE request_id = $1`, env.RequestID).Scan(&reason); err != nil || reason != "UPSTREAM_REJECTED" {
				t.Fatalf("dead letter reason %q (%v), want UPSTREAM_REJECTED", reason, err)
			}
			var results int
			if err := r.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.request_results WHERE request_id = $1 AND result IS NOT NULL AND result <> 'null'::jsonb`, env.RequestID).Scan(&results); err != nil || results != 0 {
				t.Fatalf("a cut body was stored as a result (%d, %v)", results, err)
			}
		})
	}
}
