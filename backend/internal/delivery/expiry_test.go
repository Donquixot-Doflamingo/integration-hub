package delivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// fakeDue lists, oldest first, the envelopes not yet expired, at most n (like DueExpiries).
type fakeDue struct {
	all     []core.Envelope
	done    map[string]bool
	listed  int
	tried   map[string]int
	stuckID func(string) bool
}

func newFakeDue(ids []string, stuck func(string) bool) *fakeDue {
	f := &fakeDue{done: map[string]bool{}, tried: map[string]int{}, stuckID: stuck}
	for _, id := range ids {
		f.all = append(f.all, core.Envelope{RequestID: id})
	}
	return f
}

func (f *fakeDue) due(_ context.Context, n int) ([]core.Envelope, error) {
	f.listed++
	var out []core.Envelope
	for _, e := range f.all {
		if !f.done[e.RequestID] && len(out) < n {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeDue) expire(_ context.Context, e core.Envelope) error {
	f.tried[e.RequestID]++
	if f.stuckID(e.RequestID) {
		return errors.New("audit: not pending")
	}
	f.done[e.RequestID] = true
	return nil
}

func TestExpirySweepStopsWhenNothingProgresses(t *testing.T) {
	var ids []string
	for i := range 250 {
		ids = append(ids, fmt.Sprintf("stuck-%03d", i))
	}
	f := newFakeDue(ids, func(string) bool { return true })
	now := time.Unix(1_000_000, 0)
	var tr expiryTracker
	done := make(chan struct{})
	go func() {
		defer close(done)
		tr.run(t.Context(), slog.New(slog.DiscardHandler), func() time.Time { return now }, f.due, f.expire)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep spins on rows that cannot be expired")
	}
	for id, n := range f.tried {
		if n != 1 {
			t.Fatalf("%s tried %d times in one sweep, want 1", id, n)
		}
	}
	// Still in backoff: the next sweep leaves them alone, and tries again once it has passed.
	tr.run(t.Context(), slog.New(slog.DiscardHandler), func() time.Time { return now }, f.due, f.expire)
	if f.tried["stuck-000"] != 1 {
		t.Fatalf("a stuck row was retried inside its backoff: %d", f.tried["stuck-000"])
	}
	tr.run(t.Context(), slog.New(slog.DiscardHandler), func() time.Time { return now.Add(time.Hour) }, f.due, f.expire)
	if f.tried["stuck-000"] != 2 {
		t.Fatalf("a stuck row was not retried after its backoff: %d", f.tried["stuck-000"])
	}
}

func TestExpirySweepStuckRowsDoNotBlockNewerOnes(t *testing.T) {
	var ids []string
	for i := range 120 {
		ids = append(ids, fmt.Sprintf("a-stuck-%03d", i))
	}
	ids = append(ids, "z-good-1", "z-good-2")
	f := newFakeDue(ids, func(id string) bool { return id[0] == 'a' })
	now := time.Unix(1_000_000, 0)
	var tr expiryTracker
	tr.run(t.Context(), slog.New(slog.DiscardHandler), func() time.Time { return now }, f.due, f.expire)
	tr.run(t.Context(), slog.New(slog.DiscardHandler), func() time.Time { return now }, f.due, f.expire)
	if !f.done["z-good-1"] || !f.done["z-good-2"] {
		t.Fatalf("newer expiries are blocked behind stuck rows: %v", f.done)
	}
}

func TestExpirySweepLostRowsCountAsProgress(t *testing.T) {
	var ids []string
	for i := range 150 {
		ids = append(ids, fmt.Sprintf("r-%03d", i))
	}
	f := newFakeDue(ids, func(string) bool { return false })
	var tr expiryTracker
	tr.run(t.Context(), slog.New(slog.DiscardHandler), time.Now, f.due, func(ctx context.Context, e core.Envelope) error {
		_ = f.expire(ctx, e)
		return queue.ErrLost // another actor won: the row is gone
	})
	if len(f.done) != 150 {
		t.Fatalf("expired %d of 150", len(f.done))
	}
}

func TestExpirySweepChecksContext(t *testing.T) {
	f := newFakeDue([]string{"a", "b", "c"}, func(string) bool { return false })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var tr expiryTracker
	tr.run(ctx, slog.New(slog.DiscardHandler), time.Now, f.due, f.expire)
	if len(f.tried) != 0 {
		t.Fatalf("expired after cancel: %v", f.tried)
	}
}
