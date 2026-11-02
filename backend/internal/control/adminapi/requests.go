package adminapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Request explorer (DESIGN §16.1, §13, HUB-207): search the request ledger, read one request,
// its §13.1 audit timeline and its upstream attempts. Payloads follow the request's audit
// policy; sealed payloads are only readable through unsealRequest (HUB-208).

func init() { registrars = append(registrars, (*Server).registerRequests) }

func (s *Server) registerRequests() {
	s.handle("searchRequests", RoleTenantViewer, false, "GET /requests", s.searchRequests)
	s.handle("getRequest", RoleTenantViewer, false, "GET /requests/{request_id}", s.getRequest)
	s.handle("getRequestTimeline", RoleTenantViewer, false, "GET /requests/{request_id}/timeline", s.getRequestTimeline)
	s.handle("listRequestAttempts", RoleTenantViewer, false, "GET /requests/{request_id}/attempts", s.listRequestAttempts)
	s.handle("unsealRequest", RoleAdmin, true, "POST /requests/{request_id}/unseal", s.unsealRequest)
}

// sealer is the key source unsealRequest decrypts with (nil: unseal answers 500).
func (s *Server) sealer() *audit.Sealer { return s.d.Sealer }

// maxSearchSpan is the widest created_at range one search may cover (contract: ≤ 31 days);
// defaultSearchSpan applies when from is omitted.
const (
	maxSearchSpan     = 31 * 24 * time.Hour
	defaultSearchSpan = 24 * time.Hour
)

var errNoDB = errors.New("no database")

// requestFilter is one explorer search. Every query bounds created_at, so Postgres prunes
// the day partitions before it uses an index (00003, 00008).
type requestFilter struct {
	Tenants     []string // nil: every tenant
	Integration string
	Statuses    []string
	Codes       []string
	Accounts    []string // last attempt's account; nil: no filter
	Idempotency string
	From, To    time.Time
	AfterAt     *time.Time // cursor: strictly older than (AfterAt, AfterID)
	AfterID     string
	Limit       int
}

const summaryColumns = `r.request_id, r.tenant_id, r.integration_id, r.capability, r.operation, r.mode, r.status, r.error_code,
	COALESCE(la.integration_version, r.integration_version), la.account_id, r.attempts, r.created_at, r.completed_at, r.hub_overhead_ms`

// lastAttempt joins the latest attempt of each request (vendor, account, version).
const lastAttempt = `LEFT JOIN LATERAL (SELECT a.account_id, a.integration_version FROM hub.request_attempts a
	WHERE a.request_id = r.request_id AND a.started_at >= r.created_at - interval '1 minute'
	ORDER BY a.attempt_no DESC LIMIT 1) la ON true`

// sql builds the search statement (shared by the handler and the EXPLAIN test).
func (f requestFilter) sql() (string, []any) {
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	where := []string{"r.created_at >= " + arg(f.From), "r.created_at < " + arg(f.To)}
	if f.Tenants != nil {
		where = append(where, "r.tenant_id = ANY("+arg(f.Tenants)+")")
	}
	if f.Integration != "" {
		where = append(where, "r.integration_id = "+arg(f.Integration))
	}
	if len(f.Statuses) > 0 {
		where = append(where, "r.status = ANY("+arg(f.Statuses)+")")
	}
	if len(f.Codes) > 0 {
		where = append(where, "r.error_code = ANY("+arg(f.Codes)+")")
	}
	if f.Idempotency != "" {
		where = append(where, "r.idempotency_key = "+arg(f.Idempotency))
	}
	if f.Accounts != nil {
		where = append(where, "la.account_id = ANY("+arg(f.Accounts)+")")
	}
	if f.AfterAt != nil {
		where = append(where, "(r.created_at, r.request_id) < ("+arg(*f.AfterAt)+", "+arg(f.AfterID)+")")
	}
	return `SELECT ` + summaryColumns + ` FROM hub.requests r ` + lastAttempt + `
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY r.created_at DESC, r.request_id DESC LIMIT ` + strconv.Itoa(f.Limit+1), args
}

