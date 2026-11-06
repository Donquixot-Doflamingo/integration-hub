package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/sessions"
)

// Subject erasure (DESIGN §16.5, HUB-233, ADR-0012): purge every stored body of a tenant's
// requests that hold a subject identifier, keeping hashes, sizes, timelines and metadata, and
// record the deletion in change_log. Each owner package searches and purges its own tables;
// this composes them in one transaction.

// EraseAction is the change_log action of a subject erasure.
const EraseAction = "eraseSubjectPayloads"

// MinEraseReason is the shortest accepted reason (same rule as unseal: cite the ticket).
const MinEraseReason = 10

// ErrInvalidErasure is a request the job refuses before touching anything.
var ErrInvalidErasure = errors.New("control: invalid erasure request")

// Erasure runs subject erasures against Postgres.
type Erasure struct {
	Pool *pgxpool.Pool
	// Masker must use the gateway's HUB_AUDIT_MASK_KEY, or masked payloads are not found.
	Masker *audit.Masker
	// Sealer opens sealed payloads; nil fails the run when the tenant has any (audit.ErrNoSealKey).
	Sealer *audit.Sealer
}

// EraseRequest is one subject erasure.
type EraseRequest struct {
	Tenant  string
	Subject string // the identifier (PAN, phone, customer id, ...); never logged or stored
	Actor   string
	Reason  string
	// Since and Until bound the requests searched by creation time (zero: unbounded).
	Since, Until time.Time
	// DryRun reports what would be erased and changes nothing (no change_log row).
	DryRun bool
}

// EraseResult reports one erasure. It never holds the identifier.
type EraseResult struct {
	ChangeID   string   `json:"change_id,omitempty"`
	SubjectRef string   `json:"subject_ref"`
	Tenant     string   `json:"tenant"`
	DryRun     bool     `json:"dry_run"`
	Erased     []string `json:"erased_request_ids"`
	// Deferred requests are still queued or have callbacks to deliver; run the job again once
	// they are terminal.
	Deferred    []string `json:"deferred_request_ids"`
	Events      int64    `json:"events"`
	Results     int64    `json:"results"`
	DeadLetters int64    `json:"dead_letters"`
	Callbacks   int64    `json:"callbacks"`
	Sessions    int64    `json:"sessions"`
}

func (r EraseRequest) validate() error {
	var errs []string
	if strings.TrimSpace(r.Tenant) == "" {
		errs = append(errs, "tenant is required")
	}
	if len(r.Subject) < audit.MinSubjectLen {
		errs = append(errs, fmt.Sprintf("subject must be at least %d characters", audit.MinSubjectLen))
	}
	if strings.TrimSpace(r.Actor) == "" {
		errs = append(errs, "actor is required")
	}
	if len(strings.TrimSpace(r.Reason)) < MinEraseReason {
		errs = append(errs, fmt.Sprintf("reason must be at least %d characters and should cite the ticket", MinEraseReason))
	}
	if !r.Since.IsZero() && !r.Until.IsZero() && !r.Since.Before(r.Until) {
		errs = append(errs, "since must be before until")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidErasure, strings.Join(errs, "; "))
	}
	return nil
}

// EraseSubject finds the tenant's requests whose stored bodies hold the subject's identifier in
// any stored form (raw, JSON-escaped, masked HMAC, inside sealed payloads), then, in one
// transaction, purges their bodies everywhere (events, kept results, dead letters, delivered
// callbacks), deletes the tenant's vendor sessions holding it, and appends one change_log row.
// Requests still queued or with callbacks to deliver are deferred, not touched.
func (e *Erasure) EraseSubject(ctx context.Context, req EraseRequest) (EraseResult, error) {
	if err := req.validate(); err != nil {
		return EraseResult{}, err
	}
	if e.Pool == nil || e.Masker == nil {
		return EraseResult{}, errors.New("control: erasure needs a pool and a masker")
	}
	forms := e.Masker.SubjectForms(req.Tenant, req.Subject)
	text := make([]string, len(forms))
	for i, f := range forms {
		text[i] = string(f)
	}
	res := EraseResult{SubjectRef: e.Masker.SubjectRef(req.Tenant, req.Subject), Tenant: req.Tenant, DryRun: req.DryRun,
		Erased: []string{}, Deferred: []string{}}

	ids, err := e.find(ctx, req, forms, text)
	if err != nil {
		return EraseResult{}, err
	}
	q := &queue.Outbox{}
	if req.DryRun {
		deferred, err := deferredOf(ctx, e.Pool, q, ids, false)
		if err != nil {
			return EraseResult{}, err
		}
		res.Deferred, res.Erased = deferred, without(ids, deferred)
		res.Sessions, err = sessions.EraseSubject(ctx, e.Pool, req.Tenant, text, true)
		return res, err
	}

	res.ChangeID = "chg_" + core.NewULID(time.Now())
	err = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		deferred, err := deferredOf(ctx, tx, q, ids, true)
		if err != nil {
			return err
		}
		erase := without(ids, deferred)
		res.Deferred, res.Erased = deferred, erase
		pc, err := audit.PurgeRequests(ctx, tx, erase, res.ChangeID)
		if err != nil {
			return err
		}
		res.Events, res.Results = pc.Events, pc.Results
		if res.DeadLetters, err = q.EraseDeadLetters(ctx, tx, erase); err != nil {
			return err
		}
		if res.Callbacks, err = callbacks.ErasePayloads(ctx, tx, erase); err != nil {
			return err
		}
		if res.Sessions, err = sessions.EraseSubject(ctx, tx, req.Tenant, text, false); err != nil {
			return err
		}
		_, err = RecordChange(ctx, tx, Change{ChangeID: res.ChangeID, Actor: req.Actor, Action: EraseAction,
			ObjectType: "request", ObjectID: eraseObject(erase), Reason: req.Reason, After: changeOfErasure(req, res)})
		return err
	})
	if err != nil {
		return EraseResult{}, fmt.Errorf("control: erase subject: %w", err)
	}
	return res, nil
}

