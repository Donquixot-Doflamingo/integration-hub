package registry

import (
	"fmt"
	"io/fs"
	"path"

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
		Request: t.Request, Response: t.Response, Errors: t.Errors, Batch: t.Batch, AllowCredsInBody: t.AllowCredsInBody,
	}
	return transform.Compile(spec, transform.Options{RequestSchema: op.RequestSchema, ValidateResponse: op.Response.Validate})
}

// TransformerFixtures returns the fixtures/ directory of a transformer (golden cases).
func TransformerFixtures(config fs.FS, ref TransformerRef) (fs.FS, error) {
	return fs.Sub(config, path.Join("transformers", ref.ID, "fixtures"))
}
