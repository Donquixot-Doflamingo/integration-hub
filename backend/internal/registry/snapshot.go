package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// Snapshot is an immutable, fully validated view of the registry (DESIGN §8, §18.3). The hot
// path reads only snapshots: never the database or the files. Requests keep the snapshot they
// started with for their whole attempt, so a swap never changes anything mid-flight (§6.3).
type Snapshot struct {
	// Version is the snapshot archive version this content was published as
	// (hub.config_snapshots.version, the number getLatestSnapshot returns), so every node
	// serving the same publish reports the same number, restarts included (HUB-401). 0 means
	// the content is not (yet) known to be archived: built from the config directory, or
	// booted from the local cache, until the node has checked the archive.
	Version uint64
	// Checksum is "sha256:<hex>" over every config file and every capability schema used.
	// Equal content gives an equal checksum, on every node.
	Checksum string
	BuiltAt  time.Time

	Capabilities map[string]*Capability
	Vendors      map[string]*Vendor
	Accounts     map[string]*Account
	Transformers map[TransformerRef]*TransformerFile
	Integrations map[string]*Integration
	Tenants      map[string]*Tenant
	Registrar    map[string]*RegistrarEndpoint
	// Programs are the compiled transformers, one per ID@version, shared by every integration
	// version that references them. The hot path only runs them (DESIGN §7.1).
	Programs map[TransformerRef]*transform.Program

	keys map[string]KeyEntry // API key hash → tenant + key (edge auth, M3)
}

// KeyEntry is the tenant and key record for one API key hash.
type KeyEntry struct {
	Tenant *Tenant
	Key    APIKey
}

// KeyByHash finds the tenant key whose hash is "sha256:<hex>".
func (s *Snapshot) KeyByHash(hash string) (KeyEntry, bool) {
	k, ok := s.keys[hash]
	return k, ok
}

// ReindexKeys rebuilds the API-key index of s from s.Tenants (tests and admin-side copies). It
// returns s for chaining.
func ReindexKeys(s *Snapshot) *Snapshot {
	s.keys = map[string]KeyEntry{}
	for _, t := range s.Tenants {
		for _, k := range t.APIKeys {
			s.keys[k.Hash] = KeyEntry{Tenant: t, Key: k}
		}
	}
	return s
}

// Active returns the active version of the integration.
func (in *Integration) Active() *IntegrationVersion { return in.Versions[in.ActiveVersion] }

// Build loads, validates and indexes src into a new, unpublished Snapshot.
func Build(src Sources, now time.Time) (*Snapshot, error) {
	c, err := Load(src)
	if err != nil {
		return nil, err
	}
	if err := Validate(c); err != nil {
		return nil, err
	}
	programs, err := c.compileAll(src.Config)
	if err != nil {
		return nil, err
	}
	sum, err := checksum(src.Config, c)
	if err != nil {
		return nil, err
	}
	s := &Snapshot{
		Checksum: sum, BuiltAt: now.UTC(),
		Capabilities: c.Capabilities, Vendors: c.Vendors, Accounts: c.Accounts, Transformers: c.Transformers,
		Integrations: c.Integrations, Tenants: c.Tenants, Registrar: c.Registrar, Programs: programs,
		keys: map[string]KeyEntry{},
	}
	for _, t := range c.Tenants {
		for _, k := range t.APIKeys {
			if prev, dup := s.keys[k.Hash]; dup {
				return nil, ConfigErrors{{File: c.file("tenant", t.Tenant), Path: "api_keys", Message: fmt.Sprintf("key %s has the same hash as key %s of tenant %q", k.KeyID, prev.Key.KeyID, prev.Tenant.Tenant)}}
			}
			s.keys[k.Hash] = KeyEntry{Tenant: t, Key: k}
		}
	}
	return s, nil
}

// checksum hashes every file under the config FS (sorted by path) and every capability schema.
func checksum(config fs.FS, c *Config) (string, error) {
	h := sha256.New()
	var paths []string
	err := fs.WalkDir(config, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("checksum: %w", err)
	}
	slices.Sort(paths)
	for _, p := range paths {
		b, err := fs.ReadFile(config, p)
		if err != nil {
			return "", fmt.Errorf("checksum %s: %w", p, err)
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	for _, key := range sortedKeys(c.Capabilities) {
		capability := c.Capabilities[key]
		for _, name := range sortedKeys(capability.Operations) {
			op := capability.Operations[name]
			h.Write([]byte(key + "/" + name))
			h.Write(op.RequestSchema)
			h.Write(op.ResponseSchema)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Store holds the current snapshot behind an atomic pointer. Readers call Snapshot (lock-free)
// and keep the pointer for the whole request; writers call Swap or Label.
type Store struct {
	cur atomic.Pointer[Snapshot]
	mu  sync.Mutex // serialises writers
}

// Snapshot returns the current snapshot, or nil before the first Swap.
func (s *Store) Snapshot() *Snapshot { return s.cur.Load() }

// Swap publishes a copy of snap, archive version included, and returns it. snap itself is
// not modified (its maps are shared, read-only, with the published copy). The version is never
// counted locally: a per-process counter made a restarted node look behind its peers forever
// (HUB-401).
func (s *Store) Swap(snap *Snapshot) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	pub := *snap
	s.cur.Store(&pub)
	return &pub
}

// SwapVersion publishes snap as archive version v (Swap with Version set).
func (s *Store) SwapVersion(snap *Snapshot, v uint64) *Snapshot {
	cp := *snap
	cp.Version = v
	return s.Swap(&cp)
}

// Label records that the content being served was archived as version v: when the current
// snapshot has this checksum and another version, a relabelled copy replaces it (requests in
// flight keep the one they loaded). It returns the published snapshot and whether it changed.
// A node that built the archived content from its config directory, or that booted from its
// local cache, so reports the same version as its peers.
func (s *Store) Label(checksum string, v uint64) (*Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.cur.Load()
	if cur == nil || cur.Checksum != checksum || cur.Version == v {
		return cur, false
	}
	pub := *cur
	pub.Version = v
	s.cur.Store(&pub)
	return &pub, true
}
