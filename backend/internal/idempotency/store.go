package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Defaults of Options (DESIGN §4.2: 24 h; F-10 security: bounded values).
const (
	DefaultPrefix   = "hub:idem:"
	DefaultTTL      = 24 * time.Hour
	DefaultLeaseTTL = 5 * time.Minute
	DefaultMaxBytes = 1 << 20
	DefaultTimeout  = 50 * time.Millisecond // TECH_SPEC §12.2
	// maxKeyLen is the contract's IdempotencyKey maxLength.
	maxKeyLen = 255
)

var (
	// ErrUnavailable matches every *UnavailableError: Redis gave no usable answer.
	ErrUnavailable = errors.New("idempotency store unavailable")
	// ErrInvalidInput: a key part, the body hash or the request id is empty or malformed. It is
	// a caller bug (the edge validates the header against the contract first); Redis is not called.
	ErrInvalidInput = errors.New("invalid idempotency input")
	// ErrNotOwner: Complete or Release by a request that no longer holds the key (its lease
	// expired, or another request claimed the key after that). Nothing was changed.
	ErrNotOwner = errors.New("idempotency key not held by this request")
	// ErrNotReplayable: Complete with a status other than 200 or 202. Nothing was written; the
	// caller should Release instead.
	ErrNotReplayable = errors.New("response status is not replayable")
	// ErrTooLarge: the response exceeds Options.MaxBytes. Nothing was written.
	ErrTooLarge = errors.New("response too large to store for replay")
)

// UnavailableError reports a Redis failure of one operation ("begin", "complete", "release",
// "warm"). errors.Is(err, ErrUnavailable) is true; Unwrap returns the client error.
type UnavailableError struct {
	Op  string
	Err error
}

func (e *UnavailableError) Error() string {
	return "idempotency " + e.Op + ": " + ErrUnavailable.Error() + ": " + e.Err.Error()
}

func (e *UnavailableError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrUnavailable) match.
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// Key is the scope of one idempotency record: the caller's Idempotency-Key within one tenant
// and one integration (contract parameter IdempotencyKey).
type Key struct {
	Tenant      string
	Integration string
	Key         string
}

// State is what Begin found.
type State uint8

const (
	// Fresh: the caller now holds the key and proceeds; it must end with Complete or Release.
	Fresh State = iota + 1
	// InProgress: another request (Outcome.RequestID) holds the key and has not finished.
	InProgress
	// Completed: Outcome.Response is the stored result of Outcome.RequestID, to be replayed.
	Completed
	// Conflict: the key was claimed with a different body hash (Outcome.RequestID).
	Conflict
	// CompletedNotStored: Outcome.RequestID succeeded but its response was not kept for replay
	// (see MarkCompleted). The vendor was already called; the request must not run again.
	CompletedNotStored
)

func (s State) String() string {
	switch s {
	case Fresh:
		return "fresh"
	case InProgress:
		return "in_progress"
	case Completed:
		return "completed"
	case Conflict:
		return "conflict"
	case CompletedNotStored:
		return "completed_not_stored"
	default:
		return "invalid"
	}
}

// Response is a replayable result: status 200 or 202, the allowlisted headers and the body
// exactly as written to the caller.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Outcome is the answer of Begin. RequestID is the request that holds or held the key; it is
// empty for Fresh. Response is set for Completed only.
type Outcome struct {
	State     State
	RequestID string
	Response  Response
}

// Options tunes the store. Zero values take the defaults.
type Options struct {
	Prefix   string        // Redis key prefix; default DefaultPrefix
	TTL      time.Duration // lifetime of a record from its first claim; default 24 h
	LeaseTTL time.Duration // lifetime of an in-progress claim; default 5 min, at most TTL
	MaxBytes int           // largest stored status + headers + body; default 1 MiB
	// Timeout bounds each Redis round trip (default 50 ms). It reaches the socket only when the
	// client has ContextTimeoutEnabled; otherwise the client's Read/WriteTimeout apply.
	Timeout time.Duration
}

