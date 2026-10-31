//go:build integration

package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

var testCreds = map[string]string{"account_id": "fake-acct-W0RK", "passcode": "fake-pass-W0RK"}

type staticLoader map[string]string

func (l staticLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: l}}
	}
	return out, nil
}

type staticSnaps struct {
	s atomic.Pointer[registry.Snapshot]
}

func (s *staticSnaps) Snapshot() *registry.Snapshot { return s.s.Load() }

type sink struct {
	mu sync.Mutex
	b  []audit.Batch
}

func (s *sink) Emit(b audit.Batch) { s.mu.Lock(); s.b = append(s.b, b); s.mu.Unlock() }

func (s *sink) attempts() []audit.Attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Attempt
	for _, b := range s.b {
		out = append(out, b.Attempts...)
	}
	return out
}

type rig struct {
	t     *testing.T
	db    *pgxpool.Pool
	mv    *mockvendor.Instance
	snaps *staticSnaps
	w     *Worker
	sink  *sink
	clock atomic.Pointer[time.Time]
	ended []TerminalEvent
	mu    sync.Mutex
}

func (r *rig) now() time.Time { return *r.clock.Load() }
func (r *rig) advance(d time.Duration) {
	t := r.now().Add(d)
	r.clock.Store(&t)
}

func devSnap(t *testing.T, vendorURL string) *registry.Snapshot {
	snap, err := registry.Build(registry.Sources{Config: os.DirFS("../../config"), Contracts: os.DirFS("../../../contracts")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var st registry.Store
	snap = st.Swap(snap)
	cp := *snap
	cp.Vendors = map[string]*registry.Vendor{}
	for k, v := range snap.Vendors {
		vc := *v
		vc.Regions = map[string]string{}
		for r := range v.Regions {
			vc.Regions[r] = vendorURL
		}
		cp.Vendors[k] = &vc
	}
	return &cp
}

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
	db, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := (audit.PGStore{Pool: db}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -1), 3); err != nil {
		t.Fatal(err)
	}
	// Other tests share hub.outbox: push their due rows out of this test's way.
	if _, err := db.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 day', claimed_until = now() + interval '1 day' WHERE pool = 'messaging'`); err != nil {
		t.Fatal(err)
	}

	r := &rig{t: t, db: db, mv: mockvendor.Start(t), snaps: &staticSnaps{}, sink: &sink{}}
	start := time.Now().UTC()
	r.clock.Store(&start)
	r.snaps.s.Store(devSnap(t, r.mv.VendorURL))
	m := telemetry.NewMetrics("worker", "test")
	cache, err := creds.NewCache(staticLoader(testCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), creds.AccountIDs(r.snaps.Snapshot())); err != nil {
		t.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	q := &queue.Outbox{Now: r.now}
	log := slog.New(slog.DiscardHandler)
	r.w = &Worker{
		Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: db, Queue: q,
		Transitions: &Transitions{DB: db, Queue: q, Now: r.now, OnTerminal: func(_ context.Context, _ pgx.Tx, ev TerminalEvent) error {
			r.mu.Lock()
			r.ended = append(r.ended, ev)
			r.mu.Unlock()
			return nil
		}},
		Snapshots: r.snaps, Attempter: &Attempter{Creds: cache, Upstream: client, Log: log, Now: r.now},
		Audit: r.sink, Masker: audit.NewMasker([]byte("worker-test-mask-key-0123456789ab")),
		Backoff: Backoff{Rand: func() float64 { return 0.5 }}, Budget: NewBudget(nil), Metrics: m, Log: log, Now: r.now,
	}
	return r
}

func (r *rig) route(b mockvendor.Behaviour) {
	r.mv.Reset(r.t)
	r.mv.SetRoute(r.t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b})
}

var okBody = json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)

