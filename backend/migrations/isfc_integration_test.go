//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// TestISFCTablesEnforceTheirShape covers migration 00012 (HUB-480): hash-only caller keys and
// the bounded sync run trigger and result.
func TestISFCTablesEnforceTheirShape(t *testing.T) {
	db, err := sql.Open("pgx", os.Getenv("HUB_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Up(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	const hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.WithoutCancel(t.Context()), `DELETE FROM hub.isfc_caller_keys WHERE isfc_key_id LIKE 'mig_test_%'; DELETE FROM hub.isfc_sync_runs WHERE id = 'isr_00000000000000000000000000'`)
	})
	tests := []struct {
		name, stmt string
		args       []any
		wantErr    bool
	}{
		{"caller key with a hash", `INSERT INTO hub.isfc_caller_keys (isfc_key_id, key_hash, caller_client_id, tenant_ids, permissions, status)
			VALUES ('mig_test_ok', $1, 'billing-backend', ARRAY['6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f']::uuid[], ARRAY['HUB:INVOKE'], 'ACTIVE')`, []any{hash}, false},
		{"caller key with a plaintext key", `INSERT INTO hub.isfc_caller_keys (isfc_key_id, key_hash, caller_client_id, status)
			VALUES ('mig_test_plain', $1, 'billing-backend', 'ACTIVE')`, []any{"hk_live_plaintext"}, true},
		{"caller key with an unknown status", `INSERT INTO hub.isfc_caller_keys (isfc_key_id, key_hash, caller_client_id, status)
			VALUES ('mig_test_status', $1, 'billing-backend', 'MAYBE')`, []any{hash}, true},
		{"sync run", `INSERT INTO hub.isfc_sync_runs (id, trigger, started_at, finished_at, result, changes)
			VALUES ('isr_00000000000000000000000000', 'manual', now(), now(), 'ok', '{"created":1}')`, nil, false},
		{"sync run with an unknown trigger", `INSERT INTO hub.isfc_sync_runs (id, trigger, started_at, finished_at, result)
			VALUES ('isr_00000000000000000000000001', 'cron', now(), now(), 'ok')`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.ExecContext(t.Context(), tt.stmt, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
