package audit

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// Sealed payloads (DESIGN §13.2, D6, HUB-208): under the `sealed` policy each event payload is
// stored as AES-256-GCM ciphertext under a per-tenant data key. Only the admin's unseal
// (an audited admin operation) decrypts it; the hot path only encrypts. Keys are never logged
// and never stored next to the ciphertext: a blob names its key id only.
//
// Blob layout (all of it authenticated):
//
//	"HS1" | flags (1 byte) | key id length (1 byte) | key id | nonce (12 bytes) | ciphertext+tag
//
// flags bit 0 means the plaintext was truncated to MaxInlinePayload before sealing. The AAD
// binds the blob to its tenant, request and seq, so a blob copied to another row does not open.

// SealKeys supplies per-tenant data keys.
type SealKeys interface {
	// Current is the key new payloads of tenant are sealed with, and its id.
	Current(tenant string) (id string, key []byte, err error)
	// Key returns the tenant's key with id (older blobs after a key rotation).
	Key(tenant, id string) ([]byte, error)
}

// LocalKeys derives every tenant's data key from one 32-byte master key with HKDF-SHA256
// (dev and single-node installs: the master is the localfile dev key, TECH_SPEC §8.1). The
// derivation is domain-separated from the credential file's own use of that key.
type LocalKeys struct{ master []byte }

// LocalKeyID names the derived keys of LocalKeys.
const LocalKeyID = "local1"

// NewLocalKeys returns a key source for master (32 bytes).
func NewLocalKeys(master []byte) (*LocalKeys, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("audit: seal master key is %d bytes, want 32", len(master))
	}
	return &LocalKeys{master: bytes.Clone(master)}, nil
}

// Current implements SealKeys.
func (k *LocalKeys) Current(tenant string) (string, []byte, error) {
	key, err := k.Key(tenant, LocalKeyID)
	return LocalKeyID, key, err
}

// Key implements SealKeys.
func (k *LocalKeys) Key(tenant, id string) ([]byte, error) {
	if id != LocalKeyID {
		return nil, fmt.Errorf("audit: unknown seal key id %q", id)
	}
	return hkdf.Key(sha256.New, k.master, nil, "hub-audit-seal/v1:"+tenant, 32)
}

// Sealer encrypts and decrypts sealed payloads. Safe for concurrent use; it caches one AEAD
// per (tenant, key id).
type Sealer struct {
	keys  SealKeys
	aeads sync.Map // tenant + "\x00" + id → cipher.AEAD
}

// NewSealer returns a sealer over keys.
func NewSealer(keys SealKeys) *Sealer { return &Sealer{keys: keys} }

var sealMagic = []byte("HS1")

const flagTruncated = 1

// ErrUnseal is any failure to open a sealed blob (wrong key, tampering, wrong row).
var ErrUnseal = errors.New("audit: sealed payload does not open")

func (s *Sealer) aead(tenant, id string, key func() ([]byte, error)) (cipher.AEAD, error) {
	ck := tenant + "\x00" + id
	if a, ok := s.aeads.Load(ck); ok {
		return a.(cipher.AEAD), nil
	}
	k, err := key()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	clear(k)
	if err != nil {
		return nil, fmt.Errorf("audit: seal cipher: %w", err)
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("audit: seal gcm: %w", err)
	}
	s.aeads.Store(ck, a)
	return a, nil
}

func sealAAD(header []byte, tenant, requestID string, seq int) []byte {
	aad := append([]byte(nil), header...)
	return append(aad, "|"+tenant+"|"+requestID+"|"+strconv.Itoa(seq)...)
}

// Seal encrypts plaintext for one event row.
func (s *Sealer) Seal(tenant, requestID string, seq int, plaintext []byte, truncated bool) ([]byte, error) {
	id, key, err := s.keys.Current(tenant)
	if err != nil {
		return nil, err
	}
	if len(id) == 0 || len(id) > 255 {
		return nil, fmt.Errorf("audit: seal key id must be 1..255 bytes")
	}
	a, err := s.aead(tenant, id, func() ([]byte, error) { return key, nil })
	if err != nil {
		return nil, err
	}
	var flags byte
	if truncated {
		flags |= flagTruncated
	}
	header := append(append([]byte(nil), sealMagic...), flags, byte(len(id))) //nolint:gosec // 1..255, checked above
	header = append(header, id...)
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("audit: seal nonce: %w", err)
	}
	out := append(append(header, nonce...), a.Seal(nil, nonce, plaintext, sealAAD(header, tenant, requestID, seq))...)
	return out, nil
}

// parseSealed splits a blob into header, key id, nonce and ciphertext.
func parseSealed(blob []byte) (header []byte, flags byte, id string, rest []byte, ok bool) {
	if len(blob) < len(sealMagic)+2 || !bytes.HasPrefix(blob, sealMagic) {
		return nil, 0, "", nil, false
	}
	flags, n := blob[3], int(blob[4])
	end := 5 + n
	if n == 0 || len(blob) < end {
		return nil, 0, "", nil, false
	}
	return blob[:end], flags, string(blob[5:end]), blob[end:], true
}

// SealedTruncated reports whether a sealed blob holds a truncated payload (readable without
// the key: the timeline shows it).
func SealedTruncated(blob []byte) bool {
	_, flags, _, _, ok := parseSealed(blob)
	return ok && flags&flagTruncated != 0
}

// Open decrypts one event row's blob. Any mismatch (key, tenant, request, seq, tampering) is
// ErrUnseal.
func (s *Sealer) Open(tenant, requestID string, seq int, blob []byte) (plaintext []byte, truncated bool, err error) {
	header, flags, id, rest, ok := parseSealed(blob)
	if !ok {
		return nil, false, ErrUnseal
	}
	a, err := s.aead(tenant, id, func() ([]byte, error) { return s.keys.Key(tenant, id) })
	if err != nil {
		return nil, false, err
	}
	if len(rest) < a.NonceSize()+a.Overhead() {
		return nil, false, ErrUnseal
	}
	nonce, ct := rest[:a.NonceSize()], rest[a.NonceSize():]
	pt, err := a.Open(nil, nonce, ct, sealAAD(header, tenant, requestID, seq))
	if err != nil {
		return nil, false, ErrUnseal
	}
	return pt, flags&flagTruncated != 0, nil
}
