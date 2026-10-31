//go:build integration

package control_test

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/db"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

var registryTables = []string{
	"tenants", "api_keys", "vendors", "vendor_endpoints", "accounts", "account_credentials", "capabilities",
	"transformers", "integrations", "integration_versions", "integration_grants", "registrar_endpoints", "config_snapshots",
}

func connect(t *testing.T) (*sql.DB, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sdb.Close() })
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(t.Context()) })
	return sdb, conn
}

func tableCount(t *testing.T, sdb *sql.DB) int {
	t.Helper()
	var n int
	if err := sdb.QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'hub' AND table_name = ANY($1)`, registryTables).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegistryMigrationUpDownUp(t *testing.T) {
	sdb, _ := connect(t)
	if n := tableCount(t, sdb); n != len(registryTables) {
		t.Fatalf("after up: %d of %d registry tables", n, len(registryTables))
	}
	if err := migrations.DownTo(t.Context(), sdb, 1); err != nil {
		t.Fatal(err)
	}
	if n := tableCount(t, sdb); n != 0 {
		t.Fatalf("after down: %d registry tables remain", n)
	}
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if n := tableCount(t, sdb); n != len(registryTables) {
		t.Fatalf("after re-up: %d tables", n)
	}
}

func TestVersionsAndTransformersRejectUpdate(t *testing.T) {
	_, conn := connect(t)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }() // leave no rows behind
	q := db.New(tx)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(q.UpsertTenant(ctx, db.UpsertTenantParams{TenantID: "orders-service", Status: "active", RateLimitRps: 100, RateLimitBurst: 200}))
	must(q.InsertIntegration(ctx, db.InsertIntegrationParams{IntegrationID: "int_evt_test", OwnerTenant: "orders-service", Capability: "events.track@v1", ActiveVersion: 1}))
	must(q.InsertIntegrationVersion(ctx, db.InsertIntegrationVersionParams{IntegrationID: "int_evt_test", Version: 1, Spec: []byte(`{"targets":[]}`), Author: "test", Reason: "v1"}))
	must(q.InsertIntegrationVersion(ctx, db.InsertIntegrationVersionParams{IntegrationID: "int_evt_test", Version: 2, Spec: []byte(`{"targets":[]}`), Author: "test", Reason: "v2"}))
	must(q.InsertTransformer(ctx, db.InsertTransformerParams{TransformerID: "T1", Version: 7, Kind: "request", Capability: "events.track@v1", Yaml: "transformer: T1", Fixtures: []byte(`{}`)}))

	// The active pointer moves (cutover / rollback); versions themselves never change.
	if n, err := q.SetActiveVersion(ctx, db.SetActiveVersionParams{IntegrationID: "int_evt_test", ActiveVersion: 2}); err != nil || n != 1 {
		t.Fatalf("set active: %d %v", n, err)
	}
	versions, err := q.ListIntegrationVersions(ctx, "int_evt_test")
	if err != nil || len(versions) != 2 || versions[0].Version != 1 {
		t.Fatalf("versions %v %v", versions, err)
	}

	for _, stmt := range []string{
		`UPDATE hub.integration_versions SET reason = 'edited' WHERE integration_id = 'int_evt_test' AND version = 1`,
		`UPDATE hub.transformers SET yaml = 'edited' WHERE transformer_id = 'T1' AND version = 7`,
	} {
		_, err := tx.Exec(ctx, "SAVEPOINT s")
		must(err)
		_, err = tx.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23001" {
			t.Fatalf("%s: want restrict_violation (23001), got %v", stmt, err)
		}
		_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT s")
		must(err)
	}
}

func TestPruneSnapshotsKeepsNewest(t *testing.T) {
	_, conn := connect(t)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	q := db.New(tx)
	ctx := t.Context()
	sum := "sha256:" + strings.Repeat("0", 64)
	for v := int64(1); v <= 3; v++ {
		if _, err := q.InsertSnapshot(ctx, db.InsertSnapshotParams{Version: v, Checksum: sum, BlobRef: "file://x", Author: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	// Age every row past the cut-off: the newest must still survive.
	if _, err := tx.Exec(ctx, `UPDATE hub.config_snapshots SET created_at = now() - interval '100 days'`); err != nil {
		t.Fatal(err)
	}
	pruned, err := q.PruneSnapshots(ctx, pgtype.Timestamptz{Time: time.Now().Add(-90 * 24 * time.Hour), Valid: true})
	if err != nil || len(pruned) != 2 {
		t.Fatalf("pruned %d (%v), want 2", len(pruned), err)
	}
	latest, err := q.LatestSnapshot(ctx)
	if err != nil || latest.Version != 3 {
		t.Fatalf("latest %v %v", latest, err)
	}
}
