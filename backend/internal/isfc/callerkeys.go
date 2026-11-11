package isfc

import (
	"strings"
	"sync/atomic"
)

// ISFC permissions on a caller key that the Hub maps to its key scopes (AUTH_PLAN §8.2).
const (
	PermInvoke       = "HUB:INVOKE"
	PermDryRun       = "HUB:DRY_RUN"
	PermRequestsRead = "HUB:REQUESTS_READ"
)

// Hub key scopes (contract ApiKeyScope), the targets of the permissions above.
const (
	ScopeInvoke       = "invoke"
	ScopeDryRun       = "dry_run"
	ScopeRequestsRead = "requests_read"
)

// ScopesFromPermissions maps ISFC permissions to Hub scopes, in a fixed order, ignoring every
// permission that is not a Hub caller permission. Integration-level control stays in Hub grants,
// so no per-integration scopes come from ISFC.
func ScopesFromPermissions(perms []string) []string {
	var has [3]bool
	for _, p := range perms {
		switch strings.ToUpper(strings.TrimSpace(p)) {
		case PermInvoke:
			has[0] = true
		case PermDryRun:
			has[1] = true
		case PermRequestsRead:
			has[2] = true
		}
	}
	var out []string
	for i, s := range []string{ScopeInvoke, ScopeDryRun, ScopeRequestsRead} {
		if has[i] {
			out = append(out, s)
		}
	}
	return out
}

// CallerEntry is one ISFC caller key as the gateway holds it: the hash (never the key), the caller
// client it was issued to, the ISFC tenants it covers and the Hub scopes its permissions grant.
type CallerEntry struct {
	KeyID     string
	Hash      string   // "sha256:<hex>" (registry.HashKey)
	ClientID  string   // the caller client the key was issued to
	TenantIDs []string // ISFC tenant UUIDs, lower case
	Scopes    []string // Hub scopes (ScopesFromPermissions)
}

// KeyMap is the gateway's in-memory map of ACTIVE ISFC caller keys by hash. Readers call Lookup
// (lock-free, no allocation); the loader swaps the whole map with Replace. It is separate from the
// registry snapshot on purpose: a key change must not publish a snapshot or flood the change log
// (AUTH_PLAN §6.2).
type KeyMap struct {
	m atomic.Pointer[map[string]*CallerEntry]
}

// Lookup finds the entry for a key hash. The entry is shared and read-only.
func (k *KeyMap) Lookup(hash string) (*CallerEntry, bool) {
	m := k.m.Load()
	if m == nil {
		return nil, false
	}
	e, ok := (*m)[hash]
	return e, ok
}

// Replace publishes entries as the new map.
func (k *KeyMap) Replace(entries []CallerEntry) {
	m := make(map[string]*CallerEntry, len(entries))
	for i := range entries {
		e := entries[i]
		m[e.Hash] = &e
	}
	k.m.Store(&m)
}

// Len is the number of keys held.
func (k *KeyMap) Len() int {
	if m := k.m.Load(); m != nil {
		return len(*m)
	}
	return 0
}

// Loaded reports whether a map was ever published (a node that never loaded one refuses ISFC
// credentials instead of treating them as unknown).
func (k *KeyMap) Loaded() bool { return k.m.Load() != nil }
