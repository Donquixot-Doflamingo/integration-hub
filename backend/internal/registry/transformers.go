package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// operationFor returns the capability operation a transformer maps.
func (c *Config) operationFor(t *TransformerFile) (*Operation, error) {
	capability, ok := c.Capabilities[t.Capability]
	if !ok {
		return nil, fmt.Errorf("unknown capability %q", t.Capability)
	}
	if t.Operation != "" {
		op, ok := capability.Operations[t.Operation]
		if !ok {
			return nil, fmt.Errorf("capability %s has no operation %q", t.Capability, t.Operation)
		}
		return op, nil
	}
	if len(capability.Operations) != 1 {
		return nil, fmt.Errorf("capability %s has %d operations: set operation", t.Capability, len(capability.Operations))
	}
	for _, op := range capability.Operations {
		return op, nil
	}
	return nil, nil
}

// CompileTransformer compiles one transformer against its capability operation's request schema
// (req.* paths) and response schema (mapped output).
func (c *Config) CompileTransformer(ref TransformerRef) (*transform.Program, error) {
	t, ok := c.Transformers[ref]
	if !ok {
		return nil, fmt.Errorf("unknown transformer %s", ref)
	}
	op, err := c.operationFor(t)
	if err != nil {
		return nil, fmt.Errorf("transformer %s: %w", ref, err)
	}
	spec := transform.Spec{
		ID: t.Transformer, Version: t.Version, Kind: string(t.Kind), Capability: t.Capability,
		Request: t.Request, Response: t.Response, Errors: t.Errors, Batch: t.Batch, Session: t.Session, AllowCredsInBody: t.AllowCredsInBody,
	}
	return transform.Compile(spec, transform.Options{RequestSchema: op.RequestSchema, ValidateResponse: op.Response.Validate})
}

// TransformerFixtures returns the fixtures/ directory of a transformer (golden cases).
func TransformerFixtures(config fs.FS, ref TransformerRef) (fs.FS, error) {
	return fs.Sub(config, FixtureDir(config, ref))
}

// FixtureDir is the fixture directory of ref: transformers/<ID>/fixtures/v<N> when version N
// has its own cases (its output differs from other versions), else the shared
// transformers/<ID>/fixtures.
func FixtureDir(config fs.FS, ref TransformerRef) string {
	shared := path.Join("transformers", ref.ID, "fixtures")
	own := path.Join(shared, fmt.Sprintf("v%d", ref.Version))
	if st, err := fs.Stat(config, own); err == nil && st.IsDir() {
		return own
	}
	return shared
}

// compileAll compiles every transformer once and runs its goldens (DESIGN §6.4, AGENTS §3.4). A
// type error or failing golden refuses the snapshot, naming the transformer and fixture.
func (c *Config) compileAll(config fs.FS) (map[TransformerRef]*transform.Program, error) {
	programs := make(map[TransformerRef]*transform.Program, len(c.Transformers))
	var ps ConfigErrors
	for _, ref := range sortedRefs(c.Transformers) {
		file := c.file("transformer", ref.String())
		prog, err := c.CompileTransformer(ref)
		if err != nil {
			var ce *transform.CompileError
			if errors.As(err, &ce) {
				for _, p := range ce.Problems {
					ps.add(file, p.Path, "%s", p.Message)
				}
			} else {
				ps.add(file, "", "%v", err)
			}
			continue
		}
		fixtures, err := TransformerFixtures(config, ref)
		if err != nil {
			ps.add(file, "", "fixtures: %v", err)
			continue
		}
		failed := false
		for _, r := range transform.RunGoldens(prog, fixtures) {
			if r.Passed() {
				continue
			}
			failed = true
			at := path.Join(FixtureDir(config, ref), r.Case)
			if r.Case == "" {
				at = FixtureDir(config, ref)
			}
			msg := r.Err
			if len(r.Diffs) > 0 {
				msg = "golden mismatch: " + strings.Join(r.Diffs, "; ")
			}
			ps.add(at, "", "%s: %s", ref, msg)
		}
		if !failed {
			programs[ref] = prog
		}
	}
	if len(ps) > 0 {
		return nil, ps
	}
	return programs, nil
}
