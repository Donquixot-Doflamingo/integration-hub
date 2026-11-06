package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// Subject erasure (DESIGN §16.5, HUB-233): a job finds every request of a tenant whose stored
// bodies hold a subject identifier, in any of the forms the payload policies store it, and
// replaces those bodies while keeping hashes, sizes, timelines and metadata (DESIGN §13.3).
// This file owns the audit side: the identifier's stored forms, the search over the ledger and
// the purge of request_events payloads and kept results.

// MinSubjectLen is the shortest identifier the job accepts. Masking ignores shorter values too
// (policy.go: they would match unrelated text).
const MinSubjectLen = 3

// ErrNoSealKey means sealed payloads of the tenant exist but no key opens them, so the search
// would be incomplete. The job refuses to run instead of reporting a partial erasure as done.
var ErrNoSealKey = errors.New("audit: sealed payloads exist but no seal key is configured")

// SubjectForms returns every byte string the identifier v of tenant may be stored as: the raw
// value (full payloads, sealed plaintext, envelopes, results, callbacks), its JSON-escaped
// forms, and the masked forms the `masked` policy writes for the x-pii kinds whose mask is
// derived from the whole value (identifier, ip_address and device_id: the keyed HMAC; email:
// HMAC of the local part, domain kept). Kinds masked to a fixed string or the last four
// characters (name, address, free_text, phone, government_id, financial) keep nothing that
// identifies the subject, so they add no form. The same masker (HUB_AUDIT_MASK_KEY) as the
// gateway's must be used, or masked rows are not found.
func (m *Masker) SubjectForms(tenant, v string) [][]byte {
	forms := []string{v, m.hmac(tenant, v)}
	if local, domain, ok := strings.Cut(v, "@"); ok {
		forms = append(forms, m.hmac(tenant, local)+"@"+domain)
	}
	var out [][]byte
	add := func(b []byte) {
		if len(b) > 0 && !slices.ContainsFunc(out, func(o []byte) bool { return bytes.Equal(o, b) }) {
			out = append(out, b)
		}
	}
	for _, f := range forms {
		add([]byte(f))
		add(jsonEscaped(f, true))  // Go's encoding/json (masked payloads, envelopes)
		add(jsonEscaped(f, false)) // Postgres jsonb text output
	}
	return out
}

// jsonEscaped is the inside of the JSON string literal of s.
func jsonEscaped(s string, html bool) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(html)
	_ = enc.Encode(s) // a string always encodes
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	return b[1 : len(b)-1]
}

// SubjectRef is the reference the change log keeps for an erased subject: an HMAC of the
// identifier under a per-tenant key with its own domain label, so an auditor who holds the
// identifier and the mask key can prove which subject a deletion was for, while the log itself
// holds no stored form of the identifier (it differs from the masked value).
func (m *Masker) SubjectRef(tenant, v string) string {
	key := hmac.New(sha256.New, m.master)
	key.Write([]byte("audit-erase-ref:" + tenant))
	mac := hmac.New(sha256.New, key.Sum(nil))
	mac.Write([]byte(v))
	return "subject:" + hex.EncodeToString(mac.Sum(nil))[:32]
}

