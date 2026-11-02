//go:build integration

package control_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func publisher(t *testing.T) (*control.Publisher, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("HUB_PG_DSN")
	if dsn == "" {
		t.Fatal("HUB_PG_DSN not set; run via `make itest`")
	}
	sdb, _ := sql.Open("pgx", dsn)
	if _, err := migrations.Up(t.Context(), sdb); err != nil {
		t.Fatal(err)
	}
	_ = sdb.Close()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// The archive is global: start each test from the dev config tree.
	if _, err := pool.Exec(t.Context(), `DELETE FROM hub.config_snapshots`); err != nil {
		t.Fatal(err)
	}
	blobs := control.DirBlobStore{Root: t.TempDir()}
	return &control.Publisher{
		Pool: pool, Archive: control.NewArchive(pool, blobs),
		Bootstrap: registry.Sources{Config: os.DirFS("../../config"), Contracts: os.DirFS("../../../contracts")},
	}, pool
}

// spec is int_evt_brandA's v2 with a different response transformer and account.
func spec(account, response string) []byte {
	return []byte(`
targets:
  - vendor: clevertap
    account: ` + account + `
    transformer: { request: T1@7, response: ` + response + ` }
    weight: 100
delivery: { mode: wait, wait_ms: 3000, ttl: 6h, on_circuit_open: fail_fast, pending: re_resolve, polling: true }
audit: { payloads: masked, retention: 30d }
`)
}

func TestPublishValidation(t *testing.T) {
	p, _ := publisher(t)
	ctx := t.Context()
	cases := []struct {
		name, spec, author, reason string
		wantErr                    string
	}{
		{"unknown transformer", string(spec("ct-brandA-in", "R9@1")), "a", "r", "R9@1"},
		{"unknown account", string(spec("ct-nope", "R1@2")), "a", "r", "ct-nope"},
		{"wait window out of range", strings.Replace(string(spec("ct-brandA-in", "R1@2")), "wait_ms: 3000", "wait_ms: 999999", 1), "a", "r", "wait_ms"},
		{"failover policy without a chain", strings.Replace(string(spec("ct-brandA-in", "R1@2")), "fail_fast", "failover", 1), "a", "r", "failover"},
		{"invalid YAML", "targets: [", "a", "r", "version document"},
		{"no author", string(spec("ct-brandA-in", "R1@2")), "", "r", "author"},
		{"no reason", string(spec("ct-brandA-in", "R1@2")), "a", " ", "reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.PublishVersion(ctx, "int_evt_brandA", []byte(tc.spec), true, tc.author, tc.reason)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err %v, want it to mention %q", err, tc.wantErr)
			}
			var ce registry.ConfigErrors
			if errors.As(err, &ce) && (ce[0].File == "" || ce[0].Path == "") {
				t.Fatalf("validation errors must be path-qualified: %+v", ce)
			}
		})
	}
	if _, snap, _ := p.Current(ctx); len(snap.Integrations["int_evt_brandA"].Versions) != 2 {
		t.Fatal("a refused publish changed the tree")
	}
}

