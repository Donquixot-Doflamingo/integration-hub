//go:build integration

package queue_test

import (
	"testing"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// TestReplayAfterTTLCarriesTheNewExpiry: the worker reads expires_at from the envelope JSON, so
// a dead letter replayed after its original TTL must carry the new expiry there too, or it is
// claimed and expired at once without a delivery attempt (HUB-449).
func TestReplayAfterTTLCarriesTheNewExpiry(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	q := &queue.Outbox{}
	ctx := t.Context()
	e := env(queue.PoolLLM, time.Now().Add(-8*time.Hour)) // original TTL (6 h) long gone
	cleanup(t, p, e.RequestID)
	if err := q.Enqueue(ctx, p, e); err != nil {
		t.Fatal(err)
	}
	if got, err := q.Claim(ctx, p, "w", queue.PoolLLM, 1); err != nil || len(got) != 1 {
		t.Fatalf("claim: %v %d", err, len(got))
	}
	if err := q.DeadLetter(ctx, p, e.RequestID, "w", queue.DeadLetter{Reason: "UPSTREAM_REJECTED"}); err != nil {
		t.Fatal(err)
	}
	newExpiry := time.Now().Add(6 * time.Hour).Truncate(time.Microsecond)
	if err := q.Replay(ctx, p, e.RequestID, newExpiry); err != nil {
		t.Fatal(err)
	}
	got, err := q.Claim(ctx, p, "w2", queue.PoolLLM, 1)
	if err != nil || len(got) != 1 {
		t.Fatalf("claim after replay: %v %d", err, len(got))
	}
	if !got[0].ExpiresAt.Equal(newExpiry) {
		t.Fatalf("envelope expires_at %v, want the replay expiry %v", got[0].ExpiresAt, newExpiry)
	}
	if !time.Now().Before(got[0].ExpiresAt) {
		t.Fatal("the worker would expire the replayed envelope before delivering it")
	}
}
