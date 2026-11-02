//go:build integration

package edge

import (
	"context"
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// Cutover fixtures (HUB-178, DESIGN §6.3): a test overlay of backend/config where v7 → v8 of an
// integration changes one thing each:
//
//	int_cut_account      ct-brandA-in → ct-brandB-us (same vendor and transformer)
//	int_cut_vendor       ct-brandA-in → alt-brandA-in, a second, test-only vendor ("altvendor")
//	int_cut_transformer  T1@7 → T1@8 (same account); T1@8 has its own goldens (fixtures/v8)
//
// Each target answers on its own mock path, so the vendor log says who was called:
// clevertap region in → /ct-in, region us → /ct-us, altvendor → /alt.

type cutoverChange struct {
	name, integration string
	v7, v8            string // mock path prefix of each version's target
}

var cutoverChanges = []cutoverChange{
	{"account", "int_cut_account", "/ct-in", "/ct-us"},
	{"vendor", "int_cut_vendor", "/ct-in", "/alt"},
	{"transformer", "int_cut_transformer", "/ct-in", "/ct-in"}, // same target: T1@8 adds X-Hub-Transformer-Version
}

const t1v8 = `# CleverTap Upload Events API, checked against https://developer.clevertap.com/docs/upload-events-api on 2026-10-31 (AGENTS §3.3).
# Test-only overlay (HUB-178): T1@7 plus a version header, so a vendor call shows which version built it.
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
    X-Hub-Transformer-Version: '"T1@8"'
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())
        evtData:  req.event.properties
batch: { path: d, max: 1000 }
`

// altvendor is not a real vendor: it exists only in this overlay and only behind mockvendor.
const altVendor = `vendor: altvendor
auth: static_headers
regions: { in: http://127.0.0.1:1 }
endpoints:
  upload@1: { method: POST, path: /1/upload, timeout_ms: 3000, connect_timeout_ms: 500, idempotent: true, batch_max: 1000 }
defaults:
  rate_limit: { per_account_rps: 50 }
  breaker: { window_s: 30, min_calls: 20, failure_ratio: 0.5, consecutive_timeouts: 5, open_s: 30, open_max_s: 600, half_open_probes: 3 }
`

const altAccount = `account: alt-brandA-in
vendor: altvendor
region: in
state: active
credentials_ref: localfile://alt-brandA-in
config: { project_id: "ALT-A", brand_code: "A" }
quota: { rps: 50, daily: 5000000 }
`

const tAlt = `# Test-only overlay (HUB-178): the request transformer of the test-only altvendor, CleverTap upload shape
# (https://developer.clevertap.com/docs/upload-events-api, checked 2026-10-31); no real vendor is called.
transformer: TALT
version: 1
kind: request
capability: events.track@v1
docs_url: https://developer.clevertap.com/docs/upload-events-api
vendor_endpoint: altvendor.upload@1
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
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())
        evtData:  req.event.properties
batch: { path: d, max: 1000 }
`

func cutVersion(id string, n int, vendor, account, req string, mode registry.Mode, pending registry.Pending) string {
	d := "  mode: " + string(mode) + "\n  ttl: 6h\n  pending: " + string(pending) + "\n"
	if mode != registry.ModeSync {
		d += "  polling: true\n"
	}
	return "integration: " + id + "\nversion: " + strconv.Itoa(n) + "\ntargets:\n  - vendor: " + vendor + "\n    account: " + account +
		"\n    transformer: { request: " + req + ", response: R2@1 }\n    weight: 100\ndelivery:\n" + d +
		"audit: { payloads: masked, retention: 30d }\npublish: { author: itest, reason: \"HUB-178 v" + strconv.Itoa(n) + "\" }\n"
}

// cutoverOverlay is backend/config plus the cutover fixtures, with v<active> active.
func cutoverOverlay(t *testing.T, active int, mode registry.Mode, pending registry.Pending) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	base := os.DirFS("../../config")
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
	add("vendors/altvendor.yaml", altVendor)
	add("accounts/alt-brandA-in.yaml", altAccount)
	add("transformers/T1/8.yaml", t1v8)
	add("transformers/TALT/1.yaml", tAlt)
	for _, c := range []string{"minimal", "full"} {
		in, _ := fs.ReadFile(base, "transformers/T1/fixtures/"+c+".input.json")
		exp, _ := fs.ReadFile(base, "transformers/T1/fixtures/"+c+".expected_request.json")
		add("transformers/T1/fixtures/v8/"+c+".input.json", string(in))
		add("transformers/T1/fixtures/v8/"+c+".expected_request.json",
			strings.Replace(string(exp), `"headers": {`, `"headers": {"X-Hub-Transformer-Version": "T1@8", `, 1))
		add("transformers/TALT/fixtures/"+c+".input.json", string(in))
		add("transformers/TALT/fixtures/"+c+".expected_request.json", string(exp))
	}
	v8 := map[string][3]string{
		"int_cut_account":     {"clevertap", "ct-brandB-us", "T1@7"},
		"int_cut_vendor":      {"altvendor", "alt-brandA-in", "TALT@1"},
		"int_cut_transformer": {"clevertap", "ct-brandA-in", "T1@8"},
	}
	for id, t8 := range v8 {
		add("integrations/"+id+"/integration.yaml", "integration: "+id+"\nowner_tenant: orders-service\ncapability: events.track@v1\ngrants: []\nactive_version: "+
			strconv.Itoa(active)+"\ndescription: HUB-178 cutover fixture\n")
		add("integrations/"+id+"/versions/7.yaml", cutVersion(id, 7, "clevertap", "ct-brandA-in", "T1@7", mode, pending))
		add("integrations/"+id+"/versions/8.yaml", cutVersion(id, 8, t8[0], t8[1], t8[2], mode, pending))
	}
	return m
}

