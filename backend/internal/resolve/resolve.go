// Package resolve picks the integration version, target (weighted), failover chain and policies
// for a request from a snapshot (DESIGN §5.1 step 3, §6, §18.3). It is a pure function of the
// snapshot: no I/O, safe on the hot path.
package resolve

import (
	"errors"
	"fmt"
	"hash/fnv"
	"slices"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Errors the edge maps to canonical codes.
var (
	ErrIntegrationNotFound = errors.New("integration not found")  // INTEGRATION_NOT_FOUND
	ErrNotGranted          = errors.New("tenant is not granted")  // FORBIDDEN (reason not_granted)
	ErrVersionNotFound     = errors.New("version not found")      // pinned/session version gone
	ErrNoTarget            = errors.New("version has no target")  // cannot happen in a validated snapshot
	ErrAccountNotInVersion = errors.New("account not in version") // session account gone from its version
)

// Resolution is everything the pipeline needs for one attempt. Every attempt records
// VersionNumber and SnapshotVersion (DESIGN §6.3).
type Resolution struct {
	Integration     *registry.Integration
	Version         *registry.IntegrationVersion
	VersionNumber   int
	Target          registry.Target
	TargetIndex     int
	Failover        []registry.Target
	Delivery        registry.Delivery
	SnapshotVersion uint64
	// Operation is the invoked operation of the integration (envelope operation). The caller of
	// Resolve sets it; Pair uses it to pick the target's transformer pair (HUB-343).
	Operation string
}

// Pair is the transformer pair of Target for the invoked operation: its per-operation pair
// when the target lists one, else the default pair. Failover and shadow copies of a
// Resolution swap Target and keep Operation, so they select the same way.
func (r Resolution) Pair() registry.TransformerPair { return r.Target.Pair(r.Operation) }

// Granted reports whether tenant may invoke the integration: the owner or a grantee.
func Granted(in *registry.Integration, tenant string) bool {
	return in.OwnerTenant == tenant || slices.Contains(in.Grants, tenant)
}

// Resolve returns the resolution for tenant calling integrationID. version 0 means the active
// version; any other value is that exact version (pinned requests, vendor sessions). routeKey
// (idempotency key, else request id) makes the weighted choice sticky.
func Resolve(snap *registry.Snapshot, tenant, integrationID string, version int, routeKey string) (Resolution, error) {
	in, ok := snap.Integrations[integrationID]
	if !ok {
		return Resolution{}, ErrIntegrationNotFound
	}
	if !Granted(in, tenant) {
		return Resolution{}, ErrNotGranted
	}
	n := version
	if n == 0 {
		n = in.ActiveVersion
	}
	v, ok := in.Versions[n]
	if !ok {
		return Resolution{}, fmt.Errorf("%w: %s v%d", ErrVersionNotFound, integrationID, n)
	}
	idx, err := pick(v.Targets, routeKey)
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{
		Integration: in, Version: v, VersionNumber: n, Target: v.Targets[idx], TargetIndex: idx,
		Failover: v.Failover, Delivery: v.Delivery, SnapshotVersion: snap.Version,
	}, nil
}

// pick chooses a target by weight using a stable hash of routeKey into [0, 100).
func pick(targets []registry.Target, routeKey string) (int, error) {
	switch len(targets) {
	case 0:
		return 0, ErrNoTarget
	case 1:
		return 0, nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(routeKey))
	slot := int(h.Sum64() % 100) // #nosec G115 -- modulo 100 fits in int
	sum := 0
	for i, t := range targets {
		sum += t.Weight
		if slot < sum {
			return i, nil
		}
	}
	return len(targets) - 1, nil // weights total 100 in a validated snapshot
}

// ResolveSession pins a later step of a vendor session to the exact version and the account that
// opened it (DESIGN §6.3 row 6): the target of that version (primary or failover) whose account is
// account. The txn id lives at that account, so the result has no failover chain. A version that
// is gone or no longer has a target on the account is ErrVersionNotFound / ErrAccountNotInVersion.
// When several targets share the account, the first in file order wins (targets, then failover).
func ResolveSession(snap *registry.Snapshot, tenant, integrationID string, version int, account string) (Resolution, error) {
	if version <= 0 {
		return Resolution{}, fmt.Errorf("%w: %s v%d", ErrVersionNotFound, integrationID, version)
	}
	res, err := Resolve(snap, tenant, integrationID, version, "")
	if err != nil {
		return Resolution{}, err
	}
	res.Failover = nil
	for i, t := range res.Version.Targets {
		if t.Account == account {
			res.Target, res.TargetIndex = t, i
			return res, nil
		}
	}
	for i, t := range res.Version.Failover {
		if t.Account == account {
			res.Target, res.TargetIndex = t, len(res.Version.Targets)+i
			return res, nil
		}
	}
	return Resolution{}, fmt.Errorf("%w: %s v%d has no target on %s", ErrAccountNotInVersion, integrationID, version, account)
}
