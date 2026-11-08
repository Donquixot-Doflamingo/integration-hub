//go:build integration

package adminapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// testPool migrates the shared dev database and connects to it.
func testPool(t *testing.T) *pgxpool.Pool {
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
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestChangeLogListAndGet(t *testing.T) {
	pool := testPool(t)
	spec := contract.Load(t, "hub-admin.v1.yaml")
	s, iss := testServer(t, Deps{Pool: pool})
	h := s.Handler()
	object := "int_cl_" + core.NewULID(time.Now())[20:]
	ctx := control.WithChangeMeta(t.Context(), []string{"editor"}, "req_"+core.NewULID(time.Now()))
	var ids []string
	for i := range 3 {
		id, err := control.RecordChange(ctx, pool, control.Change{Actor: "priya@example.com", Action: "updateIntegration",
			ObjectType: "integration", ObjectID: object, Reason: "step", Before: map[string]any{"n": i}, After: map[string]any{"n": i + 1}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	get := func(path string) (int, map[string]any) {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, Base+path, nil)
		req.Header.Set("Authorization", "Bearer "+token(t, iss, RoleViewer))
		rec := spec.Serve(t, h, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, first := get("/change-log?object_type=integration&object_id=" + object + "&limit=2")
	items, _ := first["items"].([]any)
	if code != 200 || len(items) != 2 || first["next_cursor"] == nil {
		t.Fatalf("page 1: %d %v", code, first)
	}
	if items[0].(map[string]any)["change_id"] != ids[2] { // newest first
		t.Fatalf("order: %v", items)
	}
	if _, leaked := items[0].(map[string]any)["before"]; leaked {
		t.Fatal("the list must omit before/after")
	}
	_, second := get("/change-log?object_type=integration&object_id=" + object + "&limit=2&cursor=" + first["next_cursor"].(string))
	items2, _ := second["items"].([]any)
	if len(items2) != 1 || items2[0].(map[string]any)["change_id"] != ids[0] || second["next_cursor"] != nil {
		t.Fatalf("page 2: %v", second)
	}
	// A cursor with the wrong number of parts ("x") is a 400, not a silent first page (HUB-451).
	if code, _ := get("/change-log?cursor=eA"); code != 400 {
		t.Fatalf("malformed cursor: %d, want 400", code)
	}
	code, entry := get("/change-log/" + ids[1])
	if code != 200 || entry["before"].(map[string]any)["n"] != 1.0 || entry["actor_roles"].([]any)[0] != "editor" {
		t.Fatalf("entry %d %v", code, entry)
	}
	if code, _ := get("/change-log/chg_" + core.NewULID(time.Now())); code != 404 {
		t.Fatalf("unknown entry %d", code)
	}
}