// enqueue queues an events.track request the way the edge does: requests row + outbox in one TX.
func (r *rig) enqueue(integration string, ttl time.Duration) core.Envelope {
	r.t.Helper()
	now := r.now()
	env := core.Envelope{
		RequestID: "req_" + core.NewULID(now), Tenant: "orders-service", IntegrationID: integration, ResolvedVersion: 2,
		Capability: "events.track@v1", Operation: "default", Mode: "async",
		Canonical:     json.RawMessage(`{"user":{"id":"u_48213"},"event":{"name":"order_placed","timestamp":"2026-10-31T09:15:00Z"}}`),
		NextAttemptAt: now.Add(-time.Millisecond), ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	err := pgx.BeginFunc(r.t.Context(), r.db, func(tx pgx.Tx) error {
		if err := (audit.Ledger{}).Insert(r.t.Context(), tx, audit.Record{RequestID: env.RequestID, CreatedAt: now, TenantID: env.Tenant,
			IntegrationID: integration, Capability: env.Capability, Operation: "default", Mode: "async", Status: "accepted", PayloadPolicy: "masked"}); err != nil {
			return err
		}
		return r.w.Queue.Enqueue(r.t.Context(), tx, env, "")
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = r.db.Exec(ctx, `DELETE FROM hub.outbox WHERE request_id = $1`, env.RequestID)
		_, _ = r.db.Exec(ctx, `DELETE FROM hub.dead_letters WHERE request_id = $1`, env.RequestID)
	})
	return env
}

func (r *rig) status(id string) (string, int) {
	var s string
	var n int
	from, _ := core.RequestIDTime(id)
	if err := r.db.QueryRow(r.t.Context(), `SELECT status, attempts FROM hub.requests WHERE request_id = $1 AND created_at BETWEEN $2 AND $3`,
		id, from.Add(-time.Minute), from.Add(time.Minute)).Scan(&s, &n); err != nil {
		r.t.Fatal(err)
	}
	return s, n
}

func (r *rig) process() int {
	r.t.Helper()
	n, err := r.w.ProcessOnce(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestWorkerDeliversAndReTransformsAtSendTime(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	// The envelope was accepted under version 2; version 1 is active by the time it is sent.
	snap := r.snaps.Snapshot()
	cp := *snap
	cp.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		if k == "int_evt_brandA" {
			c.ActiveVersion = 1
		}
		cp.Integrations[k] = &c
	}
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.snaps.s.Store(&cp)
	if r.process() != 1 {
		t.Fatal("not claimed")
	}
	if s, n := r.status(env.RequestID); s != "succeeded" || n != 1 {
		t.Fatalf("status %s attempts %d", s, n)
	}
	reqs := r.mv.Requests(t, nil)
	if len(reqs) != 1 || !strings.Contains(string(reqs[0].Body), `"identity":"u_48213"`) {
		t.Fatalf("vendor saw %+v", reqs)
	}
	if strings.Contains(string(env.Canonical), "identity") || strings.Contains(string(env.Canonical), "evtName") {
		t.Fatal("the envelope must hold the canonical body only")
	}
	if a := r.sink.attempts(); len(a) != 1 || a[0].IntegrationVersion != 1 || a[0].HTTPStatus != 200 {
		t.Fatalf("attempt rows %+v", a)
	}
	var result map[string]any
	if err := r.db.QueryRow(t.Context(), `SELECT result FROM hub.request_results WHERE request_id = $1`, env.RequestID).Scan(&result); err != nil || result["status"] != "ok" {
		t.Fatalf("result %v %v", result, err)
	}
	if len(r.ended) != 1 || r.ended[0].State != core.StateSucceeded {
		t.Fatalf("terminal events %+v", r.ended)
	}
}

func TestWorkerRetriesThenSucceeds(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 503, Body: json.RawMessage(`{"status":"fail","error":"down"}`)})
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.process()
	if s, n := r.status(env.RequestID); s != "retry_scheduled" || n != 1 {
		t.Fatalf("after a 503: %s %d", s, n)
	}
	var next time.Time
	_ = r.db.QueryRow(t.Context(), `SELECT next_attempt_at FROM hub.outbox WHERE request_id = $1`, env.RequestID).Scan(&next)
	if d := next.Sub(r.now()); d < 0 || d > time.Second {
		t.Fatalf("next attempt in %s, want within the first 1 s step", d)
	}
	if r.process() != 0 {
		t.Fatal("claimed before it was due")
	}
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	r.advance(2 * time.Second)
	r.process()
	if s, n := r.status(env.RequestID); s != "succeeded" || n != 2 {
		t.Fatalf("after the retry: %s %d", s, n)
	}
}

