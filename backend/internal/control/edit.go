package control

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Tree is the editable config tree of one Edit: file paths (vendors/clevertap.yaml, ...) to
// bytes. Edits are validated as a whole by the full snapshot build before anything is archived.
type Tree struct{ t *tree }

// Get returns a file's bytes.
func (t Tree) Get(path string) ([]byte, bool) {
	b, ok := t.t.files[path]
	return b, ok
}

// Put creates or replaces a file.
func (t Tree) Put(path string, b []byte) { t.t.files[path] = b }

// Delete removes a file (objects the contract allows deleting, e.g. registrar endpoints).
func (t Tree) Delete(path string) { delete(t.t.files, path) }

// Paths lists the files under dir ("" for all), sorted.
func (t Tree) Paths(dir string) []string {
	var out []string
	for p := range t.t.files {
		if dir == "" || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// SetScalar sets a top-level key of a YAML mapping file, keeping comments and order.
func (t Tree) SetScalar(path, key, value string) error {
	b, ok := t.t.files[path]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	out, err := setScalar(b, key, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	t.t.files[path] = out
	return nil
}

// SetString is SetScalar for free text (always a quoted YAML string).
func (t Tree) SetString(path, key, value string) error {
	b, ok := t.t.files[path]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	out, err := setString(b, key, value)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	t.t.files[path] = out
	return nil
}

// Edit applies one change to the config tree under the publish lock: fn edits the tree of the
// current snapshot (snap) and returns the change-log row; the result is built and validated in
// full, archived, logged and announced, exactly like a publish. An error from fn aborts the
// edit with nothing written.
func (p *Publisher) Edit(ctx context.Context, fn func(t Tree, snap *registry.Snapshot) (Change, error)) (Result, error) {
	return p.locked(ctx, func() (Result, error) {
		t, snap, err := p.load(ctx)
		if err != nil {
			return Result{}, err
		}
		ch, err := fn(Tree{t}, snap)
		if err != nil {
			return Result{}, err
		}
		return p.commit(ctx, t, ch)
	})
}
