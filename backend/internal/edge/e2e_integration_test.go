//go:build integration

package edge

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// DESIGN §19 M3 Accept: caller → hub → mockvendor for each error class maps to the right
// canonical code; the audit timeline is complete and secrets never appear in stored events.

type staticLoader map[string]string

func (l staticLoader) Load(_ context.Context, ids []string) (map[string]creds.AccountSecrets, error) {
	out := map[string]creds.AccountSecrets{}
	for _, id := range ids {
		out[id] = creds.AccountSecrets{Active: creds.Slot{Version: 1, Values: l}}
	}
	return out, nil
}

// e2eSnapshot points every vendor region at mv and lowers the endpoint timeouts so timeout
// cases stay fast.
func e2eSnapshot(t *testing.T, vendorURL string) *registry.Snapshot {
	snap := devSnapshot(t)
	cp := *snap
	cp.Vendors = map[string]*registry.Vendor{}
	for k, v := range snap.Vendors {
		vc := *v
		vc.Regions = map[string]string{}
		for r := range v.Regions {
			vc.Regions[r] = vendorURL
		}
		vc.Endpoints = map[string]registry.Endpoint{}
		for name, ep := range v.Endpoints {
			ep.TimeoutMS, ep.ConnectTimeoutMS = 300, 200
			vc.Endpoints[name] = ep
		}
		cp.Vendors[k] = &vc
	}
	return &cp
}

type e2e struct {
	logs   *syncBuffer
	h      http.Handler
	mv     *mockvendor.Instance
	pool   *pgxpool.Pool
	writer *audit.Writer
	stop   func()
	ids    []string
	mu     sync.Mutex
}

func startE2E(t *testing.T) *e2e {
	t.Helper()
	return startE2EWith(t, nil)
}

