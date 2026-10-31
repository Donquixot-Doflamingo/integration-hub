//go:build integration

package audit_test

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sdb.Close() }()
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	p, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func TestPGStorePartitionsAndWrites(t *testing.T) {
	p := pool(t)
	store := audit.PGStore{Pool: p}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if err := store.EnsurePartitions(t.Context(), today, 8); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsurePartitions(t.Context(), today, 8); err != nil { // idempotent
		t.Fatal(err)
	}
	for _, table := range []string{"requests", "request_events", "request_attempts"} {
		var n int
		err := p.QueryRow(t.Context(), `SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'hub' AND c.relname = $1`, table).Scan(&n)
		if err != nil || n < 8 {
			t.Fatalf("%s partitions %d %v", table, n, err)
		}
	}

	id := core.NewRequestID()
	now := time.Now().UTC()
	b := audit.Batch{
		Events: []audit.Event{
			{At: now, RequestID: id, Seq: 1, Type: audit.RequestReceived, SHA256: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Bytes: 12, Payload: []byte(`{"a":"b"}`), PayloadState: audit.PayloadMasked},
			{At: now, RequestID: id, Seq: 2, Type: audit.IntegrationResolved, Meta: map[string]any{"version": 2}},
		},
		Attempts: []audit.Attempt{{StartedAt: now, RequestID: id, AttemptNo: 1, AccountID: "ct-brandA-in", EndpointID: "upload@1", DurationMS: 3.5, HTTPStatus: 200}},
		Records:  []audit.Record{{RequestID: id, CreatedAt: now, TenantID: "orders-service", IntegrationID: "int_evt_brandA", Capability: "events.track@v1", Operation: "default", Mode: "sync", Status: "in_flight", PayloadPolicy: "masked"}},
	}
	if err := store.Write(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	final := b.Records[0]
	final.Status, final.HTTPStatus, final.Attempts, final.CompletedAt = "succeeded", 200, 1, now
	if err := store.Write(t.Context(), audit.Batch{Records: []audit.Record{final}}); err != nil {
		t.Fatal(err)
	}
	var status, policy string
	var events int
	if err := p.QueryRow(t.Context(), `SELECT status, payload_policy, (SELECT count(*) FROM hub.request_events WHERE request_id = $1)
		FROM hub.requests WHERE request_id = $1`, id).Scan(&status, &policy, &events); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || policy != "masked" || events != 2 {
		t.Fatalf("status %s policy %s events %d", status, policy, events)
	}
}
