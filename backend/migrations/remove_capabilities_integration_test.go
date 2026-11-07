//go:build integration

package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// scratchDB creates an empty database next to HUB_PG_DSN's (the test moves the schema up and
// down, which the shared test database must not see) and drops it afterwards.
func scratchDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest` with `make up`")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("%s_%s_%d", u.Path[1:], name, os.Getpid())
	if _, err := admin.ExecContext(t.Context(), `DROP DATABASE IF EXISTS `+dbName); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+dbName); err != nil {
		t.Fatalf("create scratch database (needs CREATEDB, compose's hub user has it): %v", err)
	}
	u.Path = "/" + dbName
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+dbName+` WITH (FORCE)`)
	})
	return db
}

// The data a database written before ADR-0023 holds (migration 00010 applied).
const preADR0023Data = `
INSERT INTO hub.tenants (tenant_id, rate_limit_rps, rate_limit_burst) VALUES ('t_mig', 10, 10);
INSERT INTO hub.capabilities (capability, major, minor, request_schema, response_schema, operations, interactive) VALUES
 ('events.track@v1', 1, 0, '{"type":"object"}', '{}', '{}', false),
 ('events.track@v1', 1, 1, '{"type":"object","required":["user"],"properties":{"user":{"type":"object","x-pii":"identifier"}}}', '{}', '{}', true),
 ('kyc.otp@v1', 1, 0, '{"type":"object"}', '{}',
  '{"generate":{"description":"Send an OTP.","idempotent":false,"request_schema":{"type":"object","required":["mobile"]}},"verify":{"idempotent":true}}', false),
 ('llm.chat@v1', 1, 0, '{"type":"object"}', '{}', '{}', false);
INSERT INTO hub.integrations (integration_id, owner_tenant, capability, active_version) VALUES
 ('int_mig_a', 't_mig', 'events.track@v1', 1),
 ('int_mig_b', 't_mig', 'kyc.otp@v1', 1),
 ('int_mig_c', 't_mig', 'gone.cap@v1', 1);
INSERT INTO hub.integration_versions (integration_id, version, spec, author, reason) VALUES
 ('int_mig_a', 1, '{"capability":"events.track@v1","targets":[]}', 'a', 'v1'),
 ('int_mig_a', 2, '{"targets":[],"operations":{"default":{"request_schema":{"body":{"type":"object"}}}}}', 'a', 'v2'),
 ('int_mig_b', 1, '{"targets":[]}', 'a', 'v1'),
 ('int_mig_c', 1, '{"targets":[]}', 'a', 'v1');
INSERT INTO hub.vendors (vendor_id, auth, regions, defaults) VALUES ('v_mig', 'api_key', '{}', '{}');
INSERT INTO hub.vendor_endpoints (vendor_id, endpoint_id, method, path, timeout_ms, connect_timeout_ms) VALUES
 ('v_mig', 'upload@1', 'POST', '/up', 1000, 100);
INSERT INTO hub.transformers (transformer_id, version, kind, capability, endpoint_id, yaml) VALUES
 ('T_mig', 1, 'request', 'events.track@v1', 'upload@1', 'transformer: T_mig');
INSERT INTO hub.outbox (request_id, status, tenant_id, integration_id, next_attempt_at, expires_at, envelope, created_at) VALUES
 ('req_01JAAAAAAAAAAAAAAAAAAAAAAA', 'accepted', 't_mig', 'int_mig_a', now(), now() + interval '1 hour', '{"capability":"events.track@v1","mode":"async"}', now()),
 ('req_01JBBBBBBBBBBBBBBBBBBBBBBB', 'accepted', 't_mig', 'int_mig_a', now(), now() + interval '1 hour', '{"capability":"llm.chat@v1","mode":"async"}', now());
INSERT INTO hub.dead_letters (request_id, tenant_id, integration_id, reason, envelope) VALUES
 ('req_01JCCCCCCCCCCCCCCCCCCCCCCC', 't_mig', 'int_mig_a', 'vendor_rejected', '{"capability":"llm.chat@v1","mode":"async"}');
