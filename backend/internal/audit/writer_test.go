package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// fakeStore records writes; block makes every Write wait until release is closed (a stalled DB).
type fakeStore struct {
	mu       sync.Mutex
	batches  []Batch
	parts    []time.Time
	block    chan struct{}
	writeErr error
}

func (f *fakeStore) Write(ctx context.Context, b Batch) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, b)
	return f.writeErr
}

func (f *fakeStore) EnsurePartitions(_ context.Context, day time.Time, days int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range days {
		f.parts = append(f.parts, day.AddDate(0, 0, i))
	}
	return nil
}

func (f *fakeStore) rows() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += b.len()
	}
	return n
}

func events(n int) Batch {
	b := Batch{}
	for i := range n {
		b.Events = append(b.Events, Event{RequestID: "req_x", Seq: i + 1, Type: RequestReceived})
	}
	return b
}

func run(t *testing.T, w *Writer) (stop func()) {
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

func TestEmitNeverBlocksOnStalledStore(t *testing.T) {
	store := &fakeStore{block: make(chan struct{})}
	m := telemetry.NewMetrics("test", "test")
	w := NewWriter(store, m, WriterOptions{Buffer: 4, MaxRows: 1, WriteTimeout: time.Minute})
	stop := run(t, w)
	start := time.Now()
	for range 1000 {
		w.Emit(events(2))
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("1000 Emits took %s with a stalled store", d)
	}
	dropped := testutil.ToFloat64(w.dropped.WithLabelValues("buffer_full"))
	// At most Buffer queued + 1 batch in the writer's hands; everything else is dropped.
	if dropped < 2*(1000-6) || dropped > 2000 {
		t.Fatalf("dropped %v", dropped)
	}
	if fill := testutil.ToFloat64(w.fill); fill < 0.5 {
		t.Fatalf("fill ratio %v with a full buffer", fill)
	}
	close(store.block)
	stop()
	if got := store.rows() + int(dropped); got != 2000 {
		t.Fatalf("written %d + dropped %v != 2000 emitted", store.rows(), dropped)
	}
}

func TestWriterBatchesAndFlushes(t *testing.T) {
	store := &fakeStore{}
	w := NewWriter(store, telemetry.NewMetrics("test", "test"), WriterOptions{MaxRows: 10, FlushEvery: 20 * time.Millisecond})
	stop := run(t, w)
	for range 25 {
		w.Emit(events(1))
	}
	w.Emit(Batch{Records: []Record{{RequestID: "req_x"}}})
	w.Emit(Batch{}) // empty: ignored
	deadline := time.Now().Add(2 * time.Second)
	for store.rows() < 26 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if store.rows() != 26 {
		t.Fatalf("rows %d", store.rows())
	}
	for _, b := range store.batches {
		if b.len() > 10 {
			t.Fatalf("batch of %d rows over MaxRows", b.len())
		}
	}
}

func TestWriterDrainsOnStop(t *testing.T) {
	store := &fakeStore{}
	w := NewWriter(store, telemetry.NewMetrics("test", "test"), WriterOptions{FlushEvery: time.Hour})
	stop := run(t, w)
	for range 5 {
		w.Emit(events(3))
	}
	stop()
	if store.rows() != 15 {
		t.Fatalf("rows after stop %d", store.rows())
	}
}

func TestWriteErrorsCountAsDropped(t *testing.T) {
	store := &fakeStore{writeErr: errors.New("db down")}
	w := NewWriter(store, telemetry.NewMetrics("test", "test"), WriterOptions{FlushEvery: time.Hour})
	stop := run(t, w)
	w.Emit(events(4))
	stop()
	if got := testutil.ToFloat64(w.dropped.WithLabelValues("write_error")); got != 4 {
		t.Fatalf("write_error drops %v", got)
	}
	if testutil.ToFloat64(w.failed) != 1 {
		t.Fatal("failure counter")
	}
}

func TestPartitionsAheadAtStart(t *testing.T) {
	store := &fakeStore{}
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	w := NewWriter(store, telemetry.NewMetrics("test", "test"), WriterOptions{Now: func() time.Time { return now }})
	stop := run(t, w)
	stop()
	if len(store.parts) != 8 || !store.parts[0].Equal(now.AddDate(0, 0, -1)) {
		t.Fatalf("partitions %v", store.parts)
	}
	if last := store.parts[7]; last.Before(now.AddDate(0, 0, 6)) {
		t.Fatalf("last partition %v is not 7 days ahead", last)
	}
}