func TestWorkerDeadLettersRejections(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 400, Body: json.RawMessage(`{"status":"fail","error":"Invalid event name"}`)})
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.process()
	if s, _ := r.status(env.RequestID); s != "dead_lettered" {
		t.Fatalf("status %s", s)
	}
	var reason string
	var resp []byte
	if err := r.db.QueryRow(t.Context(), `SELECT reason, last_vendor_response FROM hub.dead_letters WHERE request_id = $1`, env.RequestID).Scan(&reason, &resp); err != nil {
		t.Fatal(err)
	}
	if reason != "UPSTREAM_REJECTED" || !strings.Contains(string(resp), "400") || strings.Contains(string(resp), testCreds["passcode"]) {
		t.Fatalf("dead letter %s %s", reason, resp)
	}
	if len(r.mv.Requests(t, nil)) != 1 {
		t.Fatal("a rejection must not be retried")
	}
}

func TestPoisonAfterTwoCrashes(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	for range 2 { // a worker claims and dies, twice
		if got, _ := r.w.Queue.Claim(t.Context(), r.db, "doomed", queue.PoolMessaging, 1); len(got) != 1 {
			t.Fatal("claim")
		}
		r.advance(31 * time.Second)
	}
	r.process()
	if s, _ := r.status(env.RequestID); s != "dead_lettered" {
		t.Fatalf("status %s", s)
	}
	var reason string
	_ = r.db.QueryRow(t.Context(), `SELECT reason FROM hub.dead_letters WHERE request_id = $1`, env.RequestID).Scan(&reason)
	if reason != "poison" || len(r.mv.Requests(t, nil)) != 0 {
		t.Fatalf("reason %s vendor calls %d", reason, len(r.mv.Requests(t, nil)))
	}
}

func TestExpiredBeforeAttempt(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	env := r.enqueue("int_evt_brandA", time.Minute)
	r.advance(2 * time.Minute)
	r.process()
	if s, _ := r.status(env.RequestID); s != "expired" {
		t.Fatalf("status %s", s)
	}
	if len(r.mv.Requests(t, nil)) != 0 || len(r.ended) != 1 || r.ended[0].Err.Code != core.CodeExpired {
		t.Fatalf("vendor calls %d, terminal %+v", len(r.mv.Requests(t, nil)), r.ended)
	}
}

func TestPausedIntegrationIsHeldWithoutAnAttempt(t *testing.T) {
	r := newRig(t)
	r.route(mockvendor.Behaviour{Status: 200, Body: okBody})
	snap := r.snaps.Snapshot()
	cp := *snap
	cp.Integrations = map[string]*registry.Integration{}
	for k, v := range snap.Integrations {
		c := *v
		c.Paused = true
		cp.Integrations[k] = &c
	}
	r.snaps.s.Store(&cp)
	env := r.enqueue("int_evt_brandA", 6*time.Hour)
	r.process()
	var status string
	var attempt int
	_ = r.db.QueryRow(t.Context(), `SELECT status, attempt FROM hub.outbox WHERE request_id = $1`, env.RequestID).Scan(&status, &attempt)
	if status != "accepted" || attempt != 0 || len(r.mv.Requests(t, nil)) != 0 {
		t.Fatalf("paused: %s attempt %d", status, attempt)
	}
}
