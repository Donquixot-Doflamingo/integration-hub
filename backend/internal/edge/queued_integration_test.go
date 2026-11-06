//go:build integration

package edge

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

type queuedRig struct {
	h      http.Handler
	mv     *mockvendor.Instance
	db     *pgxpool.Pool
	worker *delivery.Worker
	spec   *contract.Spec
}

// queuedSnapshot points vendors at mv and lets each test tune integrations. base builds the
// registry (default e2eSnapshot, the dev config).
func queuedSnapshot(t *testing.T, base func(*testing.T, string) *registry.Snapshot, vendorURL string, tune func(id string, in *registry.Integration, v *registry.IntegrationVersion)) *registry.Snapshot {
	snap := base(t, vendorURL)
	for _, v := range snap.Vendors { // e2eSnapshot lowers timeouts to 300 ms; queued tests need room
		for name, ep := range v.Endpoints {
			ep.TimeoutMS = 3000
			v.Endpoints[name] = ep
		}
	}
	cp := *snap
	cp.Integrations = map[string]*registry.Integration{}
	for id, in := range snap.Integrations {
		c := *in
		c.Versions = map[int]*registry.IntegrationVersion{}
		for n, v := range in.Versions {
			vc := *v
			if tune != nil {
				tune(id, &c, &vc)
			}
			c.Versions[n] = &vc
		}
		cp.Integrations[id] = &c
	}
	return &cp
}

func newQueuedRig(t *testing.T, redisAddr string, tune func(string, *registry.Integration, *registry.IntegrationVersion), cfg ...func(*Deps, *delivery.Worker)) *queuedRig {
	t.Helper()
	return newQueuedRigFrom(t, e2eSnapshot, redisAddr, tune, cfg...)
}

// newQueuedRigFrom is newQueuedRig on the registry base builds.
func newQueuedRigFrom(t *testing.T, base func(*testing.T, string) *registry.Snapshot, redisAddr string, tune func(string, *registry.Integration, *registry.IntegrationVersion), cfg ...func(*Deps, *delivery.Worker)) *queuedRig {
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
	if _, err := db.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 day', claimed_until = now() + interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	mv := mockvendor.Start(t)
	snap := queuedSnapshot(t, base, mv.VendorURL, tune)
	m := telemetry.NewMetrics("gateway", "itest")
	log := slog.New(slog.DiscardHandler)
	cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	q := &queue.Outbox{}
	tr := &delivery.Transitions{DB: db, Queue: q}
	settler := delivery.Settler{Transitions: tr, Backoff: delivery.Backoff{}, Budget: delivery.NewBudget(nil)}
	attempter := &delivery.Attempter{Creds: cache, Upstream: client, Log: log}
	d := Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Metrics: m, Log: log,
		Queueing: &Queueing{DB: db, Queue: q, Settler: settler, Name: "gw-" + core.NewULID(time.Now())}}
	if redisAddr != "" {
		rdb := redis.NewClient(&redis.Options{Addr: redisAddr, DialTimeout: 200 * time.Millisecond, ReadTimeout: 200 * time.Millisecond,
			WriteTimeout: 200 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true})
		t.Cleanup(func() { _ = rdb.Close() })
		d.Idempotency = idempotency.New(rdb, m, idempotency.Options{Prefix: "itest:edge:" + core.NewULID(time.Now()) + ":"})
	}
	w := &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: db, Queue: q, Transitions: tr,
		Snapshots: staticSnapshots{snap}, Attempter: attempter, Audit: audit.Discard{}, Budget: delivery.NewBudget(nil), Log: log}
	for _, f := range cfg {
		f(&d, w)
	}
	return &queuedRig{h: New(d).Handler(), mv: mv, db: db, worker: w, spec: contract.Load(t, "hub-public.v1.yaml")}
}

func (q *queuedRig) route(t *testing.T, b mockvendor.Behaviour) {
	q.mv.Reset(t)
	q.mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b})
}

