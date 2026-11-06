package callbacks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Subject erasure (DESIGN §16.5, HUB-233), callback side. A callback row keeps the exact bytes
// it signs and POSTs, which can carry the canonical result in clear. Rows still to be delivered
// (pending, in_flight, retry_scheduled) are deferred by the erasure; delivered and dead-lettered
// rows (terminal: there is no callback replay) lose their payload. The audit's
// callback.delivered events keep each payload's sha256 and size.

// DB runs the erasure's statements on a pool or its transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// FindSubject returns the request ids of the tenant's callback deliveries whose payload holds
// one of forms (stored forms of the identifier).
func FindSubject(ctx context.Context, db DB, tenant string, forms [][]byte) ([]string, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT request_id FROM hub.callback_deliveries
		WHERE tenant_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::bytea[]) AS f(form) WHERE position(f.form IN payload) > 0)
		ORDER BY 1`, tenant, forms)
	if err != nil {
		return nil, fmt.Errorf("callbacks: find subject: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("callbacks: find subject: %w", err)
	}
	return ids, nil
}

// Undelivered returns the ids among ids that still have a callback to deliver; the erasure
// defers them.
func Undelivered(ctx context.Context, db DB, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT request_id FROM hub.callback_deliveries
		WHERE request_id = ANY($1) AND status IN ('pending', 'in_flight', 'retry_scheduled') ORDER BY 1`, ids)
	if err != nil {
		return nil, fmt.Errorf("callbacks: undelivered: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("callbacks: undelivered: %w", err)
	}
	return out, nil
}

// ErasePayloads empties the payload of the delivered and dead-lettered callbacks of ids. Status,
// attempts, HTTP status, error class and times stay.
func ErasePayloads(ctx context.Context, db DB, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, `UPDATE hub.callback_deliveries SET payload = ''::bytea, updated_at = now()
		WHERE request_id = ANY($1) AND status IN ('delivered', 'dead_lettered') AND payload <> ''::bytea`, ids)
	if err != nil {
		return 0, fmt.Errorf("callbacks: erase payloads: %w", err)
	}
	return tag.RowsAffected(), nil
}