// requestSummary is the contract RequestSummary.
type requestSummary struct {
	RequestID     string     `json:"request_id"`
	TenantID      string     `json:"tenant_id"`
	IntegrationID string     `json:"integration_id"`
	Capability    string     `json:"capability"`
	Operation     string     `json:"operation"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	Code          *string    `json:"code"`
	FinalVersion  *int       `json:"final_version"`
	VendorID      *string    `json:"vendor_id"`
	AccountID     *string    `json:"account_id"`
	Attempts      int        `json:"attempts"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at"`
	DurationMS    *int64     `json:"duration_ms"`
	HubOverheadMS *int64     `json:"hub_overhead_ms"`
}

func scanSummary(row pgx.Row, snap *registry.Snapshot) (requestSummary, error) {
	var s requestSummary
	var overhead *float64
	if err := row.Scan(&s.RequestID, &s.TenantID, &s.IntegrationID, &s.Capability, &s.Operation, &s.Mode, &s.Status, &s.Code,
		&s.FinalVersion, &s.AccountID, &s.Attempts, &s.CreatedAt, &s.CompletedAt, &overhead); err != nil {
		return s, err
	}
	s.CreatedAt = s.CreatedAt.UTC()
	if s.CompletedAt != nil {
		c := s.CompletedAt.UTC()
		s.CompletedAt = &c
		d := max(c.Sub(s.CreatedAt).Milliseconds(), 0)
		s.DurationMS = &d
	}
	s.HubOverheadMS = millis(overhead)
	if s.AccountID != nil {
		if v := vendorOf(snap, *s.AccountID); v != "" {
			s.VendorID = &v
		}
	}
	return s, nil
}

func millis(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(math.Round(max(*v, 0)))
	return &n
}

// vendorOf is the vendor of an account in the current snapshot ("" when unknown).
func vendorOf(snap *registry.Snapshot, account string) string {
	if snap == nil {
		return ""
	}
	if a := snap.Accounts[account]; a != nil {
		return a.Vendor
	}
	return ""
}

func (s *Server) servingSnapshot() *registry.Snapshot {
	if s.d.Snapshot == nil {
		return nil
	}
	return s.d.Snapshot()
}

