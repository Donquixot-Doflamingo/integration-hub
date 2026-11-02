package control

import (
	"context"
	"fmt"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// The helpers below let the admin API create, read and remove whole credential entries
// (account credentials at createAccount and retire, registrar signing secrets at create, rotate
// and delete). They hold the rotator's lock, so they never interleave with a rotation step's
// read-modify-write of the store. They write no change-log row: the config edit they belong to
// records the change (with slot metadata only).

// ReadCredentials returns the stored entry of id (an account or a registrar secret id).
func (r *Rotator) ReadCredentials(id string) (creds.AccountSecrets, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.Store.ReadAll()
	if err != nil {
		return creds.AccountSecrets{}, false, fmt.Errorf("credentials: read store: %w", err)
	}
	s, ok := all[id]
	return s, ok, nil
}

// CredentialSlots returns the slot metadata (never values) of every entry in the store.
func (r *Rotator) CredentialSlots() (map[string][]creds.SlotView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.Store.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("credentials: read store: %w", err)
	}
	out := make(map[string][]creds.SlotView, len(all))
	for id, s := range all {
		out[id] = s.Views()
	}
	return out, nil
}

// PutCredentials replaces the entry of id with s, or deletes it when s is nil. With create,
// an existing entry is a conflict (ErrConflict): ids are shared by accounts and registrar
// endpoints in one store. Nodes are pinged to reload the id.
func (r *Rotator) PutCredentials(ctx context.Context, id string, s *creds.AccountSecrets, create bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.Store.ReadAll()
	if err != nil {
		return fmt.Errorf("credentials: read store: %w", err)
	}
	_, exists := all[id]
	if create && exists {
		return fmt.Errorf("%w: the credential store already holds an entry %q", ErrConflict, id)
	}
	if s == nil {
		if !exists {
			return nil
		}
		delete(all, id)
	} else {
		all[id] = *s
	}
	if err := r.Store.WriteAll(all); err != nil {
		return fmt.Errorf("credentials: write store: %w", err)
	}
	if r.Notify != nil {
		r.Notify(ctx, id)
	}
	return nil
}
