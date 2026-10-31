package callbacks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Callback headers (contracts/openapi/hub-public.v1.yaml components/parameters).
const (
	SignatureHeader  = "X-Hub-Signature"
	EventHeader      = "X-Hub-Event"
	DeliveryIDHeader = "X-Hub-Delivery-Id"
)

// DefaultTolerance is how far a signature timestamp may be from the receiver's clock (DESIGN
// §12.1: tenants reject timestamps older than 5 minutes).
const DefaultTolerance = 5 * time.Minute

// Signature errors.
var (
	ErrNoSecret           = errors.New("callbacks: no signing secret")
	ErrSignatureMalformed = errors.New("callbacks: malformed X-Hub-Signature")
	ErrSignatureExpired   = errors.New("callbacks: X-Hub-Signature timestamp outside the tolerance")
	ErrSignatureMismatch  = errors.New("callbacks: no X-Hub-Signature v1 value matches")
)

func mac(secret []byte, t string, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(t))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// Sign returns the X-Hub-Signature value for body at time t:
// "t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>", one v1= per secret (several during a
// rotation overlap, active first).
func Sign(secrets [][]byte, t time.Time, body []byte) (string, error) {
	if len(secrets) == 0 {
		return "", ErrNoSecret
	}
	unix := strconv.FormatInt(t.Unix(), 10)
	var b strings.Builder
	b.Grow(2 + len(unix) + len(secrets)*68)
	b.WriteString("t=")
	b.WriteString(unix)
	for _, s := range secrets {
		if len(s) == 0 {
			return "", ErrNoSecret
		}
		b.WriteString(",v1=")
		b.WriteString(hex.EncodeToString(mac(s, unix, body)))
	}
	return b.String(), nil
}

// Verify checks an X-Hub-Signature header against the raw body, the way a receiver must: the
// timestamp is within tolerance of now (DefaultTolerance when ≤ 0), and at least one v1= value
// matches the HMAC under one of secrets (constant-time). Unknown vN= schemes are ignored.
func Verify(header string, body []byte, secrets [][]byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	parts := strings.Split(header, ",")
	t, ok := strings.CutPrefix(parts[0], "t=")
	if !ok {
		return ErrSignatureMalformed
	}
	unix, err := strconv.ParseInt(t, 10, 64)
	if err != nil || unix < 0 {
		return ErrSignatureMalformed
	}
	var sigs [][]byte
	for _, p := range parts[1:] {
		v, ok := strings.CutPrefix(p, "v1=")
		if !ok {
			continue
		}
		raw, err := hex.DecodeString(v)
		if err != nil || len(raw) != sha256.Size {
			return ErrSignatureMalformed
		}
		sigs = append(sigs, raw)
	}
	if len(sigs) == 0 {
		return ErrSignatureMalformed
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return ErrSignatureExpired
	}
	for _, s := range secrets {
		want := mac(s, t, body)
		for _, got := range sigs {
			if hmac.Equal(want, got) {
				return nil
			}
		}
	}
	return ErrSignatureMismatch
}
