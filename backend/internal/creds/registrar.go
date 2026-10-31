package creds

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Registrar signing secrets (DESIGN §12.1, HUB-119) live in the same credential source as
// vendor credentials: one entry per secret_ref id with the value SigningSecretKey, active and
// optionally next slot. They are loaded by Cache.Warm (encrypted at rest, registered with the
// redaction set on load) and never decrypted on the delivery path.

// SigningSecretKey is the value name that holds a registrar endpoint's signing secret.
const SigningSecretKey = "signing_secret"

// MinSigningSecretLen is the shortest signing secret RegistrarSecrets serves. HMAC-SHA256 keys
// shorter than this are guessable; hubctl only enforces the 6-byte redaction minimum.
const MinSigningSecretLen = 16

const localFileScheme = "localfile://"

// ErrBadSecretRef means a secret_ref is not a localfile://<id> reference (the only source in
// phase 1; kms:// and vault:// arrive with F-15).
var ErrBadSecretRef = errors.New("creds: secret_ref must be localfile://<id>")

// SecretID returns the credential-source id named by ref ("localfile://reg_orders_cb" →
// "reg_orders_cb").
func SecretID(ref string) (string, error) {
	id, ok := strings.CutPrefix(ref, localFileScheme)
	if !ok || id == "" || strings.ContainsAny(id, "/ ") {
		return "", fmt.Errorf("%w, got %q", ErrBadSecretRef, ref)
	}
	return id, nil
}

// RegistrarSecretIDs returns the credential ids referenced by snap's registrar endpoints,
// sorted and de-duplicated. Refs that do not parse are skipped (delivery then fails for that
// endpoint with ErrBadSecretRef).
func RegistrarSecretIDs(snap *registry.Snapshot) []string {
	ids := make([]string, 0, len(snap.Registrar))
	for _, r := range snap.Registrar {
		if id, err := SecretID(r.SecretRef); err == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

// SnapshotSecretIDs is AccountIDs plus RegistrarSecretIDs: what a role that both calls vendors
// and signs callbacks (the worker) passes to Cache.Warm before swapping snap in.
func SnapshotSecretIDs(snap *registry.Snapshot) []string {
	ids := append(AccountIDs(snap), RegistrarSecretIDs(snap)...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

// RegistrarSecrets serves registrar signing secrets from a warmed Cache. It satisfies the
// callbacks package's SigningSecrets port without callbacks importing creds (TECH_SPEC §4.1).
type RegistrarSecrets struct {
	Cache *Cache
}

// SigningSecrets returns the secrets to sign with for ref: the active slot first, then the
// next slot during a rotation overlap (contract CallbackSignature: several v1= values).
// Identical values are returned once. A cache miss is a *MissError (errors.Is ErrCacheMiss).
func (r RegistrarSecrets) SigningSecrets(ctx context.Context, ref string) ([][]byte, error) {
	id, err := SecretID(ref)
	if err != nil {
		return nil, err
	}
	slots, err := r.Cache.Slots(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(slots))
	for _, s := range slots {
		v := s.Values[SigningSecretKey]
		if v == "" {
			return nil, fmt.Errorf("%w: %s (slot version %d) has no %s value", ErrMissingCredential, id, s.Version, SigningSecretKey)
		}
		if len(v) < MinSigningSecretLen {
			return nil, fmt.Errorf("%w: %s (slot version %d) %s is shorter than %d bytes", ErrMissingCredential, id, s.Version, SigningSecretKey, MinSigningSecretLen)
		}
		if !slices.ContainsFunc(out, func(b []byte) bool { return string(b) == v }) {
			out = append(out, []byte(v))
		}
	}
	return out, nil
}