`

// TestRemoveCapabilitiesMigration runs 00011 (ADR-0023, HUB-434) up and down over a database
// holding pre-ADR-0023 data.
func TestRemoveCapabilitiesMigration(t *testing.T) {
	ctx := t.Context()
	db := scratchDB(t, "mig00011")
	if err := migrations.UpTo(ctx, db, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, preADR0023Data); err != nil {
		t.Fatalf("seed pre-ADR-0023 data: %v", err)
	}
	if n, err := migrations.Up(ctx, db); err != nil || n != 1 {
		t.Fatalf("up 00011: applied %d, %v", n, err)
	}

	jsonOf := func(q string, args ...any) any {
		t.Helper()
		var raw []byte
		if err := db.QueryRowContext(ctx, q, args...).Scan(&raw); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	spec := func(id string, version int) any {
		return jsonOf(`SELECT spec FROM hub.integration_versions WHERE integration_id = $1 AND version = $2`, id, version)
	}
	parse := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	t.Run("operations copied from the latest capability minor", func(t *testing.T) {
		cases := []struct {
			id      string
			version int
			want    string
		}{
			{"int_mig_a", 1, `{"targets":[],"operations":{"default":{"idempotent":false,"request_schema":{"body":{"type":"object","required":["user"],"properties":{"user":{"type":"object","x-pii":"identifier"}}}}}}}`},
			{"int_mig_a", 2, `{"targets":[],"operations":{"default":{"request_schema":{"body":{"type":"object"}}}}}`}, // already had operations
			{"int_mig_b", 1, `{"targets":[],"operations":{
				"generate":{"description":"Send an OTP.","idempotent":false,"request_schema":{"body":{"type":"object","required":["mobile"]}}},
				"verify":{"idempotent":true,"request_schema":{"body":{"type":"object"}}}}}`},
			{"int_mig_c", 1, `{"targets":[]}`}, // its capability is unknown: left for the operator
		}
		for _, c := range cases {
			if got := spec(c.id, c.version); !reflect.DeepEqual(got, parse(c.want)) {
				t.Errorf("%s v%d spec %v, want %s", c.id, c.version, got, c.want)
			}
		}
	})

	t.Run("integrations get interactive from the capability", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, `SELECT integration_id, interactive FROM hub.integrations ORDER BY integration_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		got := map[string]bool{}
		for rows.Next() {
			var id string
			var v bool
			if err := rows.Scan(&id, &v); err != nil {
				t.Fatal(err)
			}
			got[id] = v
		}
		if want := map[string]bool{"int_mig_a": true, "int_mig_b": false, "int_mig_c": false}; !reflect.DeepEqual(got, want) {
			t.Fatalf("interactive %v, want %v", got, want)
		}
	})

	t.Run("endpoints get an empty request schema", func(t *testing.T) {
		if got := jsonOf(`SELECT request_schema FROM hub.vendor_endpoints WHERE endpoint_id = 'upload@1'`); !reflect.DeepEqual(got, map[string]any{}) {
			t.Fatalf("request_schema %v", got)
		}
		var resp sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT response_schema FROM hub.vendor_endpoints WHERE endpoint_id = 'upload@1'`).Scan(&resp); err != nil || resp.Valid {
			t.Fatalf("response_schema %v %v, want NULL", resp, err)
		}
	})

	t.Run("envelopes lose capability, llm ones keep their pool", func(t *testing.T) {
		cases := []struct{ q, want string }{
			{`SELECT envelope FROM hub.outbox WHERE request_id = 'req_01JAAAAAAAAAAAAAAAAAAAAAAA'`, `{"mode":"async"}`},
			{`SELECT envelope FROM hub.outbox WHERE request_id = 'req_01JBBBBBBBBBBBBBBBBBBBBBBB'`, `{"mode":"async","pool":"llm"}`},
			{`SELECT envelope FROM hub.dead_letters WHERE request_id = 'req_01JCCCCCCCCCCCCCCCCCCCCCCC'`, `{"mode":"async","pool":"llm"}`},
		}
		for _, c := range cases {
			if got := jsonOf(c.q); !reflect.DeepEqual(got, parse(c.want)) {
				t.Errorf("%s: %v, want %s", c.q, got, c.want)
			}
		}
	})

	t.Run("capability table and columns are gone", func(t *testing.T) {
		assertColumns(t, db, map[string]bool{
			"capabilities.capability": false, "integrations.capability": false, "transformers.capability": false,
			"requests.capability": false, "integrations.interactive": true,
			"vendor_endpoints.request_schema": true, "vendor_endpoints.response_schema": true,
		})
	})

	t.Run("versions stay immutable", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `UPDATE hub.integration_versions SET reason = 'x' WHERE integration_id = 'int_mig_a'`); err == nil {
			t.Fatal("the immutability trigger was left disabled")
		}
	})

	t.Run("down restores the pre-ADR-0023 schema and up runs again", func(t *testing.T) {
		if err := migrations.DownTo(ctx, db, 10); err != nil {
			t.Fatal(err)
		}
		assertColumns(t, db, map[string]bool{
			"capabilities.capability": true, "integrations.capability": true, "transformers.capability": true,
			"requests.capability": true, "integrations.interactive": false,
			"vendor_endpoints.request_schema": false, "vendor_endpoints.response_schema": false,
		})
		if got := spec("int_mig_a", 1); !reflect.DeepEqual(got, parse(`{"targets":[]}`)) {
			t.Fatalf("down left operations: %v", got)
		}
		// The old code writes these columns again.
		if _, err := db.ExecContext(ctx, `INSERT INTO hub.integrations (integration_id, owner_tenant, capability, active_version) VALUES ('int_mig_d', 't_mig', 'events.track@v1', 1)`); err != nil {
			t.Fatalf("pre-ADR-0023 insert after down: %v", err)
		}
		if n, err := migrations.Up(ctx, db); err != nil || n != 1 {
			t.Fatalf("up again: applied %d, %v", n, err)
		}
		assertColumns(t, db, map[string]bool{"capabilities.capability": false, "integrations.interactive": true})
	})
}

// assertColumns checks that each "table.column" of schema hub exists (true) or not (false).
func assertColumns(t *testing.T, db *sql.DB, want map[string]bool) {
	t.Helper()
	for tc, exists := range want {
		var n int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'hub' AND table_name || '.' || column_name = $1`, tc).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if (n > 0) != exists {
			t.Errorf("column hub.%s exists=%v, want %v", tc, n > 0, exists)
		}
	}
}
