package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Dead-letter queue (DESIGN §10.6, §16.1, HUB-209): list, read, replay by ids or by filter
// (back to accepted; the worker re-resolves and re-transforms, so a fixed transformer
// applies) and purge. Replay and purge write one change-log row in their own transaction.

func init() { registrars = append(registrars, (*Server).registerDLQ) }

func (s *Server) registerDLQ() {
	s.handle("listDeadLetters", RoleTenantViewer, false, "GET /dlq", s.listDeadLetters)
	s.handle("getDeadLetter", RoleTenantViewer, false, "GET /dlq/{request_id}", s.getDeadLetter)
	s.handle("replayDeadLetters", RoleEditor, true, "POST /dlq/replay", s.replayDeadLetters)
	s.handle("purgeDeadLetters", RoleAdmin, true, "POST /dlq/purge", s.purgeDeadLetters)
}

const (
	// dlqRetention is how long a dead letter is kept (DESIGN §10.6).
	dlqRetention = 14 * 24 * time.Hour
	// maxBulk caps one replay or purge (contract: 10 000, truncated: true beyond).
	maxBulk = 10_000
	// defaultReplayTTL applies when an envelope has no usable TTL of its own.
	defaultReplayTTL = 6 * time.Hour
)

// dlqFilter is the contract DlqFilter (also the list's query parameters).
type dlqFilter struct {
	TenantID      string     `json:"tenant_id,omitempty"`
	IntegrationID string     `json:"integration_id,omitempty"`
	VendorID      string     `json:"vendor_id,omitempty"`
	AccountID     string     `json:"account_id,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	From          *time.Time `json:"from,omitempty"`
	To            *time.Time `json:"to,omitempty"`
	Replayed      *bool      `json:"replayed,omitempty"`

	tenants []string // tenant_viewer scope (nil: every tenant)
}

// where builds the SQL conditions on hub.dead_letters d.
func (f dlqFilter) where(snap *registry.Snapshot, arg func(any) string) []string {
	var where []string
	switch {
	case f.TenantID != "":
		where = append(where, "d.tenant_id = "+arg(f.TenantID))
	case f.tenants != nil:
		where = append(where, "d.tenant_id = ANY("+arg(f.tenants)+")")
	}
	if f.IntegrationID != "" {
		where = append(where, "d.integration_id = "+arg(f.IntegrationID))
	}
	if f.AccountID != "" {
		where = append(where, "d.account_id = "+arg(f.AccountID))
	}
	if f.VendorID != "" {
		accounts := []string{}
		if snap != nil {
			for id, a := range snap.Accounts {
				if a.Vendor == f.VendorID {
					accounts = append(accounts, id)
				}
			}
		}
		where = append(where, "d.account_id = ANY("+arg(accounts)+")")
	}
	if f.Reason != "" {
		where = append(where, "d.reason = "+arg(f.Reason))
	}
	if f.From != nil {
		where = append(where, "d.created_at >= "+arg(*f.From))
	}
	if f.To != nil {
		where = append(where, "d.created_at < "+arg(*f.To))
	}
	if f.Replayed != nil {
		if *f.Replayed {
			where = append(where, "d.replayed_at IS NOT NULL")
		} else {
			where = append(where, "d.replayed_at IS NULL")
		}
	}
	return where
}

// deadLetter is the contract DeadLetter.
type deadLetter struct {
	RequestID      string     `json:"request_id"`
	TenantID       string     `json:"tenant_id"`
	IntegrationID  string     `json:"integration_id"`
	VendorID       *string    `json:"vendor_id"`
	AccountID      *string    `json:"account_id"`
	Reason         string     `json:"reason"`
	Message        *string    `json:"message"`
	Attempts       int        `json:"attempts"`
	DeadLetteredAt time.Time  `json:"dead_lettered_at"`
	ReplayedAt     *time.Time `json:"replayed_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
}

const deadLetterColumns = `d.request_id, d.tenant_id, d.integration_id, d.account_id, d.reason, d.last_error, d.attempts, d.created_at, d.replayed_at`

func scanDeadLetter(row pgx.Row, snap *registry.Snapshot, extra ...any) (deadLetter, error) {
	var d deadLetter
	if err := row.Scan(append([]any{&d.RequestID, &d.TenantID, &d.IntegrationID, &d.AccountID, &d.Reason, &d.Message, &d.Attempts,
		&d.DeadLetteredAt, &d.ReplayedAt}, extra...)...); err != nil {
		return d, err
	}
	d.DeadLetteredAt = d.DeadLetteredAt.UTC()
	d.ExpiresAt = d.DeadLetteredAt.Add(dlqRetention)
	d.ReplayedAt = utc(d.ReplayedAt)
	if d.AccountID != nil {
		if v := vendorOf(snap, *d.AccountID); v != "" {
			d.VendorID = &v
		}
	}
	return d, nil
}

