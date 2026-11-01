//go:build integration

package sessions_test

import (
	"database/sql"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
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
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	p, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func uniq(prefix string) string { return prefix + "-" + core.NewULID(time.Now()) }

func TestCreateGetScopesAndExpiry(t *testing.T) {
	db := pool(t)
	tenant, integration, account := uniq("tenant"), uniq("int"), uniq("acct")
	s, err := sessions.Create(t.Context(), db, sessions.Open{TenantID: tenant, IntegrationID: integration, Version: 7, AccountID: account,
		Data: map[string]any{"vendor_txn_id": "vr-1", "user": "u_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !sessions.IDPattern.MatchString(s.ID) || s.VendorTxnID != "vr-1" {
		t.Fatalf("session %+v", s)
	}
	if ttl := s.ExpiresAt.Sub(s.CreatedAt); ttl != sessions.DefaultTTL {
		t.Fatalf("ttl %s", ttl)
	}
	got, err := sessions.Get(t.Context(), db, tenant, integration, s.ID)
	if err != nil || got.Version != 7 || got.AccountID != account || got.Data["user"] != "u_1" || got.VendorTxnID != "vr-1" {
		t.Fatalf("get %+v %v", got, err)
	}
	for name, args := range map[string][2]string{"other tenant": {"x", integration}, "other integration": {tenant, "x"}} {
		if _, err := sessions.Get(t.Context(), db, args[0], args[1], s.ID); !errors.Is(err, sessions.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := sessions.Get(t.Context(), db, tenant, integration, "ses_"+core.NewULID(time.Now())); !errors.Is(err, sessions.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if n, err := sessions.CountOpen(t.Context(), db, account); err != nil || n != 1 {
		t.Fatalf("CountOpen %d %v", n, err)
	}

	short, err := sessions.Create(t.Context(), db, sessions.Open{TenantID: tenant, IntegrationID: integration, Version: 7, AccountID: account, TTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := sessions.Get(t.Context(), db, tenant, integration, short.ID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("expired session served: %v", err)
	}
	if n, _ := sessions.CountOpen(t.Context(), db, account); n != 1 {
		t.Fatalf("an expired session counts as open: %d", n)
	}
	// Expire takes the caller's clock; leave a minute for skew between this host and Postgres.
	removed, err := sessions.Expire(t.Context(), db, time.Now().Add(time.Minute))
	if err != nil || removed < 1 {
		t.Fatalf("Expire %d %v", removed, err)
	}
	var left int
	_ = db.QueryRow(t.Context(), `SELECT count(*) FROM hub.vendor_sessions WHERE session_id = $1`, short.ID).Scan(&left)
	if left != 0 {
		t.Fatal("expired row kept")
	}
	if _, err := sessions.Get(t.Context(), db, tenant, integration, s.ID); err != nil {
		t.Fatalf("Expire removed an open session: %v", err)
	}
}

// TestSchemaHasNoRawIdentifierColumn is HUB-175's schema test: vendor_sessions has exactly the
// DESIGN §18.2 columns (+ tenant_id, created_at), none meant for a raw identifier, and the table
// itself refuses an Aadhaar-like number in data or vendor_txn_id even if the store is bypassed.
func TestSchemaHasNoRawIdentifierColumn(t *testing.T) {
	db := pool(t)
	rows, err := db.Query(t.Context(), `SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'hub' AND table_name = 'vendor_sessions' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"account_id", "created_at", "data", "expires_at", "integration_id", "session_id", "tenant_id", "vendor_txn_id", "version"}
	if !slices.Equal(cols, want) {
		t.Fatalf("columns %v, want %v", cols, want)
	}
	for _, c := range cols {
		for _, bad := range []string{"aadhaar", "uid", "vid", "pan", "identifier", "number", "otp"} {
			if strings.Contains(c, bad) {
				t.Errorf("column %s looks meant for a raw identifier", c)
			}
		}
	}
	insert := `INSERT INTO hub.vendor_sessions (session_id, tenant_id, integration_id, version, account_id, vendor_txn_id, data, expires_at)
		VALUES ($1, 't', 'i', 1, 'a', $2, $3, now() + interval '1 minute')`
	for name, args := range map[string][2]any{
		"data":          {nil, `{"uid": "2345 6789 0123"}`},
		"data number":   {nil, `{"uid": 234567890123}`},
		"vendor_txn_id": {"234567890123", `{}`},
	} {
		_, err := db.Exec(t.Context(), insert, "ses_"+core.NewULID(time.Now()), args[0], args[1])
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "23514" { // check_violation
			t.Errorf("%s: raw insert of an Aadhaar-like number: %v", name, err)
		}
	}
	// The store refuses it first, without a round trip.
	_, err = sessions.Create(t.Context(), db, sessions.Open{TenantID: "t", IntegrationID: "i", Version: 1, AccountID: "a",
		Data: map[string]any{"vendor_txn_id": "234567890123"}})
	if !errors.Is(err, sessions.ErrSensitiveData) {
		t.Fatalf("store: %v", err)
	}
}
