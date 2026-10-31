package transform

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Spec is one transformer as stored in config (backend/config/transformers/<id>/<version>.yaml).
// The registry fills it from its TransformerFile; this package never reads files.
type Spec struct {
	ID               string
	Version          int
	Kind             string // request | response
	Capability       string
	Request          yaml.Node
	Response         yaml.Node
	Errors           yaml.Node
	Batch            yaml.Node
	AllowCredsInBody bool
}

// Ref is "T1@7".
func (s Spec) Ref() string { return fmt.Sprintf("%s@%d", s.ID, s.Version) }

// Problem is one compile problem at a target path inside the transformer
// (e.g. "request.body.d[0].evtName").
type Problem struct {
	Path    string
	Message string
}

func (p Problem) String() string {
	if p.Path == "" {
		return p.Message
	}
	return p.Path + ": " + p.Message
}

// CompileError lists every problem found while parsing or compiling one transformer.
type CompileError struct {
	Transformer string
	Problems    []Problem
}

func (e *CompileError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return fmt.Sprintf("transformer %s: %s", e.Transformer, strings.Join(lines, "; "))
}

func (e *CompileError) add(path, format string, args ...any) {
	e.Problems = append(e.Problems, Problem{Path: path, Message: fmt.Sprintf(format, args...)})
}

// nodeKind is the shape of one template node.
type nodeKind uint8

const (
	nodeExpr    nodeKind = iota // a string scalar: an expression
	nodeLiteral                 // a number, bool or null scalar: used as is
	nodeObject
	nodeArray
)

// tmpl is one parsed target: an object, an array, an expression or a literal.
type tmpl struct {
	kind    nodeKind
	path    string // target path, for errors
	expr    string
	literal any
	fields  []field // objects, in file order
	items   []*tmpl // arrays
}

type field struct {
	key  string
	node *tmpl
}

// parsed is the syntax tree of a transformer before expressions are compiled.
type parsed struct {
	method, path   string
	headers, query []field // values are expression nodes
	body           *tmpl
	response       *tmpl
	errors         []errorRule
	batchPath      string
	batchMax       int
}

type errorRule struct {
	path         string
	when         string
	code         string
	retryable    bool
	retryAfterMS string // expression, optional
	message      string // expression, optional
}

var requestKeys = []string{"method", "path", "headers", "query", "body"}

// parse turns the YAML sections into templates (HUB-060). Unknown keys, duplicate or
// conflicting targets and wrong shapes are reported with their path.
func parse(s Spec) (*parsed, *CompileError) {
	ce := &CompileError{Transformer: s.Ref()}
	p := &parsed{}
	present := func(n yaml.Node) bool { return n.Kind != 0 }
	switch s.Kind {
	case "request":
		if !present(s.Request) {
			ce.add("request", "required for a request transformer")
		}
		for name, n := range map[string]yaml.Node{"response": s.Response, "errors": s.Errors} {
			if present(n) {
				ce.add(name, "not allowed in a request transformer")
			}
		}
		if present(s.Request) {
			p.parseRequest(&s.Request, ce)
		}
		if present(s.Batch) {
			p.parseBatch(&s.Batch, ce)
		}
	case "response":
		if !present(s.Response) {
			ce.add("response", "required for a response transformer")
		}
		for name, n := range map[string]yaml.Node{"request": s.Request, "batch": s.Batch} {
			if present(n) {
				ce.add(name, "not allowed in a response transformer")
			}
		}
		if present(s.Response) {
			p.response = parseTmpl(&s.Response, "response", ce)
			if p.response != nil && p.response.kind != nodeObject {
				ce.add("response", "must be a mapping of canonical fields")
			}
		}
		if present(s.Errors) {
			p.parseErrors(&s.Errors, ce)
		}
	default:
		ce.add("kind", "must be request or response, got %q", s.Kind)
	}
	if len(ce.Problems) > 0 {
		return nil, ce
	}
	return p, nil
}

func (p *parsed) parseRequest(n *yaml.Node, ce *CompileError) {
	if n.Kind != yaml.MappingNode {
		ce.add("request", "must be a mapping")
		return
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		at := "request." + k
		if !slices.Contains(requestKeys, k) {
			ce.add(at, "unknown key (want one of %s)", strings.Join(requestKeys, ", "))
			continue
		}
		if seen[k] {
			ce.add(at, "defined twice")
			continue
		}
		seen[k] = true
		switch k {
		case "method":
			p.method = strings.ToUpper(v.Value)
			if v.Kind != yaml.ScalarNode || !slices.Contains([]string{"GET", "POST", "PUT", "PATCH", "DELETE"}, p.method) {
				ce.add(at, "must be GET, POST, PUT, PATCH or DELETE")
			}
		case "path":
			p.path = v.Value
			if v.Kind != yaml.ScalarNode || !strings.HasPrefix(p.path, "/") {
				ce.add(at, "must be a literal path starting with /")
			}
		case "headers", "query":
			fields := parseFlatMap(v, at, k == "headers", ce)
			if k == "headers" {
				p.headers = fields
			} else {
				p.query = fields
			}
		case "body":
			p.body = parseTmpl(v, "request.body", ce)
		}
	}
	if p.method == "" {
		ce.add("request.method", "required")
	}
	if p.path == "" {
		ce.add("request.path", "required")
	}
}