func (q *queuedRig) invoke(t *testing.T, integration, body string, hdr ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/"+integration+"/invoke", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := q.spec.Serve(t, q.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (q *queuedRig) status(t *testing.T, key, id string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/requests/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := q.spec.Serve(t, q.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

var okVendor = json.RawMessage(`{"status":"success","processed":1,"unprocessed":[]}`)

func TestAsyncAcceptThenWorkerDelivers(t *testing.T) {
	q := newQueuedRig(t, "", nil)
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	rec, body := q.invoke(t, "int_evt_brandB", trackBody) // async_callback
	if rec.Code != 202 || body["status"] != "accepted" || body["reason"] != "async_mode" || rec.Header().Get("Location") != body["poll"] {
		t.Fatalf("%d %v %v", rec.Code, body, rec.Header())
	}
	id := body["request_id"].(string)
	if n := len(q.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("async reached the vendor before the worker: %d", n)
	}
	if code, st := q.status(t, devKey, id); code != 200 || st["status"] != "accepted" || st["result"] != nil {
		t.Fatalf("queued status %d %v", code, st)
	}
	if n, err := q.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	code, st := q.status(t, devKey, id)
	if code != 200 || st["status"] != "succeeded" || st["attempts"] != 1.0 || st["completed_at"] == nil {
		t.Fatalf("final status %d %v", code, st)
	}
	if res, ok := st["result"].(map[string]any); !ok || res["status"] != "ok" {
		t.Fatalf("result %v", st["result"])
	}
	// Tenant-scoped: another tenant's key sees 404, never 403.
	other := withTenants(t, devSnapshot(t), &registry.Tenant{Tenant: "growth-team", APIKeys: []registry.APIKey{
		{KeyID: "k", Hash: HashKey(growthKey), Scopes: []string{"invoke"}, State: "active"}}})
	q2 := *q
	q2.h = New(Deps{Snapshots: staticSnapshots{other}, Queueing: &Queueing{DB: q.db}}).Handler()
	if code, st := q2.status(t, growthKey, id); code != 404 || st["error"].(map[string]any)["code"] != "REQUEST_NOT_FOUND" {
		t.Fatalf("other tenant %d %v", code, st)
	}
	if code, _ := q.status(t, devKey, "req_"+core.NewULID(time.Now())); code != 404 {
		t.Fatalf("unknown id %d", code)
	}
}

func TestWaitModeWithinWindow(t *testing.T) {
	q := newQueuedRig(t, "", nil)
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 100}})
	rec, body := q.invoke(t, "int_evt_brandA", trackBody) // wait, 3000 ms
	if rec.Code != 200 || body["status"] != "ok" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	id := rec.Header().Get("X-Hub-Request-Id")
	if _, st := q.status(t, devKey, id); st["status"] != "succeeded" || st["mode"] != "wait" {
		t.Fatalf("status %v", st)
	}
}

// TestWaitModeWindowElapses: vendor slower than wait_ms → 202 in_flight, the gateway's own
// attempt finishes, and a worker running meanwhile never delivers it too.
func TestWaitModeWindowElapses(t *testing.T) {
	q := newQueuedRig(t, "", func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandA" {
			v.Delivery.WaitMS = 300
		}
	})
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 800}})
	rec, body := q.invoke(t, "int_evt_brandA", trackBody) // wait 300 ms, vendor 800 ms, endpoint timeout 3 s
	if rec.Code != 202 || body["status"] != "in_flight" || body["reason"] != "wait_window_elapsed" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	id := body["request_id"].(string)
	for range 5 { // a worker polling meanwhile must not claim the gateway's envelope
		if n, _ := q.worker.ProcessOnce(t.Context()); n != 0 {
			t.Fatal("worker claimed an envelope the gateway is still attempting")
		}
	}
	eventually(t, "the gateway attempt to finish", func() bool {
		_, st := q.status(t, devKey, id)
		return st["status"] == "succeeded" || st["status"] == "dead_lettered" || st["status"] == "retry_scheduled"
	})
	_, st := q.status(t, devKey, id)
	calls := 0
	for _, r := range q.mv.Requests(t, nil) {
		if strings.Contains(string(r.Body), "u_48213") {
			calls++
		}
	}
	if st["status"] != "succeeded" || st["attempts"] != 1.0 || calls != 1 {
		t.Fatalf("final %v after %d vendor calls", st, calls)
	}
}

