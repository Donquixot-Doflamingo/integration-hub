//go:build integration

package edge

import (
	"database/sql"
	"encoding/json"
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
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/trackops"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// HUB-175 / HUB-183 (DESIGN §6.3 row 6): a two-step flow on a test overlay of backend/config.
// int_ses v7 targets ct-brandA-in with TA@1 / RA@1; v8 targets ct-brandB-us with TB@1 / RB@1, so a
// publish of v8 changes the account and both transformers. RA/RB map a session (the vendor's
// X-Request-Id as vendor_txn_id); TA/TB echo what they see into test headers (account, own name,
// session.*), which mockvendor records.

func reqTransformer(id string) string {
	return `# Test-only overlay transformer (HUB-183), CleverTap upload shape of T1@7:
# https://developer.clevertap.com/docs/upload-events-api (checked 2026-10-31).
transformer: ` + id + `
version: 1
kind: request
docs_url: https://developer.clevertap.com/docs/upload-events-api
vendor_endpoint: clevertap.upload@1
request:
  method: POST
  path: /1/upload
  headers:
    X-CleverTap-Account-Id: creds.account_id
    X-CleverTap-Passcode:   creds.passcode
    Content-Type:           '"application/json; charset=utf-8"'
    X-Test-Transformer:     '"` + id + `@1"'
    X-Test-Account:         account.id
    X-Test-Txn:             'session.vendor_txn_id ?? "none"'
    X-Test-Opened-By:       'session.opened_by ?? "none"'
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())
`
}

func reqFixtures(id string) map[string]string {
	return map[string]string{
		"step1.input.json": `{"user": {"id": "u_1"}, "event": {"name": "otp"}, "_context": {"creds": {"account_id": "fixture-account-id", "passcode": "fixture-passcode"}, "account": {"id": "acct"}}}`,
		"step1.expected_request.json": `{"method": "POST", "path": "/1/upload", "headers": {"X-CleverTap-Account-Id": "fixture-account-id", "X-CleverTap-Passcode": "fixture-passcode",
  "Content-Type": "application/json; charset=utf-8", "X-Test-Transformer": "` + id + `@1", "X-Test-Account": "acct", "X-Test-Txn": "none", "X-Test-Opened-By": "none"},
  "body": {"d": [{"identity": "u_1", "type": "event", "evtName": "otp", "ts": 1793438100}]}}`,
		"step2.input.json": `{"user": {"id": "u_1"}, "event": {"name": "otp"}, "_context": {"creds": {"account_id": "fixture-account-id", "passcode": "fixture-passcode"}, "account": {"id": "acct"},
  "session": {"vendor_txn_id": "txn-1", "opened_by": "R@1"}}}`,
		"step2.expected_request.json": `{"method": "POST", "path": "/1/upload", "headers": {"X-CleverTap-Account-Id": "fixture-account-id", "X-CleverTap-Passcode": "fixture-passcode",
  "Content-Type": "application/json; charset=utf-8", "X-Test-Transformer": "` + id + `@1", "X-Test-Account": "acct", "X-Test-Txn": "txn-1", "X-Test-Opened-By": "R@1"},
  "body": {"d": [{"identity": "u_1", "type": "event", "evtName": "otp", "ts": 1793438100}]}}`,
	}
}

func respTransformer(id string) string {
	return `# Test-only overlay response transformer (HUB-175): keeps the vendor request id as the session's txn id.
transformer: ` + id + `
version: 1
kind: response
docs_url: https://developer.clevertap.com/docs/upload-events-api
response:
  status:     '"ok"'
  accepted:   res.body.processed
  rejected:   '[]'
  vendor_ref: header(res, "X-Request-Id")
session:
  vendor_txn_id: header(res, "X-Request-Id")
  user:          req.user.id
  opened_by:     '"` + id + `@1"'
errors:
  - when: res.status >= 400
    code: UPSTREAM_REJECTED
    retryable: false
`
}

func respFixtures(id string) map[string]string {
	return map[string]string{
		"ok.vendor_response.json":         `{"status": 200, "headers": {"X-Request-Id": "txn-1"}, "body": {"processed": 1}, "_context": {"req": {"user": {"id": "u_1"}}}}`,
		"ok.expected_response.json":       `{"response": {"status": "ok", "accepted": 1, "rejected": [], "vendor_ref": "txn-1"}, "session": {"vendor_txn_id": "txn-1", "user": "u_1", "opened_by": "` + id + `@1"}}`,
		"rejected.vendor_response.json":   `{"status": 400, "body": {"error": "bad"}}`,
		"rejected.expected_response.json": `{"error": {"code": "UPSTREAM_REJECTED", "retryable": false}}`,
	}
}

func sesVersion(n int, account, req, resp string, mode registry.Mode) string {
	d := "  mode: " + string(mode) + "\n  ttl: 6h\n"
	if mode == registry.ModeWait {
		d += "  wait_ms: 3000\n"
	}
	if mode != registry.ModeSync {
		d += "  polling: true\n"
	}
	return "integration: int_ses\nversion: " + strconv.Itoa(n) + "\n" + trackops.Block + "targets:\n  - vendor: clevertap\n    account: " + account +
		"\n    transformer: { request: " + req + ", response: " + resp + " }\n    weight: 100\ndelivery:\n" + d +
		"audit: { payloads: masked, retention: 30d }\npublish: { author: itest, reason: \"HUB-183 v" + strconv.Itoa(n) + "\" }\n"
}

// sessionOverlay is backend/config plus the int_ses integration with v7 and v8, v<active> active.
func sessionOverlay(t *testing.T, active int, mode registry.Mode) fs.FS {
	t.Helper()
	m := fstest.MapFS{}
	base := os.DirFS("../../config")
	err := fs.WalkDir(base, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(base, p)
		m[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	add := func(p, s string) { m[p] = &fstest.MapFile{Data: []byte(s)} }
	for _, id := range []string{"TA", "TB"} {
		add("transformers/"+id+"/1.yaml", reqTransformer(id))
		for k, v := range reqFixtures(id) {
			add("transformers/"+id+"/fixtures/"+k, v)
		}
	}
	for _, id := range []string{"RA", "RB"} {
		add("transformers/"+id+"/1.yaml", respTransformer(id))
		for k, v := range respFixtures(id) {
			add("transformers/"+id+"/fixtures/"+k, v)
		}
	}
	add("integrations/int_ses/integration.yaml", "integration: int_ses\nowner_tenant: orders-service\ngrants: []\nactive_version: "+strconv.Itoa(active)+"\ndescription: HUB-183 two-step test\n")
	add("integrations/int_ses/versions/7.yaml", sesVersion(7, "ct-brandA-in", "TA@1", "RA@1", mode))
	add("integrations/int_ses/versions/8.yaml", sesVersion(8, "ct-brandB-us", "TB@1", "RB@1", mode))
	return m
}

// sessionSnapshot builds the overlay with every vendor region pointed at mockvendor.
func sessionSnapshot(t *testing.T, vendorURL string, active int, mode registry.Mode) *registry.Snapshot {
	t.Helper()
	snap, err := registry.Build(registry.Sources{Config: sessionOverlay(t, active, mode)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range snap.Vendors {
		for r := range v.Regions {
			v.Regions[r] = vendorURL
		}
	}
	return snap
}

type sessionRig struct {
	h      http.Handler
	mv     *mockvendor.Instance
	db     *pgxpool.Pool
	store  *registry.Store
	worker *delivery.Worker
	spec   *contract.Spec
	sink   *recordingSink
	mode   registry.Mode
}

func newSessionRig(t *testing.T, mode registry.Mode) *sessionRig {
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
	// Leftovers of other tests must not be claimed by this rig's worker.
	if _, err := db.Exec(t.Context(), `UPDATE hub.outbox SET next_attempt_at = now() + interval '1 day', claimed_until = now() + interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	mv := mockvendor.Start(t)
	store := &registry.Store{}
	store.Swap(sessionSnapshot(t, mv.VendorURL, 7, mode))
	m := telemetry.NewMetrics("gateway", "itest")
	log := slog.New(slog.DiscardHandler)
	cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	if err := cache.Warm(t.Context(), creds.AccountIDs(store.Snapshot())); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	t.Cleanup(client.Close)
	q := &queue.Outbox{}
	tr := &delivery.Transitions{DB: db, Queue: q}
	settler := delivery.Settler{Transitions: tr, Backoff: delivery.Backoff{}, Budget: delivery.NewBudget(nil)}
	sink := &recordingSink{}
	d := Deps{Snapshots: store, Creds: cache, Upstream: client, Metrics: m, Log: log, Audit: sink, DebugHeaders: true,
		Queueing: &Queueing{DB: db, Queue: q, Settler: settler, Name: "gw-" + core.NewULID(time.Now())}}
	w := &delivery.Worker{Name: "worker-" + core.NewULID(time.Now()), Pool: queue.PoolMessaging, DB: db, Queue: q, Transitions: tr,
		Snapshots: store, Attempter: &delivery.Attempter{Creds: cache, Upstream: client, Log: log}, Audit: sink, Budget: delivery.NewBudget(nil), Log: log}
	return &sessionRig{h: New(d).Handler(), mv: mv, db: db, store: store, worker: w, spec: contract.Load(t, "hub-public.v1.yaml"), sink: sink, mode: mode}
}

// publish swaps in the snapshot where v<active> is the active version (the publish-equivalent).
func (s *sessionRig) publish(t *testing.T, active int) {
	t.Helper()
	s.store.Swap(sessionSnapshot(t, s.mv.VendorURL, active, s.mode))
}

// vendorAnswers makes mockvendor answer every upload with X-Request-Id txn.
func (s *sessionRig) vendorAnswers(t *testing.T, txn string) {
	t.Helper()
	s.mv.Reset(t)
	s.mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Headers: map[string]string{"X-Request-Id": txn}, Body: okVendor}})
}

func (s *sessionRig) call(t *testing.T, path, session string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(trackBody))
	req.Header.Set("Authorization", "Bearer "+devKey)
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("X-Hub-Session", session)
	}
	rec := s.spec.Serve(t, s.h, req) // every exchange is validated against hub-public
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func (s *sessionRig) step1(t *testing.T) (*httptest.ResponseRecorder, map[string]any) {
	return s.call(t, "/v1/integrations/int_ses/invoke", "")
}

func (s *sessionRig) step2(t *testing.T, session string) (*httptest.ResponseRecorder, map[string]any) {
	return s.call(t, "/v1/integrations/int_ses/invoke/default", session)
}

// lastVendorCall is the one upload mockvendor recorded since the last vendorAnswers.
func (s *sessionRig) lastVendorCall(t *testing.T) http.Header {
	t.Helper()
	reqs := s.mv.Requests(t, nil)
	if len(reqs) != 1 {
		t.Fatalf("%d vendor calls, want 1", len(reqs))
	}
	return http.Header(reqs[0].Headers)
}

func wantVendor(t *testing.T, h http.Header, transformer, account, txn, openedBy string) {
	t.Helper()
	got := [4]string{h.Get("X-Test-Transformer"), h.Get("X-Test-Account"), h.Get("X-Test-Txn"), h.Get("X-Test-Opened-By")}
	if want := [4]string{transformer, account, txn, openedBy}; got != want {
		t.Fatalf("vendor saw transformer/account/txn/opened_by %v, want %v", got, want)
	}
}

// TestSessionNotFound: expired, unknown and another integration's sessions are 404
// SESSION_NOT_FOUND and nothing reaches the vendor.
func TestSessionNotFound(t *testing.T) {
	s := newSessionRig(t, registry.ModeSync)
	s.vendorAnswers(t, "txn-1")
	expired, err := sessions.Create(t.Context(), s.db, sessions.Open{TenantID: "orders-service", IntegrationID: "int_ses", Version: 7,
		AccountID: "ct-brandA-in", Data: map[string]any{"vendor_txn_id": "txn-old"}, TTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	rec, _ := s.step1(t)
	live := rec.Header().Get("X-Hub-Session")
	s.vendorAnswers(t, "txn-1")
	for name, c := range map[string][2]string{
		"expired":           {"/v1/integrations/int_ses/invoke/default", expired.ID},
		"unknown":           {"/v1/integrations/int_ses/invoke/default", "ses_" + core.NewULID(time.Now())},
		"other integration": {"/v1/integrations/int_evt_brandA/invoke/default", live},
		"dry-run expired":   {"/v1/integrations/int_ses/dry-run", expired.ID},
	} {
		rec, body := s.call(t, c[0], c[1])
		e, _ := body["error"].(map[string]any)
		if rec.Code != 404 || e["code"] != "SESSION_NOT_FOUND" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if n := len(s.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("%d vendor calls for unservable sessions", n)
	}
}

// TestSessionDryRunUsesTheSession: dry-run with X-Hub-Session shows the request the pinned
// account and transformer would send, with the session's data, and opens nothing.
func TestSessionDryRunUsesTheSession(t *testing.T) {
	s := newSessionRig(t, registry.ModeSync)
	s.vendorAnswers(t, "txn-v7")
	rec, _ := s.step1(t)
	sid := rec.Header().Get("X-Hub-Session")
	s.publish(t, 8)
	rec, body := s.call(t, "/v1/integrations/int_ses/dry-run", sid)
	if rec.Code != 200 || rec.Header().Get("X-Hub-Session") != "" {
		t.Fatalf("dry-run %d %s", rec.Code, rec.Body)
	}
	h, _ := body["upstream_request"].(map[string]any)["headers"].(map[string]any)
	if h["X-Test-Transformer"] != "TA@1" || h["X-Test-Account"] != "ct-brandA-in" || h["X-Test-Txn"] != "txn-v7" {
		t.Fatalf("dry-run headers %v", h)
	}
	// Without the session, dry-run shows the active v8 target.
	_, body = s.call(t, "/v1/integrations/int_ses/dry-run", "")
	h, _ = body["upstream_request"].(map[string]any)["headers"].(map[string]any)
	if h["X-Test-Transformer"] != "TB@1" || h["X-Test-Txn"] != "none" {
		t.Fatalf("dry-run without session %v", h)
	}
}

// TestSessionRefusesAadhaarLikeData: a response transformer that would store an Aadhaar-like
// number fails the step as TRANSFORM_FAILED and opens no session (DESIGN §14).
func TestSessionRefusesAadhaarLikeData(t *testing.T) {
	s := newSessionRig(t, registry.ModeSync)
	s.vendorAnswers(t, "2345 6789 0123")
	var before int
	_ = s.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.vendor_sessions`).Scan(&before)
	rec, body := s.step1(t)
	e, _ := body["error"].(map[string]any)
	if rec.Code != 500 || e["code"] != "TRANSFORM_FAILED" || rec.Header().Get("X-Hub-Session") != "" {
		t.Fatalf("step 1: %d %s", rec.Code, rec.Body)
	}
	var after int
	_ = s.db.QueryRow(t.Context(), `SELECT count(*) FROM hub.vendor_sessions`).Scan(&after)
	if after != before {
		t.Fatalf("a session was stored: %d -> %d", before, after)
	}
}

// TestQueuedSessionStepIsPinned: an async step 2 carries session_id and the version in its
// envelope; the worker delivers it through the session's account and transformer after v8 is
// published, and dead-letters it as SESSION_NOT_FOUND when the session expired in the queue.
func TestQueuedSessionStepIsPinned(t *testing.T) {
	s := newSessionRig(t, registry.ModeAsync)
	open := func(ttl time.Duration) string {
		sess, err := sessions.Create(t.Context(), s.db, sessions.Open{TenantID: "orders-service", IntegrationID: "int_ses", Version: 7,
			AccountID: "ct-brandA-in", Data: map[string]any{"vendor_txn_id": "txn-q", "opened_by": "RA@1"}, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}
	sid := open(0)
	s.publish(t, 8)
	s.vendorAnswers(t, "txn-step2")
	rec, body := s.step2(t, sid)
	if rec.Code != 202 {
		t.Fatalf("async step 2: %d %s", rec.Code, rec.Body)
	}
	id := body["request_id"].(string)
	var env core.Envelope
	var raw []byte
	if err := s.db.QueryRow(t.Context(), `SELECT envelope FROM hub.outbox WHERE request_id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &env)
	if env.SessionID == nil || *env.SessionID != sid || env.ResolvedVersion != 7 {
		t.Fatalf("envelope session %v version %d", env.SessionID, env.ResolvedVersion)
	}
	if n, err := s.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	wantVendor(t, s.lastVendorCall(t), "TA@1", "ct-brandA-in", "txn-q", "RA@1")
	if st := s.status(t, id); st["status"] != "succeeded" {
		t.Fatalf("status %v", st)
	}

	// The session expires while the step waits in the queue.
	short := open(200 * time.Millisecond)
	s.vendorAnswers(t, "txn-step2")
	rec, body = s.step2(t, short)
	if rec.Code != 202 {
		t.Fatalf("async step 2: %d %s", rec.Code, rec.Body)
	}
	id = body["request_id"].(string)
	time.Sleep(300 * time.Millisecond)
	if n, err := s.worker.ProcessOnce(t.Context()); err != nil || n != 1 {
		t.Fatalf("worker %d %v", n, err)
	}
	st := s.status(t, id)
	e, _ := st["error"].(map[string]any)
	if st["status"] != "dead_lettered" || e["code"] != "SESSION_NOT_FOUND" {
		t.Fatalf("expired queued step: %v", st)
	}
	if n := len(s.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("%d vendor calls for an expired session", n)
	}
}

func (s *sessionRig) status(t *testing.T, id string) map[string]any {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/requests/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+devKey)
	rec := s.spec.Serve(t, s.h, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}