// Querier runs reads and writes on a pool or a transaction.
type Querier interface {
	DB
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// SubjectScan selects the ledger rows searched for a subject.
type SubjectScan struct {
	Tenant string
	// Forms are the stored forms of the identifier (Masker.SubjectForms).
	Forms [][]byte
	// Since and Until bound requests.created_at (zero: unbounded). Day partitions outside the
	// window are pruned.
	Since, Until time.Time
	// Sealer opens sealed payloads. Nil is accepted only when the tenant has none in the window.
	Sealer *Sealer
}

func (s SubjectScan) window() (time.Time, time.Time) {
	since, until := s.Since, s.Until
	if since.IsZero() {
		since = time.Unix(0, 0)
	}
	if until.IsZero() {
		until = time.Now().Add(24 * time.Hour) // request ids are minted at accept: never in the future
	}
	return since.UTC(), until.UTC()
}

// FindSubjectRequests returns the sorted ids of the tenant's requests whose stored event
// payloads or kept results hold one of the forms. Clear payloads (full, masked, truncated) are
// searched in Postgres; sealed payloads are opened one by one with the sealer. There is no
// subject index (HUB-240): the search reads every event of the tenant inside the window.
func FindSubjectRequests(ctx context.Context, db Querier, s SubjectScan) ([]string, error) {
	if s.Tenant == "" || len(s.Forms) == 0 {
		return nil, errors.New("audit: subject scan needs a tenant and at least one form")
	}
	since, until := s.window()
	found := map[string]bool{}
	collect := func(sql string, args ...any) error {
		rows, err := db.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		for _, id := range ids {
			found[id] = true
		}
		return err
	}
	// The events of a request live in the day partition of their own time (at), which is never
	// before the request's creation: bounding at from below prunes older partitions.
	if err := collect(`SELECT DISTINCT e.request_id FROM hub.request_events e
		JOIN hub.requests r ON r.request_id = e.request_id AND r.created_at >= $2 AND r.created_at < $3 AND r.tenant_id = $1
		WHERE e.at >= $2 - interval '1 minute' AND e.payload IS NOT NULL AND e.payload_state <> 'sealed'
			AND EXISTS (SELECT 1 FROM unnest($4::bytea[]) AS f(form) WHERE position(f.form IN e.payload) > 0)`,
		s.Tenant, since, until, s.Forms); err != nil {
		return nil, fmt.Errorf("audit: search events: %w", err)
	}
	if err := collect(`SELECT request_id FROM hub.request_results
		WHERE tenant_id = $1 AND EXISTS (SELECT 1 FROM unnest($2::text[]) AS f(form)
			WHERE strpos(coalesce(result::text, ''), f.form) > 0 OR strpos(coalesce(error::text, ''), f.form) > 0)`,
		s.Tenant, textForms(s.Forms)); err != nil {
		return nil, fmt.Errorf("audit: search results: %w", err)
	}
	if err := findSealed(ctx, db, s, since, until, found); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(found))
	for id := range found {
		if t, ok := core.RequestIDTime(id); ok && (t.Before(since.Add(-time.Minute)) || !t.Before(until.Add(time.Minute))) {
			continue // a kept result of a request outside the window
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

// findSealed opens the tenant's sealed payloads in the window and adds the requests whose
// plaintext holds a form. Requests already found are skipped (their bodies go anyway).
func findSealed(ctx context.Context, db Querier, s SubjectScan, since, until time.Time, found map[string]bool) error {
	rows, err := db.Query(ctx, `SELECT e.request_id, e.seq, e.payload FROM hub.request_events e
		JOIN hub.requests r ON r.request_id = e.request_id AND r.created_at >= $2 AND r.created_at < $3 AND r.tenant_id = $1
		WHERE e.at >= $2 - interval '1 minute' AND e.payload_state = 'sealed' AND e.payload IS NOT NULL`,
		s.Tenant, since, until)
	if err != nil {
		return fmt.Errorf("audit: search sealed events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var seq int
		var blob []byte
		if err := rows.Scan(&id, &seq, &blob); err != nil {
			return fmt.Errorf("audit: search sealed events: %w", err)
		}
		if found[id] {
			continue
		}
		if s.Sealer == nil {
			return ErrNoSealKey
		}
		pt, _, err := s.Sealer.Open(s.Tenant, id, seq, blob)
		if err != nil {
			return fmt.Errorf("audit: open sealed event %s/%d: %w", id, seq, err)
		}
		if containsAny(pt, s.Forms) {
			found[id] = true
		}
		clear(pt)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("audit: search sealed events: %w", err)
	}
	return nil
}

func containsAny(b []byte, forms [][]byte) bool {
	return slices.ContainsFunc(forms, func(f []byte) bool { return bytes.Contains(b, f) })
}

func textForms(forms [][]byte) []string {
	out := make([]string, len(forms))
	for i, f := range forms {
		out[i] = string(f)
	}
	return out
}

// PurgeCounts is what PurgeRequests changed.
type PurgeCounts struct {
	Events  int64 // request_events rows whose payload was removed
	Results int64 // request_results rows whose result and error were removed
}

// PurgeRequests removes the stored bodies of the given requests: every request_events payload
// becomes NULL with payload_state 'omitted' and meta.pii_erased = changeID, and the kept result
// and error of each become NULL. Event rows, their sha256, bytes, type, time, attempt and the
// rest of their meta, the requests rows and request_attempts are kept (DESIGN §13.3). Run it in
// the erasure's transaction.
func PurgeRequests(ctx context.Context, db DB, ids []string, changeID string) (PurgeCounts, error) {
	var c PurgeCounts
	if len(ids) == 0 {
		return c, nil
	}
	from := time.Now()
	for _, id := range ids {
		t, ok := core.RequestIDTime(id)
		if !ok {
			return c, fmt.Errorf("audit: %q is not a request id", id)
		}
		if t.Before(from) {
			from = t
		}
	}
	tag, err := db.Exec(ctx, `UPDATE hub.request_events
		SET payload = NULL, payload_state = 'omitted', meta = meta || jsonb_build_object('pii_erased', $3::text)
		WHERE request_id = ANY($1) AND at >= $2 AND payload IS NOT NULL`, ids, from.Add(-time.Minute), changeID)
	if err != nil {
		return c, fmt.Errorf("audit: purge events: %w", err)
	}
	c.Events = tag.RowsAffected()
	tag, err = db.Exec(ctx, `UPDATE hub.request_results SET result = NULL, error = NULL
		WHERE request_id = ANY($1) AND (result IS NOT NULL OR error IS NOT NULL)`, ids)
	if err != nil {
		return c, fmt.Errorf("audit: purge results: %w", err)
	}
	c.Results = tag.RowsAffected()
	return c, nil
}