// startE2EWith is startE2E with the snapshot changed by edit (nil: unchanged).
func startE2EWith(t *testing.T, edit func(*registry.Snapshot) *registry.Snapshot) *e2e {
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

	mv := mockvendor.Start(t)
	m := telemetry.NewMetrics("gateway", "e2e")
	secrets := telemetry.NewSecretSet()
	logs := &syncBuffer{}
	log := telemetry.NewLogger(logs, slog.LevelDebug, secrets, "gateway")
	cache, err := creds.NewCache(staticLoader(fakeCreds), secrets, m, creds.CacheOptions{Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	snap := e2eSnapshot(t, mv.VendorURL)
	if edit != nil {
		snap = edit(snap)
	}
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, err := upstream.New(m, upstream.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	writer := audit.NewWriter(audit.PGStore{Pool: pool}, m, audit.WriterOptions{FlushEvery: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { writer.Run(ctx); close(done) }()
	e := &e2e{mv: mv, pool: pool, writer: writer, logs: logs, stop: sync.OnceFunc(func() { cancel(); <-done })}
	t.Cleanup(e.stop)
	e.h = New(Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Audit: writer, Log: log,
		Masker: audit.NewMasker([]byte("e2e-mask-key-0123456789abcdef0123")), Metrics: m}).Handler()
	return e
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (e *e2e) call(t *testing.T, key, integration, reqBody string) (int, map[string]any) {
	t.Helper()
	rec := post(t, e.h, key, "/v1/integrations/"+integration+"/invoke", reqBody)
	// Calls rejected before authorization (401, unknown integration) are not audited.
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusNotFound {
		e.mu.Lock()
		e.ids = append(e.ids, rec.Header().Get("X-Hub-Request-Id"))
		e.mu.Unlock()
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func route(e *e2e, t *testing.T, b mockvendor.Behaviour) {
	e.mv.Reset(t)
	e.mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"}, Behaviour: b})
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestE2EErrorClasses(t *testing.T) {
	e := startE2E(t)
	ok := raw(`{"status":"success","processed":1,"unprocessed":[]}`)
	// One row per DESIGN §10.2 sync outcome, plus the caller-side errors.
	cases := []struct {
		name        string
		behaviour   mockvendor.Behaviour
		key, body   string
		integration string
		status      int
		code        string
		retryable   bool
		retryAfter  float64 // 0 = must be absent
		calls       int
	}{
		{"success", mockvendor.Behaviour{Status: 200, Body: ok}, devKey, trackBody, "int_evt_brandA", 200, "", false, 0, 1},
		{"success, status fallback transformer", mockvendor.Behaviour{Status: 200, Body: ok}, devKey, trackBody, "int_evt_brandB_min", 200, "", false, 0, 1},
		{"5xx", mockvendor.Behaviour{Status: 502, Body: raw(`{"status":"fail","error":"bad gateway"}`)}, devKey, trackBody, "int_evt_brandA", 503, "UPSTREAM_UNAVAILABLE", true, 0, 1},
		{"html error page", mockvendor.Behaviour{Status: 503, Malformed: &mockvendor.Malformed{Kind: "html_error_page"}}, devKey, trackBody, "int_evt_brandA", 503, "UPSTREAM_UNAVAILABLE", true, 0, 1},
		{"rate limited", mockvendor.Behaviour{Status: 200, Body: ok, RateLimit: &mockvendor.RateLimit{EveryNth: 1, RetryAfterMS: 2000}}, devKey, trackBody, "int_evt_brandA", 429, "UPSTREAM_RATE_LIMITED", true, 2000, 1},
		{"auth failed", mockvendor.Behaviour{Status: 401, Body: raw(`{"status":"fail","error":"Invalid passcode"}`)}, devKey, trackBody, "int_evt_brandA", 502, "UPSTREAM_AUTH_FAILED", false, 0, 1},
		{"rejected", mockvendor.Behaviour{Status: 400, Body: raw(`{"status":"fail","error":"Invalid event name"}`)}, devKey, trackBody, "int_evt_brandA", 422, "UPSTREAM_REJECTED", false, 0, 1},
		{"timeout, retried once (idempotent endpoint)", mockvendor.Behaviour{Status: 200, Body: ok, Latency: &mockvendor.Latency{Mode: "fixed", FixedMS: 800}}, devKey, trackBody, "int_evt_brandA", 504, "UPSTREAM_TIMEOUT", true, 0, 2},
		{"connection reset, retried once", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "reset"}}, devKey, trackBody, "int_evt_brandA", 503, "UPSTREAM_UNAVAILABLE", true, 0, 2},
		{"connection refused", mockvendor.Behaviour{Down: &mockvendor.Down{Mode: "refuse"}}, devKey, trackBody, "int_evt_brandA", 503, "UPSTREAM_UNAVAILABLE", true, 0, 1},
		{"validation", mockvendor.Behaviour{Status: 200, Body: ok}, devKey, `{"user": {}}`, "int_evt_brandA", 400, "VALIDATION_FAILED", false, 0, 0},
		{"unauthenticated", mockvendor.Behaviour{Status: 200, Body: ok}, "hk_wrong", trackBody, "int_evt_brandA", 401, "UNAUTHORIZED", false, 0, 0},
		{"unknown integration", mockvendor.Behaviour{Status: 200, Body: ok}, devKey, trackBody, "int_nope", 404, "INTEGRATION_NOT_FOUND", false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route(e, t, tc.behaviour)
			status, body := e.call(t, tc.key, tc.integration, tc.body)
			if status != tc.status {
				t.Fatalf("status %d, want %d: %v", status, tc.status, body)
			}
			if tc.code != "" {
				ce := body["error"].(map[string]any)
				if ce["code"] != tc.code || ce["retryable"] != tc.retryable {
					t.Fatalf("code %v retryable %v, want %s %v", ce["code"], ce["retryable"], tc.code, tc.retryable)
				}
				if got, has := ce["retry_after_ms"]; (tc.retryAfter == 0 && has && got != nil) || (tc.retryAfter > 0 && got != tc.retryAfter) {
					t.Fatalf("retry_after_ms %v, want %v", got, tc.retryAfter)
				}
			}
			if tc.name == "rejected" && body["error"].(map[string]any)["message"] != "Invalid event name" {
				t.Fatalf("vendor message not passed through: %v", body["error"])
			}
			// Refused connections never reach the recorder; count only what it can see.
			if tc.behaviour.Down == nil || tc.behaviour.Down.Mode != "refuse" {
				if got := len(e.mv.Requests(t, nil)); got != tc.calls {
					t.Fatalf("vendor calls %d, want %d", got, tc.calls)
				}
			}
			if tc.status == 200 {
				req := e.mv.Requests(t, nil)[0]
				if req.Headers["X-Clevertap-Passcode"][0] != fakeCreds["passcode"] {
					t.Fatalf("credentials not sent upstream: %v", req.Headers)
				}
			}
		})
	}
	// Dry-run: no vendor call (HUB-089).
	e.mv.Reset(t)
	if rec := post(t, e.h, devKey, "/v1/integrations/int_evt_brandA/dry-run", trackBody); rec.Code != 200 {
		t.Fatalf("dry-run %d", rec.Code)
	} else {
		e.ids = append(e.ids, rec.Header().Get("X-Hub-Request-Id"))
	}
	if n := len(e.mv.Requests(t, nil)); n != 0 {
		t.Fatalf("dry-run reached the vendor %d times", n)
	}

	e.stop() // drains the audit writer
	checkStoredAudit(t, e)
}

// checkStoredAudit is HUB-096: every request has a complete, gap-free timeline and a final
// record, and no stored event (payload or meta) holds a credential, the API key or an x-pii
// value in clear.
func checkStoredAudit(t *testing.T, e *e2e) {
	t.Helper()
	ctx := t.Context()
	for _, id := range e.ids {
		var status string
		var attempts, rows int
		if err := e.pool.QueryRow(ctx, `SELECT status, attempts, (SELECT count(*) FROM hub.request_attempts WHERE request_id = $1)
			FROM hub.requests WHERE request_id = $1`, id).Scan(&status, &attempts, &rows); err != nil {
			t.Fatalf("%s: no requests row: %v", id, err)
		}
		if attempts != rows {
			t.Errorf("%s: record says %d attempts, %d attempt rows", id, attempts, rows)
		}
		evRows, err := e.pool.Query(ctx, `SELECT seq, type, coalesce(payload_sha256, '') FROM hub.request_events WHERE request_id = $1 ORDER BY seq`, id)
		if err != nil {
			t.Fatal(err)
		}
		var types []string
		seq := 0
		for evRows.Next() {
			var n int
			var typ, sum string
			if err := evRows.Scan(&n, &typ, &sum); err != nil {
				t.Fatal(err)
			}
			seq++
			if n != seq {
				t.Errorf("%s: seq gap at %d", id, n)
			}
			if (typ == "request.received" || typ == "request.validated" || typ == "request.transformed") && sum == "" {
				t.Errorf("%s: %s has no sha256", id, typ)
			}
			types = append(types, typ)
		}
		evRows.Close()
		joined := strings.Join(types, ",")
		final := "request.completed"
		if status == "failed" {
			final = "request.failed"
		}
		if types[0] != "request.received" || types[len(types)-1] != final {
			t.Errorf("%s (%s): timeline %s", id, status, joined)
		}
		if attempts > 0 && !strings.HasPrefix(joined, "request.received,request.validated,integration.resolved,request.transformed,vendor.sent,vendor.responded") {
			t.Errorf("%s: a request that reached the vendor has timeline %s", id, joined)
		}
		if status == "succeeded" && attempts > 0 && !strings.Contains(joined, "vendor.sent,vendor.responded,response.transformed,request.completed") {
			t.Errorf("%s: sync timeline %s", id, joined)
		}
	}

	// Every credential value the store holds (a secret added to the dev creds is covered
	// automatically), the API key and the request's x-pii values.
	forbidden := []string{devKey, "u_48213", "ord_8821"}
	for _, v := range fakeCreds {
		forbidden = append(forbidden, v)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"level"`) {
		t.Fatal("no logs captured")
	}
	for _, f := range forbidden {
		if strings.Contains(logs, f) {
			t.Errorf("logs contain %q", f)
		}
	}
	var leaks int
	for _, f := range forbidden {
		var n int
		err := e.pool.QueryRow(ctx, `SELECT count(*) FROM hub.request_events
			WHERE request_id = ANY($1) AND (position($2 in convert_from(coalesce(payload, ''::bytea), 'UTF8')) > 0 OR position($2 in meta::text) > 0)`,
			e.ids, f).Scan(&n)
		if err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			t.Errorf("%d stored events contain %q", n, f)
			leaks += n
		}
	}
	// Positive control: the same query finds a value that is stored in clear (an untagged
	// field), so 0 leaks means absent, not a broken search.
	var control int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM hub.request_events WHERE request_id = ANY($1)
		AND position('order_placed' in convert_from(coalesce(payload, ''::bytea), 'UTF8')) > 0`, e.ids).Scan(&control); err != nil || control == 0 {
		t.Fatalf("grep control found %d events (%v)", control, err)
	}
	t.Logf("audit: %d requests checked, %d forbidden values searched, %d leaks", len(e.ids), len(forbidden), leaks)
}

// TestE2ENoDatabaseOnTheRequestPath: with the audit store stalled (a dead database), the sync
// path still answers at full speed; the audit writer drops instead (HUB-088, DESIGN §18.5).
func TestE2ENoDatabaseOnTheRequestPath(t *testing.T) {
	mv := mockvendor.Start(t)
	mv.SetRoute(t, "upload", mockvendor.RouteSpec{Match: mockvendor.RouteMatch{Method: "POST", Path: "/1/upload"},
		Behaviour: mockvendor.Behaviour{Status: 200, Body: raw(`{"status":"success","processed":1,"unprocessed":[]}`)}})
	m := telemetry.NewMetrics("gateway", "e2e")
	cache, _ := creds.NewCache(staticLoader(fakeCreds), telemetry.NewSecretSet(), m, creds.CacheOptions{})
	snap := e2eSnapshot(t, mv.VendorURL)
	if err := cache.Warm(t.Context(), creds.AccountIDs(snap)); err != nil {
		t.Fatal(err)
	}
	client, _ := upstream.New(m, upstream.Options{})
	defer client.Close()
	stalled := stalledStore{block: make(chan struct{})}
	writer := audit.NewWriter(stalled, m, audit.WriterOptions{Buffer: 2, MaxRows: 1, WriteTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() { writer.Run(ctx); close(done) }()
	defer func() { close(stalled.block); cancel(); <-done }()
	h := New(Deps{Snapshots: staticSnapshots{snap}, Creds: cache, Upstream: client, Audit: writer, Metrics: m}).Handler()
	start := time.Now()
	for i := range 20 {
		if rec := post(t, h, devKey, "/v1/integrations/int_evt_brandA/invoke", trackBody); rec.Code != 200 {
			t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("20 requests took %s with the database stalled", d)
	}
}

type stalledStore struct{ block chan struct{} }

func (s stalledStore) Write(ctx context.Context, _ audit.Batch) error {
	select {
	case <-s.block:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stalled: %w", ctx.Err())
	}
}

func (s stalledStore) EnsurePartitions(ctx context.Context, _ time.Time, _ int) error {
	return s.Write(ctx, audit.Batch{})
}
