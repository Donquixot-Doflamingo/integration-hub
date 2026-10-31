package hubclient

import (
	"crypto/rand"
	"encoding/base64"
)

// NewIdempotencyKey returns a random, URL-safe key ("idem_" + 22 base64url characters, 128 bits
// of entropy) for WithIdempotencyKey.
//
// Integrations that move money or quota require a key. A random key protects only if you create
// it once per business operation, store it with that operation, and send the same key on every
// retry; a new key per attempt is a new request. When a stable business id exists (an order id,
// a payout id), deriving the key from it is usually simpler. Never put PII in a key.
func NewIdempotencyKey() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (it aborts the program instead)
	return "idem_" + base64.RawURLEncoding.EncodeToString(b[:])
}
