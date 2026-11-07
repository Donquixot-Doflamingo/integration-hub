package queue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Subject erasure (DESIGN §16.5, HUB-233), queue side. The outbox and the DLQ keep the
// canonical body in clear whatever the payload policy (the worker re-transforms it at send time,
// replay re-delivers it), so both are searched for the subject's identifier. A queued request is
// never erased: its envelope is still needed for delivery, and it would write new audit rows
// after the purge. The erasure defers it until it is terminal (its outbox row is gone).

// FindSubject returns the ids of the tenant's queued envelopes and dead letters whose envelope
// or last vendor exchange holds one of forms (the JSON-escaped stored forms of the identifier).
func (o *Outbox) FindSubject(ctx context.Context, db DB, tenant string, forms []string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT request_id FROM hub.outbox
		WHERE tenant_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS f(form) WHERE strpos(envelope::text, f.form) > 0)
		UNION
		SELECT request_id FROM hub.dead_letters
		WHERE tenant_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS f(form)
			WHERE strpos(envelope::text, f.form) > 0
				OR strpos(coalesce(last_vendor_request::text, ''), f.form) > 0
				OR strpos(coalesce(last_vendor_response::text, ''), f.form) > 0)
		ORDER BY 1`, tenant, forms)
	if err != nil {
		return nil, fmt.Errorf("queue: find subject: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("queue: find subject: %w", err)
	}
	return ids, nil
}

// LockForErase locks the dead letters of ids (a concurrent replay waits, then finds the purged
// envelope and does nothing) and returns the ids that are still queued, which the erasure must
// defer. Call it in the erasure's transaction before EraseDeadLetters.
func (o *Outbox) LockForErase(ctx context.Context, db DB, ids []string) (queued []string, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := db.Exec(ctx, `SELECT 1 FROM hub.dead_letters WHERE request_id = ANY($1) ORDER BY request_id FOR UPDATE`, ids); err != nil {
		return nil, fmt.Errorf("queue: lock dead letters: %w", err)
	}
	rows, err := db.Query(ctx, `SELECT request_id FROM hub.outbox WHERE request_id = ANY($1) ORDER BY request_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("queue: queued for erase: %w", err)
	}
	queued, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("queue: queued for erase: %w", err)
	}
	return queued, nil
}

// EraseDeadLetters removes the bodies of the dead letters of ids: the envelope's canonical body
// becomes null, its caller headers go, and the last vendor request and response are dropped. The row (reason, attempts,
// error class, times) stays; Replay refuses an erased dead letter.
func (o *Outbox) EraseDeadLetters(ctx context.Context, db DB, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, `UPDATE hub.dead_letters
		SET envelope = jsonb_set(envelope, '{canonical}', 'null'::jsonb) - 'headers', last_vendor_request = NULL, last_vendor_response = NULL
		WHERE request_id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("queue: erase dead letters: %w", err)
	}
	return tag.RowsAffected(), nil
}
