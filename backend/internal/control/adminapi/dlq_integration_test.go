//go:build integration

package adminapi

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// insertDeadLetter writes a dead letter (and its ledger row) directly, for list and purge cases.
func insertDeadLetter(t *testing.T, pool *pgxpool.Pool, id, tenant, integration, account, reason string) {
	t.Helper()
	created, _ := core.RequestIDTime(id)
	env := core.Envelope{RequestID: id, Tenant: tenant, IntegrationID: integration, ResolvedVersion: 2, Capability: "events.track@v1",
		Operation: "default", Mode: "async", Canonical: json.RawMessage(`{"user":{"id":"u_1"},"event":{"name":"x"}}`),
		ExpiresAt: created.Add(time.Hour), CreatedAt: created}
	body, _ := json.Marshal(env)
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if err := (audit.Ledger{}).Insert(t.Context(), tx, audit.Record{RequestID: id, CreatedAt: created, TenantID: tenant, IntegrationID: integration,
			Capability: "events.track@v1", Operation: "default", Mode: "async", Status: "dead_lettered", PayloadPolicy: "masked"}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO hub.dead_letters (request_id, created_at, tenant_id, integration_id, account_id, reason, last_error, attempts, envelope,
			last_vendor_response) VALUES ($1, $2, $3, $4, $5, $6, 'vendor said no', 1, $7, '{"status":400}')`, id, created, tenant, integration, account, reason, body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.dead_letters WHERE request_id = $1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.outbox WHERE request_id = $1`, id)
	})
}

var replayCaseID, purgeCaseID = "req_" + core.NewULID(time.Now()), "req_" + core.NewULID(time.Now())

func init() {
	mutationCases["replayDeadLetters"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/dlq/replay",
		body: `{"request_ids":["` + replayCaseID + `"],"reason":"T1 fixed in T1@8"}`,
		setup: func(a *adminRig) {
			ensurePartitions(a.t, a.s.d.Pool)
			insertDeadLetter(a.t, a.s.d.Pool, replayCaseID, "orders-service", "int_evt_brandA", "ct-brandA-in", "UPSTREAM_REJECTED")
		}}
	mutationCases["purgeDeadLetters"] = mutationCase{role: RoleAdmin, method: http.MethodPost, path: "/dlq/purge",
		body: `{"request_ids":["` + purgeCaseID + `"],"reason":"staging test traffic"}`,
		setup: func(a *adminRig) {
			ensurePartitions(a.t, a.s.d.Pool)
			insertDeadLetter(a.t, a.s.d.Pool, purgeCaseID, "orders-service", "int_evt_brandA", "ct-brandA-in", "UPSTREAM_REJECTED")
		}}
}

// t1v8 is the "transformer fix" of the replay test: event names with spaces are sanitised.
const t1v8 = `# Test-only fix (HUB-209): T1@7 plus event-name sanitising. CleverTap upload shape
# (https://developer.clevertap.com/docs/upload-events-api, checked 2026-10-31); only mockvendor is called.
transformer: T1
version: 8
kind: request
capability: events.track@v1
docs_url: https://developer.clevertap.com/docs/upload-events-api
vendor_endpoint: clevertap.upload@1
request:
  method: POST
  path: /1/upload
  headers:
    X-CleverTap-Account-Id: creds.account_id
    X-CleverTap-Passcode:   creds.passcode
    Content-Type:           '"application/json; charset=utf-8"'
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  replace(req.event.name, " ", "_")
        ts:       epochSeconds(req.event.timestamp ?? now())
        evtData:  req.event.properties
batch: { path: d, max: 1000 }
`

// configWithFix is backend/config plus T1@8 (goldens copied: they have no spaces) and
// int_evt_brandA v3 on it, active.
func configWithFix(t *testing.T) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	base := os.DirFS("../../../config")
	if err := fs.WalkDir(base, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(base, p)
		m[p] = &fstest.MapFile{Data: b}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	add := func(p, s string) { m[p] = &fstest.MapFile{Data: []byte(s)} }
	add("transformers/T1/8.yaml", t1v8)
	for _, c := range []string{"minimal", "full"} {
		for _, f := range []string{".input.json", ".expected_request.json"} {
			b, _ := fs.ReadFile(base, "transformers/T1/fixtures/"+c+f)
			add("transformers/T1/fixtures/v8/"+c+f, string(b))
		}
	}
	v2, _ := fs.ReadFile(base, "integrations/int_evt_brandA/versions/2.yaml")
	v3 := strings.NewReplacer("version: 2", "version: 3", "request: T1@7", "request: T1@8", `reason: "full response (R1) for support"`, `reason: "T1@8 sanitises event names"`).Replace(string(v2))
	add("integrations/int_evt_brandA/versions/3.yaml", v3)
	in, _ := fs.ReadFile(base, "integrations/int_evt_brandA/integration.yaml")
	add("integrations/int_evt_brandA/integration.yaml", strings.Replace(string(in), "active_version: 2", "active_version: 3", 1))
	return m
}

