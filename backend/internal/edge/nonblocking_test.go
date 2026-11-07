package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// hungAuditStore is an audit store whose writes hang until release is closed: a Postgres that
// stopped answering.
type hungAuditStore struct {
	mu      sync.Mutex
	writes  int
	release chan struct{}
}

func (s *hungAuditStore) Write(ctx context.Context, _ audit.Batch) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *hungAuditStore) EnsurePartitions(context.Context, time.Time, int) error { return nil }

func (s *hungAuditStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// gathered is the sum of the samples of the metric name on m whose labels contain label=value.
func gathered(t *testing.T, m *telemetry.Metrics, name, label, value string) float64 {
	t.Helper()
	mfs, err := m.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, s := range mf.GetMetric() {
			for _, l := range s.GetLabel() {
				if l.GetName() == label && l.GetValue() == value {
					sum += s.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}

// TestSyncRequestDoesNotWaitForAudit is HUB-413: the audit ledger is written off the caller's
// request (DESIGN §18.5 rule 3). The real audit.Writer sits on a store that never answers and a
// one-batch buffer: the first batch is stuck in a write, the second fills the buffer, and every
// later sync call still answers at once while its audit batch is dropped and counted. A sync
// request that waited for its audit write, or for room in the buffer, would hang here.
func TestSyncRequestDoesNotWaitForAudit(t *testing.T) {
	store := &hungAuditStore{release: make(chan struct{})}
	m := telemetry.NewMetrics("gateway", "test")
	w := audit.NewWriter(store, m, audit.WriterOptions{Buffer: 1, MaxRows: 1, FlushEvery: time.Millisecond, WriteTimeout: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() { close(store.release); cancel(); <-done })

	up := &fakeUpstream{steps: []step{ok200()}}
	h, _ := invokeServer(t, devSnapshot(t), up, func(d *Deps) { d.Audit, d.Metrics = w, m })
	call := func() (int, time.Duration) {
		t.Helper()
		res := make(chan int, 1)
		start := time.Now()
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/v1/integrations/int_evt_brandA/invoke", strings.NewReader(trackBody))
			req.Header.Set("Authorization", "Bearer "+devKey)
			req.Header.Set("Content-Type", "application/json")
			h.ServeHTTP(rec, req)
			res <- rec.Code
		}()
		select {
		case code := <-res:
			return code, time.Since(start)
		case <-time.After(5 * time.Second):
			t.Fatal("a sync request is waiting for the audit store")
			return 0, 0
		}
	}

	// The first call's batch reaches the store and hangs there.
	if code, _ := call(); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for store.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the writer never received the first batch")
		}
		time.Sleep(time.Millisecond)
	}
	const n = 50
	var slowest time.Duration
	for range n {
		code, d := call()
		if code != http.StatusOK {
			t.Fatalf("status %d with the audit store stalled", code)
		}
		slowest = max(slowest, d)
	}
	if slowest > time.Second {
		t.Fatalf("slowest sync call took %s with the audit store stalled", slowest)
	}
	if store.count() != 1 {
		t.Fatalf("%d writes reached the store, want the single stuck one", store.count())
	}
	// One batch waits in the buffer; the batches of the other n-1 calls were dropped.
	if dropped := gathered(t, m, "audit_events_dropped_total", "reason", "buffer_full"); dropped < n-1 {
		t.Fatalf("audit_events_dropped_total{reason=buffer_full} = %v, want at least the events of %d requests", dropped, n-1)
	}
	if up.calls() != n+1 {
		t.Fatalf("%d vendor calls, want %d", up.calls(), n+1)
	}
}
