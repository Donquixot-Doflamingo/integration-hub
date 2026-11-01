package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// DB is what the store runs statements on: a pool or a transaction.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DefaultTTL is how long a session stays open (DESIGN §14: the vendor txn id is kept 10 min).
const DefaultTTL = 10 * time.Minute

// MaxDataBytes bounds the JSON size of one session's data.
const MaxDataBytes = 16 << 10

// TxnKey is the data key that is also stored in the vendor_txn_id column (support lookups).
const TxnKey = "vendor_txn_id"

var (
	// ErrNotFound: unknown id, another tenant's or integration's session, or expired. The edge
	// answers 404 SESSION_NOT_FOUND for all of them alike.
	ErrNotFound = errors.New("sessions: not found or expired")
	// ErrSensitiveData: the data holds an Aadhaar-like number (DESIGN §14: never stored).
	ErrSensitiveData = errors.New("sessions: data contains an Aadhaar-like number")
	// ErrInvalid: the data is too large or not JSON-encodable, or a required field is missing.
	ErrInvalid = errors.New("sessions: invalid session")
)

// IDPattern matches a session id: "ses_" + ULID (contracts: SessionId).
var IDPattern = regexp.MustCompile(`^ses_[0-9A-HJKMNP-TV-Z]{26}$`)

// aadhaarLike is 12 digits, optionally grouped 4-4-4 with a space or hyphen, not part of a
// longer digit run. The same expression guards the table (00006_sessions.sql).
var aadhaarLike = regexp.MustCompile(`(^|[^0-9])[0-9]{4}[ -]?[0-9]{4}[ -]?[0-9]{4}([^0-9]|$)`)

// Session is one hub.vendor_sessions row.
type Session struct {
	ID            string
	TenantID      string
	IntegrationID string
	Version       int
	AccountID     string
	VendorTxnID   string
	Data          map[string]any
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// Open describes a session to create.
type Open struct {
	TenantID      string
	IntegrationID string
	Version       int    // the integration version step 1 ran on
	AccountID     string // the account step 1 was served by
	Data          map[string]any
	TTL           time.Duration // 0 = DefaultTTL
}

// CheckData refuses data that must never be stored: an Aadhaar-like number in any key or value
// (numbers included), or data larger than MaxDataBytes.
func CheckData(data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("%w: data: %w", ErrInvalid, err)
	}
	if len(b) > MaxDataBytes {
		return fmt.Errorf("%w: data is %d bytes, limit %d", ErrInvalid, len(b), MaxDataBytes)
	}
	if containsAadhaar(data) {
		return ErrSensitiveData
	}
	return nil
}

func containsAadhaar(v any) bool {
	switch x := v.(type) {
	case string:
		return aadhaarLike.MatchString(x)
	case float64:
		return x == math.Trunc(x) && aadhaarLike.MatchString(strconv.FormatFloat(math.Abs(x), 'f', 0, 64))
	case int:
		return aadhaarLike.MatchString(strconv.Itoa(x))
	case int64:
		return aadhaarLike.MatchString(strconv.FormatInt(x, 10))
	case json.Number:
		return aadhaarLike.MatchString(x.String())
	case map[string]any:
		for k, e := range x {
			if aadhaarLike.MatchString(k) || containsAadhaar(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if containsAadhaar(e) {
				return true
			}
		}
	}
	return false
}

const insertSession = `
INSERT INTO hub.vendor_sessions (session_id, tenant_id, integration_id, version, account_id, vendor_txn_id, data, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, now(), now() + $8::interval)
RETURNING created_at, expires_at`

// Create opens a session ("ses_" + ULID) that expires after TTL. The data is checked first
// (CheckData); a string data[TxnKey] is also kept in vendor_txn_id.
func Create(ctx context.Context, db DB, o Open) (Session, error) {
	if o.TenantID == "" || o.IntegrationID == "" || o.AccountID == "" || o.Version <= 0 {
		return Session{}, fmt.Errorf("%w: tenant, integration, version and account are required", ErrInvalid)
	}
	if o.Data == nil {
		o.Data = map[string]any{}
	}
	if err := CheckData(o.Data); err != nil {
		return Session{}, err
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	s := Session{ID: "ses_" + core.NewULID(time.Now()), TenantID: o.TenantID, IntegrationID: o.IntegrationID,
		Version: o.Version, AccountID: o.AccountID, Data: o.Data}
	var txn *string
	if v, ok := o.Data[TxnKey].(string); ok && v != "" {
		s.VendorTxnID, txn = v, &v
	}
	data, _ := json.Marshal(o.Data) // CheckData encoded it already
	interval := strconv.FormatInt(ttl.Microseconds(), 10) + " microseconds"
	if err := db.QueryRow(ctx, insertSession, s.ID, s.TenantID, s.IntegrationID, s.Version, s.AccountID, txn, data, interval).
		Scan(&s.CreatedAt, &s.ExpiresAt); err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return s, nil
}

const selectSession = `
SELECT version, account_id, coalesce(vendor_txn_id, ''), data, created_at, expires_at
FROM hub.vendor_sessions
WHERE session_id = $1 AND tenant_id = $2 AND integration_id = $3 AND expires_at > now()`

// Get loads an open session of tenant on integration. An unknown or malformed id, a session of
// another tenant or integration, and an expired session are all ErrNotFound.
func Get(ctx context.Context, db DB, tenant, integration, id string) (Session, error) {
	if !IDPattern.MatchString(id) {
		return Session{}, ErrNotFound
	}
	s := Session{ID: id, TenantID: tenant, IntegrationID: integration}
	var data []byte
	err := db.QueryRow(ctx, selectSession, id, tenant, integration).Scan(&s.Version, &s.AccountID, &s.VendorTxnID, &data, &s.CreatedAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	if err := json.Unmarshal(data, &s.Data); err != nil {
		return Session{}, fmt.Errorf("get session %s: data: %w", id, err)
	}
	return s, nil
}

// Expire deletes sessions whose expiry is at or before now and returns how many it removed. The
// scheduler leader calls it every tick (DESIGN §17).
func Expire(ctx context.Context, db DB, now time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM hub.vendor_sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("expire sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountOpen counts the open (unexpired) sessions pinned to an account: the retire guard refuses
// to retire an account while it is not zero (DESIGN §6.5).
func CountOpen(ctx context.Context, db DB, accountID string) (int, error) {
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM hub.vendor_sessions WHERE account_id = $1 AND expires_at > now()`, accountID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count open sessions: %w", err)
	}
	return n, nil
}