func buildSnap(t *testing.T, config fs.FS, vendorURL string) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(registry.Sources{Config: config, Contracts: os.DirFS("../../../../contracts")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var st registry.Store
	snap = st.Swap(snap)
	for _, v := range snap.Vendors {
		for r := range v.Regions {
			v.Regions[r] = vendorURL
		}
	}
	return snap
}

// storeSink writes each batch at once (the ledger the explorer reads).
type storeSink struct {
	t *testing.T
	s audit.PGStore
}

func (k storeSink) Emit(b audit.Batch) {
	if err := k.s.Write(context.Background(), b); err != nil {
		k.t.Error(err)
	}
}

type snaps struct {
	p atomic.Pointer[registry.Snapshot]
}

func (s *snaps) Snapshot() *registry.Snapshot { return s.p.Load() }

type staticLoader map[string]string

func (l staticLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: l}}
	}
	return out, nil
}

// testWorker is a real delivery worker against mockvendor.
func testWorker(t *testing.T, pool *pgxpool.Pool, sn *snaps) *delivery.Worker {
	t.Helper()
	m := telemetry.NewMetrics("worker", "test")
	cache, err := creds.NewCache(staticLoader{"account_id": "fake-acct-DLQ", "passcode": "fake-pass-DLQ"}, telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Warm(t.Context(), creds.AccountIDs(sn.Snapshot())); err != nil {
		t.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	q := &queue.Outbox{}
	log := slog.New(slog.DiscardHandler)
	return &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: pool, Queue: q,
		Transitions: &delivery.Transitions{DB: pool, Queue: q}, Snapshots: sn,
		Attempter: &delivery.Attempter{Creds: cache, Upstream: client, Log: log},
		Audit:     storeSink{t: t, s: audit.PGStore{Pool: pool}}, Masker: audit.NewMasker([]byte("dlq-test-mask-key-0123456789abcd")),
		Backoff: delivery.Backoff{Rand: func() float64 { return 0.5 }}, Budget: delivery.NewBudget(nil), Metrics: m, Log: log}
}

func enqueueEvent(t *testing.T, pool *pgxpool.Pool, integration, name string) core.Envelope {
	t.Helper()
	now := time.Now().UTC()
	env := core.Envelope{RequestID: "req_" + core.NewULID(now), Tenant: "orders-service", IntegrationID: integration, ResolvedVersion: 2,
		Capability: "events.track@v1", Operation: "default", Mode: "async",
		Canonical:     json.RawMessage(`{"user":{"id":"u_48213"},"event":{"name":"` + name + `","timestamp":"2026-10-31T09:15:00Z"}}`),
		NextAttemptAt: now.Add(-time.Millisecond), ExpiresAt: now.Add(6 * time.Hour), CreatedAt: now}
	err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if err := (audit.Ledger{}).Insert(t.Context(), tx, audit.Record{RequestID: env.RequestID, CreatedAt: now, TenantID: env.Tenant,
			IntegrationID: integration, Capability: env.Capability, Operation: "default", Mode: "async", Status: "accepted", PayloadPolicy: "masked"}); err != nil {
			return err
		}
		return (&queue.Outbox{}).Enqueue(t.Context(), tx, env)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.outbox WHERE request_id = $1`, env.RequestID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM hub.dead_letters WHERE request_id = $1`, env.RequestID)
	})
	return env
}

