//go:build integration

package isfcsync_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	pub  *control.Publisher
	mock *mockisfc.Server
	rec  *isfcsync.Reconciler
	m    *isfcsync.Metrics
}

const (
	hubClient = "integration-hub"
	console   = "integration-hub-console"
)

func newRig(t *testing.T) *rig {
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
	// The archive is global: every rig starts from the dev config tree.
	if _, err := pool.Exec(t.Context(), `TRUNCATE hub.config_snapshots`); err != nil {
		t.Fatal(err)
	}
	pub := &control.Publisher{Pool: pool, Archive: control.NewArchive(pool, control.DirBlobStore{Root: t.TempDir()}),
		Bootstrap: registry.Sources{Config: os.DirFS("../../config")}}

	mock := mockisfc.New(mockisfc.Options{})
	mock.Seed(hubClient, "hub-secret", console)
	ts := httptest.NewServer(mock.Handler())
	t.Cleanup(ts.Close)
	client, err := isfc.New(isfc.Config{BaseURL: ts.URL, ClientID: hubClient, ClientSecret: "hub-secret"})
	if err != nil {
		t.Fatal(err)
	}
	m := isfcsync.NewMetrics(nil, nil)
	return &rig{t: t, pool: pool, pub: pub, mock: mock, m: m,
		rec: &isfcsync.Reconciler{Source: client, Editor: pub, Store: isfcsync.PGStore{Pool: pool}, Metrics: m,
			Config: isfcsync.Config{ConsoleClientID: console, BackendClientID: hubClient}}}
}

