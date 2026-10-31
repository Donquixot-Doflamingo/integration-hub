package creds

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Credentials are the decrypted secret values of one account slot (DESIGN §9). Values is
// shared with the cache and must be treated as read-only.
type Credentials struct {
	AccountID string
	Version   int
	Values    map[string]string
}

// Source returns the active credentials of an account. The request path uses *Cache, which
// never decrypts: a miss is an error, not a slow path (DESIGN §18.5 rule 4).
type Source interface {
	Get(ctx context.Context, accountID string) (Credentials, error)
}

// Slot is one credential slot as stored by a credential source.
type Slot struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// AccountSecrets holds the rotation slots of one account (DESIGN §6.6): Active serves
// traffic, Next is staged for promotion.
type AccountSecrets struct {
	Active Slot  `json:"active"`
	Next   *Slot `json:"next,omitempty"`
}

// Validate checks that the active slot (and next, when present) is usable.
func (a AccountSecrets) Validate() error {
	if err := a.Active.validate(); err != nil {
		return fmt.Errorf("active: %w", err)
	}
	if a.Next != nil {
		if err := a.Next.validate(); err != nil {
			return fmt.Errorf("next: %w", err)
		}
		if a.Next.Version == a.Active.Version {
			return fmt.Errorf("next: version %d equals the active version", a.Next.Version)
		}
	}
	return nil
}

func (s Slot) validate() error {
	if s.Version < 1 {
		return fmt.Errorf("version must be >= 1, got %d", s.Version)
	}
	if len(s.Values) == 0 {
		return errors.New("no values")
	}
	for k, v := range s.Values {
		if k == "" {
			return errors.New("empty key")
		}
		if v == "" {
			return fmt.Errorf("value of %q is empty", k)
		}
	}
	return nil
}

// Keys returns the slot's value names, sorted. It never exposes values.
func (s Slot) Keys() []string {
	keys := make([]string, 0, len(s.Values))
	for k := range s.Values {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Errors callers branch on.
var (
	// ErrCacheMiss means the account's credentials are not in the warmed cache. On the request
	// path this is a bug or an outage of the credential source; edge maps it to
	// UPSTREAM_UNAVAILABLE (retryable, TECH_SPEC §9).
	ErrCacheMiss = errors.New("creds: credentials not in cache")
	// ErrNotFound means the credential source holds nothing for the account.
	ErrNotFound = errors.New("creds: no credentials for account")
	// ErrDecrypt means the encrypted file failed authentication: tampered, truncated or the
	// wrong key. It always fails closed.
	ErrDecrypt = errors.New("creds: secrets file failed authentication (tampered, truncated or wrong key)")
	// ErrUnknownScheme means a vendor names an auth scheme this build does not implement.
	ErrUnknownScheme = errors.New("creds: unknown auth scheme")
	// ErrMissingCredential means the credentials lack a value the auth scheme needs.
	ErrMissingCredential = errors.New("creds: missing credential value")
)

// MissError is the typed cache miss returned by Cache.Get.
type MissError struct {
	AccountID string
}

func (e *MissError) Error() string {
	return fmt.Sprintf("creds: credentials for account %q not in cache", e.AccountID)
}

// Unwrap lets errors.Is(err, ErrCacheMiss) match.
func (e *MissError) Unwrap() error { return ErrCacheMiss }

// AccountIDs returns the accounts of snap that need credentials (every state except retired),
// sorted. Call Cache.Warm with it before the snapshot is swapped in.
func AccountIDs(snap *registry.Snapshot) []string {
	ids := make([]string, 0, len(snap.Accounts))
	for id, a := range snap.Accounts {
		if a.State != registry.AccountRetired {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
