package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Drafts and pending integrations live in Postgres, not in the config tree: nodes must not see
// them until they are published (hub.integration_drafts, hub.integration_pending). Publishing
// goes through Publisher.Publish like every other version; the caller removes the row after.

// Draft is the one editable next version of an integration. Its number is not stored: it is
// always the next version (latest published + 1), so a rollback in between moves it up.
type Draft struct {
	Integration string
	Spec        []byte // registry version document (YAML)
	BasedOn     *int
	CopiedFrom  *int
	Author      string
	Reason      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PendingIntegration is a created integration without a published version.
type PendingIntegration struct {
	Integration string
	Header      []byte // integration.yaml
	Author      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

const draftColumns = `integration_id, spec, based_on, copied_from_version, author, reason, created_at, updated_at`

func scanDraft(row pgx.Row) (Draft, error) {
	var d Draft
	var spec string
	err := row.Scan(&d.Integration, &spec, &d.BasedOn, &d.CopiedFrom, &d.Author, &d.Reason, &d.CreatedAt, &d.UpdatedAt)
	d.Spec, d.CreatedAt, d.UpdatedAt = []byte(spec), d.CreatedAt.UTC(), d.UpdatedAt.UTC()
	return d, err
}

// GetDraft returns the draft of integration (ErrNotFound when there is none).
func GetDraft(ctx context.Context, db querier, integration string) (Draft, error) {
	d, err := scanDraft(db.QueryRow(ctx, `SELECT `+draftColumns+` FROM hub.integration_drafts WHERE integration_id = $1`, integration))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("%w: %s has no draft", ErrNotFound, integration)
	}
	return d, err
}

// ListDrafts returns every draft by integration.
func ListDrafts(ctx context.Context, db querier) (map[string]Draft, error) {
	rows, err := db.Query(ctx, `SELECT `+draftColumns+` FROM hub.integration_drafts`)
	if err != nil {
		return nil, fmt.Errorf("drafts: %w", err)
	}
	defer rows.Close()
	out := map[string]Draft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, fmt.Errorf("drafts: %w", err)
		}
		out[d.Integration] = d
	}
	return out, rows.Err()
}

// inTx runs fn in a transaction.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

// CreateDraft stores d and its change-log row in one transaction. A second draft of the same
// integration is ErrConflict.
func CreateDraft(ctx context.Context, pool *pgxpool.Pool, d Draft, ch Change) (Draft, string, error) {
	var id string
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		d, err = scanDraft(tx.QueryRow(ctx, `INSERT INTO hub.integration_drafts (integration_id, spec, based_on, copied_from_version, author, reason)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+draftColumns, d.Integration, string(d.Spec), d.BasedOn, d.CopiedFrom, d.Author, d.Reason))
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s already has a draft (only one draft at a time)", ErrConflict, d.Integration)
		}
		if err != nil {
			return fmt.Errorf("create draft: %w", err)
		}
		id, err = RecordChange(ctx, tx, ch)
		return err
	})
	return d, id, err
}

// UpdateDraft replaces the spec (and reason) of integration's draft with its change-log row.
// before receives the draft as it was, so the row can carry it.
func UpdateDraft(ctx context.Context, pool *pgxpool.Pool, integration string, spec []byte, reason string, ch func(before Draft) Change) (Draft, string, error) {
	var d Draft
	var id string
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		before, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM hub.integration_drafts WHERE integration_id = $1 FOR UPDATE`, integration))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s has no draft", ErrNotFound, integration)
		}
		if err != nil {
			return fmt.Errorf("update draft: %w", err)
		}
		if d, err = scanDraft(tx.QueryRow(ctx, `UPDATE hub.integration_drafts SET spec = $2, reason = $3, updated_at = now()
WHERE integration_id = $1 RETURNING `+draftColumns, integration, string(spec), reason)); err != nil {
			return fmt.Errorf("update draft: %w", err)
		}
		id, err = RecordChange(ctx, tx, ch(before))
		return err
	})
	return d, id, err
}

// DeleteDraft removes integration's draft (after it was published).
func DeleteDraft(ctx context.Context, db querier, integration string) error {
	_, err := db.Exec(ctx, `DELETE FROM hub.integration_drafts WHERE integration_id = $1`, integration)
	return err
}

const pendingColumns = `integration_id, header, author, created_at, updated_at`

func scanPending(row pgx.Row) (PendingIntegration, error) {
	var p PendingIntegration
	var header string
	err := row.Scan(&p.Integration, &header, &p.Author, &p.CreatedAt, &p.UpdatedAt)
	p.Header, p.CreatedAt, p.UpdatedAt = []byte(header), p.CreatedAt.UTC(), p.UpdatedAt.UTC()
	return p, err
}

// ListPending returns every created integration that has no published version yet.
func ListPending(ctx context.Context, db querier) (map[string]PendingIntegration, error) {
	rows, err := db.Query(ctx, `SELECT `+pendingColumns+` FROM hub.integration_pending`)
	if err != nil {
		return nil, fmt.Errorf("pending integrations: %w", err)
	}
	defer rows.Close()
	out := map[string]PendingIntegration{}
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, fmt.Errorf("pending integrations: %w", err)
		}
		out[p.Integration] = p
	}
	return out, rows.Err()
}

// CreatePending stores a created integration and its change-log row in one transaction
// (ErrConflict when the id is taken by another pending integration).
func CreatePending(ctx context.Context, pool *pgxpool.Pool, p PendingIntegration, ch Change) (PendingIntegration, string, error) {
	var id string
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		p, err = scanPending(tx.QueryRow(ctx, `INSERT INTO hub.integration_pending (integration_id, header, author) VALUES ($1, $2, $3)
RETURNING `+pendingColumns, p.Integration, string(p.Header), p.Author))
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: integration %q already exists", ErrConflict, p.Integration)
		}
		if err != nil {
			return fmt.Errorf("create integration: %w", err)
		}
		id, err = RecordChange(ctx, tx, ch)
		return err
	})
	return p, id, err
}

// UpdatePending rewrites a pending integration's header under a row lock: edit receives the
// current row and returns the new header and the change-log row (or an error to abort).
func UpdatePending(ctx context.Context, pool *pgxpool.Pool, integration string, edit func(cur PendingIntegration) ([]byte, Change, error)) (PendingIntegration, string, error) {
	var p PendingIntegration
	var id string
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		cur, err := scanPending(tx.QueryRow(ctx, `SELECT `+pendingColumns+` FROM hub.integration_pending WHERE integration_id = $1 FOR UPDATE`, integration))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: integration %q", ErrNotFound, integration)
		}
		if err != nil {
			return fmt.Errorf("update integration: %w", err)
		}
		header, ch, err := edit(cur)
		if err != nil {
			return err
		}
		if p, err = scanPending(tx.QueryRow(ctx, `UPDATE hub.integration_pending SET header = $2, updated_at = now()
WHERE integration_id = $1 RETURNING `+pendingColumns, integration, string(header))); err != nil {
			return fmt.Errorf("update integration: %w", err)
		}
		id, err = RecordChange(ctx, tx, ch)
		return err
	})
	return p, id, err
}

// DeletePending removes a pending integration (after its first version was published).
func DeletePending(ctx context.Context, db querier, integration string) error {
	_, err := db.Exec(ctx, `DELETE FROM hub.integration_pending WHERE integration_id = $1`, integration)
	return err
}
