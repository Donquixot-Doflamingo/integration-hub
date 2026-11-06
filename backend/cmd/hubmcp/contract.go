package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// toolContractJSON is a byte-for-byte copy of contracts/mcp/hubmcp-tools.json, the frozen tool
// contract (go:embed cannot reach outside the module). TestEmbeddedContractIsTheFrozenOne fails
// whenever the two differ; refresh the copy with `go generate ./cmd/hubmcp`.
//
//go:generate cp ../../../contracts/mcp/hubmcp-tools.json hubmcp-tools.json
//go:embed hubmcp-tools.json
var toolContractJSON []byte

// toolContract is the part of hubmcp-tools.json the server needs at run time.
type toolContract struct {
	Contract string `json:"contract"`
	Version  string `json:"version"`
	Server   struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"server"`
	Tools []toolSpec `json:"tools"`
}

// toolSpec is one tool of the contract. The schemas are kept as raw JSON so the server advertises
// exactly the frozen documents.
type toolSpec struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Audience     string          `json:"audience"` // tenant or admin
	Description  string          `json:"description"`
	MapsTo       []mapsTo        `json:"maps_to"`
	Annotations  annotations     `json:"annotations"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`

	input *jsonschema.Schema // compiled InputSchema
}

// mapsTo names one Hub operation a tool may call.
type mapsTo struct {
	Spec        string `json:"spec"`
	OperationID string `json:"operationId"`
}

// annotations are the MCP tool hints of the contract.
type annotations struct {
	ReadOnlyHint    bool `json:"readOnlyHint"`
	DestructiveHint bool `json:"destructiveHint"`
	IdempotentHint  bool `json:"idempotentHint"`
	OpenWorldHint   bool `json:"openWorldHint"`
}

// Audiences of a tool.
const (
	audienceTenant = "tenant"
	audienceAdmin  = "admin"
)

// loadContract parses the tool contract and compiles every input schema.
func loadContract(raw []byte) (*toolContract, error) {
	var c toolContract
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse tool contract: %w", err)
	}
	if c.Contract != "hubmcp-tools" || len(c.Tools) == 0 {
		return nil, fmt.Errorf("tool contract: want contract hubmcp-tools with tools, got %q with %d tools", c.Contract, len(c.Tools))
	}
	seen := map[string]bool{}
	for i := range c.Tools {
		t := &c.Tools[i]
		if seen[t.Name] {
			return nil, fmt.Errorf("tool contract: duplicate tool %q", t.Name)
		}
		seen[t.Name] = true
		if t.Audience != audienceTenant && t.Audience != audienceAdmin {
			return nil, fmt.Errorf("tool %s: unknown audience %q", t.Name, t.Audience)
		}
		s, err := compileSchema(t.Name+"/input", t.InputSchema)
		if err != nil {
			return nil, err
		}
		t.input = s
	}
	return &c, nil
}

// compileSchema compiles one JSON Schema 2020-12 document. Formats (date-time) are asserted so a
// bad timestamp fails locally instead of at the Hub.
func compileSchema(name string, raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "hubmcp:///" + name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	return s, nil
}

// validateArgs checks tool arguments against the tool's input schema. Missing arguments are an
// empty object (MCP clients may omit them for tools without required fields).
func (t *toolSpec) validateArgs(args json.RawMessage) error {
	if len(bytes.TrimSpace(args)) == 0 || bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
		args = json.RawMessage("{}")
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	return t.input.Validate(v)
}
