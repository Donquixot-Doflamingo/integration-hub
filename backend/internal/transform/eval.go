package transform

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/expr-lang/expr/vm"
)

// Input is everything a transformer may read (DESIGN §7.2). Callers build it per request from the
// validated canonical body, the allowlisted caller headers, the account's non-secret config and
// the decrypted credentials from the cache.
type Input struct {
	Req     map[string]any
	Hdr     map[string]any
	Account map[string]any // {"id": ..., "region": ..., "config": {...}}
	Creds   map[string]string
	Session map[string]any
	// Now is the clock for now(); zero means time.Now (goldens pin it).
	Now time.Time
	// UUID generates uuid() values; nil means random v4 (goldens pin it).
	UUID func() string
}

// EvalError is a transformer failure at run time: an expression threw, the output was too large
// or did not match the capability schema, or the time budget was exceeded. Callers map it to
// TRANSFORM_FAILED (DESIGN §4.4) and record Path and Expr in the audit trail; it never contains
// request values.
type EvalError struct {
	Transformer string
	Path        string
	Expr        string
	Err         error
	// detail is the expression engine's own message. It can quote input values (personal data,
	// secrets), so Error() never includes it; only developer tools on fixture data print Detail().
	detail string
}

// errEvaluation replaces engine messages in Error(), which reaches logs and the audit trail.
var errEvaluation = errors.New("expression failed at run time")

// Detail is the engine message, which may contain input values: show it only for fixture data
// (goldens, hubctl), never in logs or audit.
func (e *EvalError) Detail() string { return e.detail }

func (e *EvalError) Error() string {
	if e.Expr == "" {
		return fmt.Sprintf("transformer %s: %s: %v", e.Transformer, e.Path, e.Err)
	}
	return fmt.Sprintf("transformer %s: %s: `%s`: %v", e.Transformer, e.Path, e.Expr, e.Err)
}

func (e *EvalError) Unwrap() error { return e.Err }

func (p *Program) env(in Input, res map[string]any) map[string]any {
	now := in.Now
	clock := func() time.Time { return time.Now().UTC() }
	if !now.IsZero() {
		clock = func() time.Time { return now }
	}
	uuid := in.UUID
	if uuid == nil {
		uuid = randomUUID
	}
	creds := make(map[string]any, len(in.Creds))
	for k, v := range in.Creds {
		creds[k] = v
	}
	env := map[string]any{"account": orEmpty(in.Account), "now": clock, "uuid": uuid}
	if p.kind == "request" {
		env["req"], env["hdr"], env["session"], env["creds"] = orEmpty(in.Req), orEmpty(in.Hdr), orEmpty(in.Session), creds
	} else {
		env["res"] = res
	}
	return env
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// eval evaluates a template node. Object fields whose value is nil are omitted, so an absent
// optional canonical field does not send null to the vendor.
func (p *Program) eval(n *cnode, env map[string]any) (any, error) {
	switch n.kind {
	case nodeLiteral:
		return n.literal, nil
	case nodeExpr:
		m := vm.VM{MemoryBudget: p.limits.MemoryBudget}
		v, err := m.Run(n.prog, env)
		if err != nil {
			return nil, &EvalError{Transformer: p.ref, Path: n.path, Expr: n.src, Err: errEvaluation, detail: firstLine(err.Error())}
		}
		return v, nil
	case nodeObject:
		out := make(map[string]any, len(n.fields))
		for _, f := range n.fields {
			v, err := p.eval(f.node, env)
			if err != nil {
				return nil, err
			}
			if v != nil {
				out[f.key] = v
			}
		}
		return out, nil
	case nodeArray:
		out := make([]any, 0, len(n.items))
		for _, it := range n.items {
			v, err := p.eval(it, env)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown node kind %d", n.kind)
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
