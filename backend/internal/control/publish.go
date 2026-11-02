package control

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Publisher is the publish pipeline (DESIGN §6.4, ADR-0010): it edits the config tree of the
// latest archived snapshot, validates the result with the full snapshot build (goldens
// included), archives it with a change-log row and pings the nodes.
type Publisher struct {
	Pool    *pgxpool.Pool
	Archive *Archive
	// Bootstrap is the tree to start from while the archive is empty (the dev config directory).
	Bootstrap registry.Sources
	// Notify tells nodes a new snapshot is archived (Redis PUBLISH hub:snapshot); may be nil.
	Notify func(ctx context.Context, archiveVersion int64)
	// Guard counts what still references an account (pinned requests, open sessions) for the
	// retire guard (DESIGN §6.5); may be nil (counts as zero).
	Guard func(ctx context.Context, snap *registry.Snapshot, account string) (pinned, sessions int, err error)
	Now   func() time.Time
}

// Result is a published change.
type Result struct {
	ArchiveVersion int64 // hub.config_snapshots version nodes will load
	Version        int   // integration version created (publish, rollback)
	ChangeID       string
	Snapshot       *registry.Snapshot
	// Drained lists accounts this publish moved to draining (no active version targets them).
	Drained []string
}

// ErrMissingAuthor and ErrMissingReason: every change is attributable (DESIGN §6.4).
// ErrNotFound and ErrConflict classify the other refusals (the admin API maps them to 404, 409).
var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("conflict")
	ErrMissingAuthor = errors.New("publish: author is required")
	ErrMissingReason = errors.New("publish: reason is required")
)

// RetireBlockedError refuses a retire while requests or sessions still reference the account.
type RetireBlockedError struct {
	Account          string
	Pinned, Sessions int
}

func (e *RetireBlockedError) Error() string {
	return fmt.Sprintf("account %s still has %d pinned request(s) and %d open session(s): retire refused (DESIGN §6.5)", e.Account, e.Pinned, e.Sessions)
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// tree is a mutable copy of a config tree.
type tree struct {
	files     map[string][]byte
	contracts fs.FS
}

func (t *tree) sources() registry.Sources {
	m := fstest.MapFS{}
	for name, b := range t.files {
		m[name] = &fstest.MapFile{Data: b}
	}
	return registry.Sources{Config: m, Contracts: t.contracts}
}

// Current returns the tree nodes should run: the latest archived snapshot's, else Bootstrap.
func (p *Publisher) Current(ctx context.Context) (registry.Sources, *registry.Snapshot, error) {
	snap, src, _, err := p.Archive.Latest(ctx, p.now())
	if errors.Is(err, ErrNoSnapshot) {
		s, err := registry.Build(p.Bootstrap, p.now())
		return p.Bootstrap, s, err
	}
	return src, snap, err
}

func (p *Publisher) load(ctx context.Context) (*tree, *registry.Snapshot, error) {
	src, snap, err := p.Current(ctx)
	if err != nil {
		return nil, nil, err
	}
	t := &tree{files: map[string][]byte{}, contracts: src.Contracts}
	err = fs.WalkDir(src.Config, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(src.Config, name)
		t.files[name] = b
		return err
	})
	return t, snap, err
}

// commit validates the edited tree (full snapshot build), applies the account lifecycle,
// archives it with a change-log row and notifies nodes.
func (p *Publisher) commit(ctx context.Context, t *tree, ch Change) (Result, error) {
	if strings.TrimSpace(ch.Actor) == "" {
		return Result{}, ErrMissingAuthor
	}
	if strings.TrimSpace(ch.Reason) == "" {
		return Result{}, ErrMissingReason
	}
	snap, err := registry.Build(t.sources(), p.now())
	if err != nil {
		return Result{}, err
	}
	drained, err := t.drainUnreferenced(snap)
	if err != nil {
		return Result{}, err
	}
	if len(drained) > 0 {
		if snap, err = registry.Build(t.sources(), p.now()); err != nil {
			return Result{}, err
		}
	}
	v, _, err := p.Archive.Publish(ctx, t.sources(), snap, ch.Actor, ch.Reason)
	if err != nil {
		return Result{}, err
	}
	ch.SnapshotVersion = v
	id, err := RecordChange(ctx, p.Pool, ch)
	if err != nil {
		return Result{}, err
	}
	if p.Notify != nil {
		p.Notify(ctx, v)
	}
	return Result{ArchiveVersion: v, ChangeID: id, Snapshot: snap, Drained: drained}, nil
}

// publishLock serialises every edit of the config tree across admin replicas: load → commit of
// one change must not interleave with another (both would start from the same version).
const publishLock = 0x6875622d7075626c // "hub-publ"