// parseFlatMap reads headers or query: name → expression. Header names are case-insensitive, so
// "Content-Type" and "content-type" conflict.
func parseFlatMap(n *yaml.Node, at string, caseInsensitive bool, ce *CompileError) []field {
	if n.Kind != yaml.MappingNode {
		ce.add(at, "must be a mapping of name: expression")
		return nil
	}
	var out []field
	seen := map[string]string{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		key := k
		if caseInsensitive {
			key = strings.ToLower(k)
		}
		if prev, dup := seen[key]; dup {
			ce.add(at+"."+k, "conflicts with %s.%s", at, prev)
			continue
		}
		seen[key] = k
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
			ce.add(at+"."+k, "must be an expression (quote string literals: '\"value\"')")
			continue
		}
		out = append(out, field{key: k, node: &tmpl{kind: nodeExpr, path: at + "." + k, expr: v.Value}})
	}
	return out
}

func parseTmpl(n *yaml.Node, at string, ce *CompileError) *tmpl {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!str" {
			if strings.TrimSpace(n.Value) == "" {
				ce.add(at, "empty expression")
				return nil
			}
			return &tmpl{kind: nodeExpr, path: at, expr: n.Value}
		}
		var lit any
		if err := n.Decode(&lit); err != nil {
			ce.add(at, "invalid literal: %v", err)
			return nil
		}
		return &tmpl{kind: nodeLiteral, path: at, literal: lit}
	case yaml.MappingNode:
		t := &tmpl{kind: nodeObject, path: at}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if seen[k] {
				ce.add(at+"."+k, "target defined twice")
				continue
			}
			seen[k] = true
			if child := parseTmpl(n.Content[i+1], at+"."+k, ce); child != nil {
				t.fields = append(t.fields, field{key: k, node: child})
			}
		}
		return t
	case yaml.SequenceNode:
		t := &tmpl{kind: nodeArray, path: at}
		for i, c := range n.Content {
			if child := parseTmpl(c, fmt.Sprintf("%s[%d]", at, i), ce); child != nil {
				t.items = append(t.items, child)
			}
		}
		return t
	case yaml.AliasNode, yaml.DocumentNode:
	}
	ce.add(at, "unsupported YAML node (anchors and aliases are not allowed)")
	return nil
}

func (p *parsed) parseErrors(n *yaml.Node, ce *CompileError) {
	if n.Kind != yaml.SequenceNode {
		ce.add("errors", "must be a list of rules")
		return
	}
	for i, r := range n.Content {
		at := fmt.Sprintf("errors[%d]", i)
		var raw struct {
			When         string `yaml:"when"`
			Code         string `yaml:"code"`
			Retryable    bool   `yaml:"retryable"`
			RetryAfterMS string `yaml:"retry_after_ms"`
			Message      string `yaml:"message"`
		}
		if err := r.Decode(&raw); err != nil {
			ce.add(at, "%v", err)
			continue
		}
		for j := 0; j+1 < len(r.Content); j += 2 {
			if k := r.Content[j].Value; !slices.Contains([]string{"when", "code", "retryable", "retry_after_ms", "message"}, k) {
				ce.add(at+"."+k, "unknown key")
			}
		}
		if raw.When == "" {
			ce.add(at+".when", "required")
		}
		if !slices.Contains(upstreamCodes, raw.Code) {
			ce.add(at+".code", "must be one of %s (TRANSFORM_FAILED is raised by the engine, never mapped)", strings.Join(upstreamCodes, ", "))
		}
		p.errors = append(p.errors, errorRule{path: at, when: raw.When, code: raw.Code, retryable: raw.Retryable, retryAfterMS: raw.RetryAfterMS, message: raw.Message})
	}
}

// upstreamCodes are the only codes an error rule may produce (DESIGN §4.4).
var upstreamCodes = []string{"UPSTREAM_TIMEOUT", "UPSTREAM_UNAVAILABLE", "UPSTREAM_RATE_LIMITED", "UPSTREAM_REJECTED", "UPSTREAM_AUTH_FAILED"}

func (p *parsed) parseBatch(n *yaml.Node, ce *CompileError) {
	var b struct {
		Path string `yaml:"path"`
		Max  int    `yaml:"max"`
	}
	if err := n.Decode(&b); err != nil {
		ce.add("batch", "%v", err)
		return
	}
	if b.Path == "" || b.Max < 1 {
		ce.add("batch", "needs path and max >= 1")
		return
	}
	if p.body == nil || p.body.kind != nodeObject || !slices.ContainsFunc(p.body.fields, func(f field) bool { return f.key == b.Path && f.node.kind == nodeArray }) {
		ce.add("batch.path", "%q must name an array at the top of request.body", b.Path)
		return
	}
	p.batchPath, p.batchMax = b.Path, b.Max
}
