package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

// Schemas (ADR-0023): an integration version declares, per operation, the caller's request
// schema (body + optional headers); a vendor endpoint declares the request it accepts (path,
// query, headers, body). Both are JSON Schema 2020-12 documents written inline in the YAML
// file. Each is compiled once when the snapshot is built: the hot path only uses the compiled
// pointers.

// JSONSchema is one inline JSON Schema document plus its compiled form.
type JSONSchema struct {
	Doc      map[string]any
	raw      json.RawMessage
	compiled *jsonschema.Schema
}

// UnmarshalYAML reads the schema as a YAML (or JSON) mapping.
func (s *JSONSchema) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return errors.New("a JSON Schema must be a mapping")
	}
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		return err
	}
	if m == nil {
		m = map[string]any{}
	}
	s.Doc = m
	return nil
}

// MarshalYAML writes the document back.
func (s JSONSchema) MarshalYAML() (any, error) {
	if s.Doc == nil {
		return map[string]any{}, nil
	}
	return s.Doc, nil
}

// NewJSONSchema returns an uncompiled schema for doc (compiled when a snapshot is built).
func NewJSONSchema(doc map[string]any) *JSONSchema { return &JSONSchema{Doc: doc} }

// JSON is the document as JSON (after Compile).
func (s *JSONSchema) JSON() json.RawMessage {
	if s == nil {
		return nil
	}
	if s.raw == nil {
		s.raw, _ = json.Marshal(s.Doc)
	}
	return s.raw
}

// Compiled is the compiled schema, nil before Compile.
func (s *JSONSchema) Compiled() *jsonschema.Schema {
	if s == nil {
		return nil
	}
	return s.compiled
}

// Compile compiles the document (2020-12, formats asserted). Each schema gets its own compiler,
// so two documents with the same $id (copied between versions) never clash.
func (s *JSONSchema) Compile(name string) error {
	raw, err := json.Marshal(s.Doc)
	if err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	url := "registry:///" + strings.NewReplacer("#", "/", " ", "_", "%", "_").Replace(strings.TrimPrefix(name, "/"))
	if err := c.AddResource(url, doc); err != nil {
		return err
	}
	sch, err := c.Compile(url)
	if err != nil {
		return errors.New(firstLine(err.Error()))
	}
	s.raw, s.compiled = raw, sch
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Operation is one operation of an integration version: what callers invoke and send.
type Operation struct {
	Description string `yaml:"description,omitempty"`
	Idempotent  bool   `yaml:"idempotent"`
	// ValidateInput: check live calls against RequestSchema (default true, VALIDATION_FAILED).
	ValidateInput *bool         `yaml:"validate_input,omitempty"`
	RequestSchema RequestSchema `yaml:"request_schema"`

	// Filled when the version is loaded.
	Name string `yaml:"-"`
	// PII lists the x-pii tagged locations of the body (JSON pointer; "*" = any array item).
	PII []PIIPath `yaml:"-"`
	// headers are the declared caller header names (as written), the hdr.* allowlist.
	headers []string
}

// RequestSchema is the caller's input of one operation.
type RequestSchema struct {
	Body    *JSONSchema `yaml:"body"`
	Headers *JSONSchema `yaml:"headers,omitempty"`
}

// Validates reports whether live calls are checked against the request schema.
func (o *Operation) Validates() bool { return o.ValidateInput == nil || *o.ValidateInput }

// HeaderNames are the caller headers declared in request_schema.headers, as written. They are
// the only caller headers a request transformer can read (hdr.*).
func (o *Operation) HeaderNames() []string { return o.headers }

// PIIPath is one x-pii tagged location.
type PIIPath struct {
	Pointer string
	Kind    string
}

// hubHeaders are the Hub's own request headers: an operation can never declare them.
var hubHeaders = []string{"authorization", "idempotency-key", "prefer", "x-request-deadline", "x-hub-session", "traceparent"}

// HdrIdempotencyKey is the one hdr.* entry the Hub always provides: the caller's Idempotency-Key
// (vendors with their own idempotency support read it). It is not a declared caller header.
const HdrIdempotencyKey = "idempotency_key"

// CompileOperation compiles an operation built in code (tests, admin API checks) the way the
// loader does: ConfigErrors when a schema does not compile or declares a Hub header.
func CompileOperation(op *Operation, name string) error {
	var ps ConfigErrors
	compileOperation(op, name, "", "operations."+name, &ps)
	if len(ps) > 0 {
		return ps
	}
	return nil
}

// compileOperation compiles the schemas of op and fills its derived fields. at is the YAML path
// of the operation for problems.
func compileOperation(op *Operation, name, file, at string, ps *ConfigErrors) {
	op.Name = name
	if !opNamePattern.MatchString(name) {
		ps.addCompile(file, at, "operation names must match %s", opNamePattern)
	}
	if op.RequestSchema.Body == nil {
		ps.addCompile(file, at+".request_schema.body", "required (use {type: object} when a call has no fields)")
	} else if err := op.RequestSchema.Body.Compile(file + "/" + at + ".body"); err != nil {
		ps.addCompile(file, at+".request_schema.body", "does not compile: %v", err)
	} else {
		op.PII = piiPaths(op.RequestSchema.Body.Doc, "", nil)
	}
	if h := op.RequestSchema.Headers; h != nil {
		if err := h.Compile(file + "/" + at + ".headers"); err != nil {
			ps.addCompile(file, at+".request_schema.headers", "does not compile: %v", err)
		}
		props, _ := h.Doc["properties"].(map[string]any)
		for _, k := range slices.Sorted(mapKeys(props)) {
			if slices.Contains(hubHeaders, strings.ToLower(k)) {
				ps.addCompile(file, at+".request_schema.headers.properties."+k, "%s is a Hub header and cannot be declared", k)
				continue
			}
			op.headers = append(op.headers, k)
		}
	}
}

// compileEndpoint compiles the request schema parts of an endpoint version.
func compileEndpoint(ep *Endpoint, file, at string, ps *ConfigErrors) {
	if ep.RequestSchema == nil {
		ps.addCompile(file, at+".request_schema", "required ({} when the endpoint takes nothing besides method and path)")
		return
	}
	for _, part := range ep.RequestSchema.parts() {
		if part.s == nil {
			continue
		}
		if err := part.s.Compile(file + "/" + at + "." + part.name); err != nil {
			ps.addCompile(file, at+".request_schema."+part.name, "does not compile: %v", err)
		}
	}
}

type schemaPart struct {
	name string
	s    *JSONSchema
}

func (e *EndpointRequestSchema) parts() []schemaPart {
	return []schemaPart{{"path", e.Path}, {"query", e.Query}, {"headers", e.Headers}, {"body", e.Body}}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// piiPaths walks properties/items and collects x-pii tags as instance JSON pointers.
// A tag on an object covers its subtree, so the walk stops there.
func piiPaths(node any, at string, out []PIIPath) []PIIPath {
	m, ok := node.(map[string]any)
	if !ok {
		return out
	}
	if kind, ok := m["x-pii"].(string); ok {
		ptr := at
		if ptr == "" {
			ptr = "/"
		}
		return append(out, PIIPath{Pointer: ptr, Kind: kind})
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for _, k := range slices.Sorted(mapKeys(props)) {
			out = piiPaths(props[k], at+"/"+EscapePointer(k), out)
		}
	}
	if items, ok := m["items"]; ok {
		out = piiPaths(items, at+"/*", out)
	}
	return out
}

// EscapePointer escapes one JSON pointer reference token.
func EscapePointer(s string) string { return strings.NewReplacer("~", "~0", "/", "~1").Replace(s) }

// Violation is one JSON Schema violation, with the JSON pointer of the field in the checked
// object (admin contract SchemaViolation).
type Violation struct {
	Field   string `json:"field"`
	Keyword string `json:"keyword,omitempty"`
	Message string `json:"message"`
}

// MaxViolations bounds the violations reported for one value (contract details maxItems).
const MaxViolations = 100

// Violations validates v against sch and lists every leaf violation, its pointer prefixed with
// prefix (e.g. "/query"). nil means v fits (or sch is nil).
func Violations(sch *jsonschema.Schema, v any, prefix string) []Violation {
	if sch == nil {
		return nil
	}
	err := sch.Validate(v)
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Violation{{Field: prefix, Message: err.Error()}}
	}
	var out []Violation
	seen := map[string]bool{}
	for _, u := range ve.BasicOutput().Errors {
		if u.Error == nil {
			continue
		}
		msg := u.Error.String()
		if strings.HasPrefix(msg, "validation failed") || strings.HasPrefix(msg, "allOf failed") {
			continue // group wrappers: the leaves carry the detail
		}
		field := prefix + u.InstanceLocation
		if kw := path.Base(u.KeywordLocation); kw == "required" {
			// "missing property 'name'": point at the missing field itself.
			if name, ok := missingProperty(msg); ok {
				field += "/" + EscapePointer(name)
			}
		}
		key := field + "|" + msg
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Violation{Field: field, Keyword: path.Base(u.KeywordLocation), Message: msg})
		if len(out) == MaxViolations {
			break
		}
	}
	if len(out) == 0 {
		out = []Violation{{Field: prefix, Message: firstLine(err.Error())}}
	}
	return out
}