func ledgerStatus(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	from, to, _ := partitionBounds(id)
	var s string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM hub.requests WHERE request_id = $1 AND created_at BETWEEN $2 AND $3`, id, from, to).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func drain(t *testing.T, w *delivery.Worker) {
	t.Helper()
	for range 5 {
		n, err := w.ProcessOnce(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// TestReplayByFilterDeliversWithTheFixedTransformer is HUB-209: requests dead-letter on a
// vendor rejection; after a transformer fix (T1@8 active), replay by filter (integration,
// reason, time range, not replayed) moves exactly the matching entries to accepted, a real
// worker delivers them to mockvendor, and the vendor sees the fixed transformer's output.
// Dry runs count only; non-matching entries stay; purge removes by id; each writes one change.
func TestReplayByFilterDeliversWithTheFixedTransformer(t *testing.T) {
	a := newAdminRig(t)
	pool := a.s.d.Pool
	ensurePartitions(t, pool)
	// Other tests share hub.outbox: push their due rows out of this test's way.
	if _, err := pool.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 day', claimed_until = now() + interval '1 day' WHERE pool = 'messaging'`); err != nil {
		t.Fatal(err)
	}
	mv := mockvendor.Start(t)
	sn := &snaps{}
	sn.p.Store(buildSnap(t, os.DirFS("../../../config"), mv.VendorURL))
	w := testWorker(t, pool, sn)
	route := func(status int, body string) {
		mv.Reset(t)
		mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
			Behaviour: mockvendor.Behaviour{Status: status, Body: json.RawMessage(body)}})
	}

	start := time.Now().UTC().Add(-time.Second)
	route(400, `{"status":"fail","error":"Invalid event name"}`)
	e1 := enqueueEvent(t, pool, "int_evt_brandA", "order placed")
	e2 := enqueueEvent(t, pool, "int_evt_brandA", "cart viewed")
	other := enqueueEvent(t, pool, "int_evt_brandB", "order placed")
	drain(t, w)
	for _, e := range []core.Envelope{e1, e2, other} {
		if s := ledgerStatus(t, pool, e.RequestID); s != "dead_lettered" {
			t.Fatalf("%s: %s", e.RequestID, s)
		}
	}
	if reqs := mv.Requests(t, nil); len(reqs) != 3 || !strings.Contains(string(reqs[0].Body)+string(reqs[1].Body)+string(reqs[2].Body), `"evtName":"order placed"`) {
		t.Fatalf("first attempts %d %s", len(reqs), reqs[0].Body)
	}

	// The DLQ as an operator sees it.
	window := "&from=" + url.QueryEscape(start.Format(time.RFC3339)) + "&to=" + url.QueryEscape(time.Now().Add(time.Minute).Format(time.RFC3339))
	resp, list := a.do(RoleTenantViewer, http.MethodGet, "/dlq?integration_id=int_evt_brandA&reason=UPSTREAM_REJECTED&replayed=false"+window, "")
	if resp.Code != 200 || len(ids(list)) != 2 {
		t.Fatalf("list %d %v", resp.Code, list)
	}
	item := list["items"].([]any)[0].(map[string]any)
	if item["vendor_id"] != "clevertap" || item["account_id"] != "ct-brandA-in" || item["attempts"] != 1.0 {
		t.Fatalf("item %v", item)
	}
	resp, detail := a.do(RoleTenantViewer, http.MethodGet, "/dlq/"+e1.RequestID, "")
	if resp.Code != 200 || len(detail["attempt_history"].([]any)) != 1 || detail["last_vendor_response"] == nil {
		t.Fatalf("detail %d %v", resp.Code, detail)
	}
	if strings.Contains(resp.Body.String(), "fake-pass-DLQ") || strings.Contains(resp.Body.String(), "u_48213") {
		t.Fatalf("the masked DLQ entry leaks: %s", resp.Body)
	}

	// The fix: T1@8 becomes active (version 3); the vendor accepts.
	sn.p.Store(buildSnap(t, configWithFix(t), mv.VendorURL))
	route(200, `{"status":"success","processed":1,"unprocessed":[]}`)
	filter := `{"integration_id":"int_evt_brandA","reason":"UPSTREAM_REJECTED","from":"` + start.Format(time.RFC3339Nano) +
		`","to":"` + time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano) + `","replayed":false}`
	_, dry := a.do(RoleEditor, http.MethodPost, "/dlq/replay", `{"filter":`+filter+`,"dry_run":true,"reason":"T1@8 sanitises event names"}`)
	if dry["matched"] != 2.0 || dry["affected"] != 0.0 || dry["change_id"] != nil || dry["dry_run"] != true {
		t.Fatalf("dry run %v", dry)
	}
	if s := ledgerStatus(t, pool, e1.RequestID); s != "dead_lettered" {
		t.Fatalf("a dry run replayed: %s", s)
	}
	resp, res := a.do(RoleEditor, http.MethodPost, "/dlq/replay", `{"filter":`+filter+`,"reason":"T1@8 sanitises event names"}`)
	if resp.Code != 200 || res["matched"] != 2.0 || res["affected"] != 2.0 || res["truncated"] != false || res["change_id"] == nil {
		t.Fatalf("replay %d %v", resp.Code, res)
	}
	if n := changesOf(t, a, resp.Header().Get("X-Hub-Request-Id")); n != 1 {
		t.Fatalf("%d changes", n)
	}
	for _, e := range []core.Envelope{e1, e2} {
		if s := ledgerStatus(t, pool, e.RequestID); s != "accepted" {
			t.Fatalf("after replay %s: %s", e.RequestID, s)
		}
	}
	// Replaying again matches nothing (already replayed).
	if _, again := a.do(RoleEditor, http.MethodPost, "/dlq/replay", `{"filter":`+filter+`,"reason":"double click"}`); again["affected"] != 0.0 {
		t.Fatalf("second replay %v", again)
	}

	drain(t, w)
	for _, e := range []core.Envelope{e1, e2} {
		if s := ledgerStatus(t, pool, e.RequestID); s != "succeeded" {
			t.Fatalf("delivered %s: %s", e.RequestID, s)
		}
	}
	reqs := mv.Requests(t, nil)
	if len(reqs) != 2 {
		t.Fatalf("%d vendor calls after replay", len(reqs))
	}
	var bodies []string
	for _, r := range reqs {
		bodies = append(bodies, string(r.Body))
	}
	all := strings.Join(bodies, "\n")
	if !strings.Contains(all, `"evtName":"order_placed"`) || !strings.Contains(all, `"evtName":"cart_viewed"`) || strings.Contains(all, "order placed") {
		t.Fatalf("the replay did not use the fixed transformer:\n%s", all)
	}
	var version int
	_ = pool.QueryRow(t.Context(), `SELECT integration_version FROM hub.requests WHERE request_id = $1`, e1.RequestID).Scan(&version)
	if version != 3 {
		t.Fatalf("delivered on version %d, want 3", version)
	}
	if s := ledgerStatus(t, pool, other.RequestID); s != "dead_lettered" {
		t.Fatalf("the other integration was replayed: %s", s)
	}

	// Purge: by id; admin only; selection rules.
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/dlq/purge", `{"request_ids":["`+other.RequestID+`"],"reason":"cleanup"}`); resp.Code != http.StatusForbidden {
		t.Fatalf("editor purge: %d", resp.Code)
	}
	if resp, _ := a.do(RoleAdmin, http.MethodPost, "/dlq/purge", `{"request_ids":["`+other.RequestID+`"],"filter":{},"reason":"cleanup"}`); resp.Code != http.StatusBadRequest {
		t.Fatalf("both selections: %d", resp.Code)
	}
	_, purged := a.do(RoleAdmin, http.MethodPost, "/dlq/purge", `{"request_ids":["`+other.RequestID+`"],"reason":"cleanup"}`)
	if purged["affected"] != 1.0 {
		t.Fatalf("purge %v", purged)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/dlq/"+other.RequestID, ""); resp.Code != http.StatusNotFound {
		t.Fatalf("purged entry: %d", resp.Code)
	}
	if s := ledgerStatus(t, pool, other.RequestID); s != "dead_lettered" {
		t.Fatalf("the ledger must stay: %s", s)
	}
}

