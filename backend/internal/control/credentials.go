package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// CredentialsChannel is the Redis channel the rotator pings with an account id after a
// rotation step; every data-plane node reloads its credential cache on it (DESIGN §6.6).
const CredentialsChannel = "hub:creds"

// ErrUnknownAccount means the credential store holds nothing for the account.
var ErrUnknownAccount = errors.New("credentials: account has no credentials")

// Rotator runs the credential rotation steps (DESIGN §6.6): add next → test → promote →
// delete old. Every step writes a change-log row with slot metadata only, never values.
type Rotator struct {
	Pool  *pgxpool.Pool
	Store creds.Store
	// Notify tells nodes an account's credentials changed (Redis PUBLISH hub:creds); may be nil.
	Notify func(ctx context.Context, account string)
	Now    func() time.Time

	mu sync.Mutex // the store is read-modify-written whole
}

// Rotation is the outcome of one step.
type Rotation struct {
	ChangeID string
	Account  string
	Slots    []creds.SlotView
}

func (r *Rotator) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// AddNext stages values as the account's next credential (addNextCredential).
func (r *Rotator) AddNext(ctx context.Context, account string, values map[string]string, expiresAt *time.Time, author, reason string) (Rotation, error) {
	return r.step(ctx, account, "addNextCredential", author, reason, func(a creds.AccountSecrets) (creds.AccountSecrets, error) {
		return creds.AddNext(a, creds.NewSlot(a, values, expiresAt, r.now()))
	})
}

// Promote makes next active; the old active is kept as previous (promoteNextCredential).
func (r *Rotator) Promote(ctx context.Context, account string, expectedNext int, author, reason string) (Rotation, error) {
	return r.step(ctx, account, "promoteNextCredential", author, reason, func(a creds.AccountSecrets) (creds.AccountSecrets, error) {
		return creds.Promote(a, expectedNext)
	})
}

// DeleteOld destroys the previous credential (deleteOldCredential).
func (r *Rotator) DeleteOld(ctx context.Context, account string, expectedPrevious int, author, reason string) (Rotation, error) {
	return r.step(ctx, account, "deleteOldCredential", author, reason, func(a creds.AccountSecrets) (creds.AccountSecrets, error) {
		return creds.DeleteOld(a, expectedPrevious)
	})
}

func (r *Rotator) step(ctx context.Context, account, action, author, reason string, edit func(creds.AccountSecrets) (creds.AccountSecrets, error)) (Rotation, error) {
	if strings.TrimSpace(author) == "" {
		return Rotation{}, ErrMissingAuthor
	}
	if strings.TrimSpace(reason) == "" {
		return Rotation{}, ErrMissingReason
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.Store.ReadAll()
	if err != nil {
		return Rotation{}, fmt.Errorf("credentials: read store: %w", err)
	}
	cur, ok := all[account]
	if !ok {
		return Rotation{}, fmt.Errorf("%w: %s", ErrUnknownAccount, account)
	}
	next, err := edit(cur)
	if err != nil {
		return Rotation{}, err
	}
	// The change row goes first: a write without an audit entry must not happen. A failed
	// store write after it leaves an entry for a change that did not land, which says so.
	id, err := RecordChange(ctx, r.Pool, Change{
		Actor: author, Action: action, ObjectType: "account", ObjectID: account, Reason: reason,
		Before: map[string]any{"credentials": cur.Views()}, After: map[string]any{"credentials": next.Views()},
	})
	if err != nil {
		return Rotation{}, err
	}
	all[account] = next
	if err := r.Store.WriteAll(all); err != nil {
		return Rotation{}, fmt.Errorf("credentials: write store (change %s recorded): %w", id, err)
	}
	if r.Notify != nil {
		r.Notify(ctx, account)
	}
	return Rotation{ChangeID: id, Account: account, Slots: next.Views()}, nil
}