func TestPublishImmutableLinearRollback(t *testing.T) {
	p, pool := publisher(t)
	ctx := t.Context()
	r3, err := p.PublishVersion(ctx, "int_evt_brandA", spec("ct-brandA-in", "R2@1"), true, "priya@example.com", "minimal response")
	if err != nil {
		t.Fatal(err)
	}
	if r3.Version != 3 || r3.Snapshot.Integrations["int_evt_brandA"].ActiveVersion != 3 || r3.ChangeID == "" {
		t.Fatalf("publish %+v", r3)
	}
	r4, err := p.PublishVersion(ctx, "int_evt_brandA", spec("ct-brandA-in", "R1@2"), false, "priya@example.com", "staged, not active")
	if err != nil || r4.Version != 4 || r4.Snapshot.Integrations["int_evt_brandA"].ActiveVersion != 3 {
		t.Fatalf("staged publish %+v %v", r4, err)
	}
	// Rollback to v1: v5 = v1's content, active.
	r5, err := p.Rollback(ctx, "int_evt_brandA", 1, "priya@example.com", "v3 drops vendor_ref")
	if err != nil {
		t.Fatal(err)
	}
	in := r5.Snapshot.Integrations["int_evt_brandA"]
	if r5.Version != 5 || in.ActiveVersion != 5 {
		t.Fatalf("rollback %+v", r5)
	}
	if in.Versions[5].Targets[0].Transformer != in.Versions[1].Targets[0].Transformer || in.Versions[5].Targets[0].Account != in.Versions[1].Targets[0].Account {
		t.Fatalf("v5 is not v1's content: %+v vs %+v", in.Versions[5].Targets, in.Versions[1].Targets)
	}
	for v := 1; v <= 5; v++ { // strictly increasing, no gaps, nothing deleted
		if in.Versions[v] == nil {
			t.Fatalf("version %d missing", v)
		}
	}
	if in.Versions[3].Targets[0].Transformer.Response.String() != "R2@1" {
		t.Fatal("an older version changed")
	}
	if !strings.Contains(in.Versions[5].Publish.Reason, "rollback to v1") || in.Versions[5].Publish.Author != "priya@example.com" {
		t.Fatalf("publish meta %+v", in.Versions[5].Publish)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hub.change_log WHERE change_id = ANY($1)`, []string{r3.ChangeID, r4.ChangeID, r5.ChangeID}).Scan(&n)
	if n != 3 {
		t.Fatalf("%d change log rows", n)
	}
	if _, err := pool.Exec(ctx, `UPDATE hub.change_log SET reason = 'x' WHERE change_id = $1`, r3.ChangeID); err == nil {
		t.Fatal("change_log must be append-only")
	}
}

func TestAccountLifecycle(t *testing.T) {
	p, _ := publisher(t)
	ctx := t.Context()
	pinned, sessions := 2, 1
	p.Guard = func(context.Context, *registry.Snapshot, string) (int, int, error) { return pinned, sessions, nil }
	// Move int_evt_brandA off ct-brandA-in: the account drains automatically.
	r, err := p.PublishVersion(ctx, "int_evt_brandA", spec("ct-brandB-us", "R1@2"), true, "ops@example.com", "move brand A to the US account")
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot.Accounts["ct-brandA-in"].State != registry.AccountDraining || len(r.Drained) != 1 {
		t.Fatalf("ct-brandA-in is %s (drained %v)", r.Snapshot.Accounts["ct-brandA-in"].State, r.Drained)
	}
	_, err = p.RetireAccount(ctx, "ct-brandA-in", "ops@example.com", "decommission")
	var blocked *control.RetireBlockedError
	if !errors.As(err, &blocked) || blocked.Pinned != 2 || blocked.Sessions != 1 {
		t.Fatalf("retire with references: %v", err)
	}
	pinned, sessions = 0, 0
	r, err = p.RetireAccount(ctx, "ct-brandA-in", "ops@example.com", "decommission")
	if err != nil || r.Snapshot.Accounts["ct-brandA-in"].State != registry.AccountRetired {
		t.Fatalf("retire at zero: %v", err)
	}
	if _, err := p.PublishVersion(ctx, "int_evt_brandA", spec("ct-brandA-in", "R1@2"), true, "ops@example.com", "back"); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a retired account must not be referenced: %v", err)
	}
}

// TestRetireGuardCountsPinnedWorkAndSessions is HUB-176/184: the real guard refuses a retire
// while a pinned or session-bound envelope will still be sent on ct-brandA-in, or a vendor
// session is open on it, and lets it through at zero.
func TestRetireGuardCountsPinnedWorkAndSessions(t *testing.T) {
	p, pool := publisher(t)
	ctx := t.Context()
	p.Guard = control.RetireGuard(pool)
	// Other tests share hub.vendor_sessions and hub.outbox: start from no references.
	if _, err := pool.Exec(ctx, `DELETE FROM hub.vendor_sessions WHERE account_id = 'ct-brandA-in'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM hub.outbox WHERE integration_id = 'int_evt_brandA'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.PublishVersion(ctx, "int_evt_brandA", spec("ct-brandB-us", "R1@2"), true, "ops@example.com", "move brand A to the US account"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	env := core.Envelope{RequestID: "req_" + core.NewULID(now), Tenant: "orders-service", IntegrationID: "int_evt_brandA", ResolvedVersion: 2,
		Pinned: true, Capability: "events.track@v1", Operation: "default", Mode: "async", Canonical: json.RawMessage(`{}`),
		NextAttemptAt: now.Add(time.Hour), ExpiresAt: now.Add(6 * time.Hour), CreatedAt: now}
	if err := (&queue.Outbox{}).Enqueue(ctx, pool, env); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.outbox WHERE request_id = $1`, env.RequestID)
	})
	ses, err := sessions.Create(ctx, pool, sessions.Open{TenantID: "orders-service", IntegrationID: "int_evt_brandA", Version: 2,
		AccountID: "ct-brandA-in", Data: map[string]any{"step": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.vendor_sessions WHERE session_id = $1`, ses.ID)
	})

	_, err = p.RetireAccount(ctx, "ct-brandA-in", "ops@example.com", "decommission")
	var blocked *control.RetireBlockedError
	if !errors.As(err, &blocked) || blocked.Pinned != 1 || blocked.Sessions != 1 {
		t.Fatalf("retire with a pinned envelope and a session: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = $1`, env.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM hub.vendor_sessions WHERE session_id = $1`, ses.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := p.RetireAccount(ctx, "ct-brandA-in", "ops@example.com", "decommission"); err != nil || r.Snapshot.Accounts["ct-brandA-in"].State != registry.AccountRetired {
		t.Fatalf("retire at zero: %v", err)
	}
}