func cutoverSnapshot(t *testing.T, vendorURL string, active int, mode registry.Mode, pending registry.Pending) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(registry.Sources{Config: cutoverOverlay(t, active, mode, pending), Contracts: os.DirFS("../../../contracts")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snap.Vendors["clevertap"].Regions = map[string]string{"in": vendorURL + "/ct-in", "us": vendorURL + "/ct-us"}
	snap.Vendors["altvendor"].Regions = map[string]string{"in": vendorURL + "/alt"}
	return snap
}

// teeSink records batches for the test and writes them to Postgres.
type teeSink struct {
	rec *recordingSink
	db  audit.Sink
}

func (s teeSink) Emit(b audit.Batch) { s.rec.Emit(b); s.db.Emit(b) }

type cutoverRig struct {
	h       http.Handler
	mv      *mockvendor.Instance
	db      *pgxpool.Pool
	store   *registry.Store
	worker  *delivery.Worker
	spec    *contract.Spec
	sink    *recordingSink
	mode    registry.Mode
	pending registry.Pending
}

func newCutoverRig(t *testing.T, mode registry.Mode, pending registry.Pending) *cutoverRig {
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
	mv.Reset(t)
	for _, p := range []string{"/ct-in", "/ct-us", "/alt"} {
		mv.SetRoute(t, strings.TrimPrefix(p, "/"), mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: p + "/1/upload"},
			Behaviour: mockvendor.Behaviour{Status: 200, Headers: map[string]string{"X-Request-Id": "mv" + strings.ReplaceAll(p, "/", "-")}, Body: okVendor}})
	}
	store := &registry.Store{}
	store.Swap(cutoverSnapshot(t, mv.VendorURL, 7, mode, pending))
	m := telemetry.NewMetrics("gateway", "cutover")
	log := slog.New(slog.DiscardHandler)
	cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(store.Snapshot())); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	writer := audit.NewWriter(audit.PGStore{Pool: db}, m, audit.WriterOptions{FlushEvery: 20 * time.Millisecond, Log: log})
	wctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { writer.Run(wctx); close(done) }()
	t.Cleanup(func() { stop(); <-done })
	rec := &recordingSink{}
	sink := teeSink{rec: rec, db: writer}
	q := &queue.Outbox{}
	tr := &delivery.Transitions{DB: db, Queue: q}
	settler := delivery.Settler{Transitions: tr, Backoff: delivery.Backoff{}, Budget: delivery.NewBudget(nil)}
	rdb := redis.NewClient(&redis.Options{Addr: os.Getenv("HUB_REDIS_ADDR")})
	t.Cleanup(func() { _ = rdb.Close() })
	d := Deps{Snapshots: store, Creds: cache, Upstream: client, Metrics: m, Log: log, Audit: sink, DebugHeaders: true,
		Idempotency: idempotency.New(rdb, m, idempotency.Options{Prefix: "itest:cutover:" + core.NewULID(time.Now()) + ":"}),
		Queueing:    &Queueing{DB: db, Queue: q, Settler: settler, Name: "gw-" + core.NewULID(time.Now())}}
	w := &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: db, Queue: q, Transitions: tr,
		Snapshots: store, Attempter: &delivery.Attempter{Creds: cache, Upstream: client, Log: log}, Audit: sink, Budget: delivery.NewBudget(nil), Log: log}
	return &cutoverRig{h: New(d).Handler(), mv: mv, db: db, store: store, worker: w, spec: contract.Load(t, "hub-public.v1.yaml"), sink: rec, mode: mode, pending: pending}
}

func (c *cutoverRig) publish(t *testing.T, active int) {
	t.Helper()
	c.store.Swap(cutoverSnapshot(t, c.mv.VendorURL, active, c.mode, c.pending))
}