func (o *Options) withDefaults() {
	if o.Prefix == "" {
		o.Prefix = DefaultPrefix
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.LeaseTTL <= 0 {
		o.LeaseTTL = DefaultLeaseTTL
	}
	o.LeaseTTL = min(o.LeaseTTL, o.TTL)
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
}

// Store is safe for concurrent use.
type Store struct {
	rdb                                 redis.Scripter
	opts                                Options
	m                                   storeMetrics
	begin, complete, release, tombstone *redis.Script
}

// New returns a store on rdb (*redis.Client, *redis.ClusterClient and *redis.Ring all satisfy
// redis.Scripter) and registers its metrics on m. Create one Store per process: a second New on
// the same m panics on duplicate registration.
func New(rdb redis.Scripter, m *telemetry.Metrics, opts Options) *Store {
	opts.withDefaults()
	return &Store{
		rdb: rdb, opts: opts, m: newStoreMetrics(m),
		begin: redis.NewScript(beginSrc), complete: redis.NewScript(completeSrc), release: redis.NewScript(releaseSrc), tombstone: redis.NewScript(tombstoneSrc),
	}
}

// BodyHash is the request fingerprint stored with a claim: SHA-256 over the operation and the
// request body bytes. A key reused for another operation or another body is a Conflict.
func BodyHash(operation string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(operation))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Warm loads the scripts into Redis so the first request does not pay the EVAL fallback.
// Call it at startup; failure is not fatal (Begin falls back to EVAL).
func (s *Store) Warm(ctx context.Context) error {
	for _, sc := range []*redis.Script{s.begin, s.complete, s.release, s.tombstone} {
		if err := sc.Load(ctx, s.rdb).Err(); err != nil {
			return &UnavailableError{Op: "warm", Err: err}
		}
	}
	return nil
}

// Begin claims k for requestID, or reports who holds it. Fresh means the caller holds the key
// for Options.LeaseTTL and must end with Complete, MarkCompleted or Release. Calling Begin again with the
// same requestID while the claim is held returns Fresh again.
func (s *Store) Begin(ctx context.Context, k Key, bodyHash, requestID string) (Outcome, error) {
	if err := validKey(k); err != nil {
		return Outcome{}, err
	}
	if bodyHash == "" || requestID == "" {
		return Outcome{}, fmt.Errorf("%w: body hash and request id are required", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	start := time.Now()
	vals, err := s.begin.Run(ctx, s.rdb, []string{s.redisKey(k)},
		bodyHash, requestID, s.opts.LeaseTTL.Milliseconds(), s.opts.TTL.Milliseconds()).Slice()
	s.m.begin.Observe(time.Since(start).Seconds())
	if err != nil {
		s.m.hitError.Inc()
		return Outcome{}, &UnavailableError{Op: "begin", Err: err}
	}
	out, err := parseBegin(vals)
	if err != nil {
		s.m.hitError.Inc()
		return Outcome{}, &UnavailableError{Op: "begin", Err: err}
	}
	s.m.hit(out.State).Inc()
	return out, nil
}

func parseBegin(vals []any) (Outcome, error) {
	str := func(i int) (string, bool) {
		if i >= len(vals) {
			return "", false
		}
		v, ok := vals[i].(string)
		return v, ok
	}
	tag, _ := str(0)
	switch tag {
	case "f":
		return Outcome{State: Fresh}, nil
	case "x", "p", "t":
		rid, ok := str(1)
		if !ok {
			break
		}
		switch tag {
		case "x":
			return Outcome{State: Conflict, RequestID: rid}, nil
		case "t":
			return Outcome{State: CompletedNotStored, RequestID: rid}, nil
		}
		return Outcome{State: InProgress, RequestID: rid}, nil
	case "c":
		rid, ok1 := str(1)
		code, ok2 := str(2)
		hdr, ok3 := str(3)
		body, ok4 := str(4)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			break
		}
		status, err := strconv.Atoi(code)
		if err != nil {
			break
		}
		header, err := decodeHeader(hdr)
		if err != nil {
			return Outcome{}, fmt.Errorf("decode stored headers: %w", err)
		}
		return Outcome{State: Completed, RequestID: rid, Response: Response{Status: status, Header: header, Body: []byte(body)}}, nil
	}
	return Outcome{}, fmt.Errorf("unexpected begin reply %v", vals)
}

// Complete stores r as the result of requestID, replayed by every later Begin with the same key
// and body until the record expires TTL after the first claim. Only status 200 and 202 are
// stored (ErrNotReplayable otherwise); headers outside the replay allowlist are dropped.
// ErrNotOwner means requestID no longer holds the key; ErrTooLarge means r exceeds MaxBytes.
// In both cases, and for ErrNotReplayable, Redis is unchanged.
func (s *Store) Complete(ctx context.Context, k Key, requestID string, r Response) error {
	if err := validKey(k); err != nil {
		return err
	}
	if requestID == "" {
		return fmt.Errorf("%w: request id is required", ErrInvalidInput)
	}
	if r.Status != http.StatusOK && r.Status != http.StatusAccepted {
		s.m.completeNotReplayable.Inc()
		return fmt.Errorf("%w: %d", ErrNotReplayable, r.Status)
	}
	hdr := encodeHeader(r.Header)
	if size := len("200") + len(hdr) + len(r.Body); size > s.opts.MaxBytes {
		s.m.completeTooLarge.Inc()
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, size, s.opts.MaxBytes)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	start := time.Now()
	n, err := s.complete.Run(ctx, s.rdb, []string{s.redisKey(k)},
		requestID, strconv.Itoa(r.Status), hdr, r.Body).Int64()
	s.m.complete.Observe(time.Since(start).Seconds())
	switch {
	case err != nil:
		s.m.completeError.Inc()
		return &UnavailableError{Op: "complete", Err: err}
	case n == 1:
		s.m.completeOK.Inc()
		return nil
	case n == 0 || n == -1:
		s.m.completeNotOwner.Inc()
		return ErrNotOwner
	default:
		s.m.completeError.Inc()
		return &UnavailableError{Op: "complete", Err: fmt.Errorf("unexpected complete reply %d", n)}
	}
}

// MarkCompleted records that requestID succeeded although its response could not be stored by
// Complete (ErrTooLarge, or Redis failed meanwhile). The record is kept until the TTL from the
// first claim, and every later Begin with the same key and body answers CompletedNotStored, so
// the vendor is not called again. ErrNotOwner means requestID no longer holds the key.
func (s *Store) MarkCompleted(ctx context.Context, k Key, requestID string) error {
	if err := validKey(k); err != nil {
		return err
	}
	if requestID == "" {
		return fmt.Errorf("%w: request id is required", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	start := time.Now()
	n, err := s.tombstone.Run(ctx, s.rdb, []string{s.redisKey(k)}, requestID).Int64()
	s.m.complete.Observe(time.Since(start).Seconds())
	switch {
	case err != nil:
		s.m.tombstoneError.Inc()
		return &UnavailableError{Op: "mark_completed", Err: err}
	case n == 1:
		s.m.tombstoneOK.Inc()
		return nil
	case n == 0 || n == -1:
		s.m.tombstoneNotOwner.Inc()
		return ErrNotOwner
	default:
		s.m.tombstoneError.Inc()
		return &UnavailableError{Op: "mark_completed", Err: fmt.Errorf("unexpected mark_completed reply %d", n)}
	}
}

// Release deletes the record of k if requestID holds it (in progress or completed), so the next
// request with the key runs fresh. A missing record (lease already expired) is not an error;
// ErrNotOwner means another request holds the key and nothing was deleted.
func (s *Store) Release(ctx context.Context, k Key, requestID string) error {
	if err := validKey(k); err != nil {
		return err
	}
	if requestID == "" {
		return fmt.Errorf("%w: request id is required", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	start := time.Now()
	n, err := s.release.Run(ctx, s.rdb, []string{s.redisKey(k)}, requestID).Int64()
	s.m.release.Observe(time.Since(start).Seconds())
	switch {
	case err != nil:
		s.m.releaseError.Inc()
		return &UnavailableError{Op: "release", Err: err}
	case n == 1:
		s.m.releaseOK.Inc()
		return nil
	case n == 0:
		s.m.releaseMissing.Inc()
		return nil
	case n == -1:
		s.m.releaseNotOwner.Inc()
		return ErrNotOwner
	default:
		s.m.releaseError.Inc()
		return &UnavailableError{Op: "release", Err: fmt.Errorf("unexpected release reply %d", n)}
	}
}

// redisKey hashes the scope so raw caller keys never reach Redis and the key length is fixed.
func (s *Store) redisKey(k Key) string {
	h := sha256.New()
	h.Write([]byte(k.Tenant))
	h.Write([]byte{0})
	h.Write([]byte(k.Integration))
	h.Write([]byte{0})
	h.Write([]byte(k.Key))
	return s.opts.Prefix + hex.EncodeToString(h.Sum(nil))
}

// validKey applies the contract's IdempotencyKey shape (1–255 visible ASCII characters) and
// requires both scope parts.
func validKey(k Key) error {
	if k.Tenant == "" || k.Integration == "" {
		return fmt.Errorf("%w: tenant and integration are required", ErrInvalidInput)
	}
	if k.Key == "" || len(k.Key) > maxKeyLen {
		return fmt.Errorf("%w: key length must be 1-%d", ErrInvalidInput, maxKeyLen)
	}
	for i := range len(k.Key) {
		if c := k.Key[i]; c < 0x21 || c > 0x7e {
			return fmt.Errorf("%w: key must be visible ASCII", ErrInvalidInput)
		}
	}
	return nil
}

// replayHeaders are the contract response headers of invokeIntegration that belong to the
// result (InvokeOk, Accepted). X-Hub-Request-Id travels as Outcome.RequestID; the debug-only
// X-Hub-Integration-Version and anything else (credentials, cookies, hop-by-hop) is never stored.
var replayHeaders = [...]string{"Content-Type", "Location", "X-Hub-Served-By", "X-Hub-Session"}

func encodeHeader(h http.Header) string {
	kept := make(map[string]string, len(replayHeaders))
	for _, name := range replayHeaders {
		if v := h.Get(name); v != "" {
			kept[name] = v
		}
	}
	b, _ := json.Marshal(kept) // map[string]string cannot fail to marshal
	return string(b)
}

func decodeHeader(s string) (http.Header, error) {
	var kept map[string]string
	if err := json.Unmarshal([]byte(s), &kept); err != nil {
		return nil, err
	}
	h := make(http.Header, len(kept))
	for k, v := range kept {
		h.Set(k, v)
	}
	return h, nil
}