// listParam reads a form-style list (status=a,b) or repeated values.
func listParam(r *http.Request, name string) []string {
	var out []string
	for _, v := range r.URL.Query()[name] {
		for p := range strings.SplitSeq(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// tenantScope returns the tenants a list may show: nil for every tenant, else the requested
// tenant (if allowed) or a tenant_viewer's own tenants. A tenant outside the scope is 403.
func tenantScope(r *http.Request) ([]string, error) {
	p, want := principal(r), r.URL.Query().Get("tenant_id")
	switch {
	case want != "" && !p.SeesTenant(want):
		return nil, &Error{Status: http.StatusForbidden, Code: CodeForbidden, Message: "tenant_id is outside the tenants of your token"}
	case want != "":
		return []string{want}, nil
	case p.Scoped():
		return append([]string{}, p.Tenants...), nil
	}
	return nil, nil
}

// searchWindow applies the contract's time rules: to defaults to now, from to 24 h earlier,
// and the span is at most 31 days.
func searchWindow(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	from, to, err := timeRange(r)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	t := now.UTC()
	if to != nil {
		t = *to
	}
	f := t.Add(-defaultSearchSpan)
	if from != nil {
		f = *from
	}
	if !f.Before(t) {
		return time.Time{}, time.Time{}, badRequest("", "from must be before to")
	}
	if t.Sub(f) > maxSearchSpan {
		return time.Time{}, time.Time{}, badRequest("", "The time range may span at most 31 days")
	}
	return f, t, nil
}

func (s *Server) searchRequests(w http.ResponseWriter, r *http.Request) {
	f, err := s.requestFilterOf(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Pool == nil {
		s.fail(w, r, errNoDB)
		return
	}
	snap := s.servingSnapshot()
	sql, args := f.sql()
	rows, err := s.d.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (requestSummary, error) { return scanSummary(row, snap) })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(items) > f.Limit {
		items = items[:f.Limit]
		last := items[f.Limit-1]
		next = nextCursor(last.CreatedAt.Format(time.RFC3339Nano), last.RequestID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) requestFilterOf(r *http.Request) (requestFilter, error) {
	limit, cursor, err := page(r)
	if err != nil {
		return requestFilter{}, err
	}
	from, to, err := searchWindow(r, s.d.Now())
	if err != nil {
		return requestFilter{}, err
	}
	tenants, err := tenantScope(r)
	if err != nil {
		return requestFilter{}, err
	}
	q := r.URL.Query()
	f := requestFilter{Tenants: tenants, Integration: q.Get("integration_id"), Statuses: listParam(r, "status"), Codes: listParam(r, "code"),
		Idempotency: q.Get("idempotency_key"), From: from, To: to, Limit: limit}
	if a := q.Get("account_id"); a != "" {
		f.Accounts = []string{a}
	}
	if v := q.Get("vendor_id"); v != "" {
		accounts := []string{}
		if snap := s.servingSnapshot(); snap != nil {
			for id, a := range snap.Accounts {
				if a.Vendor == v && (f.Accounts == nil || slices.Contains(f.Accounts, id)) {
					accounts = append(accounts, id)
				}
			}
		}
		f.Accounts = accounts
	}
	if len(cursor) == 2 {
		at, perr := time.Parse(time.RFC3339Nano, cursor[0])
		if perr != nil {
			return requestFilter{}, badRequest("", "cursor is not a cursor of this list")
		}
		f.AfterAt, f.AfterID = &at, cursor[1]
	} else if cursor != nil {
		return requestFilter{}, badRequest("", "cursor is not a cursor of this list")
	}
	return f, nil
}

// requestRow is one hub.requests row with what the detail and the timeline need.
type requestRow struct {
	requestSummary
	Policy          string
	IdempotencyKey  *string
	SnapshotVersion *int64
	TraceID         *string
	VendorMS        *float64
	QueueWaitMS     *float64
}

// partitionBounds bounds created_at for a lookup by id (the ULID's time ± 1 minute).
func partitionBounds(id string) (time.Time, time.Time, bool) {
	t, ok := core.RequestIDTime(id)
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return t.Add(-time.Minute), t.Add(time.Minute), true
}

// loadRequest reads one request the caller may see; anything else is 404.
func (s *Server) loadRequest(ctx context.Context, r *http.Request, id string) (requestRow, error) {
	var row requestRow
	from, to, ok := partitionBounds(id)
	if !ok {
		return row, notFound("Request")
	}
	if s.d.Pool == nil {
		return row, errNoDB
	}
	var overhead *float64
	err := s.d.Pool.QueryRow(ctx, `SELECT `+summaryColumns+`, r.payload_policy, r.idempotency_key, r.snapshot_version, r.trace_id,
		r.vendor_ms, r.queue_wait_ms
FROM hub.requests r `+lastAttempt+`
WHERE r.created_at BETWEEN $2 AND $3 AND r.request_id = $1`, id, from, to).Scan(
		&row.RequestID, &row.TenantID, &row.IntegrationID, &row.Capability, &row.Operation, &row.Mode, &row.Status, &row.Code,
		&row.FinalVersion, &row.AccountID, &row.Attempts, &row.CreatedAt, &row.CompletedAt, &overhead,
		&row.Policy, &row.IdempotencyKey, &row.SnapshotVersion, &row.TraceID, &row.VendorMS, &row.QueueWaitMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, notFound("Request")
	}
	if err != nil {
		return row, err
	}
	if !principal(r).SeesTenant(row.TenantID) {
		return row, notFound("Request")
	}
	row.CreatedAt = row.CreatedAt.UTC()
	if row.CompletedAt != nil {
		c := row.CompletedAt.UTC()
		row.CompletedAt = &c
		d := max(c.Sub(row.CreatedAt).Milliseconds(), 0)
		row.DurationMS = &d
	}
	row.HubOverheadMS = millis(overhead)
	if row.AccountID != nil {
		if v := vendorOf(s.servingSnapshot(), *row.AccountID); v != "" {
			row.VendorID = &v
		}
	}
	return row, nil
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.loadRequest(ctx, r, r.PathValue("request_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var expires, next *time.Time
	_ = s.d.Pool.QueryRow(ctx, `SELECT expires_at, next_attempt_at FROM hub.outbox WHERE request_id = $1`, row.RequestID).Scan(&expires, &next)
	var stored, storedErr []byte
	_ = s.d.Pool.QueryRow(ctx, `SELECT result, error FROM hub.request_results WHERE request_id = $1`, row.RequestID).Scan(&stored, &storedErr)
	result, err := s.resultOf(ctx, row, stored)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	timings := map[string]any{}
	for k, v := range map[string]*int64{"total_ms": row.DurationMS, "hub_overhead_ms": row.HubOverheadMS,
		"vendor_ms": millis(row.VendorMS), "queue_wait_ms": millis(row.QueueWaitMS)} {
		if v != nil {
			timings[k] = *v
		}
	}
	writeJSON(w, http.StatusOK, struct {
		requestSummary
		IdempotencyKey  *string         `json:"idempotency_key"`
		ExpiresAt       *time.Time      `json:"expires_at"`
		NextAttemptAt   *time.Time      `json:"next_attempt_at"`
		PayloadPolicy   string          `json:"payload_policy"`
		Sealed          bool            `json:"sealed"`
		Timings         map[string]any  `json:"timings"`
		Result          json.RawMessage `json:"result"`
		Error           json.RawMessage `json:"error"`
		SnapshotVersion *int64          `json:"snapshot_version"`
		TraceID         *string         `json:"trace_id"`
	}{row.requestSummary, row.IdempotencyKey, utc(expires), utc(next), row.Policy, row.Policy == string(registry.PayloadSealed), timings,
		result, objectOrNull(storedErr), row.SnapshotVersion, row.TraceID})
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// resultOf is the canonical result under the request's policy: the kept result (full), the
// stored response.transformed payload (masked), never anything for sealed or metadata_only.
func (s *Server) resultOf(ctx context.Context, row requestRow, kept []byte) (json.RawMessage, error) {
	switch registry.PayloadPolicy(row.Policy) {
	case registry.PayloadFull:
		if o := objectOrNull(kept); string(o) != "null" {
			return o, nil
		}
	case registry.PayloadMasked:
	default:
		return objectOrNull(nil), nil
	}
	var payload []byte
	err := s.d.Pool.QueryRow(ctx, `SELECT payload FROM hub.request_events WHERE request_id = $1 AND at >= $2 AND type = $3
		AND payload_state IN ('full', 'masked') ORDER BY seq DESC LIMIT 1`, row.RequestID, row.CreatedAt.Add(-time.Minute), audit.ResponseTransformed).Scan(&payload)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return objectOrNull(payload), nil
}

// objectOrNull returns b when it is a JSON object, else null (contract: object or null).
func objectOrNull(b []byte) json.RawMessage {
	var m map[string]json.RawMessage
	if len(b) == 0 || json.Unmarshal(b, &m) != nil || m == nil {
		return json.RawMessage("null")
	}
	return b
}

// timelineTypes are the contract's TimelineEventType values; aliases map ledger event types
// the contract does not list yet (change request in the HUB-207 report).
var (
	timelineTypes = map[string]bool{
		"request.received": true, "request.validated": true, "integration.resolved": true, "idempotency.checked": true,
		"request.transformed": true, "policy.applied": true, "request.persisted": true, "request.accepted": true,
		"request.parked": true, "vendor.sent": true, "vendor.responded": true, "response.transformed": true,
		"response.transform_failed": true, "callback.delivered": true, "request.completed": true, "request.failed": true,
		"request.expired": true,
	}
	timelineAliases = map[string]string{audit.RequestDeadLettered: audit.RequestFailed}
)

// emptySHA256 is the hash of an event without a payload (zero bytes).
var emptySHA256 = func() string { s := sha256.Sum256(nil); return hex.EncodeToString(s[:]) }()

// timelineEvent is the contract TimelineEvent.
type timelineEvent struct {
	Seq        int            `json:"seq"`
	Type       string         `json:"type"`
	At         time.Time      `json:"at"`
	AttemptNo  *int           `json:"attempt_no"`
	Payload    map[string]any `json:"payload"`
	PayloadRef *string        `json:"payload_ref"`
	SHA256     string         `json:"sha256"`
	Bytes      int            `json:"bytes"`
	Sealed     bool           `json:"sealed"`
	Truncated  bool           `json:"truncated"`
}

// storedEvent is one request_events row as stored.
type storedEvent struct {
	Seq        int
	Type       string
	At         time.Time
	AttemptNo  *int
	SHA256     *string
	Bytes      *int
	Payload    []byte
	State      audit.PayloadState
	PayloadRef *string
	Meta       map[string]any
}

func (s *Server) loadEvents(ctx context.Context, row requestRow) ([]storedEvent, error) {
	rows, err := s.d.Pool.Query(ctx, `SELECT seq, type, at, attempt_no, payload_sha256, payload_bytes, payload, payload_state, payload_ref, meta
FROM hub.request_events WHERE request_id = $1 AND at >= $2 ORDER BY seq`, row.RequestID, row.CreatedAt.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (storedEvent, error) {
		var e storedEvent
		var state string
		err := r.Scan(&e.Seq, &e.Type, &e.At, &e.AttemptNo, &e.SHA256, &e.Bytes, &e.Payload, &state, &e.PayloadRef, &e.Meta)
		e.State = audit.PayloadState(state)
		return e, err
	})
}

// view renders a stored event. unsealed (unsealRequest only) shows sealed events with clear,
// their decrypted payload; otherwise sealed and metadata_only events carry no payload.
func (e storedEvent) view(policy string, unsealed bool, clear []byte, truncated bool) (timelineEvent, bool) {
	typ := e.Type
	if alias, ok := timelineAliases[typ]; ok {
		typ = alias
	}
	if !timelineTypes[typ] {
		return timelineEvent{}, false
	}
	v := timelineEvent{Seq: e.Seq, Type: typ, At: e.At.UTC(), AttemptNo: e.AttemptNo, PayloadRef: e.PayloadRef, SHA256: emptySHA256,
		Sealed: e.State == audit.PayloadSealed && !unsealed, Truncated: e.State == audit.PayloadTruncated || truncated}
	if e.SHA256 != nil {
		v.SHA256 = strings.TrimPrefix(*e.SHA256, "sha256:")
	}
	if e.Bytes != nil {
		v.Bytes = *e.Bytes
	}
	hidden := policy == string(registry.PayloadMetadataOnly) || e.State == audit.PayloadOmitted ||
		((policy == string(registry.PayloadSealed) || e.State == audit.PayloadSealed) && !unsealed)
	if hidden {
		return v, true
	}
	body := e.Payload
	if e.State == audit.PayloadSealed {
		body = clear
	}
	v.Payload = map[string]any{}
	for k, m := range e.Meta {
		v.Payload[k] = m
	}
	if body != nil {
		var doc any
		if json.Unmarshal(body, &doc) == nil {
			v.Payload["body"] = doc
		} else {
			v.Payload["body"] = string(body) // truncated JSON or a non-JSON vendor body
		}
	}
	return v, true
}

func (s *Server) getRequestTimeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.loadRequest(ctx, r, r.PathValue("request_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	stored, err := s.loadEvents(ctx, row)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	events := make([]timelineEvent, 0, len(stored))
	for _, e := range stored {
		if v, ok := e.view(row.Policy, false, nil, e.State == audit.PayloadSealed && audit.SealedTruncated(e.Payload)); ok {
			events = append(events, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"request_id": row.RequestID, "payload_policy": row.Policy, "events": events})
}

// attemptView is the contract Attempt.
type attemptView struct {
	AttemptNo          int       `json:"attempt_no"`
	IntegrationVersion int       `json:"integration_version"`
	VendorID           string    `json:"vendor_id"`
	AccountID          string    `json:"account_id"`
	EndpointRef        string    `json:"endpoint_ref"`
	StartedAt          time.Time `json:"started_at"`
	DurationMS         *int64    `json:"duration_ms"`
	VendorStatus       *int      `json:"vendor_status"`
	ErrorCode          *string   `json:"error_code"`
	BreakerState       *string   `json:"breaker_state"`
	CredVersion        *int      `json:"cred_version"`
}

// loadAttempts lists a request's attempts in order (fallbackVersion when a row has none).
func (s *Server) loadAttempts(ctx context.Context, requestID string, since time.Time, fallbackVersion *int) ([]attemptView, error) {
	snap := s.servingSnapshot()
	rows, err := s.d.Pool.Query(ctx, `SELECT attempt_no, integration_version, account_id, endpoint_id, started_at, duration_ms, http_status, error_class
FROM hub.request_attempts WHERE request_id = $1 AND started_at >= $2 ORDER BY attempt_no, started_at`, requestID, since.Add(-time.Minute))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (attemptView, error) {
		var a attemptView
		var version *int
		var duration float64
		if err := r.Scan(&a.AttemptNo, &version, &a.AccountID, &a.EndpointRef, &a.StartedAt, &duration, &a.VendorStatus, &a.ErrorCode); err != nil {
			return a, err
		}
		switch {
		case version != nil:
			a.IntegrationVersion = *version
		case fallbackVersion != nil:
			a.IntegrationVersion = *fallbackVersion
		}
		if _, ep, ok := strings.Cut(a.EndpointRef, "."); ok { // "vendor.endpoint@v" → "endpoint@v"
			a.EndpointRef = ep
		}
		a.VendorID = vendorOf(snap, a.AccountID)
		if a.VendorID == "" {
			a.VendorID = "unknown"
		}
		a.StartedAt = a.StartedAt.UTC()
		a.DurationMS = millis(&duration)
		return a, nil
	})
}

func (s *Server) listRequestAttempts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.loadRequest(ctx, r, r.PathValue("request_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := s.loadAttempts(ctx, row.RequestID, row.CreatedAt, row.FinalVersion)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request_id": row.RequestID, "items": items})
}

// unsealRequest decrypts the sealed payloads of a request for an admin (DESIGN §13.2, D6,
// HUB-208). The clear payloads exist only in this response (Cache-Control: no-store); the
// change log records who opened which events and why, never the payloads.
func (s *Server) unsealRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		EventSeqs []int  `json:"event_seqs"`
		Reason    string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if utf8.RuneCountInString(reason) < 10 {
		s.fail(w, r, badRequest("/reason", "An unseal reason must be at least 10 characters and should cite a ticket"))
		return
	}
	row, err := s.loadRequest(ctx, r, r.PathValue("request_id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if row.Policy != string(registry.PayloadSealed) {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: "The request is not sealed (payload policy " + row.Policy + ")"})
		return
	}
	sealer := s.sealer()
	if sealer == nil {
		s.fail(w, r, errors.New("no seal key source configured"))
		return
	}
	stored, err := s.loadEvents(ctx, row)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	events, opened := make([]timelineEvent, 0, len(stored)), make([]int, 0, len(stored))
	for _, e := range stored {
		if len(body.EventSeqs) > 0 && !slices.Contains(body.EventSeqs, e.Seq) {
			continue
		}
		var clear []byte
		var truncated bool
		if e.State == audit.PayloadSealed {
			if clear, truncated, err = sealer.Open(row.TenantID, row.RequestID, e.Seq, e.Payload); err != nil {
				s.fail(w, r, fmt.Errorf("unseal %s seq %d: %w", row.RequestID, e.Seq, err))
				return
			}
			opened = append(opened, e.Seq)
		}
		if v, ok := e.view(row.Policy, true, clear, truncated); ok {
			events = append(events, v)
		}
	}
	changeID, err := control.RecordChange(ctx, s.d.Pool, control.Change{Actor: principal(r).Actor(), Action: "unsealRequest",
		ObjectType: "request", ObjectID: row.RequestID, Reason: reason,
		After: map[string]any{"tenant_id": row.TenantID, "event_seqs": opened}})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"change_id": changeID, "request_id": row.RequestID, "events": events})
}
