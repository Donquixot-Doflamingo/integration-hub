//go:build integration

package adminapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// seedRequest writes one ledger request (record, events, attempts) the way audit.Writer does.
type seedRequest struct {
	tenant, integration, status, code, idem, account, policy string
	at                                                       time.Time
	events                                                   []audit.Event
	attempts                                                 int
}

func seed(t *testing.T, pool *pgxpool.Pool, s seedRequest) string {
	t.Helper()
	if s.policy == "" {
		s.policy = "masked"
	}
	id := "req_" + core.NewULID(s.at)
	rec := audit.Record{RequestID: id, CreatedAt: s.at, TenantID: s.tenant, IntegrationID: s.integration, IntegrationVersion: 2,
		Capability: "events.track@v1", Operation: "default", Mode: "async", Status: s.status, PayloadPolicy: s.policy,
		IdempotencyKey: s.idem, ErrorCode: s.code, Attempts: s.attempts, HubOverheadMS: 2.6, VendorMS: 40, CompletedAt: s.at.Add(1500 * time.Millisecond)}
	b := audit.Batch{Records: []audit.Record{rec}}
	for i, e := range s.events {
		e.RequestID = id
		if e.At.IsZero() {
			e.At = s.at.Add(time.Duration(i) * time.Millisecond)
		}
		b.Events = append(b.Events, e)
	}
	for n := 1; n <= s.attempts; n++ {
		b.Attempts = append(b.Attempts, audit.Attempt{StartedAt: s.at.Add(time.Duration(n) * 10 * time.Millisecond), RequestID: id, AttemptNo: n,
			AccountID: s.account, EndpointID: "upload@1", IntegrationVersion: 2, DurationMS: 12.4, HTTPStatus: 500 - n*100 + 100})
	}
	if err := (audit.PGStore{Pool: pool}).Write(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return id
}

func ensurePartitions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := (audit.PGStore{Pool: pool}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -3), 5); err != nil {
		t.Fatal(err)
	}
}

func uniq(prefix string) string { return prefix + strings.ToLower(core.NewULID(time.Now())[18:]) }

func ids(body map[string]any) []string {
	var out []string
	items, _ := body["items"].([]any)
	for _, it := range items {
		out = append(out, it.(map[string]any)["request_id"].(string))
	}
	return out
}

// TestSearchRequests is HUB-207: every filter, AND across filters, OR within status and code,
// newest first, cursor pages, tenant_viewer limited to its tenants, the 31-day span rule.
func TestSearchRequests(t *testing.T) {
	a := newAdminRig(t)
	pool := a.s.d.Pool
	ensurePartitions(t, pool)
	integ, other := uniq("int_s_"), uniq("int_o_")
	now := time.Now().UTC().Truncate(time.Millisecond)
	ok := seed(t, pool, seedRequest{tenant: "orders-service", integration: integ, status: "succeeded", account: "ct-brandA-in", attempts: 1, at: now.Add(-3 * time.Minute)})
	dead := seed(t, pool, seedRequest{tenant: "orders-service", integration: integ, status: "dead_lettered", code: "UPSTREAM_REJECTED", account: "ct-brandA-in", attempts: 1, at: now.Add(-2 * time.Minute)})
	parked := seed(t, pool, seedRequest{tenant: "orders-service", integration: integ, status: "parked", idem: "order-" + integ, account: "ct-brandB-us", attempts: 2, at: now.Add(-time.Minute)})
	foreign := seed(t, pool, seedRequest{tenant: "billing-service", integration: other, status: "succeeded", account: "ct-brandB-us", attempts: 1, at: now.Add(-time.Minute)})
	window := "&from=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339)) + "&to=" + url.QueryEscape(now.Add(time.Hour).Format(time.RFC3339))
	cases := []struct {
		name  string
		role  Role
		query string
		want  []string
	}{
		{"integration newest first", RoleViewer, "integration_id=" + integ, []string{parked, dead, ok}},
		{"status OR", RoleViewer, "integration_id=" + integ + "&status=dead_lettered,parked", []string{parked, dead}},
		{"code", RoleViewer, "integration_id=" + integ + "&code=UPSTREAM_REJECTED", []string{dead}},
		{"idempotency key", RoleViewer, "idempotency_key=order-" + integ, []string{parked}},
		{"account of the last attempt", RoleViewer, "integration_id=" + integ + "&account_id=ct-brandB-us", []string{parked}},
		{"vendor of the last attempt", RoleViewer, "integration_id=" + integ + "&vendor_id=clevertap", []string{parked, dead, ok}},
		{"unknown vendor", RoleViewer, "integration_id=" + integ + "&vendor_id=nobody", nil},
		{"tenant AND integration", RoleViewer, "tenant_id=billing-service&integration_id=" + integ, nil},
		{"other tenant as viewer", RoleViewer, "integration_id=" + other, []string{foreign}},
		{"tenant_viewer sees its tenants only", RoleTenantViewer, "integration_id=" + other, nil},
		{"tenant_viewer own tenant", RoleTenantViewer, "integration_id=" + integ + "&status=succeeded", []string{ok}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a.t = t
			resp, body := a.do(c.role, http.MethodGet, "/requests?"+c.query+window, "")
			if resp.Code != 200 || fmt.Sprint(ids(body)) != fmt.Sprint(c.want) {
				t.Fatalf("%d got %v want %v (%s)", resp.Code, ids(body), c.want, resp.Body)
			}
		})
	}
	a.t = t
	// Summary fields of one row.
	_, body := a.do(RoleViewer, http.MethodGet, "/requests?integration_id="+integ+"&status=parked"+window, "")
	row := body["items"].([]any)[0].(map[string]any)
	if row["vendor_id"] != "clevertap" || row["account_id"] != "ct-brandB-us" || row["final_version"] != 2.0 || row["attempts"] != 2.0 ||
		row["duration_ms"] != 1500.0 || row["hub_overhead_ms"] != 3.0 {
		t.Fatalf("summary %v", row)
	}
	// Cursor pages: 2 + 1, no overlap.
	_, p1 := a.do(RoleViewer, http.MethodGet, "/requests?limit=2&integration_id="+integ+window, "")
	cur, _ := p1["next_cursor"].(string)
	_, p2 := a.do(RoleViewer, http.MethodGet, "/requests?limit=2&integration_id="+integ+window+"&cursor="+cur, "")
	if got := append(ids(p1), ids(p2)...); cur == "" || fmt.Sprint(got) != fmt.Sprint([]string{parked, dead, ok}) || p2["next_cursor"] != nil {
		t.Fatalf("pages %v / %v", p1, p2)
	}
	// The default window is the last 24 hours.
	if _, body := a.do(RoleViewer, http.MethodGet, "/requests?integration_id="+integ, ""); len(ids(body)) != 3 {
		t.Fatalf("default window %v", body)
	}
	// Refusals.
	if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/requests?tenant_id=billing-service", ""); resp.Code != http.StatusForbidden {
		t.Fatalf("tenant outside scope: %d", resp.Code)
	}
	long := "&from=" + url.QueryEscape(now.AddDate(0, 0, -40).Format(time.RFC3339)) + "&to=" + url.QueryEscape(now.Format(time.RFC3339))
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/requests?integration_id="+integ+long, ""); resp.Code != http.StatusBadRequest {
		t.Fatalf("40-day span: %d", resp.Code)
	}
}

