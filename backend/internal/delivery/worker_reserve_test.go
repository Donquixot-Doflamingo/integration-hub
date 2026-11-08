package delivery

import (
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// TestClaimReservesSlotsUntilTheAttemptStarts is HUB-444: envelopes a claim took for a target
// count against its room until their attempt takes a slot, so the next claim (the loop claims
// again as soon as a worker slot frees) cannot take the same free slots a second time while the
// previous claim's goroutines are still starting. Before, rooms() counted only started attempts
// and a target could be over-claimed, its extra envelopes deferred and re-queued.
func TestClaimReservesSlotsUntilTheAttemptStarts(t *testing.T) {
	b := queue.Target{Account: "acct-b", Endpoint: "vendor.ep@1"}
	a := queue.Target{Account: "acct-a", Endpoint: "vendor.ep@1"}
	claim := func(tg queue.Target, n int) []queue.Claimed {
		out := make([]queue.Claimed, n)
		for i := range out {
			out[i].Target = &tg
		}
		return out
	}
	room := func(w *Worker, tg queue.Target) (int, bool) {
		for _, r := range w.rooms() {
			if r.Target == tg {
				return r.Slots, true
			}
		}
		return 0, false
	}
	w := &Worker{Concurrency: 16} // cap 4 per target

	first := claim(b, 4)
	w.reserve(first)
	if s, ok := room(w, b); !ok || s != 0 {
		t.Fatalf("after claiming 4 of b with no attempt started: room %d (listed %v), want 0", s, ok)
	}

	// Two attempts start: their reservations turn into in-flight slots; the room stays 0.
	for _, c := range first[:2] {
		un := w.unreserver(c)
		un()
		if !w.acquire(*c.Target) {
			t.Fatal("acquire refused under the cap")
		}
		un() // a second call gives nothing back twice
	}
	if s, _ := room(w, b); s != 0 {
		t.Fatalf("2 in flight + 2 reserved: room %d, want 0", s)
	}

	// The other two end without an attempt (e.g. breaker open): their slots come back.
	for _, c := range first[2:] {
		w.unreserver(c)()
	}
	if s, _ := room(w, b); s != 2 {
		t.Fatalf("2 in flight, none reserved: room %d, want 2", s)
	}
	w.release(b)
	w.release(b)
	if _, ok := room(w, b); ok {
		t.Fatal("b listed with nothing in flight or reserved")
	}

	// Reservations are per target, and envelopes without a target hint reserve nothing.
	w.reserve(append(claim(a, 1), queue.Claimed{}))
	if s, _ := room(w, a); s != 3 {
		t.Fatalf("a with 1 reserved: room %d, want 3", s)
	}
	if _, ok := room(w, b); ok {
		t.Fatal("reserving a must not touch b")
	}
}