func (s *Server) listDeadLetters(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := page(r, 2)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	from, to, err := timeRange(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tenants, err := tenantScope(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := dlqFilter{IntegrationID: q.Get("integration_id"), VendorID: q.Get("vendor_id"), AccountID: q.Get("account_id"),
		Reason: q.Get("reason"), From: from, To: to, tenants: tenants}
	if v := q.Get("replayed"); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			s.fail(w, r, badRequest("", "replayed must be true or false"))
			return
		}
		f.Replayed = &b
	}
	if s.d.Pool == nil {
		s.fail(w, r, errNoDB)
		return
	}
	snap := s.servingSnapshot()
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	where := f.where(snap, arg)
	switch {
	case len(cursor) == 2:
		at, perr := time.Parse(time.RFC3339Nano, cursor[0])
		if perr != nil {
			s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
			return
		}
		where = append(where, "(d.created_at, d.request_id) < ("+arg(at)+", "+arg(cursor[1])+")")
	case cursor != nil:
		s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
		return
	}
	sql := `SELECT ` + deadLetterColumns + ` FROM hub.dead_letters d`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY d.created_at DESC, d.request_id DESC LIMIT " + strconv.Itoa(limit+1)
	rows, err := s.d.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (deadLetter, error) { return scanDeadLetter(row, snap) })
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = nextCursor(last.DeadLetteredAt.Format(time.RFC3339Nano), last.RequestID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getDeadLetter(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.d.Pool == nil {
		s.fail(w, r, errNoDB)
		return
	}
	id := r.PathValue("request_id")
	var envelope, lastReq, lastResp []byte
	d, err := scanDeadLetter(s.d.Pool.QueryRow(ctx, `SELECT `+deadLetterColumns+`, d.envelope, d.last_vendor_request, d.last_vendor_response
FROM hub.dead_letters d WHERE d.request_id = $1`, id), s.servingSnapshot(), &envelope, &lastReq, &lastResp)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !principal(r).SeesTenant(d.TenantID)) {
		s.fail(w, r, notFound("Dead letter"))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	canonical, history, err := s.deadLetterPayloads(ctx, r, id, envelope)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		deadLetter
		Canonical          json.RawMessage `json:"canonical"`
		LastVendorRequest  json.RawMessage `json:"last_vendor_request"`
		LastVendorResponse json.RawMessage `json:"last_vendor_response"`
		AttemptHistory     []attemptView   `json:"attempt_history"`
	}{d, canonical, objectOrNull(lastReq), objectOrNull(lastResp), history})
}

