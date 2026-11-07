package transform

import (
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// Limits bound what a transformer may do (DESIGN §7.1: pure and bounded). expr-lang cannot
// pre-empt a running expression, so the time budget is enforced by construction (node, depth,
// memory and input limits keep every expression short) and checked after each run (ADR-0002).
type Limits struct {
	MaxNodes       uint          // AST nodes per expression
	MaxDepth       int           // AST depth per expression
	MemoryBudget   uint          // expr VM allocation budget per expression run (bounds map/filter)
	MaxOutputBytes int           // JSON size of a built body or mapped response
	TimeBudget     time.Duration // whole transform, checked after the run
}

// DefaultLimits are the production limits.
func DefaultLimits() Limits {
	return Limits{MaxNodes: 500, MaxDepth: 24, MemoryBudget: 100_000, MaxOutputBytes: 1 << 20, TimeBudget: 5 * time.Millisecond}
}

// Options configure Compile.
type Options struct {
	// Limits; zero value means DefaultLimits.
	Limits Limits
}

// Program is a compiled, immutable transformer. It is safe for concurrent use and shared by
// every integration version that references the same ID@version. It knows no caller schema
// (ADR-0023): the req.* and hdr.* fields it reads (FieldRefs) are checked against each
// integration operation that uses it.
type Program struct {
	ref, kind      string
	method, path   string
	headers, query []cfield
	body, response *cnode
	session        *cnode
	errors         []crule
	batchPath      string
	batchMax       int
	limits         Limits
	credsInBody    bool
	refs           []FieldRef
}

// FieldRef is one statically known read of the caller's input: req.a.b (Root "req", Segs
// [a b]) or hdr["X-Source"] (Root "hdr", Segs [X-Source]). Path is the transformer target that
// reads it.
type FieldRef struct {
	Path string
	Root string
	Segs []string
}

// FieldRefs lists the req.* and hdr.* reads of the transformer (request mapping and session:
// section), in file order, without duplicates.
func (p *Program) FieldRefs() []FieldRef { return p.refs }

// Ref is "T1@7".
func (p *Program) Ref() string { return p.ref }

// Kind is request or response.
func (p *Program) Kind() string { return p.kind }

// HasSession reports whether the transformer maps vendor session data (a session: section).
func (p *Program) HasSession() bool { return p.session != nil }

// Batch returns the batchable body array and its max size, if declared (DESIGN §7.5).
func (p *Program) Batch() (path string, maxItems int) { return p.batchPath, p.batchMax }

type cnode struct {
	kind      nodeKind
	path, src string
	prog      *vm.Program
	usesCreds bool
	literal   any
	fields    []cfield
	items     []*cnode
}

type cfield struct {
	key  string
	node *cnode
}

type crule struct {
	path         string
	when         *cnode
	code         string
	retryable    bool
	retryAfterMS *cnode
	message      *cnode
}

// compiled counts expressions compiled since process start. Tests assert it does not move while
// requests are transformed (DESIGN §7.1: compile once, never per request).
var compiled atomic.Int64

// CompiledExpressions reports how many expressions this process has compiled.
func CompiledExpressions() int64 { return compiled.Load() }

// Variables each mapping direction may read (DESIGN §7.2).
var (
	requestVars  = []string{"req", "hdr", "account", "creds", "session", "now", "uuid"}
	responseVars = []string{"res", "req", "account", "now", "uuid"}
	// sessionVars: a response transformer's session: section also reads the canonical request
	// (e.g. which user opened the session). Never creds or hdr: session data is stored.
	sessionVars = []string{"res", "req", "account", "now", "uuid"}
)

// allowedBuiltins are the expr-lang builtins that are part of the DESIGN §7.3 standard library.
// Every other builtin (reduce, sortBy, all, any, ...) is disabled: no loops beyond map/filter.
var allowedBuiltins = []string{"len", "map", "filter", "trim", "upper", "lower", "split", "join", "replace", "int", "float", "string"}

// envSample gives expr the variable names and shapes it type-checks against.
func envSample(vars []string) map[string]any {
	env := map[string]any{}
	for _, v := range vars {
		switch v {
		case "now":
			env[v] = func() time.Time { return time.Time{} }
		case "uuid":
			env[v] = func() string { return "" }
		default:
			env[v] = map[string]any{}
		}
	}
	return env
}

func exprOptions(vars []string, limits Limits) []expr.Option {
	opts := []expr.Option{expr.Env(envSample(vars)), expr.MaxNodes(limits.MaxNodes), expr.Patch(nilSafeMembers{})}
	for _, b := range builtin.Builtins {
		if !slices.Contains(allowedBuiltins, b.Name) {
			opts = append(opts, expr.DisableBuiltin(b.Name))
		}
	}
	return append(opts, stdlibOptions()...)
}

// Compile parses and compiles s once (HUB-061). Every problem is reported with its target path.
func Compile(s Spec, o Options) (*Program, error) {
	if o.Limits == (Limits{}) {
		o.Limits = DefaultLimits()
	}
	pt, perr := parse(s)
	if perr != nil {
		return nil, perr
	}
	c := &compiler{spec: s, limits: o.Limits, ce: &CompileError{Transformer: s.Ref()}, mapping: s.Kind, seen: map[string]bool{}}
	if s.Kind == "request" {
		c.vars, c.otherVars = requestVars, responseVars
	} else {
		c.vars, c.otherVars = responseVars, requestVars
	}
	c.opts = exprOptions(c.vars, o.Limits)

	p := &Program{
		ref: s.Ref(), kind: s.Kind, method: pt.method, path: pt.path,
		batchPath: pt.batchPath, batchMax: pt.batchMax, limits: o.Limits, credsInBody: s.AllowCredsInBody,
	}
	for _, f := range pt.headers {
		p.headers = append(p.headers, cfield{key: f.key, node: c.node(f.node, false)})
	}
	for _, f := range pt.query {
		p.query = append(p.query, cfield{key: f.key, node: c.node(f.node, false)})
	}
	if pt.body != nil {
		p.body = c.node(pt.body, !s.AllowCredsInBody)
	}
	if pt.response != nil {
		p.response = c.node(pt.response, false)
	}
	if pt.session != nil {
		p.session = c.sessionNode(pt.session)
	}
	for _, r := range pt.errors {
		cr := crule{path: r.path, code: r.code, retryable: r.retryable,
			when: c.node(&tmpl{kind: nodeExpr, path: r.path + ".when", expr: r.when}, false)}
		if r.retryAfterMS != "" {
			cr.retryAfterMS = c.node(&tmpl{kind: nodeExpr, path: r.path + ".retry_after_ms", expr: r.retryAfterMS}, false)
		}
		if r.message != "" {
			cr.message = c.node(&tmpl{kind: nodeExpr, path: r.path + ".message", expr: r.message}, false)
		}
		p.errors = append(p.errors, cr)
	}
	if len(c.ce.Problems) > 0 {
		return nil, c.ce
	}
	p.refs = c.refs
	return p, nil
}

type compiler struct {
	spec            Spec
	limits          Limits
	refs            []FieldRef
	seen            map[string]bool
	vars, otherVars []string
	opts            []expr.Option
	ce              *CompileError
	mapping         string // request | response | session: the section being compiled, for errors
}

// sessionNode compiles the session: section with its own variables (sessionVars). creds is
// never allowed: session data is stored in Postgres (DESIGN §14, no secrets at rest).
func (c *compiler) sessionNode(t *tmpl) *cnode {
	vars, other, opts, mapping := c.vars, c.otherVars, c.opts, c.mapping
	defer func() { c.vars, c.otherVars, c.opts, c.mapping = vars, other, opts, mapping }()
	c.vars, c.otherVars, c.mapping = sessionVars, []string{"hdr", "session"}, "session"
	c.opts = exprOptions(sessionVars, c.limits)
	return c.node(t, true)
}

// node compiles a template; noCreds forbids creds.* (request body unless allow_creds_in_body).
func (c *compiler) node(t *tmpl, noCreds bool) *cnode {
	n := &cnode{kind: t.kind, path: t.path, literal: t.literal}
	switch t.kind {
	case nodeExpr:
		c.expression(n, t.expr, noCreds)
	case nodeObject:
		for _, f := range t.fields {
			n.fields = append(n.fields, cfield{key: f.key, node: c.node(f.node, noCreds)})
		}
	case nodeArray:
		for _, it := range t.items {
			n.items = append(n.items, c.node(it, noCreds))
		}
	case nodeLiteral:
	}
	return n
}

func (c *compiler) expression(n *cnode, src string, noCreds bool) {
	n.src = src
	tree, err := parser.Parse(src)
	if err != nil {
		c.ce.add(n.path, "syntax: %s", firstLine(err.Error()))
		return
	}
	if d := nestingDepth(src); d > c.limits.MaxDepth {
		c.ce.add(n.path, "expression is nested %d deep, limit %d", d, c.limits.MaxDepth)
		return
	}
	v := &inspector{}
	ast.Walk(&tree.Node, v)
	failed := false
	for _, b := range v.builtins {
		_, isStdlib := stdlib[b]
		if !slices.Contains(allowedBuiltins, b) && !slices.Contains(c.vars, b) && !isStdlib {
			c.ce.add(n.path, "unknown name %s (builtin not in the transformer standard library, DESIGN §7.3)", b)
			failed = true
		}
	}
	for _, id := range v.idents {
		if !slices.Contains(c.vars, id) && slices.Contains(c.otherVars, id) {
			c.ce.add(n.path, "%s is not available in %s mapping (DESIGN §7.2)", id, c.mapping)
			failed = true
		}
	}
	if slices.Contains(v.idents, "creds") {
		n.usesCreds = true
		switch {
		case c.mapping == "session":
			c.ce.add(n.path, "creds.* is never allowed in session: session data is stored (DESIGN §14)")
			failed = true
		case noCreds:
			c.ce.add(n.path, "creds.* may only be used in headers or query (set allow_creds_in_body to override, DESIGN §7.2)")
			failed = true
		}
	}
	for _, chain := range v.chains {
		if (chain[0] != "req" && chain[0] != "hdr") || len(chain) < 2 || !slices.Contains(c.vars, chain[0]) {
			continue
		}
		// a.b.c is also visited as a.b: keep only the longest chain.
		if slices.ContainsFunc(v.chains, func(o []string) bool { return len(o) > len(chain) && slices.Equal(o[:len(chain)], chain) }) {
			continue
		}
		key := chain[0] + "\x00" + strings.Join(chain[1:], "\x00")
		if c.seen[key] {
			continue
		}
		c.seen[key] = true
		c.refs = append(c.refs, FieldRef{Path: n.path, Root: chain[0], Segs: chain[1:]})
	}
	if failed {
		return
	}
	prog, err := expr.Compile(src, c.opts...)
	if err != nil {
		c.ce.add(n.path, "%s", firstLine(err.Error()))
		return
	}
	compiled.Add(1)
	n.prog = prog
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// nilSafeMembers makes every a.b access behave like a?.b: canonical bodies omit optional objects,
// and DESIGN §7.4 writes req.context.campaign ?? "organic" expecting nil, not an error, when
// context is absent.
type nilSafeMembers struct{}

func (nilSafeMembers) Visit(n *ast.Node) {
	m, ok := (*n).(*ast.MemberNode)
	if !ok || m.Method || m.Optional {
		return
	}
	if _, byName := m.Property.(*ast.StringNode); !byName {
		return
	}
	// expr only honours Optional inside a ChainNode, and jumps to that chain's end. Wrapping each
	// access in its own chain keeps "a.b.c ?? x" working: a nil anywhere yields nil for the chain,
	// and the ?? still applies.
	m.Optional = true
	ast.Patch(n, &ast.ChainNode{Node: m})
}

// inspector collects identifiers and member chains (req.user.id → [req user id]).
type inspector struct {
	idents   []string
	chains   [][]string
	builtins []string
}

func (v *inspector) Visit(n *ast.Node) {
	switch node := (*n).(type) {
	case *ast.IdentifierNode:
		if !slices.Contains(v.idents, node.Value) {
			v.idents = append(v.idents, node.Value)
		}
	case *ast.MemberNode:
		if chain, ok := memberChain(node); ok {
			v.chains = append(v.chains, chain)
		}
	case *ast.BuiltinNode:
		v.builtins = append(v.builtins, node.Name)
	}
}

// memberChain returns [root, seg...] for a.b.c (property access by name only).
func memberChain(m *ast.MemberNode) ([]string, bool) {
	var segs []string
	var cur ast.Node = m
	for {
		switch x := cur.(type) {
		case *ast.MemberNode:
			s, ok := x.Property.(*ast.StringNode)
			if !ok {
				return nil, false // computed index: not statically checkable
			}
			segs = append([]string{s.Value}, segs...)
			cur = x.Node
		case *ast.ChainNode:
			cur = x.Node
		case *ast.IdentifierNode:
			return append([]string{x.Value}, segs...), true
		default:
			return nil, false
		}
	}
}

// nestingDepth is the deepest bracket nesting of (), [] and {} outside string literals. With
// MaxNodes it bounds expression complexity (deep calls, groupings and map/filter nests).
func nestingDepth(src string) int {
	depth, deepest := 0, 0
	var quote rune
	escaped := false
	for _, r := range src {
		switch {
		case quote != 0:
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == quote:
				quote = 0
			}
		case r == '"' || r == '\'' || r == '`':
			quote = r
		case r == '(' || r == '[' || r == '{':
			depth++
			deepest = max(deepest, depth)
		case r == ')' || r == ']' || r == '}':
			depth--
		}
	}
	return deepest
}
