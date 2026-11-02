package control

import (
	"context"
	"fmt"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// TransitionError refuses an account lifecycle transition that DESIGN §6.5 does not allow from
// the account's current state (409 CONFLICT with from/to). It matches ErrConflict.
type TransitionError struct {
	Account  string
	From, To registry.AccountState
	Why      string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("cannot transition account %s from '%s' to '%s': %s", e.Account, e.From, e.To, e.Why)
}

// Unwrap lets errors.Is(err, ErrConflict) match.
func (e *TransitionError) Unwrap() error { return ErrConflict }

// TransitionAccount moves an account through its lifecycle (transitionAccountLifecycle,
// DESIGN §6.5): active → draining, draining → active, draining → retired. The publisher also
// drains and re-activates accounts on every commit from the active versions' targets, so a
// manual transition that contradicts them is refused instead of being silently undone:
// draining an account an active version targets, or re-activating one nothing targets.
// Retire runs the retire guard (pinned requests, open sessions) and returns a
// *RetireBlockedError with its counts. Credentials are not touched here: the caller deletes
// them after a retire (Rotator.PutCredentials).
func (p *Publisher) TransitionAccount(ctx context.Context, account string, to registry.AccountState, author, reason string) (Result, error) {
	return p.locked(ctx, func() (Result, error) {
		t, snap, err := p.load(ctx)
		if err != nil {
			return Result{}, err
		}
		a, ok := snap.Accounts[account]
		if !ok {
			return Result{}, fmt.Errorf("%w: account %q", ErrNotFound, account)
		}
		from := a.State
		refused := func(why string) (Result, error) {
			return Result{}, &TransitionError{Account: account, From: from, To: to, Why: why}
		}
		targeted := referenced(snap)[account]
		switch {
		case from == registry.AccountActive && to == registry.AccountDraining:
			if targeted {
				return refused("an active integration version still targets it")
			}
		case from == registry.AccountDraining && to == registry.AccountActive:
			if !targeted {
				return refused("no active integration version targets it, so it would drain again")
			}
		case from == registry.AccountDraining && to == registry.AccountRetired:
			if targeted {
				return refused("an active integration version still targets it")
			}
			if p.Guard != nil {
				pinned, sessions, err := p.Guard(ctx, snap, account)
				if err != nil {
					return Result{}, err
				}
				if pinned > 0 || sessions > 0 {
					return Result{}, &RetireBlockedError{Account: account, Pinned: pinned, Sessions: sessions}
				}
			}
		default:
			return refused("allowed transitions are active -> draining, draining -> active and draining -> retired")
		}
		file, err := t.accountFile(account)
		if err != nil {
			return Result{}, err
		}
		if t.files[file], err = setScalar(t.files[file], "state", string(to)); err != nil {
			return Result{}, err
		}
		return p.commit(ctx, t, Change{Actor: author, Action: "transitionAccountLifecycle", ObjectType: "account", ObjectID: account, Reason: reason,
			Before: map[string]any{"state": string(from)}, After: map[string]any{"state": string(to)}})
	})
}
