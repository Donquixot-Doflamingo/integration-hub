//go:build integration

package delivery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
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
