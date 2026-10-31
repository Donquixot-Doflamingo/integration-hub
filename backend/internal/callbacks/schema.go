package callbacks

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// eventSchemaDir is where the event schemas live inside the contracts directory.
const eventSchemaDir = "schemas/events"

// SchemaValidator checks callback bodies against contracts/schemas/events/<type>.json. Tests
// use it on every payload shape; a role may pass it to the Enqueuer to refuse a body that
// breaks the contract (the same contracts directory the registry reads, HUB_CONTRACTS_DIR).
type SchemaValidator struct {
	schemas map[EventType]*jsonschema.Schema
}

// NewSchemaValidator compiles the event schemas from contracts (the repository's contracts/
// directory as an fs.FS).
func NewSchemaValidator(contracts fs.FS) (*SchemaValidator, error) {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	entries, err := fs.ReadDir(contracts, eventSchemaDir)
	if err != nil {
		return nil, fmt.Errorf("callbacks: event schemas: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".json" {
			continue
		}
		name := path.Join(eventSchemaDir, e.Name())
		raw, err := fs.ReadFile(contracts, name)
		if err != nil {
			return nil, fmt.Errorf("callbacks: %s: %w", name, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("callbacks: %s: %w", name, err)
		}
		if err := c.AddResource("contracts:///"+name, doc); err != nil {
			return nil, fmt.Errorf("callbacks: %s: %w", name, err)
		}
	}
	v := &SchemaValidator{schemas: map[EventType]*jsonschema.Schema{}}
	for _, t := range EventTypes {
		s, err := c.Compile("contracts:///" + path.Join(eventSchemaDir, string(t)+".json"))
		if err != nil {
			return nil, fmt.Errorf("callbacks: compile %s schema: %w", t, err)
		}
		v.schemas[t] = s
	}
	return v, nil
}

// Validate checks body against the schema of typ.
func (v *SchemaValidator) Validate(typ EventType, body []byte) error {
	s, ok := v.schemas[typ]
	if !ok {
		return fmt.Errorf("callbacks: no schema for event type %q", typ)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("callbacks: %s body is not JSON: %w", typ, err)
	}
	if err := s.Validate(doc); err != nil {
		return fmt.Errorf("callbacks: %s body breaks the contract: %w", typ, err)
	}
	return nil
}
