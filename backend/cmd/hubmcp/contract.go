package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"

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

	// Hard exclusions of the admin tools (contracts/mcp/README.md "Admin write tools").
	ForbiddenOperations              []classifiedOp  `json:"forbidden_operations"`
	NotExposedOperations             []classifiedOp  `json:"not_exposed_operations"`
	ForbiddenInputProperties         []string        `json:"forbidden_input_properties"`
	ForbiddenMutationInputProperties []string        `json:"forbidden_mutation_input_properties"`
	SecretPatterns                   []secretPattern `json:"secret_patterns"`

	secretRes []*regexp.Regexp // compiled SecretPatterns
}

// classifiedOp is one hub-admin operation of forbidden_operations or not_exposed_operations.
type classifiedOp struct {
	Spec        string `json:"spec"`
	OperationID string `json:"operationId"`
	HTTPMethod  string `json:"http_method"`
	Reason      string `json:"reason"`
}

// secretPattern is a regular expression for a Hub secret no tool argument may contain.
type secretPattern struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
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
	// Admin tools only: requirements, the confirm guard and write-once secrets in the output.
	Requires        requires `json:"requires"`
	RequiresConfirm string   `json:"requires_confirm"` // "", live_traffic or mints_secret
	SensitiveOutput []string `json:"sensitive_output"` // JSON pointers to top-level output fields

	input      *jsonschema.Schema // compiled InputSchema
	outputETag bool               // the output carries etag (from the ETag response header)
}

// mapsTo names one Hub operation a tool may call, with its HTTP method.
type mapsTo struct {
	Spec        string `json:"spec"`
	OperationID string `json:"operationId"`
	HTTPMethod  string `json:"http_method"`
}

// requires is what an admin tool needs: the env var, the Hub role (informative: the Hub's RBAC
// decides) and, for write tools, the transports it may be registered on.
type requires struct {
	Env          string   `json:"env"`
	RequiredRole string   `json:"x-required-role"`
	Transport    []string `json:"transport"`
}

// mutating reports whether the tool changes Hub state (readOnlyHint false). hub_invoke is the
// tenant exception: it has effects at vendors but is not a control-plane mutation.
func (t *toolSpec) mutating() bool { return t.Audience == audienceAdmin && !t.Annotations.ReadOnlyHint }

// allowedOn reports whether the tool may be registered on transport (stdio or http). A tool
// without requires.transport is allowed on every transport the server offers.
func (t *toolSpec) allowedOn(transport string) bool {
	if len(t.Requires.Transport) == 0 {
		return true
	}
	return slices.Contains(t.Requires.Transport, contractTransport(transport))
}

// contractTransport names a -transport value as the contract does.
func contractTransport(transport string) string {
	if transport == transportHTTP {
		return "streamable-http"
	}
	return transport
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

// adminSpec is the file name of the control-plane contract in maps_to and the exclusion lists.
const adminSpec = "hub-admin.v1.yaml"

// loadContract parses the tool contract, compiles every input schema and the secret patterns,
// and refuses a contract in which any tool maps to an HTTP DELETE or a forbidden operation, or a
// mutating tool lacks a required reason (fail closed: such a server never starts).
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
		if err := c.checkSafety(t); err != nil {
			return nil, err
		}
		var out struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(t.OutputSchema, &out); err != nil {
			return nil, fmt.Errorf("tool %s: outputSchema: %w", t.Name, err)
		}
		_, t.outputETag = out.Properties["etag"]
	}
	if len(c.SecretPatterns) == 0 {
		return nil, errors.New("tool contract: no secret_patterns")
	}
	for _, sp := range c.SecretPatterns {
		re, err := regexp.Compile(sp.Pattern)
		if err != nil {
			return nil, fmt.Errorf("tool contract: secret pattern %s: %w", sp.Name, err)
		}
		c.secretRes = append(c.secretRes, re)
	}
	return &c, nil
}

// checkSafety enforces at load time the admin safety rules the server depends on (check.mjs §7b
// proves them on the contract; this keeps a bad embedded copy from ever being served).
func (c *toolContract) checkSafety(t *toolSpec) error {
	forbidden := map[string]bool{}
	for _, f := range c.ForbiddenOperations {
		forbidden[f.OperationID] = true
	}
	for _, m := range t.MapsTo {
		if !allowedMethod(m.HTTPMethod) {
			return fmt.Errorf("tool %s maps to %s with HTTP method %q: hubmcp sends only GET, POST, PUT and PATCH", t.Name, m.OperationID, m.HTTPMethod)
		}
		if m.Spec == adminSpec && forbidden[m.OperationID] {
			return fmt.Errorf("tool %s maps to forbidden operation %s", t.Name, m.OperationID)
		}
	}
	if t.mutating() {
		var in struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(t.InputSchema, &in); err != nil || !slices.Contains(in.Required, "reason") {
			return fmt.Errorf("mutating tool %s must require reason", t.Name)
		}
		if !slices.Equal(t.Requires.Transport, []string{transportStdio}) {
			return fmt.Errorf("mutating tool %s must be stdio-only (requires.transport)", t.Name)
		}
	}
	for _, ptr := range t.SensitiveOutput {
		if len(ptr) < 2 || ptr[0] != '/' || bytes.ContainsRune([]byte(ptr[1:]), '/') {
			return fmt.Errorf("tool %s: sensitive_output %q must point at a top-level field", t.Name, ptr)
		}
	}
	return nil
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