// find unions the request ids the owners report for the subject.
func (e *Erasure) find(ctx context.Context, req EraseRequest, forms [][]byte, text []string) ([]string, error) {
	ids, err := audit.FindSubjectRequests(ctx, e.Pool, audit.SubjectScan{Tenant: req.Tenant, Forms: forms,
		Since: req.Since, Until: req.Until, Sealer: e.Sealer})
	if err != nil {
		return nil, err
	}
	queued, err := (&queue.Outbox{}).FindSubject(ctx, e.Pool, req.Tenant, text)
	if err != nil {
		return nil, err
	}
	cbs, err := callbacks.FindSubject(ctx, e.Pool, req.Tenant, forms)
	if err != nil {
		return nil, err
	}
	for _, id := range append(queued, cbs...) {
		if inWindow(id, req.Since, req.Until) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// inWindow reports whether the request id was minted inside [since, until) (zero: unbounded).
func inWindow(id string, since, until time.Time) bool {
	t, ok := core.RequestIDTime(id)
	if !ok {
		return false
	}
	return (since.IsZero() || !t.Before(since)) && (until.IsZero() || t.Before(until))
}

type erasureDB interface {
	queue.DB
	callbacks.DB
}

// deferredOf returns the ids still queued or with callbacks to deliver. lock also locks their
// dead letters against a concurrent replay (inside the erasure's transaction).
func deferredOf(ctx context.Context, db erasureDB, q *queue.Outbox, ids []string, lock bool) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	var queued []string
	var err error
	if lock {
		queued, err = q.LockForErase(ctx, db, ids)
	} else {
		var rows pgx.Rows
		if rows, err = db.Query(ctx, `SELECT request_id FROM hub.outbox WHERE request_id = ANY($1)`, ids); err == nil {
			queued, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
	}
	if err != nil {
		return nil, err
	}
	undelivered, err := callbacks.Undelivered(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	queued = append(queued, undelivered...)
	slices.Sort(queued)
	return append([]string{}, slices.Compact(queued)...), nil
}

func without(ids, drop []string) []string {
	out := []string{}
	for _, id := range ids {
		if !slices.Contains(drop, id) {
			out = append(out, id)
		}
	}
	return out
}

func eraseObject(ids []string) string {
	switch len(ids) {
	case 0:
		return "none"
	case 1:
		return ids[0]
	}
	return "bulk"
}

// changeOfErasure is what change_log keeps: the subject's reference (never the identifier), the
// window, counts and request ids (the first 1000, as for DLQ bulk operations).
func changeOfErasure(req EraseRequest, res EraseResult) map[string]any {
	after := map[string]any{
		"tenant_id": req.Tenant, "subject_ref": res.SubjectRef,
		"erased": len(res.Erased), "deferred": len(res.Deferred),
		"request_ids":          res.Erased[:min(len(res.Erased), 1000)],
		"deferred_request_ids": res.Deferred[:min(len(res.Deferred), 1000)],
		"events":               res.Events, "results": res.Results, "dead_letters": res.DeadLetters,
		"callbacks": res.Callbacks, "sessions": res.Sessions,
	}
	if !req.Since.IsZero() {
		after["since"] = req.Since.UTC()
	}
	if !req.Until.IsZero() {
		after["until"] = req.Until.UTC()
	}
	return after
}
