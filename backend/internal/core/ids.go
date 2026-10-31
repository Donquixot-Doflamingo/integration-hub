package core

import (
	"crypto/rand"
	"encoding/binary"
	"regexp"
	"time"
)

// crockford is the ULID alphabet (Crockford base32: no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// RequestIDPattern matches a Hub request id: "req_" + ULID (contracts/schemas/events/canonical-error.json).
var RequestIDPattern = regexp.MustCompile(`^req_[0-9A-HJKMNP-TV-Z]{26}$`)

// NewULID returns a ULID for t: 48-bit millisecond timestamp + 80 random bits, encoded as 26
// Crockford base32 characters, so ids sort by creation time.
func NewULID(t time.Time) string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(t.UnixMilli())<<16) //nolint:gosec // UnixMilli is positive for any real clock
	_, _ = rand.Read(b[6:])                                      // crypto/rand.Read never fails (Go 1.24+)
	var out [26]byte
	// 128 bits → 26 chars of 5 bits; the first char carries the top 3 bits.
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&0x1f]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// NewRequestID mints a request id ("req_" + ULID).
func NewRequestID() string { return "req_" + NewULID(time.Now()) }