// missingProperty extracts the single name of "missing property 'x'".
func missingProperty(msg string) (string, bool) {
	rest, ok := strings.CutPrefix(msg, "missing property '")
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, "'")
	return name, ok && !strings.Contains(name, "'")
}

// ToJSONValue converts v (YAML- or Go-typed) into the JSON value model the validator expects
// (json.Number-free float64/string/bool/nil, map[string]any, []any).
func ToJSONValue(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(b))
}

// CheckFieldPath reports whether segs is a declared path under the JSON Schema doc: through
// properties, items, additionalProperties schemas and local $refs. An object schema that declares
// properties accepts only those (unless additionalProperties is true or a schema); an object
// without properties (free-form) accepts anything below it.
func CheckFieldPath(doc map[string]any, segs []string) error {
	node := doc
	for i, seg := range segs {
		node = resolveRef(doc, node)
		if props, ok := node["properties"].(map[string]any); ok {
			if next, ok := props[seg].(map[string]any); ok {
				node = next
				continue
			}
		}
		if items, ok := node["items"].(map[string]any); ok {
			node = items // a.b on an array reads each item's b (map/filter)
			if props, ok := resolveRef(doc, node)["properties"].(map[string]any); ok {
				if next, ok := props[seg].(map[string]any); ok {
					node = next
					continue
				}
			}
		}
		switch ap := node["additionalProperties"].(type) {
		case bool:
			if ap {
				return nil
			}
			return fmt.Errorf("%q is not declared", strings.Join(segs[:i+1], "."))
		case map[string]any:
			node = ap
			continue
		}
		if _, hasProps := node["properties"]; hasProps {
			return fmt.Errorf("%q is not declared", strings.Join(segs[:i+1], "."))
		}
		return nil // free-form: anything deeper is allowed
	}
	return nil
}

func resolveRef(root, node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/") {
		return node
	}
	cur := any(root)
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return node
		}
		cur = m[part]
	}
	if m, ok := cur.(map[string]any); ok {
		return m
	}
	return node
}