// locked runs fn while holding the publish advisory lock on a dedicated connection.
func (p *Publisher) locked(ctx context.Context, fn func() (Result, error)) (Result, error) {
	conn, err := p.Pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("publish lock: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(publishLock)); err != nil {
		return Result{}, fmt.Errorf("publish lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(publishLock))
	}()
	return fn()
}

// PublishVersion publishes spec (an integration version document) as the next immutable
// version of integration, optionally making it the active version.
func (p *Publisher) PublishVersion(ctx context.Context, integration string, spec []byte, activate bool, author, reason string) (Result, error) {
	return p.locked(ctx, func() (Result, error) { return p.publishVersion(ctx, integration, spec, activate, author, reason) })
}

func (p *Publisher) publishVersion(ctx context.Context, integration string, spec []byte, activate bool, author, reason string) (Result, error) {
	t, snap, err := p.load(ctx)
	if err != nil {
		return Result{}, err
	}
	in, ok := snap.Integrations[integration]
	if !ok {
		return Result{}, fmt.Errorf("%w: integration %q", ErrNotFound, integration)
	}
	n := 1
	for v := range in.Versions {
		n = max(n, v+1)
	}
	doc, err := versionDoc(spec, integration, n, author, reason)
	if err != nil {
		return Result{}, err
	}
	file := path.Join(path.Dir(in.File), "versions", strconv.Itoa(n)+".yaml")
	if _, exists := t.files[file]; exists {
		return Result{}, fmt.Errorf("%w: %s exists: versions are immutable", ErrConflict, file)
	}
	t.files[file] = doc
	before := map[string]any{"active_version": in.ActiveVersion}
	if activate {
		if t.files[in.File], err = setScalar(t.files[in.File], "active_version", strconv.Itoa(n)); err != nil {
			return Result{}, fmt.Errorf("publish: %s: %w", in.File, err)
		}
	}
	res, err := p.commit(ctx, t, Change{Actor: author, Action: "publishIntegrationVersion", ObjectType: "integration", ObjectID: integration,
		Reason: reason, Before: before, After: map[string]any{"version": n, "active": activate}})
	res.Version = n
	return res, err
}

// Rollback publishes a new version with the content of version to and activates it: v9 = v7,
// history stays linear and nothing is deleted (DESIGN §6.4).
func (p *Publisher) Rollback(ctx context.Context, integration string, to int, author, reason string) (Result, error) {
	return p.locked(ctx, func() (Result, error) { return p.rollback(ctx, integration, to, author, reason) })
}

func (p *Publisher) rollback(ctx context.Context, integration string, to int, author, reason string) (Result, error) {
	_, snap, err := p.load(ctx)
	if err != nil {
		return Result{}, err
	}
	in, ok := snap.Integrations[integration]
	if !ok {
		return Result{}, fmt.Errorf("%w: integration %q", ErrNotFound, integration)
	}
	old, ok := in.Versions[to]
	if !ok {
		return Result{}, fmt.Errorf("%w: %s has no version %d", ErrNotFound, integration, to)
	}
	spec, err := yaml.Marshal(old)
	if err != nil {
		return Result{}, err
	}
	res, err := p.publishVersion(ctx, integration, spec, true, author, fmt.Sprintf("%s (rollback to v%d)", reason, to))
	return res, err
}

// RetireAccount moves a draining account to retired once nothing references it: no active
// version targets it, no pinned request and no open vendor session (DESIGN §6.5).
func (p *Publisher) RetireAccount(ctx context.Context, account, author, reason string) (Result, error) {
	return p.locked(ctx, func() (Result, error) { return p.retireAccount(ctx, account, author, reason) })
}

func (p *Publisher) retireAccount(ctx context.Context, account, author, reason string) (Result, error) {
	t, snap, err := p.load(ctx)
	if err != nil {
		return Result{}, err
	}
	a, ok := snap.Accounts[account]
	if !ok {
		return Result{}, fmt.Errorf("%w: account %q", ErrNotFound, account)
	}
	if referenced(snap)[account] {
		return Result{}, fmt.Errorf("%w: account %s is targeted by an active integration version", ErrConflict, account)
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
	file, err := t.accountFile(account)
	if err != nil {
		return Result{}, err
	}
	if t.files[file], err = setScalar(t.files[file], "state", string(registry.AccountRetired)); err != nil {
		return Result{}, err
	}
	return p.commit(ctx, t, Change{Actor: author, Action: "retireAccount", ObjectType: "account", ObjectID: account, Reason: reason,
		Before: map[string]any{"state": string(a.State)}, After: map[string]any{"state": string(registry.AccountRetired)}})
}

// SetPaused pauses or resumes an integration (the kill switch, DESIGN §10.4). Pause is not a
// version: it edits integration.yaml and propagates with the snapshot. Pausing a paused
// integration is a no-op that still records a change (the contract asks for the log).
func (p *Publisher) SetPaused(ctx context.Context, integration string, paused bool, author, reason string) (Result, error) {
	return p.locked(ctx, func() (Result, error) {
		t, snap, err := p.load(ctx)
		if err != nil {
			return Result{}, err
		}
		in, ok := snap.Integrations[integration]
		if !ok {
			return Result{}, fmt.Errorf("%w: integration %q", ErrNotFound, integration)
		}
		pausedReason := ""
		if paused {
			pausedReason = reason
		}
		if t.files[in.File], err = setScalar(t.files[in.File], "paused", strconv.FormatBool(paused)); err != nil {
			return Result{}, err
		}
		if t.files[in.File], err = setString(t.files[in.File], "paused_reason", pausedReason); err != nil {
			return Result{}, err
		}
		action := "resumeIntegration"
		if paused {
			action = "pauseIntegration"
		}
		return p.commit(ctx, t, Change{Actor: author, Action: action, ObjectType: "integration", ObjectID: integration, Reason: reason,
			Before: map[string]any{"paused": in.Paused, "paused_reason": in.PausedReason}, After: map[string]any{"paused": paused, "paused_reason": pausedReason}})
	})
}

// referenced lists the accounts targeted (targets, failover) by an active integration version.
func referenced(snap *registry.Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, in := range snap.Integrations {
		v := in.Active()
		if v == nil {
			continue
		}
		for _, t := range append(slices.Clone(v.Targets), v.Failover...) {
			out[t.Account] = true
		}
	}
	return out
}

// drainUnreferenced moves active accounts no active version targets to draining, and draining
// accounts that are targeted again back to active (DESIGN §6.5).
func (t *tree) drainUnreferenced(snap *registry.Snapshot) ([]string, error) {
	refs := referenced(snap)
	var drained []string
	for _, id := range slices.Sorted(maps.Keys(snap.Accounts)) {
		a := snap.Accounts[id]
		next := a.State
		switch {
		case a.State == registry.AccountActive && !refs[id]:
			next = registry.AccountDraining
			drained = append(drained, id)
		case a.State == registry.AccountDraining && refs[id]:
			next = registry.AccountActive
		}
		if next == a.State {
			continue
		}
		file, err := t.accountFile(id)
		if err != nil {
			return nil, err
		}
		if t.files[file], err = setScalar(t.files[file], "state", string(next)); err != nil {
			return nil, err
		}
	}
	return drained, nil
}

func (t *tree) accountFile(account string) (string, error) {
	for name, b := range t.files {
		if !strings.HasPrefix(name, "accounts/") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		var head struct {
			Account string `yaml:"account"`
		}
		if yaml.Unmarshal(b, &head) == nil && head.Account == account {
			return name, nil
		}
	}
	return "", fmt.Errorf("no file defines account %q", account)
}

// versionDoc normalises a version document: integration, version and publish metadata are set
// by the pipeline, never trusted from the input.
func versionDoc(spec []byte, integration string, n int, author, reason string) ([]byte, error) {
	var m map[string]any
	if err := yaml.Unmarshal(spec, &m); err != nil {
		return nil, fmt.Errorf("publish: version document: %w", err)
	}
	if m == nil {
		return nil, errors.New("publish: empty version document")
	}
	for _, k := range []string{"owner_tenant", "grants", "capability"} { // header copies of integration.yaml
		delete(m, k)
	}
	m["integration"], m["version"] = integration, n
	m["publish"] = map[string]any{"author": author, "reason": reason}
	out, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Published by the Hub publish pipeline (DESIGN §6.4). Immutable: publish a new version instead.\n"), out...), nil
}

// setScalar sets a top-level scalar key of a YAML mapping, keeping comments and layout.
func setScalar(doc []byte, key, value string) ([]byte, error) {
	return setNode(doc, key, value, "", 0)
}

// setString is setScalar for free text: always a quoted YAML string ("true" stays text).
func setString(doc []byte, key, value string) ([]byte, error) {
	return setNode(doc, key, value, "!!str", yaml.DoubleQuotedStyle)
}

func setNode(doc []byte, key, value, tag string, style yaml.Style) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping")
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Value, m.Content[i+1].Tag, m.Content[i+1].Style = value, tag, style
			return yaml.Marshal(&root)
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag, Style: style})
	return yaml.Marshal(&root)
}
