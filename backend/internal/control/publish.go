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

	"github.com/jackc/pgx/v5"
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
	files map[string][]byte
	// base is the snapshot the edit started from (account draining compares against it).
	base *registry.Snapshot
	// baseVersion is its archive version (0: the bootstrap tree, nothing archived yet). The
	// commit refuses to write over a newer archive version (errStaleBase).
	baseVersion int64
}

func (t *tree) sources() registry.Sources {
	m := fstest.MapFS{}
	for name, b := range t.files {
		m[name] = &fstest.MapFile{Data: b}
	}
	return registry.Sources{Config: m}
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
	var baseVersion int64
	snap, src, v, err := p.Archive.Latest(ctx, p.now())
	switch {
	case errors.Is(err, ErrNoSnapshot):
		src = p.Bootstrap
		snap, err = registry.Build(src, p.now())
	case err == nil:
		baseVersion = v
	}
	if err != nil {
		return nil, nil, err
	}
	t := &tree{files: map[string][]byte{}, base: snap, baseVersion: baseVersion}
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

// errStaleBase: another writer archived a version after this edit loaded its tree.
var errStaleBase = errors.New("publish: the archive moved on while the edit was prepared")

// commit validates the edited tree (full snapshot build), applies the account lifecycle,
// archives it with its change-log row in ONE transaction and notifies nodes. When the tree is
// identical to the archived one nothing is archived, and the change row carries no
// snapshot_version (it did not publish that snapshot).
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
	var id string
	v, published, err := p.Archive.PublishWith(ctx, t.sources(), snap, ch.Actor, ch.Reason, PublishOptions{
		Guard: func(latest Meta, exists bool) error {
			if !exists {
				latest.Version = 0
			}
			if latest.Version != t.baseVersion {
				return errStaleBase
			}
			return nil
		},
		InTx: func(ctx context.Context, tx pgx.Tx, version int64, published bool) error {
			if published {
				ch.SnapshotVersion = version
			}
			var err error
			id, err = RecordChange(ctx, tx, ch)
			return err
		},
	})
	if err != nil {
		return Result{}, err
	}
	if published && p.Notify != nil {
		p.Notify(ctx, v)
	}
	return Result{ArchiveVersion: v, ChangeID: id, Snapshot: snap, Drained: drained}, nil
}

// publishSem serialises edits of the config tree inside this process (load → commit of one
// change must not interleave with another: both would start from the same version). It holds no
// database connection while a writer waits: the previous pg_advisory_lock held one pool
// connection per waiter and starved the holder of the connection its own transaction needs
// (HUB-451). Across replicas the archive transaction's advisory lock plus the base-version check
// in commit give the same guarantee: the loser re-runs its edit on the newer tree.
var publishSem = make(chan struct{}, 1)

// maxEditAttempts bounds the re-runs after a cross-replica race.
const maxEditAttempts = 5

// locked runs fn (an edit: load, change, commit) serialised in this process. fn is re-run when
// another replica archived a version in between, so it must be repeatable.
func (p *Publisher) locked(ctx context.Context, fn func() (Result, error)) (Result, error) {
	select {
	case publishSem <- struct{}{}:
	case <-ctx.Done():
		return Result{}, fmt.Errorf("publish lock: %w", ctx.Err())
	}
	defer func() { <-publishSem }()
	for attempt := 1; ; attempt++ {
		res, err := fn()
		if !errors.Is(err, errStaleBase) {
			return res, err
		}
		if attempt == maxEditAttempts {
			return Result{}, fmt.Errorf("%w: gave up after %d attempts: %w", ErrConflict, attempt, err)
		}
	}
}

// PublishVersion publishes spec (an integration version document) as the next immutable
// version of integration, optionally making it the active version.
func (p *Publisher) PublishVersion(ctx context.Context, integration string, spec []byte, activate bool, author, reason string) (Result, error) {
	return p.Publish(ctx, VersionRequest{Integration: integration, Spec: spec, Activate: activate, Author: author, Reason: reason})
}

// VersionRequest is one new integration version through the publish pipeline.
type VersionRequest struct {
	Integration string
	// Spec is the version document; Derive, when set, computes it under the publish lock from
	// the current integration (nil for an integration not in the tree yet) instead.
	Spec   []byte
	Derive func(in *registry.Integration) ([]byte, error)
	// Version, when > 0, must be the number the pipeline assigns (the next one).
	Version  int
	Activate bool
	// ExpectActive, when set, is the active version the caller saw (0: none). Another publish
	// in between makes the request fail with *ActiveVersionConflictError (contract "Concurrency").
	ExpectActive *int
	// Header is integration.yaml of an integration that is not in the tree yet: its first
	// version publishes both.
	Header []byte
	// Action names the change-log row (default publishIntegrationVersion); After adds fields to
	// its after document (always {version, active}).
	Action         string
	After          map[string]any
	Author, Reason string
}

// ActiveVersionConflictError: the active version is not the one the caller expected.
type ActiveVersionConflictError struct {
	Integration      string
	Expected, Actual int // 0: no active version
}

func (e *ActiveVersionConflictError) Error() string {
	return fmt.Sprintf("active version of %s is %d, expected %d", e.Integration, e.Actual, e.Expected)
}

// Unwrap classifies the error as a conflict.
func (e *ActiveVersionConflictError) Unwrap() error { return ErrConflict }