// TestSearchRequestsUsesIndexes is HUB-207's index acceptance: with sequential scans
// disabled the planner still finds an index path for every filter shape the explorer builds
// (pruned to the time range's partitions), so no filter depends on a full scan. The plan is
// the handler's own statement (requestFilter.sql).
func TestSearchRequestsUsesIndexes(t *testing.T) {
	pool := testPool(t)
	ensurePartitions(t, pool)
	now := time.Now().UTC()
	base := requestFilter{From: now.Add(-time.Hour), To: now, Limit: 50}
	at := now.Add(-time.Minute)
	shapes := map[string]func(f *requestFilter){
		"no filter":       func(*requestFilter) {},
		"tenant":          func(f *requestFilter) { f.Tenants = []string{"orders-service"} },
		"tenant+integ":    func(f *requestFilter) { f.Tenants, f.Integration = []string{"orders-service"}, "int_evt_brandA" },
		"status":          func(f *requestFilter) { f.Statuses = []string{"dead_lettered", "parked"} },
		"code":            func(f *requestFilter) { f.Codes = []string{"UPSTREAM_TIMEOUT"} },
		"idempotency key": func(f *requestFilter) { f.Idempotency = "order-8821-created" },
		"account":         func(f *requestFilter) { f.Accounts = []string{"ct-brandA-in"} },
		"cursor":          func(f *requestFilter) { f.AfterAt, f.AfterID = &at, "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6" },
	}
	for name, edit := range shapes {
		t.Run(name, func(t *testing.T) {
			f := base
			edit(&f)
			sql, args := f.sql()
			var plan []string
			err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
					return err
				}
				rows, err := tx.Query(t.Context(), "EXPLAIN "+sql, args...)
				if err != nil {
					return err
				}
				plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(plan, "\n")
			if strings.Contains(text, "Seq Scan on requests") || !strings.Contains(text, "Index") {
				t.Fatalf("no index path:\n%s", text)
			}
			if strings.Contains(text, "Seq Scan on request_attempts") {
				t.Fatalf("the last-attempt join scans attempts:\n%s", text)
			}
		})
	}
}

