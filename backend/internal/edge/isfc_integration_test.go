//go:build integration

package edge

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// TestRevokedISFCKeyIs401AfterOneReconcile is the HUB-483/486 end to end: a tenant and a caller key
// created in mockisfc reach the edge through the reconciler, Postgres and the gateway key map; the
// key stops working after one reconcile once it is revoked in ISFC. Nothing calls a real ISFC.
func TestRevokedISFCKeyIs401AfterOneReconcile(t *testing.T) {
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
	if _, err := pool.Exec(t.Context(), `TRUNCATE hub.config_snapshots; DELETE FROM hub.isfc_caller_keys`); err != nil {
		t.Fatal(err)
	}
	pub := &control.Publisher{Pool: pool, Archive: control.NewArchive(pool, control.DirBlobStore{Root: t.TempDir()}),
		Bootstrap: registry.Sources{Config: os.DirFS("../../config")}}

	const hub, console = "integration-hub", "integration-hub-console"
	mock := mockisfc.New(mockisfc.Options{})
	mock.Seed(hub, "hub-secret", console)
	ts := httptest.NewServer(mock.Handler())
	t.Cleanup(ts.Close)
	client, err := isfc.New(isfc.Config{BaseURL: ts.URL, ClientID: hub, ClientSecret: "hub-secret"})
	if err != nil {
		t.Fatal(err)
	}
	store := isfcsync.PGStore{Pool: pool}
	rec := &isfcsync.Reconciler{Source: client, Editor: pub, Store: store,
		Config: isfcsync.Config{ConsoleClientID: console, BackendClientID: hub},
		Keys:   &isfcsync.KeySync{Source: client, Store: store, BackendClientID: hub}}
	reconcile := func(trigger string) {
		t.Helper()
		if res, err := rec.Run(t.Context(), trigger, isfcsync.RunOptions{Reason: "test"}); err != nil || res.Outcome != "ok" {
			t.Fatalf("reconcile: %v %+v", err, res)
		}
	}

	hl := mock.AddTenant("HL", "Home Loans")
	mock.SetClientTenants(console, hl)
	keyID, key := mock.IssueKey(hub, "billing-backend", "HUB:INVOKE")
	reconcile(isfcsync.TriggerManual) // creates hl-t, setup pending

	var snaps registry.Store
	var km isfc.KeyMap
	reload := func() {
		t.Helper()
		_, snap, err := pub.Current(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		snaps.Swap(snap)
		if err := isfcsync.LoadKeyMap(t.Context(), pool, &km); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(fn func(doc string) string) {
		t.Helper()
		if _, err := pub.Edit(t.Context(), func(tr control.Tree, _ *registry.Snapshot) (control.Change, error) {
			doc, _ := tr.Get("tenants/hl-t.yaml")
			tr.Put("tenants/hl-t.yaml", []byte(fn(string(doc))))
			return control.Change{Actor: "test", Action: "updateTenant", ObjectType: "tenant", ObjectID: "hl-t", Reason: "test edit"}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	edit(func(doc string) string { return doc + "isfc_caller_clients: [billing-backend]\n" })
	reconcile(isfcsync.TriggerTick) // key sync resolves the key's tenant from that list
	reload()

	srv := New(Deps{Snapshots: &snaps, ISFCKeys: &km})
	mux := http.NewServeMux()
	mux.Handle("POST /v1/integrations/{integration_id}/invoke", srv.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := CallerFrom(r.Context())
		w.Header().Set("X-Test-Tenant", c.Tenant.Tenant)
		w.WriteHeader(http.StatusNoContent)
	})))
	h := withRequestID(mux)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.WithoutCancel(t.Context()), http.MethodPost, "/v1/integrations/int_x/invoke", strings.NewReader("{}"))
		req.Header.Set("x-isfc-client-id", "billing-backend")
		req.Header.Set("x-isfc-api-key", key)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(); rec.Code != 401 {
		t.Fatalf("setup pending: %d %s", rec.Code, rec.Body)
	}
	edit(func(doc string) string { return strings.Replace(doc, "setup: pending", "setup: complete", 1) })
	reload()
	if rec := call(); rec.Code != 204 || rec.Header().Get("X-Test-Tenant") != "hl-t" {
		t.Fatalf("after setup: %d %s", rec.Code, rec.Body)
	}

	mock.RevokeKey(keyID)
	if rec := call(); rec.Code != 204 {
		t.Fatalf("before the reconcile the key still works (revocation is not instant): %d", rec.Code)
	}
	reconcile(isfcsync.TriggerEvent)
	reload()
	if rec := call(); rec.Code != 401 {
		t.Fatalf("after one reconcile: %d %s", rec.Code, rec.Body)
	}
}