// Publish validates and publishes one new version (see VersionRequest).
func (p *Publisher) Publish(ctx context.Context, req VersionRequest) (Result, error) {
	return p.locked(ctx, func() (Result, error) {
		t, snap, err := p.load(ctx)
		if err != nil {
			return Result{}, err
		}
		n, ch, err := t.addVersion(snap, req)
		if err != nil {
			return Result{}, err
		}
		res, err := p.commit(ctx, t, ch)
		res.Version = n
		return res, err
	})
}

// Check runs the publish validation of req (the full snapshot build, goldens included) without
// archiving anything: nil, or the registry.ConfigErrors a publish would fail with.
func (p *Publisher) Check(ctx context.Context, req VersionRequest) error {
	t, snap, err := p.load(ctx)
	if err != nil {
		return err
	}
	if req.Author == "" {
		req.Author = "validate"
	}
	if req.Reason == "" {
		req.Reason = "validate"
	}
	if _, _, err := t.addVersion(snap, req); err != nil {
		return err
	}
	_, err = registry.Build(t.sources(), p.now())
	return err
}

// addVersion writes the version file of req (and integration.yaml for a new integration) into
// t and returns the version number and the change-log row.
func (t *tree) addVersion(snap *registry.Snapshot, req VersionRequest) (int, Change, error) {
	id := req.Integration
	in, exists := snap.Integrations[id]
	file := path.Join("integrations", id, "integration.yaml")
	active, n := 0, 1
	switch {
	case exists && req.Header != nil:
		return 0, Change{}, fmt.Errorf("%w: integration %q already exists", ErrConflict, id)
	case exists:
		file, active = in.File, in.ActiveVersion
		for v := range in.Versions {
			n = max(n, v+1)
		}
	case req.Header == nil:
		return 0, Change{}, fmt.Errorf("%w: integration %q", ErrNotFound, id)
	default:
		if _, taken := t.files[file]; taken {
			return 0, Change{}, fmt.Errorf("%w: %s exists", ErrConflict, file)
		}
		t.files[file] = req.Header
		in = nil
	}
	if req.ExpectActive != nil && *req.ExpectActive != active {
		return 0, Change{}, &ActiveVersionConflictError{Integration: id, Expected: *req.ExpectActive, Actual: active}
	}
	if req.Version > 0 && req.Version != n {
		return 0, Change{}, fmt.Errorf("%w: %s v%d cannot be published: the next version is v%d", ErrConflict, id, req.Version, n)
	}
	spec := req.Spec
	if req.Derive != nil {
		var err error
		if spec, err = req.Derive(in); err != nil {
			return 0, Change{}, err
		}
	}
	doc, err := versionDoc(spec, id, n, req.Author, req.Reason)
	if err != nil {
		return 0, Change{}, err
	}
	vfile := path.Join(path.Dir(file), "versions", strconv.Itoa(n)+".yaml")
	if _, taken := t.files[vfile]; taken {
		return 0, Change{}, fmt.Errorf("%w: %s exists: versions are immutable", ErrConflict, vfile)
	}
	t.files[vfile] = doc
	if req.Activate {
		if t.files[file], err = setScalar(t.files[file], "active_version", strconv.Itoa(n)); err != nil {
			return 0, Change{}, fmt.Errorf("publish: %s: %w", file, err)
		}
	}
	var before map[string]any
	if exists {
		before = map[string]any{"active_version": active}
	}
	after := map[string]any{}
	maps.Copy(after, req.After)
	after["version"], after["active"] = n, req.Activate
	action := req.Action
	if action == "" {
		action = "publishIntegrationVersion"
	}
	return n, Change{Actor: req.Author, Action: action, ObjectType: "integration", ObjectID: id, Reason: req.Reason,
		Before: before, After: after}, nil
}

// Rollback publishes a new version with the content of version to and activates it: v9 = v7,
// history stays linear and nothing is deleted (DESIGN §6.4).
func (p *Publisher) Rollback(ctx context.Context, integration string, to int, author, reason string) (Result, error) {
	req := VersionRequest{Integration: integration, Activate: true, Action: "rollbackIntegration", Author: author,
		Reason: fmt.Sprintf("%s (rollback to v%d)", reason, to), After: map[string]any{"copied_from_version": to}}
	req.Derive = CopyVersion(integration, to)
	return p.Publish(ctx, req)
}

// CopyVersion is a Derive that copies version n of the integration (rollback, weights).
func CopyVersion(integration string, n int) func(in *registry.Integration) ([]byte, error) {
	return func(in *registry.Integration) ([]byte, error) {
		if in == nil {
			return nil, fmt.Errorf("%w: integration %q", ErrNotFound, integration)
		}
		old, ok := in.Versions[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s has no version %d", ErrNotFound, integration, n)
		}
		return yaml.Marshal(old)
	}
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

// drainUnreferenced moves active accounts that lost their last reference in this edit (targeted
// by an active version of base, by none of snap) to draining, and draining
// accounts that are targeted again back to active (DESIGN §6.5).
func (t *tree) drainUnreferenced(snap *registry.Snapshot) ([]string, error) {
	refs := referenced(snap)
	before := map[string]bool{}
	if t.base != nil {
		before = referenced(t.base)
	}
	var drained []string
	for _, id := range slices.Sorted(maps.Keys(snap.Accounts)) {
		a := snap.Accounts[id]
		next := a.State
		switch {
		case a.State == registry.AccountActive && !refs[id] && before[id]:
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
	for _, k := range []string{"owner_tenant", "grants"} { // header copies of integration.yaml
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