// TestRequestTimelineAndAttempts is HUB-207: events in seq order with sha256 (no prefix) and
// size on every event, masked payloads shown, metadata_only hidden, attempts with their
// version, and tenant_viewer gets 404 outside its tenants.
func TestRequestTimelineAndAttempts(t *testing.T) {
	a := newAdminRig(t)
	pool := a.s.d.Pool
	ensurePartitions(t, pool)
	now := time.Now().UTC().Truncate(time.Millisecond)
	raw := []byte(`{"user":{"id":"hmac:1f"},"event":{"name":"order_placed"}}`)
	sum := sha256.Sum256([]byte(`{"user":{"id":"u_1"},"event":{"name":"order_placed"}}`))
	// Rows arrive out of order (gateway block first, worker block 100+): seq orders them.
	events := []audit.Event{
		{Seq: 101, Type: audit.VendorSent, AttemptNo: 1, Meta: map[string]any{"attempt": 1}},
		{Seq: 1, Type: audit.RequestReceived, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Bytes: 55, Payload: raw, PayloadState: audit.PayloadMasked, Meta: map[string]any{"ip": "hmac:9"}},
		{Seq: 2, Type: audit.RequestAccepted},
		{Seq: 102, Type: audit.RetryScheduled},
		{Seq: 103, Type: audit.RequestDeadLettered},
	}
	integ := uniq("int_t_")
	id := seed(t, pool, seedRequest{tenant: "orders-service", integration: integ, status: "dead_lettered", account: "ct-brandA-in", attempts: 2, at: now, events: events})
	resp, body := a.do(RoleTenantViewer, http.MethodGet, "/requests/"+id+"/timeline", "")
	if resp.Code != 200 || body["payload_policy"] != "masked" {
		t.Fatalf("%d %v", resp.Code, body)
	}
	evs := body["events"].([]any)
	var seqs []any
	for _, e := range evs {
		seqs = append(seqs, e.(map[string]any)["seq"])
	}
	if fmt.Sprint(seqs) != "[1 2 101 103]" { // retry.scheduled is not a contract type yet
		t.Fatalf("seq order %v", seqs)
	}
	first := evs[0].(map[string]any)
	if first["sha256"] != hex.EncodeToString(sum[:]) || first["bytes"] != 55.0 || first["sealed"] != false {
		t.Fatalf("hash and size %v", first)
	}
	if p := first["payload"].(map[string]any); p["ip"] != "hmac:9" || p["body"].(map[string]any)["user"].(map[string]any)["id"] != "hmac:1f" {
		t.Fatalf("masked payload %v", p)
	}
	if e := evs[1].(map[string]any); e["sha256"] != emptySHA256 || e["bytes"] != 0.0 {
		t.Fatalf("payload-less event %v", e)
	}
	if e := evs[3].(map[string]any); e["type"] != "request.failed" {
		t.Fatalf("dead-letter event %v", e)
	}
	resp, body = a.do(RoleTenantViewer, http.MethodGet, "/requests/"+id+"/attempts", "")
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 2 {
		t.Fatalf("attempts %d %v", resp.Code, body)
	}
	if at := items[1].(map[string]any); at["attempt_no"] != 2.0 || at["integration_version"] != 2.0 || at["vendor_id"] != "clevertap" ||
		at["endpoint_ref"] != "upload@1" || at["vendor_status"] != 400.0 {
		t.Fatalf("attempt %v", at)
	}
	resp, body = a.do(RoleTenantViewer, http.MethodGet, "/requests/"+id, "")
	if resp.Code != 200 || body["status"] != "dead_lettered" || body["sealed"] != false || body["payload_policy"] != "masked" {
		t.Fatalf("detail %d %v", resp.Code, body)
	}

	// metadata_only: hashes and sizes, never a payload.
	meta := seed(t, pool, seedRequest{tenant: "orders-service", integration: integ, status: "succeeded", policy: "metadata_only", at: now,
		events: []audit.Event{{Seq: 1, Type: audit.RequestReceived, SHA256: "sha256:" + hex.EncodeToString(sum[:]), Bytes: 55, PayloadState: audit.PayloadOmitted, Meta: map[string]any{"ip": "x"}}}})
	_, body = a.do(RoleViewer, http.MethodGet, "/requests/"+meta+"/timeline", "")
	if e := body["events"].([]any)[0].(map[string]any); e["payload"] != nil || e["bytes"] != 55.0 {
		t.Fatalf("metadata_only %v", e)
	}

	// Another tenant's request does not exist for a tenant_viewer.
	foreign := seed(t, pool, seedRequest{tenant: "billing-service", integration: integ, status: "succeeded", at: now})
	for _, p := range []string{"", "/timeline", "/attempts"} {
		if resp, _ := a.do(RoleTenantViewer, http.MethodGet, "/requests/"+foreign+p, ""); resp.Code != http.StatusNotFound {
			t.Fatalf("foreign %s: %d", p, resp.Code)
		}
		if resp, _ := a.do(RoleViewer, http.MethodGet, "/requests/"+foreign+p, ""); resp.Code != http.StatusOK {
			t.Fatalf("viewer %s: %d", p, resp.Code)
		}
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/requests/req_"+core.NewULID(now), ""); resp.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", resp.Code)
	}
}