func TestIdempotentReplayAndConflict(t *testing.T) {
	addr := os.Getenv("HUB_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	q := newQueuedRig(t, addr, nil)
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	key := "order-" + core.NewULID(time.Now())
	first, b1 := q.invoke(t, "int_evt_brandB", trackBody, "Idempotency-Key", key) // async
	second, b2 := q.invoke(t, "int_evt_brandB", trackBody, "Idempotency-Key", key)
	if first.Code != 202 || second.Code != 202 || b1["request_id"] != b2["request_id"] || second.Header().Get("X-Hub-Idempotent-Replay") != "true" {
		t.Fatalf("replay: %d %v / %d %v %v", first.Code, b1, second.Code, b2, second.Header())
	}
	if second.Header().Get("X-Hub-Request-Id") != first.Header().Get("X-Hub-Request-Id") {
		t.Fatal("a replay answers with the original request id")
	}
	conflict, b3 := q.invoke(t, "int_evt_brandB", `{"user": {"id": "u_2"}, "event": {"name": "other"}}`, "Idempotency-Key", key)
	if conflict.Code != 409 || b3["error"].(map[string]any)["code"] != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("conflict %d %v", conflict.Code, b3)
	}
	// Exactly one envelope was queued for the key.
	var n int
	_ = q.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.requests WHERE idempotency_key = $1 AND created_at > now() - interval '1 hour'`, key).Scan(&n)
	if n != 1 {
		t.Fatalf("%d requests rows for one key", n)
	}
	// Sync path: a failure releases the key, so the retry runs fresh.
	q.route(t, mockvendor.Behaviour{Status: 400, Body: json.RawMessage(`{"status":"fail","error":"bad"}`)})
	syncKey := "sync-" + core.NewULID(time.Now())
	tune := func(id string, _ *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandA" {
			v.Delivery.Mode, v.Delivery.WaitMS = registry.ModeSync, 0
		}
	}
	qs := newQueuedRig(t, addr, tune)
	qs.route(t, mockvendor.Behaviour{Status: 400, Body: json.RawMessage(`{"status":"fail","error":"bad"}`)})
	if rec, _ := qs.invoke(t, "int_evt_brandA", trackBody, "Idempotency-Key", syncKey); rec.Code != 422 {
		t.Fatalf("sync failure %d", rec.Code)
	}
	qs.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	if rec, _ := qs.invoke(t, "int_evt_brandA", trackBody, "Idempotency-Key", syncKey); rec.Code != 200 || rec.Header().Get("X-Hub-Idempotent-Replay") == "true" {
		t.Fatalf("retry after a failure must run fresh: %d %v", rec.Code, rec.Header())
	}
	if rec, _ := qs.invoke(t, "int_evt_brandA", trackBody, "Idempotency-Key", syncKey); rec.Code != 200 || rec.Header().Get("X-Hub-Idempotent-Replay") != "true" {
		t.Fatalf("then replayed: %d %v", rec.Code, rec.Header())
	}
	if n := len(qs.mv.Requests(t, nil)); n != 1 {
		t.Fatalf("vendor calls after success + replay: %d", n)
	}
}

func TestIdempotencyStoreDown(t *testing.T) {
	money := func(id string, in *registry.Integration, v *registry.IntegrationVersion) {
		if id == "int_evt_brandA" {
			in.MovesMoneyOrQuota = true
		}
	}
	q := newQueuedRig(t, "127.0.0.1:1", money) // nothing listens: Redis is down
	q.route(t, mockvendor.Behaviour{Status: 200, Body: okVendor})
	rec, body := q.invoke(t, "int_evt_brandA", trackBody, "Idempotency-Key", "k-1")
	if rec.Code != 500 || body["error"].(map[string]any)["reason"] != "idempotency_unavailable" {
		t.Fatalf("money integration with Redis down must fail closed: %d %v", rec.Code, body)
	}
	if rec, _ := q.invoke(t, "int_evt_brandB", trackBody, "Idempotency-Key", "k-2"); rec.Code != 202 {
		t.Fatalf("other integrations pass through: %d", rec.Code)
	}
	if rec, _ := q.invoke(t, "int_evt_brandA", trackBody); rec.Code != 400 {
		t.Fatalf("missing key on a money integration: %d", rec.Code)
	}
}

// BenchmarkAsyncAccept: async accept p99 ≤ 10 ms (HUB-115), Postgres commit included.
func BenchmarkAsyncAccept(b *testing.B) {
	q := newQueuedRigB(b)
	durations := make([]time.Duration, 0, 4096)
	for b.Loop() {
		req := httptest.NewRequestWithContext(b.Context(), http.MethodPost, "/v1/integrations/int_evt_brandB/invoke", strings.NewReader(trackBody))
		req.Header.Set("Authorization", "Bearer "+devKey)
		rec := httptest.NewRecorder()
		start := time.Now()
		q.ServeHTTP(rec, req)
		durations = append(durations, time.Since(start))
		if rec.Code != 202 {
			b.Fatal(rec.Code, rec.Body)
		}
	}
	slices.Sort(durations)
	p99 := durations[len(durations)*99/100]
	b.ReportMetric(float64(p99.Microseconds()), "p99-µs")
	if p99 > 10*time.Millisecond {
		b.Errorf("async accept p99 %s over 10 ms", p99)
	}
}

func newQueuedRigB(b *testing.B) http.Handler {
	db, err := pgxpool.New(b.Context(), os.Getenv("HUB_PG_DSN"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM hub.outbox WHERE integration_id = 'int_evt_brandB' AND tenant_id = 'orders-service' AND status = 'accepted'`)
		db.Close()
	})
	snap := devSnapshot(b)
	q := &queue.Outbox{}
	return New(Deps{Snapshots: staticSnapshots{snap}, Queueing: &Queueing{DB: db, Queue: q}}).Handler()
}
