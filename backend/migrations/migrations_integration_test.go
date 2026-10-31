//go:build integration

package migrations_test

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func TestUpIsIdempotentAgainstComposePostgres(t *testing.T) {
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest` with `make up`")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := migrations.Up(t.Context(), db); err != nil {
		t.Fatalf("first up: %v", err)
	}
	n, err := migrations.Up(t.Context(), db)
	if err != nil {
		t.Fatalf("second up: %v", err)
	}
	if n != 0 {
		t.Fatalf("second up applied %d migrations, want 0", n)
	}
	st, err := migrations.Status(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !st[1] {
		t.Fatalf("baseline migration 1 not applied: %v", st)
	}
	var exists bool
	if err := db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'hub')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("schema hub missing after baseline")
	}
}
