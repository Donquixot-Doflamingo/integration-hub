package sessions

import (
	"context"
	"fmt"
)

// EraseSubject deletes the tenant's vendor sessions whose data or vendor txn id holds one of
// forms (JSON-escaped stored forms of a subject identifier; DESIGN §16.5, HUB-233). A session
// has no request id and only lives for its TTL (10 min), so it is matched by content and removed
// outright: a later step on it gets the same answer as on an expired session. dryRun only counts.
func EraseSubject(ctx context.Context, db DB, tenant string, forms []string, dryRun bool) (int64, error) {
	const match = `tenant_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS f(form)
		WHERE strpos(data::text, f.form) > 0 OR strpos(coalesce(vendor_txn_id, ''), f.form) > 0)`
	if dryRun {
		var n int64
		if err := db.QueryRow(ctx, `SELECT count(*) FROM hub.vendor_sessions WHERE `+match, tenant, forms).Scan(&n); err != nil {
			return 0, fmt.Errorf("sessions: find subject: %w", err)
		}
		return n, nil
	}
	tag, err := db.Exec(ctx, `DELETE FROM hub.vendor_sessions WHERE `+match, tenant, forms)
	if err != nil {
		return 0, fmt.Errorf("sessions: erase subject: %w", err)
	}
	return tag.RowsAffected(), nil
}