func (c *cutoverRig) invoke(t *testing.T, integration, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/integrations/"+integration+"/invoke", strings.NewReader(trackBody))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	return c.spec.Serve(t, c.h, req) // caller URL, body and response are validated against hub-public
}

// calls returns the vendor calls since the last reset as "<prefix> <transformer header>".
func (c *cutoverRig) calls(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, r := range c.mv.Requests(t, nil) {
		prefix := r.Path[:strings.Index(r.Path[1:], "/")+1]
		v := "T1@7"
		if h := http.Header(r.Headers).Get("X-Hub-Transformer-Version"); h != "" {
			v = h
		}
		out = append(out, prefix+" "+v)
	}
	return out
}

// callsSince returns the vendor calls after the first n.
func (c *cutoverRig) callsSince(t *testing.T, n int) []string {
	t.Helper()
	return c.calls(t)[n:]
}

// want is the vendor call a version of ch makes.
func (ch cutoverChange) want(version int) string {
	if version == 7 {
		return ch.v7 + " T1@7"
	}
	if ch.name == "transformer" {
		return ch.v8 + " T1@8"
	}
	return ch.v8 + " T1@7"
}

// ledger reads the request row and its attempts' (version, account), plus a checksum of every
// audit row of the request.
func (c *cutoverRig) ledger(t *testing.T, id string) (version int, attempts []string, sum string) {
	t.Helper()
	eventually(t, "audit rows of "+id, func() bool {
		var n int
		_ = c.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.request_attempts WHERE request_id = $1`, id).Scan(&n)
		return n > 0
	})
	if err := c.db.QueryRow(t.Context(), `SELECT coalesce(integration_version, 0) FROM hub.requests WHERE request_id = $1`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	rows, err := c.db.Query(t.Context(), `SELECT integration_version, account_id FROM hub.request_attempts WHERE request_id = $1 ORDER BY attempt_no`, id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		var a string
		if err := rows.Scan(&v, &a); err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, strconv.Itoa(v)+" "+a)
	}
	rows.Close()
	if err := c.db.QueryRow(t.Context(), `SELECT md5(
  (SELECT r::text FROM hub.requests r WHERE request_id = $1) ||
  (SELECT string_agg(a::text, ',' ORDER BY attempt_no) FROM hub.request_attempts a WHERE request_id = $1) ||
  (SELECT string_agg(e::text, ',' ORDER BY seq) FROM hub.request_events e WHERE request_id = $1))`, id).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	return version, attempts, sum
}

func accountOf(ch cutoverChange, version int) string {
	if version == 8 {
		switch ch.name {
		case "account":
			return "ct-brandB-us"
		case "vendor":
			return "alt-brandA-in"
		}
	}
	return "ct-brandA-in"
}

// TestCutoverCompletedAndNewRequests is HUB-178 and HUB-182 (DESIGN §6.3 rows 1 and 5), for an
// account, a vendor and a transformer change: a request completed on v7 keeps v7, its account
// and its audit rows byte for byte after v8 is published; the first new request uses v8 with
// the same caller URL, body and response contract.
func TestCutoverCompletedAndNewRequests(t *testing.T) {
	for _, ch := range cutoverChanges {
		t.Run(ch.name, func(t *testing.T) {
			c := newCutoverRig(t, registry.ModeSync, registry.ReResolve)
			rec := c.invoke(t, ch.integration, "")
			if rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "7" {
				t.Fatalf("v7 request: %d %s", rec.Code, rec.Body)
			}
			if got := c.calls(t); len(got) != 1 || got[0] != ch.want(7) {
				t.Fatalf("v7 vendor calls %v, want %s", got, ch.want(7))
			}
			id := rec.Header().Get("X-Hub-Request-Id")
			v, attempts, before := c.ledger(t, id)
			if v != 7 || len(attempts) != 1 || attempts[0] != "7 ct-brandA-in" {
				t.Fatalf("v7 ledger: v%d %v", v, attempts)
			}

			c.publish(t, 8)
			rec = c.invoke(t, ch.integration, "")
			if rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "8" || rec.Header().Get("X-Hub-Served-By") != "primary" {
				t.Fatalf("first request after publish: %d %v %s", rec.Code, rec.Header(), rec.Body)
			}
			if got := c.callsSince(t, 1); len(got) != 1 || got[0] != ch.want(8) {
				t.Fatalf("v8 vendor calls %v, want %s", got, ch.want(8))
			}
			if _, attempts, _ := c.ledger(t, rec.Header().Get("X-Hub-Request-Id")); attempts[0] != "8 "+accountOf(ch, 8) {
				t.Fatalf("v8 attempt %v", attempts)
			}
			if v, attempts, after := c.ledger(t, id); after != before || v != 7 || attempts[0] != "7 ct-brandA-in" {
				t.Fatalf("publishing v8 changed the v7 request's audit rows: v%d %v", v, attempts)
			}
		})
	}
}

// TestCutoverInFlightStaysOnV7 is HUB-179 (row 2): a call held at the vendor while v8 is
// published completes on v7's target and records v7.
func TestCutoverInFlightStaysOnV7(t *testing.T) {
	for _, ch := range cutoverChanges {
		t.Run(ch.name, func(t *testing.T) {
			c := newCutoverRig(t, registry.ModeSync, registry.ReResolve)
			c.mv.SetRoute(t, "ct-in", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/ct-in/1/upload"},
				Behaviour: mockvendor.Behaviour{Status: 200, Body: okVendor, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 600}}})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- c.invoke(t, ch.integration, "") }()
			time.Sleep(200 * time.Millisecond) // the call is at the vendor
			c.publish(t, 8)
			rec := <-done
			if rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "7" {
				t.Fatalf("in-flight request: %d %v %s", rec.Code, rec.Header(), rec.Body)
			}
			if got := c.calls(t); len(got) != 1 || got[0] != ch.want(7) {
				t.Fatalf("vendor calls %v, want %s", got, ch.want(7))
			}
			if v, attempts, _ := c.ledger(t, rec.Header().Get("X-Hub-Request-Id")); v != 7 || attempts[0] != "7 ct-brandA-in" {
				t.Fatalf("ledger v%d %v", v, attempts)
			}
		})
	}
}

// TestCutoverQueuedReResolveOrPinned is HUB-180 (row 3): a request queued under v7 is sent with
// v8 after the publish (re_resolve, the default), or with v7 when the integration is pinned.
func TestCutoverQueuedReResolveOrPinned(t *testing.T) {
	for _, pending := range []registry.Pending{registry.ReResolve, registry.Pinned} {
		for _, ch := range cutoverChanges {
			t.Run(string(pending)+"/"+ch.name, func(t *testing.T) {
				c := newCutoverRig(t, registry.ModeAsync, pending)
				rec := c.invoke(t, ch.integration, "")
				if rec.Code != 202 {
					t.Fatalf("accept: %d %s", rec.Code, rec.Body)
				}
				c.publish(t, 8)
				if n, err := c.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
					t.Fatalf("worker %d %v", n, err)
				}
				want := 8
				if pending == registry.Pinned {
					want = 7
				}
				if got := c.calls(t); len(got) != 1 || got[0] != ch.want(want) {
					t.Fatalf("vendor calls %v, want %s", got, ch.want(want))
				}
				id := rec.Header().Get("X-Hub-Request-Id")
				if _, attempts, _ := c.ledger(t, id); attempts[0] != strconv.Itoa(want)+" "+accountOf(ch, want) {
					t.Fatalf("attempt %v, want v%d", attempts, want)
				}
			})
		}
	}
}

// TestCutoverRetryWithSameKeyResolvesFresh is HUB-181 (row 4): a v7 request that failed is
// retried with the same Idempotency-Key after the publish and lands on v8; once completed, the
// same key replays the stored answer without calling the vendor again.
func TestCutoverRetryWithSameKeyResolvesFresh(t *testing.T) {
	for _, ch := range cutoverChanges {
		t.Run(ch.name, func(t *testing.T) {
			c := newCutoverRig(t, registry.ModeSync, registry.ReResolve)
			c.mv.SetRoute(t, "ct-in", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/ct-in/1/upload"},
				Behaviour: mockvendor.Behaviour{Status: 500, Body: []byte(`{"status":"fail","error":"boom"}`)}})
			key := "order-" + core.NewULID(time.Now())
			if rec := c.invoke(t, ch.integration, key); rec.Code < 500 {
				t.Fatalf("v7 attempt should fail: %d %s", rec.Code, rec.Body)
			}
			if got := c.calls(t); len(got) != 1 || got[0] != ch.want(7) {
				t.Fatalf("v7 calls %v", got)
			}
			c.mv.SetRoute(t, "ct-in", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/ct-in/1/upload"},
				Behaviour: mockvendor.Behaviour{Status: 200, Body: okVendor}})
			c.publish(t, 8)
			rec := c.invoke(t, ch.integration, key)
			if rec.Code != 200 || rec.Header().Get("X-Hub-Integration-Version") != "8" {
				t.Fatalf("retry: %d %v %s", rec.Code, rec.Header(), rec.Body)
			}
			if got := c.callsSince(t, 1); len(got) != 1 || got[0] != ch.want(8) {
				t.Fatalf("retry calls %v, want %s", got, ch.want(8))
			}
			replay := c.invoke(t, ch.integration, key)
			if replay.Code != 200 || replay.Body.String() != rec.Body.String() {
				t.Fatalf("replay: %d %s", replay.Code, replay.Body)
			}
			if n := len(c.calls(t)); n != 2 {
				t.Fatalf("the replay called the vendor: %d calls", n)
			}
		})
	}
}