// TestDeadLetterScope: tenant_viewer sees its tenants' entries only.
func TestDeadLetterScope(t *testing.T) {
	a := newAdminRig(t)
	ensurePartitions(t, a.s.d.Pool)
	integ := uniq("int_d_")
	mine, theirs := "req_"+core.NewULID(time.Now()), "req_"+core.NewULID(time.Now())
	insertDeadLetter(t, a.s.d.Pool, mine, "orders-service", integ, "ct-brandA-in", "poison")
	insertDeadLetter(t, a.s.d.Pool, theirs, "billing-service", integ, "ct-brandB-us", "poison")
	if _, body := a.do(RoleTenantViewer, http.MethodGet, "/dlq?integration_id="+integ, ""); strings.Join(ids(body), ",") != mine {
		t.Fatalf("scoped list %v", body)
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/dlq?integration_id="+integ+"&vendor_id=clevertap&limit=1", ""); len(ids(body)) != 1 || body["next_cursor"] == nil {
		t.Fatalf("viewer page %v", body)
	}
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/dlq/"+theirs, ""); resp.Code != http.StatusNotFound {
		t.Fatalf("foreign entry: %d", resp.Code)
	}
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/dlq?tenant_id=billing-service", ""); resp.Code != http.StatusForbidden {
		t.Fatalf("foreign tenant filter: %d", resp.Code)
	}
}
