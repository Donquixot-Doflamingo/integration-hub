//go:build integration

package control_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// erasureEnv is one test's isolated world: a random tenant and subject (so leftovers of other
// tests never match), the gateway's masker and sealer, and the ledger partitions.
type erasureEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	tenant  string
	subject string
	masker  *audit.Masker
	sealer  *audit.Sealer
	worker  string
}

func randomUpper(n int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func newErasureEnv(t *testing.T) *erasureEnv {
	t.Helper()
	_, pool := publisher(t)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	keys, err := audit.NewLocalKeys(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := (audit.PGStore{Pool: pool}).EnsurePartitions(t.Context(), time.Now().UTC().AddDate(0, 0, -1), 3); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(randomUpper(8))
	return &erasureEnv{t: t, pool: pool, tenant: "t-erase-" + suffix, subject: randomUpper(5) + "1234" + randomUpper(1),
		masker: audit.NewMasker(key), sealer: audit.NewSealer(keys), worker: "erase-test-" + suffix}
}

func (e *erasureEnv) erasure() *control.Erasure {
	return &control.Erasure{Pool: e.pool, Masker: e.masker, Sealer: e.sealer}
}

// request writes one request's audit trail exactly as the gateway does (Trail + PGStore) with the
// subject in the canonical body, the vendor request and the vendor response.
func (e *erasureEnv) request(tenant, subject string, policy registry.PayloadPolicy, status string) string {
	e.t.Helper()
	id := core.NewRequestID()
	now := time.Now().UTC()
	canonical := map[string]any{"pan": subject, "name": "Asha Rao", "consent": true}
	tr := audit.NewTrail(audit.TrailOptions{RequestID: id, Tenant: tenant, Policy: policy, Masker: e.masker, Sealer: e.sealer,
		PII: []registry.PIIPath{{Pointer: "/pan", Kind: "identifier"}, {Pointer: "/name", Kind: "name"}}, Canonical: canonical})
	raw, _ := json.Marshal(canonical)
	tr.Add(audit.RequestReceived, 0, raw, map[string]any{"key_id": "key_x"})
	tr.Add(audit.RequestValidated, 0, raw, map[string]any{"schema_id": "kyc.pan_verify@v1"})
	tr.Add(audit.IntegrationResolved, 0, nil, map[string]any{"version": 1})
	tr.AddJSON(audit.RequestTransformed, 0, map[string]any{"method": "POST", "body": map[string]any{"pan_number": subject}}, nil)
	tr.Add(audit.VendorResponded, 1, []byte(`{"status":"VALID","pan":"`+subject+`","msg":"PAN `+subject+` verified"}`), map[string]any{"status": 200})
	tr.Add(audit.ResponseTransformed, 0, []byte(`{"valid":true,"pan":"`+subject+`"}`), nil)
	tr.Add(audit.RequestCompleted, 0, nil, map[string]any{"status": 200})
	rec := audit.Record{RequestID: id, CreatedAt: now, TenantID: tenant, IntegrationID: "int_pan_verify", IntegrationVersion: 1, Operation: "default", Mode: "sync", Status: status, HTTPStatus: 200, Attempts: 1,
		HubOverheadMS: 1.25, VendorMS: 80.5, CompletedAt: now}
	att := audit.Attempt{StartedAt: now, RequestID: id, AttemptNo: 1, AccountID: "acct-kyc-1", EndpointID: "pan_verify@1",
		IntegrationVersion: 1, Host: "kyc.mock", DurationMS: 80.5, HTTPStatus: 200}
	if err := (audit.PGStore{Pool: e.pool}).Write(e.t.Context(), tr.Batch(rec, att)); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *erasureEnv) result(id, subject string) {
	e.t.Helper()
	res, _ := json.Marshal(map[string]any{"valid": true, "pan": subject})
	if err := (audit.Ledger{}).SaveResult(e.t.Context(), e.pool, id, e.tenant, res, nil, time.Now()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *erasureEnv) callback(id, subject, status string) {
	e.t.Helper()
	payload := `{"type":"request.completed","request_id":"` + id + `","result":{"pan":"` + subject + `"}}`
	if _, err := e.pool.Exec(e.t.Context(), `INSERT INTO hub.callback_deliveries (delivery_id, event_id, event_type, request_id,
		tenant_id, integration_id, integration_version, endpoint_id, subscription, payload, status, attempts, last_http_status)
		VALUES ($1, $2, 'request.completed', $3, $4, 'int_pan_verify', 1, 'rep_lending', 0, $5, $6, 1, 200)`,
		"dlv_"+core.NewULID(time.Now()), "evt_"+core.NewULID(time.Now()), id, e.tenant, []byte(payload), status); err != nil {
		e.t.Fatal(err)
	}
}

// queued puts a request in the outbox, claimed by the test's worker (in flight), with the
// subject in its canonical body, as the gateway does for an async/wait request.
func (e *erasureEnv) queued(id, subject string) {
	e.t.Helper()
	now := time.Now().UTC()
	env := core.Envelope{RequestID: id, Tenant: e.tenant, IntegrationID: "int_pan_verify", ResolvedVersion: 1,
		Operation: "default", Canonical: json.RawMessage(`{"pan":"` + subject + `"}`), Mode: "async", NextAttemptAt: now,
		ExpiresAt: now.Add(6 * time.Hour), CreatedAt: now}
	if err := (&queue.Outbox{}).EnqueueClaimed(e.t.Context(), e.pool, env, e.worker, 10*time.Minute); err != nil {
		e.t.Fatal(err)
	}
}

// deadLetter moves a queued request to the DLQ through the worker's own transition.
func (e *erasureEnv) deadLetter(id, lastRequest string) {
	e.t.Helper()
	if err := (&queue.Outbox{}).DeadLetter(e.t.Context(), e.pool, id, e.worker, queue.DeadLetter{Reason: "UPSTREAM_REJECTED",
		LastError: "rejected", AccountID: "acct-kyc-1", LastVendorRequest: json.RawMessage(lastRequest),
		LastVendorResponse: json.RawMessage(`{"status":400,"body":{"error":"PAN ` + e.subject + ` blocked"}}`)}); err != nil {
		e.t.Fatal(err)
	}
}

// grep searches every bytea, json, jsonb and text column of every hub table (partitioned
// parents included, so every partition) for the stored forms of the subject, and returns
// "table.column" for each hit. Tables added later are covered automatically.
func (e *erasureEnv) grep(forms [][]byte) []string {
	e.t.Helper()
	ctx := e.t.Context()
	rows, err := e.pool.Query(ctx, `SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
		FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'hub' AND c.relkind IN ('r', 'p') AND NOT c.relispartition AND a.attnum > 0 AND NOT a.attisdropped
			AND format_type(a.atttypid, a.atttypmod) IN ('bytea', 'json', 'jsonb', 'text', 'text[]') ORDER BY 1, 2`)
	if err != nil {
		e.t.Fatal(err)
	}
	type col struct{ table, column, typ string }
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (col, error) {
		var c col
		return c, r.Scan(&c.table, &c.column, &c.typ)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	if len(cols) < 20 {
		e.t.Fatalf("only %d payload-capable columns found: the catalog query is wrong", len(cols))
	}
	var hits []string
	for _, c := range cols {
		column := pgx.Identifier{c.column}.Sanitize()
		cond, formType, arg := fmt.Sprintf(`strpos(%s::text, f.form) > 0`, column), "text[]", any(textOf(forms))
		if c.typ == "bytea" {
			cond, formType, arg = fmt.Sprintf(`position(f.form IN %s) > 0`, column), "bytea[]", any(forms)
		}
		var hit bool
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE EXISTS (SELECT 1 FROM unnest($1::%s) AS f(form) WHERE %s))`,
			pgx.Identifier{"hub", c.table}.Sanitize(), formType, cond)
		if err := e.pool.QueryRow(ctx, q, arg).Scan(&hit); err != nil {
			e.t.Fatalf("grep %s.%s: %v", c.table, c.column, err)
		}
		if hit {
			hits = append(hits, c.table+"."+c.column)
		}
	}
	return hits
}

func textOf(forms [][]byte) []string {
	out := make([]string, len(forms))
	for i, f := range forms {
		out[i] = string(f)
	}
	return out
}

// sealedHolding opens every sealed payload of ids and reports those that hold a form.
func (e *erasureEnv) sealedHolding(ids []string, forms [][]byte) []string {
	e.t.Helper()
	rows, err := e.pool.Query(e.t.Context(), `SELECT request_id, seq, payload FROM hub.request_events
		WHERE request_id = ANY($1) AND payload_state = 'sealed'`, ids)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		var seq int
		var blob []byte
		if err := rows.Scan(&id, &seq, &blob); err != nil {
			e.t.Fatal(err)
		}
		pt, _, err := e.sealer.Open(e.tenant, id, seq, blob)
		if err != nil {
			e.t.Fatal(err)
		}
		if slices.ContainsFunc(forms, func(f []byte) bool { return bytes.Contains(pt, f) }) {
			out = append(out, fmt.Sprintf("%s/%d", id, seq))
		}
	}
	return out
}

// kept is everything DESIGN §13.3 says survives an erasure: per-event hashes, sizes, types,
// times and meta, the final records, attempts, dead-letter and callback metadata.
func (e *erasureEnv) kept(ids []string) string {
	e.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT request_id, seq, type, at, attempt_no, payload_sha256, payload_bytes, meta - 'pii_erased' FROM hub.request_events
			WHERE request_id = ANY($1) ORDER BY request_id, seq`,
		`SELECT request_id, created_at, tenant_id, status, payload_policy, http_status, error_code, attempts, hub_overhead_ms,
			vendor_ms, queue_wait_ms, completed_at FROM hub.requests WHERE request_id = ANY($1) ORDER BY request_id`,
		`SELECT request_id, attempt_no, started_at, account_id, endpoint_id, host, duration_ms, http_status, error_class
			FROM hub.request_attempts WHERE request_id = ANY($1) ORDER BY request_id, attempt_no`,
		`SELECT request_id, created_at, reason, last_error, attempts, account_id, replayed_at, envelope - 'canonical'
			FROM hub.dead_letters WHERE request_id = ANY($1) ORDER BY request_id`,
		`SELECT request_id, delivery_id, status, attempts, last_http_status, created_at, length(payload) > 0 OR status IN ('delivered', 'dead_lettered')
			FROM hub.callback_deliveries WHERE request_id = ANY($1) ORDER BY request_id, delivery_id`,
		`SELECT request_id, expires_at FROM hub.request_results WHERE request_id = ANY($1) ORDER BY request_id`,
	} {
		rows, err := e.pool.Query(e.t.Context(), q, ids)
		if err != nil {
			e.t.Fatal(err)
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				e.t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

// TestEraseSubjectPurgesEveryStoredBody is HUB-233: after the erasure no stored body anywhere in
// the schema holds the subject's identifier in any stored form (grep over every payload-capable
// column, plus the opened sealed payloads), hashes, sizes and timelines remain, the deletion is in
// change_log, and requests still in flight are deferred until a second run.
func TestEraseSubjectPurgesEveryStoredBody(t *testing.T) {
	e := newErasureEnv(t)
	ctx := t.Context()
	full := e.request(e.tenant, e.subject, registry.PayloadFull, "succeeded")
	e.result(full, e.subject)
	e.callback(full, e.subject, "delivered")
	masked := e.request(e.tenant, e.subject, registry.PayloadMasked, "dead_lettered")
	e.queued(masked, e.subject)
	e.deadLetter(masked, `{"method":"POST","body":{"pan_number":"`+string(e.masker.SubjectForms(e.tenant, e.subject)[1])+`"}}`)
	sealed := e.request(e.tenant, e.subject, registry.PayloadSealed, "succeeded")
	metaOnly := e.request(e.tenant, e.subject, registry.PayloadMetadataOnly, "succeeded") // only its callback holds the subject
	e.callback(metaOnly, e.subject, "dead_lettered")
	inFlight := core.NewRequestID() // still queued: deferred
	e.queued(inFlight, e.subject)
	callbackDue := e.request(e.tenant, e.subject, registry.PayloadMetadataOnly, "succeeded") // callback still to deliver: deferred
	e.callback(callbackDue, e.subject, "retry_scheduled")
	sess, err := sessions.Create(ctx, e.pool, sessions.Open{TenantID: e.tenant, IntegrationID: "int_pan_verify", Version: 1,
		AccountID: "acct-kyc-1", Data: map[string]any{"vendor_txn_id": "txn-1", "pan": e.subject}})
	if err != nil {
		t.Fatal(err)
	}
	all := []string{full, masked, sealed, metaOnly, inFlight, callbackDue}
	forms := e.masker.SubjectForms(e.tenant, e.subject)

	// Without the erasure the subject is everywhere: the grep below would fail.
	before := e.grep(forms)
	for _, want := range []string{"request_events.payload", "request_results.result", "dead_letters.envelope",
		"dead_letters.last_vendor_request", "dead_letters.last_vendor_response", "callback_deliveries.payload",
		"outbox.envelope", "vendor_sessions.data"} {
		if !slices.Contains(before, want) {
			t.Fatalf("setup: %s does not hold the subject (hits %v)", want, before)
		}
	}
	if got := e.sealedHolding([]string{sealed}, forms); len(got) == 0 {
		t.Fatal("setup: the sealed payloads do not hold the subject")
	}
	keptBefore := e.kept(all)

	// A dry run reports and changes nothing.
	dry, err := e.erasure().EraseSubject(ctx, control.EraseRequest{Tenant: e.tenant, Subject: e.subject, Actor: "dpo@example.com",
		Reason: "erasure request PRIV-42", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.ChangeID != "" || len(dry.Erased) != 4 || len(dry.Deferred) != 2 || dry.Sessions != 1 {
		t.Fatalf("dry run %+v", dry)
	}
	if got := e.grep(forms); !reflect.DeepEqual(got, before) {
		t.Fatalf("the dry run changed stored bodies: %v → %v", before, got)
	}

	// First run: everything terminal is erased, the two in-flight requests are deferred.
	res, err := e.erasure().EraseSubject(ctx, control.EraseRequest{Tenant: e.tenant, Subject: e.subject, Actor: "dpo@example.com",
		Reason: "erasure request PRIV-42"})
	if err != nil {
		t.Fatal(err)
	}
	wantErased := sorted(full, masked, sealed, metaOnly)
	if !reflect.DeepEqual(res.Erased, wantErased) || !reflect.DeepEqual(res.Deferred, sorted(inFlight, callbackDue)) {
		t.Fatalf("erased %v deferred %v, want %v and %v", res.Erased, res.Deferred, wantErased, sorted(inFlight, callbackDue))
	}
	if res.DeadLetters != 1 || res.Callbacks != 2 || res.Results != 1 || res.Sessions != 1 || res.Events == 0 {
		t.Fatalf("counts %+v", res)
	}
	if got := e.grep(forms); !reflect.DeepEqual(got, []string{"callback_deliveries.payload", "outbox.envelope"}) {
		t.Fatalf("after the first run the subject is still in %v; want only the deferred outbox envelope and undelivered callback", got)
	}
	if _, err := sessions.Get(ctx, e.pool, e.tenant, "int_pan_verify", sess.ID); !errors.Is(err, sessions.ErrNotFound) {
		t.Fatalf("session holding the subject: %v", err)
	}

	// Hashes, sizes and timelines remain; only bodies are gone.
	if after := e.kept(all); after != keptBefore {
		t.Fatalf("metadata changed:\nbefore:\n%s\nafter:\n%s", keptBefore, after)
	}
	var withBody, erasedMarked, hashed int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE payload IS NOT NULL),
			count(*) FILTER (WHERE payload_state = 'omitted' AND meta->>'pii_erased' = $2),
			count(*) FILTER (WHERE payload_sha256 IS NOT NULL AND payload_bytes > 0)
		FROM hub.request_events WHERE request_id = ANY($1)`, wantErased, res.ChangeID).Scan(&withBody, &erasedMarked, &hashed); err != nil {
		t.Fatal(err)
	}
	if withBody != 0 || int64(erasedMarked) != res.Events || hashed < int(res.Events) {
		t.Fatalf("events: %d with a body, %d marked erased (want %d), %d hashed", withBody, erasedMarked, res.Events, hashed)
	}
	if err := (&queue.Outbox{}).Replay(ctx, e.pool, masked, time.Now().Add(time.Hour)); !errors.Is(err, queue.ErrLost) {
		t.Fatalf("replay of an erased dead letter: %v, want ErrLost", err)
	}

	// The deletion is in change_log, with a subject reference and no stored form.
	var actor, action, objectType, objectID, reason string
	var after map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT actor, action, object_type, object_id, reason, after FROM hub.change_log WHERE change_id = $1`,
		res.ChangeID).Scan(&actor, &action, &objectType, &objectID, &reason, &after); err != nil {
		t.Fatal(err)
	}
	if actor != "dpo@example.com" || action != control.EraseAction || objectType != "request" || objectID != "bulk" ||
		reason != "erasure request PRIV-42" || after["subject_ref"] != e.masker.SubjectRef(e.tenant, e.subject) ||
		after["tenant_id"] != e.tenant || after["erased"] != float64(4) || after["deferred"] != float64(2) {
		t.Fatalf("change log %s %s %s %s %q %v", actor, action, objectType, objectID, reason, after)
	}

	// The deferred requests finish (dead letter, callback dead-lettered); a second run erases them.
	e.deadLetter(inFlight, `{"method":"POST","body":{"pan_number":"`+e.subject+`"}}`)
	if _, err := e.pool.Exec(ctx, `UPDATE hub.callback_deliveries SET status = 'dead_lettered' WHERE request_id = $1`, callbackDue); err != nil {
		t.Fatal(err)
	}
	res2, err := e.erasure().EraseSubject(ctx, control.EraseRequest{Tenant: e.tenant, Subject: e.subject, Actor: "dpo@example.com",
		Reason: "erasure request PRIV-42, deferred part"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res2.Erased, sorted(inFlight, callbackDue)) || len(res2.Deferred) != 0 {
		t.Fatalf("second run erased %v deferred %v", res2.Erased, res2.Deferred)
	}
	if got := e.grep(forms); len(got) != 0 {
		t.Fatalf("the subject is still stored in %v", got)
	}
	if got := e.sealedHolding(all, forms); len(got) != 0 {
		t.Fatalf("sealed payloads still hold the subject: %v", got)
	}
	var logged int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM hub.change_log WHERE action = $1 AND after->>'tenant_id' = $2`,
		control.EraseAction, e.tenant).Scan(&logged); err != nil || logged != 2 {
		t.Fatalf("change log rows %d %v, want 2 (the dry run writes none)", logged, err)
	}
}

// TestEraseSubjectScope: only the given tenant's requests that hold the given subject lose their
// bodies; another subject of the same tenant and the same identifier at another tenant stay.
func TestEraseSubjectScope(t *testing.T) {
	e := newErasureEnv(t)
	ctx := t.Context()
	other := randomUpper(5) + "9876" + randomUpper(1)
	target := e.request(e.tenant, e.subject, registry.PayloadMasked, "succeeded")
	sameTenant := e.request(e.tenant, other, registry.PayloadMasked, "succeeded")
	otherTenant := e.request(e.tenant+"-b", e.subject, registry.PayloadFull, "succeeded")
	sealedOther := e.request(e.tenant, other, registry.PayloadSealed, "succeeded") // opened and left alone
	req := control.EraseRequest{Tenant: e.tenant, Subject: e.subject, Actor: "dpo@example.com", Reason: "erasure request PRIV-43"}

	// Without the seal key the sealed payloads cannot be searched: refuse, change nothing.
	noKey := e.erasure()
	noKey.Sealer = nil
	if _, err := noKey.EraseSubject(ctx, req); !errors.Is(err, audit.ErrNoSealKey) {
		t.Fatalf("erasure without the seal key: %v, want ErrNoSealKey", err)
	}
	// A window that excludes every request finds nothing.
	windowed := req
	windowed.Since, windowed.DryRun = time.Now().Add(time.Hour), true
	if res, err := e.erasure().EraseSubject(ctx, windowed); err != nil || len(res.Erased)+len(res.Deferred) != 0 {
		t.Fatalf("window after every request: %+v %v", res, err)
	}
	// Invalid requests are refused before any read.
	bad := req
	bad.Reason = "gdpr"
	if _, err := e.erasure().EraseSubject(ctx, bad); !errors.Is(err, control.ErrInvalidErasure) {
		t.Fatalf("short reason: %v", err)
	}

	res, err := e.erasure().EraseSubject(ctx, control.EraseRequest{Tenant: e.tenant, Subject: e.subject, Actor: "dpo@example.com",
		Reason: "erasure request PRIV-43"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Erased, []string{target}) {
		t.Fatalf("erased %v, want only %s", res.Erased, target)
	}
	rows, err := e.pool.Query(ctx, `SELECT request_id, count(*) FILTER (WHERE payload IS NOT NULL)::int FROM hub.request_events
		WHERE request_id = ANY($1) GROUP BY request_id`, []string{target, sameTenant, otherTenant, sealedOther})
	if err != nil {
		t.Fatal(err)
	}
	bodies := map[string]int{}
	var id string
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&id, &n}, func() error { bodies[id] = n; return nil }); err != nil {
		t.Fatal(err)
	}
	// Each request stores 5 bodies (received, validated, transformed, responded, response transformed).
	want := map[string]int{target: 0, sameTenant: 5, otherTenant: 5, sealedOther: 5}
	if !reflect.DeepEqual(bodies, want) {
		t.Fatalf("bodies per request %v, want %v", bodies, want)
	}
}

func sorted(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
