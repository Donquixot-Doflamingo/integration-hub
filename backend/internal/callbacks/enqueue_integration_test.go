//go:build integration

package callbacks

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// pgPool migrates HUB_PG_DSN and empties hub.callback_deliveries.
func pgPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := p.Exec(t.Context(), `DELETE FROM hub.callback_deliveries`); err != nil {
		t.Fatal(err)
	}
	return p
}

func countRows(t *testing.T, p *pgxpool.Pool, where string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM hub.callback_deliveries WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEnqueueInsideCallerTransaction: deliveries exist if and only if the caller's transaction
// commits, one per matching subscription, with a contract-valid body; re-enqueueing the same
// event is a no-op.
func TestEnqueueInsideCallerTransaction(t *testing.T) {
	p := pgPool(t)
	v := validator(t)
	e, err := NewEnqueuer(staticSnapshots{testSnapshot("https://example.com/cb")}, telemetry.NewMetrics("test", "dev"), EnqueueOptions{Validator: v})
	if err != nil {
		t.Fatal(err)
	}

	ev := sampleEvents()["completed"]
	rolledBack := func(tx pgx.Tx) error {
		n, err := e.Enqueue(t.Context(), tx, ev)
		if err != nil || n != 2 {
			t.Fatalf("Enqueue = %d, %v; want 2 (reg_orders_cb, reg_orders_audit)", n, err)
		}
		if got := countRows(t, p, `request_id = $1`, ev.RequestID); got != 0 {
			t.Fatalf("rows visible outside the open transaction: %d", got)
		}
		return errRollback
	}
	if err := pgx.BeginFunc(t.Context(), p, rolledBack); !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	if got := countRows(t, p, `request_id = $1`, ev.RequestID); got != 0 {
		t.Fatalf("rolled-back transition left %d deliveries", got)
	}

	ev = sampleEvents()["completed"]
	if err := pgx.BeginFunc(t.Context(), p, func(tx pgx.Tx) error {
		_, err := e.Enqueue(t.Context(), tx, ev)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := p.Query(t.Context(), `SELECT delivery_id, event_id, event_type, endpoint_id, subscription, status, attempts, payload,
		tenant_id, integration_id, integration_version FROM hub.callback_deliveries WHERE request_id = $1 ORDER BY subscription`, ev.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, eventID, typ, endpoint, status, tenant, integration string
		sub, attempts, version                                  int
		payload                                                 []byte
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.eventID, &r.typ, &r.endpoint, &r.sub, &r.status, &r.attempts, &r.payload, &r.tenant, &r.integration, &r.version); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].endpoint != "reg_orders_cb" || got[0].sub != 0 || got[1].endpoint != "reg_orders_audit" || got[1].sub != 2 {
		t.Fatalf("rows = %+v", got)
	}
	for _, r := range got {
		if r.status != "pending" || r.attempts != 0 || r.eventID != ev.EventID || r.typ != "request.completed" ||
			r.tenant != "orders-service" || r.integration != "int_evt_brandB" || r.version != 1 || !deliveryIDPattern.MatchString(r.id) {
			t.Fatalf("row = %+v", r)
		}
		if err := v.Validate(EventRequestCompleted, r.payload); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got[0].payload, got[1].payload) || got[0].id == got[1].id {
		t.Fatal("fan-out must share the body and event id, not the delivery id")
	}

	// The same event again (a retried transaction with a stable event id): no new rows.
	if err := pgx.BeginFunc(t.Context(), p, func(tx pgx.Tx) error {
		n, err := e.Enqueue(t.Context(), tx, ev)
		if n != 0 {
			t.Fatalf("re-enqueue wrote %d rows", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	failed := sampleEvents()["failed"]
	failed.RequestID = ev.RequestID
	if err := pgx.BeginFunc(t.Context(), p, func(tx pgx.Tx) error {
		n, err := e.Enqueue(t.Context(), tx, failed)
		if n != 1 {
			t.Fatalf("request.failed wrote %d rows, want 1", n)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, p, `request_id = $1`, ev.RequestID); got != 3 {
		t.Fatalf("deliveries for the request = %d, want 3", got)
	}
}

var errRollback = errors.New("rollback")