func (r *rig) snapshot() *registry.Snapshot {
	r.t.Helper()
	_, snap, err := r.pub.Current(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	return snap
}

func (r *rig) run(trigger string, opt isfcsync.RunOptions) isfcsync.Result {
	r.t.Helper()
	res, err := r.rec.Run(r.t.Context(), trigger, opt)
	if err != nil {
		r.t.Fatalf("Run: %v", err)
	}
	return res
}

func (r *rig) changes(actor string) int {
	r.t.Helper()
	var n int
	if err := r.pool.QueryRow(r.t.Context(), `SELECT count(*) FROM hub.change_log WHERE actor = $1`, actor).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// TestTenantLifecycleAgainstMockISFC walks a tenant through every row of AUTH_PLAN §2.1 against
// real Postgres, the real publisher and the fake ISFC.
func TestTenantLifecycleAgainstMockISFC(t *testing.T) {
	r := newRig(t)
	legacy := len(r.snapshot().Tenants)
	hl := r.mock.AddTenant("HL", "Home Loans")
	pl := r.mock.AddTenant("PL", "Personal Loans")
	r.mock.SetClientTenants(console, hl, pl)
	before := r.changes(isfcsync.Actor)

	// First sight: both created, pending, active.
	res := r.run(isfcsync.TriggerManual, isfcsync.RunOptions{Reason: "link HL and PL"})
	if res.Outcome != "ok" || res.Created != 2 || res.ChangeID == "" {
		t.Fatalf("first run %+v", res)
	}
	snap := r.snapshot()
	hlT, ok := snap.TenantByISFC(hl)
	if !ok || hlT.Tenant != "hl-t" || hlT.SetupState() != "pending" || hlT.Status != "active" || hlT.Disabled || hlT.ISFC.Name != "Home Loans" {
		t.Fatalf("created tenant %+v isfc %+v", hlT, hlT.ISFC)
	}
	if len(snap.Tenants) != legacy+2 {
		t.Fatalf("tenants %d, want %d", len(snap.Tenants), legacy+2)
	}
	if got := r.changes(isfcsync.Actor); got != before+1 {
		t.Fatalf("one change-log entry per run: %d -> %d", before, got)
	}

	// Nothing changed: a tick makes no change and no new snapshot or change-log row.
	v := snap.Version
	if res := r.run(isfcsync.TriggerTick, isfcsync.RunOptions{}); res.Created+res.Updated+res.Disabled+res.Enabled != 0 || res.Unchanged != 2 {
		t.Fatalf("idle tick %+v", res)
	}
	if got := r.changes(isfcsync.Actor); got != before+1 || r.snapshot().Version != v {
		t.Fatalf("idle tick wrote something: changes %d, snapshot %d -> %d", got, v, r.snapshot().Version)
	}
	// A manual run with nothing to change still writes its one change-log entry.
	if res := r.run(isfcsync.TriggerManual, isfcsync.RunOptions{Reason: "check"}); res.ChangeID == "" || r.changes(isfcsync.Actor) != before+2 {
		t.Fatalf("manual noop %+v changes %d", res, r.changes(isfcsync.Actor))
	}

	// Renamed in ISFC: only the copy changes; the Hub id stays.
	r.mock.RenameTenant(hl, "HLN", "Housing Loans")
	if res := r.run(isfcsync.TriggerTick, isfcsync.RunOptions{}); res.Updated != 1 {
		t.Fatalf("rename %+v", res)
	}
	if tn, _ := r.snapshot().TenantByISFC(hl); tn.Tenant != "hl-t" || tn.ISFC.Name != "Housing Loans" || tn.ISFC.Key != "HLN" {
		t.Fatalf("after rename %+v", tn)
	}

	// INACTIVE in ISFC: disabled, never deleted. The Hub suspends the other tenant itself.
	r.mock.SetTenantStatus(pl, "INACTIVE")
	if res := r.run(isfcsync.TriggerEvent, isfcsync.RunOptions{}); res.Disabled != 1 {
		t.Fatalf("deactivate %+v", res)
	}
	snap = r.snapshot()
	plT, ok := snap.TenantByISFC(pl)
	if !ok || !plT.Disabled || !plT.ISFC.DisabledBySync || plT.ISFC.Status != "INACTIVE" {
		t.Fatalf("deactivated %+v", plT)
	}

	// Re-activated and still linked (INACTIVE hid it from the panel's list): enabled again.
	r.mock.SetTenantStatus(pl, "ACTIVE")
	if res := r.run(isfcsync.TriggerEvent, isfcsync.RunOptions{}); res.Enabled != 1 {
		t.Fatalf("reactivate %+v", res)
	}
	if plT, _ = r.snapshot().TenantByISFC(pl); plT.Disabled || !plT.ISFC.Linked {
		t.Fatalf("reactivated %+v", plT)
	}

	// Unlinked from the console client: disabled.
	r.mock.SetClientTenants(console, hl)
	if res := r.run(isfcsync.TriggerTick, isfcsync.RunOptions{}); res.Disabled != 1 {
		t.Fatalf("unlink %+v", res)
	}

	// ISFC unreachable: the last state is kept, nothing is disabled, the run is a failed outcome.
	r.mock.SetDown(true)
	res = r.run(isfcsync.TriggerManual, isfcsync.RunOptions{Reason: "while ISFC is down"})
	if res.Outcome != "failed" || res.Error == "" || res.ChangeID == "" {
		t.Fatalf("down %+v", res)
	}
	if tn, _ := r.snapshot().TenantByISFC(hl); tn.Disabled {
		t.Fatal("a failed fetch disabled a tenant")
	}
	var failed int
	if err := r.pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.isfc_sync_runs WHERE id = $1 AND result = 'failed' AND error IS NOT NULL`, res.RunID).Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("failed run not recorded: %d %v", failed, err)
	}
	r.mock.SetDown(false)

	// Dry run: a new tenant is reported, nothing is written.
	nu := r.mock.AddTenant("NEW", "New Tenant")
	r.mock.SetClientTenants(console, hl, nu)
	r.rec.Config.DryRun = true
	v = r.snapshot().Version
	if res := r.run(isfcsync.TriggerManual, isfcsync.RunOptions{}); !res.DryRun || res.Created != 1 || res.ChangeID != "" {
		t.Fatalf("dry run %+v", res)
	}
	if _, ok := r.snapshot().TenantByISFC(nu); ok || r.snapshot().Version != v {
		t.Fatal("a dry run wrote config")
	}
	r.rec.Config.DryRun = false

	// One tenant only.
	if res := r.run(isfcsync.TriggerManual, isfcsync.RunOptions{Tenant: "hl-t", Reason: "one"}); res.Created != 0 {
		t.Fatalf("single sync created %+v", res)
	}
	if _, err := r.rec.Run(t.Context(), isfcsync.TriggerManual, isfcsync.RunOptions{Tenant: "nope-tenant"}); !errors.Is(err, control.ErrNotFound) {
		t.Fatalf("unknown tenant: %v", err)
	}
	if _, err := r.rec.Run(t.Context(), isfcsync.TriggerManual, isfcsync.RunOptions{Tenant: "orders-service"}); !errors.Is(err, isfcsync.ErrNotLinked) {
		t.Fatalf("legacy tenant: %v", err)
	}
}

func TestRunLockMakesConcurrentRunsBusy(t *testing.T) {
	r := newRig(t)
	unlock, ok, err := isfcsync.PGStore{Pool: r.pool}.TryRunLock(t.Context())
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if _, err := r.rec.Run(t.Context(), isfcsync.TriggerManual, isfcsync.RunOptions{}); !errors.Is(err, isfcsync.ErrBusy) {
		t.Fatalf("want ErrBusy while another process holds the lock, got %v", err)
	}
	unlock()
	if _, err := r.rec.Run(t.Context(), isfcsync.TriggerManual, isfcsync.RunOptions{}); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

func TestPGElectorHasOneLeader(t *testing.T) {
	r := newRig(t)
	e := isfcsync.PGElector{Pool: r.pool}
	a, ok, err := e.TryLead(t.Context())
	if err != nil || !ok || !a.Held(t.Context()) {
		t.Fatalf("first: %v %v", ok, err)
	}
	if _, ok, err := e.TryLead(t.Context()); err != nil || ok {
		t.Fatalf("second elector became leader too: %v %v", ok, err)
	}
	a.Release()
	b, ok, err := e.TryLead(context.WithoutCancel(t.Context()))
	if err != nil || !ok {
		t.Fatalf("after release: %v %v", ok, err)
	}
	b.Release()
}

// TestEventsOnlyTriggerAndDuplicatesAreHarmless: a burst of duplicate, reordered events (one of them
// announcing a change that is not visible yet) converges to ISFC's state in a handful of runs.
func TestEventsOnlyTriggerAndDuplicatesAreHarmless(t *testing.T) {
	r := newRig(t)
	reg := prometheus.NewRegistry()
	r.rec.Metrics = isfcsync.NewMetrics(reg, nil)
	a := r.mock.AddTenant("AAA", "Tenant A")
	r.mock.SetClientTenants(console, a)
	loop := &isfcsync.Loop{Runner: r.rec, Elector: isfcsync.PGElector{Pool: r.pool}, Interval: time.Hour, Retry: 20 * time.Millisecond, Metrics: r.rec.Metrics}
	fc := &isfcsync.FakeConsumer{}
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() { loop.Run(ctx) })
	wg.Go(func() { _ = isfcsync.Consume(ctx, fc, loop.Trigger) })
	defer func() { cancel(); wg.Wait() }()

	waitFor := func(what string, ok func(*registry.Snapshot) bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if ok(r.snapshot()) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the start-up run to create tenant A", func(s *registry.Snapshot) bool { _, ok := s.TenantByISFC(a); return ok })

	b := r.mock.AddTenant("BBB", "Tenant B")
	r.mock.SetClientTenants(console, a, b)
	r.mock.SetTenantStatus(a, "INACTIVE")
	for i := range 100 {
		fc.Send(isfcsync.Event{EventID: fmt.Sprintf("e%d", i%4), Type: []string{"tenant.updated", "client.tenants_changed", "tenant.created"}[i%3], OccurredAt: time.Unix(int64(5000-i), 0)})
	}
	waitFor("B created and A disabled", func(s *registry.Snapshot) bool {
		tb, okB := s.TenantByISFC(b)
		ta, okA := s.TenantByISFC(a)
		return okB && okA && tb.Setup == "pending" && ta.Disabled
	})
	time.Sleep(200 * time.Millisecond)
	if n := testutil.ToFloat64(r.rec.Metrics.Runs.WithLabelValues(isfcsync.TriggerEvent, "ok")); n < 1 || n > 8 {
		t.Fatalf("100 duplicate events caused %v event runs", n)
	}
}
