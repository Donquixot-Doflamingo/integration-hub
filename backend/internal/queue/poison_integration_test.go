//go:build integration

package queue_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
)

// badEnvelope inserts a row whose envelope JSON is valid but does not decode into core.Envelope
// (an older stored format), due at due (HUB-453).
func badEnvelope(t *testing.T, p *pgxpool.Pool, pl string, due time.Time) string {
	t.Helper()
	id := core.NewRequestID()
	cleanup(t, p, id)
	if _, err := p.Exec(t.Context(), `INSERT INTO hub.outbox (request_id, status, pool, tenant_id, integration_id, attempt, next_attempt_at, expires_at, envelope, created_at)
		VALUES ($1, 'accepted', $2, 'orders-service', 'int_old', 0, $3, $3::timestamptz + interval '6 hours', '{"attempt":"not a number"}', $3)`, id, pl, due); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClaimSurvivesUndecodableEnvelope(t *testing.T) {
	p := pool(t)
	isolate(t, p, queue.PoolLLM)
	now := time.Now()
	q := &queue.Outbox{Now: func() time.Time { return now }}
	bad := badEnvelope(t, p, queue.PoolLLM, now.Add(-time.Hour)) // sorts first
	good := env(queue.PoolLLM, now.Add(-time.Second))
	cleanup(t, p, good.RequestID)
	if err := q.Enqueue(t.Context(), p, good); err != nil {
		t.Fatal(err)
	}
	got, err := q.Claim(t.Context(), p, "w", queue.PoolLLM, 10)
	if err != nil || len(got) != 1 || got[0].RequestID != good.RequestID {
		t.Fatalf("the good envelope must be claimed: %+v %v", got, err)
	}
	var n int
	var reason string
	if err := p.QueryRow(t.Context(), `SELECT count(*), COALESCE(max(reason), '') FROM hub.dead_letters WHERE request_id = $1`, bad).Scan(&n, &reason); err != nil || n != 1 || reason != "poison" {
		t.Fatalf("undecodable envelope must be dead-lettered as poison: n=%d reason=%q %v", n, reason, err)
	}
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM hub.outbox WHERE request_id = $1`, bad).Scan(&n); err != nil || n != 0 {
		t.Fatalf("undecodable envelope must leave the outbox: %d %v", n, err)
	}
	if again, err := q.Claim(t.Context(), p, "w2", queue.PoolLLM, 10); err != nil || len(again) != 0 {
		t.Fatalf("nothing to claim again: %+v %v", again, err)
	}
}

func TestDueExpiriesSurvivesUndecodableEnvelope(t *testing.T) {
	p := pool(t)
	now := time.Now()
	q := &queue.Outbox{Now: func() time.Time { return now }}
	if _, err := p.Exec(t.Context(), `UPDATE hub.outbox SET expires_at = now() + interval '1 day' WHERE status IN ('accepted', 'retry_scheduled', 'parked')`); err != nil {
		t.Fatal(err)
	}
	bad := badEnvelope(t, p, queue.PoolLLM, now.Add(-10*time.Hour)) // expired long ago: sorts first
	good := env(queue.PoolLLM, now.Add(-8*time.Hour))
	good.ExpiresAt = now.Add(-time.Hour)
	cleanup(t, p, good.RequestID)
	if err := q.Enqueue(t.Context(), p, good); err != nil {
		t.Fatal(err)
	}
	got, err := q.DueExpiries(t.Context(), p, 100)
	if err != nil || len(got) != 1 || got[0].RequestID != good.RequestID {
		t.Fatalf("the good expired envelope must be listed: %+v %v", got, err)
	}
	var n int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM hub.dead_letters WHERE request_id = $1 AND reason = 'poison'`, bad).Scan(&n); err != nil || n != 1 {
		t.Fatalf("undecodable envelope must be dead-lettered: %d %v", n, err)
	}
}
