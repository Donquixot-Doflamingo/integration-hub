package creds

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Store is a credential source that can be rewritten: the rotation steps read every account,
// change one and write them all back (LocalFile today).
type Store interface {
	ReadAll() (map[string]AccountSecrets, error)
	WriteAll(all map[string]AccountSecrets) error
}

var _ Store = (*LocalFile)(nil)

// ErrRotationConflict means a rotation step does not apply to the account's slots: no next to
// promote, no previous to delete, or an expected version that does not match (409 CONFLICT).
var ErrRotationConflict = errors.New("creds: rotation step conflicts with the account's slots")

// SlotView is the metadata of one slot. It never carries values.
type SlotView struct {
	Slot        string     `json:"slot"` // active, next, previous
	Version     int        `json:"cred_version"`
	Fingerprint string     `json:"fingerprint"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

// Views lists the account's slots: active, then next and previous when present.
func (a AccountSecrets) Views() []SlotView {
	v := func(name string, s Slot) SlotView {
		return SlotView{Slot: name, Version: s.Version, Fingerprint: s.Fingerprint, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt}
	}
	out := []SlotView{v("active", a.Active)}
	if a.Next != nil {
		out = append(out, v("next", *a.Next))
	}
	if a.Previous != nil {
		out = append(out, v("previous", *a.Previous))
	}
	return out
}

// NewSlot builds a slot with the next free version of a and a random fingerprint (the local
// file has no per-slot ciphertext to hash, so the fingerprint identifies the write instead).
func NewSlot(a AccountSecrets, values map[string]string, expiresAt *time.Time, now time.Time) Slot {
	v := a.Active.Version
	for _, s := range []*Slot{a.Next, a.Previous} {
		if s != nil && s.Version > v {
			v = s.Version
		}
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	sum := sha256.Sum256(nonce[:])
	return Slot{Version: v + 1, Values: values, Fingerprint: "sha256:" + hex.EncodeToString(sum[:3]), CreatedAt: now.UTC(), ExpiresAt: expiresAt}
}

// AddNext stages s as the next slot, replacing an un-promoted next (rotation step 1).
func AddNext(a AccountSecrets, s Slot) (AccountSecrets, error) {
	a.Next = &s
	if err := a.Validate(); err != nil {
		return AccountSecrets{}, err
	}
	return a, nil
}

// Promote makes next active and keeps the old active as previous (rotation step 2).
func Promote(a AccountSecrets, expectedNext int) (AccountSecrets, error) {
	if a.Next == nil {
		return AccountSecrets{}, fmt.Errorf("%w: no next credential to promote", ErrRotationConflict)
	}
	if a.Next.Version != expectedNext {
		return AccountSecrets{}, fmt.Errorf("%w: next is v%d, expected v%d", ErrRotationConflict, a.Next.Version, expectedNext)
	}
	old := a.Active
	a.Active, a.Next, a.Previous = *a.Next, nil, &old
	return a, nil
}

// DeleteOld destroys the previous slot (rotation step 3).
func DeleteOld(a AccountSecrets, expectedPrevious int) (AccountSecrets, error) {
	if a.Previous == nil {
		return AccountSecrets{}, fmt.Errorf("%w: no previous credential", ErrRotationConflict)
	}
	if a.Previous.Version != expectedPrevious {
		return AccountSecrets{}, fmt.Errorf("%w: previous is v%d, expected v%d", ErrRotationConflict, a.Previous.Version, expectedPrevious)
	}
	a.Previous = nil
	return a, nil
}