// deadLetterPayloads is the canonical body under the request's payload policy (the envelope's
// own copy for full; the stored, masked received body for masked; nothing for sealed and
// metadata_only) and the attempt history.
func (s *Server) deadLetterPayloads(ctx context.Context, r *http.Request, id string, envelope []byte) (json.RawMessage, []attemptView, error) {
	row, err := s.loadRequest(ctx, r, id)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound { // ledger row pruned: nothing per policy
		return json.RawMessage("null"), []attemptView{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	history, err := s.loadAttempts(ctx, id, row.CreatedAt, row.FinalVersion)
	if err != nil {
		return nil, nil, err
	}
	canonical := json.RawMessage("null")
	switch registry.PayloadPolicy(row.Policy) {
	case registry.PayloadFull:
		var env core.Envelope
		if json.Unmarshal(envelope, &env) == nil {
			canonical = objectOrNull(env.Canonical)
		}
	case registry.PayloadMasked:
		var payload []byte
		err := s.d.Pool.QueryRow(ctx, `SELECT payload FROM hub.request_events WHERE request_id = $1 AND at >= $2
			AND type IN ($3, $4) AND payload_state = 'masked' ORDER BY seq DESC LIMIT 1`,
			id, row.CreatedAt.Add(-time.Minute), audit.RequestValidated, audit.RequestReceived).Scan(&payload)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		canonical = objectOrNull(payload)
	default:
	}
	return canonical, history, nil
}

// dlqSelection is the body of replay and purge: exactly one of request_ids or filter.
type dlqSelection struct {
	RequestIDs []string   `json:"request_ids"`
	Filter     *dlqFilter `json:"filter"`
	DryRun     bool       `json:"dry_run"`
	Reason     string     `json:"reason"`
}

// selected is one matched dead letter.
type selected struct {
	id       string
	attempts int
	envelope []byte
}

func (s *Server) readSelection(w http.ResponseWriter, r *http.Request) (dlqSelection, string, error) {
	var body dlqSelection
	if err := decode(w, r, &body); err != nil {
		return body, "", err
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		return body, "", err
	}
	if (len(body.RequestIDs) > 0) == (body.Filter != nil) {
		return body, "", badRequest("", "Give exactly one of request_ids or filter")
	}
	if len(body.RequestIDs) > 1000 {
		return body, "", badRequest("/request_ids", "At most 1000 request_ids per call")
	}
	if s.d.Pool == nil {
		return body, "", errNoDB
	}
	return body, reason, nil
}

// match locks up to maxBulk matching dead letters (FOR UPDATE SKIP LOCKED: a concurrent
// replay of the same rows waits for none and touches none twice). unreplayed restricts the
// match to entries not replayed yet (replay); purge takes them all.
func (s *Server) match(ctx context.Context, tx pgx.Tx, sel dlqSelection, unreplayed bool) ([]selected, int, error) {
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	var where []string
	if sel.Filter != nil {
		where = sel.Filter.where(s.servingSnapshot(), arg)
	} else {
		where = []string{"d.request_id = ANY(" + arg(sel.RequestIDs) + ")"}
	}
	if unreplayed {
		where = append(where, "d.replayed_at IS NULL")
	}
	cond := ""
	if len(where) > 0 {
		cond = " WHERE " + strings.Join(where, " AND ")
	}
	var matched int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM hub.dead_letters d`+cond, args...).Scan(&matched); err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT d.request_id, d.attempts, d.envelope FROM hub.dead_letters d`+cond+
		` ORDER BY d.created_at, d.request_id LIMIT `+strconv.Itoa(maxBulk)+` FOR UPDATE SKIP LOCKED`, args...)
	if err != nil {
		return nil, 0, err
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (selected, error) {
		var x selected
		return x, r.Scan(&x.id, &x.attempts, &x.envelope)
	})
	return got, matched, err
}

// bulkResult is the contract DlqBulkResult.
type bulkResult struct {
	ChangeID  *string `json:"change_id"`
	Matched   int     `json:"matched"`
	Affected  int     `json:"affected"`
	DryRun    bool    `json:"dry_run"`
	Truncated bool    `json:"truncated"`
}

// changeAfter is what the change log keeps of a bulk operation: the selection and the ids
// (the first 1000), never payloads.
func changeAfter(sel dlqSelection, res bulkResult, ids []string) map[string]any {
	after := map[string]any{"matched": res.Matched, "affected": res.Affected, "truncated": res.Truncated, "request_ids": ids[:min(len(ids), 1000)]}
	if sel.Filter != nil {
		after["filter"] = sel.Filter
	}
	return after
}

func (s *Server) replayDeadLetters(w http.ResponseWriter, r *http.Request) {
	sel, reason, err := s.readSelection(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	q := &queue.Outbox{Now: s.d.Now}
	var res bulkResult
	err = pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		got, matched, err := s.match(ctx, tx, sel, true)
		if err != nil {
			return err
		}
		res = bulkResult{Matched: matched, Truncated: matched > len(got), DryRun: sel.DryRun}
		if sel.DryRun {
			return nil
		}
		now := s.d.Now().UTC()
		var ids []string
		for _, x := range got {
			ttl := defaultReplayTTL
			var env core.Envelope
			if json.Unmarshal(x.envelope, &env) == nil && env.ExpiresAt.After(env.CreatedAt) {
				ttl = env.ExpiresAt.Sub(env.CreatedAt)
			}
			if err := q.Replay(ctx, tx, x.id, now.Add(ttl)); err != nil {
				if errors.Is(err, queue.ErrLost) { // replayed by someone else meanwhile
					continue
				}
				return err
			}
			if err := (audit.Ledger{}).Progress(ctx, tx, x.id, core.StateAccepted, x.attempts, now); err != nil {
				return err
			}
			ids = append(ids, x.id)
		}
		res.Affected = len(ids)
		id, err := control.RecordChange(ctx, tx, control.Change{Actor: principal(r).Actor(), Action: "replayDeadLetters",
			ObjectType: "dlq", ObjectID: bulkObject(ids), Reason: reason, After: changeAfter(sel, res, ids)})
		res.ChangeID = &id
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) purgeDeadLetters(w http.ResponseWriter, r *http.Request) {
	sel, reason, err := s.readSelection(w, r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ctx := r.Context()
	var res bulkResult
	err = pgx.BeginFunc(ctx, s.d.Pool, func(tx pgx.Tx) error {
		got, matched, err := s.match(ctx, tx, sel, false)
		if err != nil {
			return err
		}
		res = bulkResult{Matched: matched, Truncated: matched > len(got), DryRun: sel.DryRun}
		if sel.DryRun {
			return nil
		}
		ids := make([]string, len(got))
		for i, x := range got {
			ids[i] = x.id
		}
		tag, err := tx.Exec(ctx, `DELETE FROM hub.dead_letters WHERE request_id = ANY($1)`, ids)
		if err != nil {
			return err
		}
		res.Affected = int(tag.RowsAffected())
		id, err := control.RecordChange(ctx, tx, control.Change{Actor: principal(r).Actor(), Action: "purgeDeadLetters",
			ObjectType: "dlq", ObjectID: bulkObject(ids), Reason: reason, After: changeAfter(sel, res, ids)})
		res.ChangeID = &id
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// bulkObject is the change log's object id: the request id of a single-entry operation, else
// "bulk" (the ids are in after.request_ids).
func bulkObject(ids []string) string {
	if len(ids) == 1 {
		return ids[0]
	}
	return "bulk"
}
