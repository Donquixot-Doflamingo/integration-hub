package transform

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
)

// Redacted replaces credential-derived values in dry-run output.
const Redacted = "***"

// BuildRequest evaluates a request transformer into the vendor request (DESIGN §7.1). It is the
// only thing the hot path runs: no compilation, no I/O.
func (p *Program) BuildRequest(in Input) (core.VendorRequest, error) {
	req, _, err := p.build(in)
	return req, err
}

// DryRun returns exactly what BuildRequest would send, with every credential-derived value
// replaced by "***" (DESIGN §4.1 dry-run, §7.6). Header and query targets that read creds are
// masked whole; any credential value appearing elsewhere is masked by value.
func (p *Program) DryRun(in Input) (core.VendorRequest, error) {
	req, credTargets, err := p.build(in)
	if err != nil {
		return core.VendorRequest{}, err
	}
	for name := range req.Headers {
		if credTargets["headers."+strings.ToLower(name)] {
			req.Headers[name] = Redacted
		}
	}
	for name := range req.Query {
		if credTargets["query."+name] {
			req.Query[name] = Redacted
		}
	}
	secrets := make([]string, 0, len(in.Creds))
	for _, v := range in.Creds {
		if v != "" {
			secrets = append(secrets, v)
		}
	}
	redact := func(s string) string {
		for _, sec := range secrets {
			s = strings.ReplaceAll(s, sec, Redacted)
		}
		return s
	}
	for k, v := range req.Headers {
		req.Headers[k] = redact(v)
	}
	for k, v := range req.Query {
		req.Query[k] = redact(v)
	}
	req.Body = redactTree(req.Body, redact)
	return req, nil
}

func redactTree(v any, redact func(string) string) any {
	switch x := v.(type) {
	case string:
		return redact(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactTree(e, redact)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactTree(e, redact)
		}
		return out
	}
	return v
}

var errNotRequest = errors.New("not a request transformer")

func (p *Program) build(in Input) (core.VendorRequest, map[string]bool, error) {
	if p.kind != "request" {
		return core.VendorRequest{}, nil, &EvalError{Transformer: p.ref, Path: "request", Err: errNotRequest}
	}
	start := time.Now()
	env := p.env(in, nil)
	req := core.VendorRequest{Method: p.method, Path: p.path}
	credTargets := map[string]bool{}
	var err error
	if req.Headers, err = p.flat(p.headers, env, "headers", credTargets, true); err != nil {
		return core.VendorRequest{}, nil, err
	}
	if req.Query, err = p.flat(p.query, env, "query", credTargets, false); err != nil {
		return core.VendorRequest{}, nil, err
	}
	if p.body != nil {
		if req.Body, err = p.eval(p.body, env); err != nil {
			return core.VendorRequest{}, nil, err
		}
		if err := p.checkOutput("request.body", req.Body); err != nil {
			return core.VendorRequest{}, nil, err
		}
	}
	if err := p.checkTime("request", start); err != nil {
		return core.VendorRequest{}, nil, err
	}
	return req, credTargets, nil
}

// flat evaluates headers or query parameters. Values must be scalars; nil omits the parameter.
func (p *Program) flat(fields []cfield, env map[string]any, section string, credTargets map[string]bool, lowerKey bool) (map[string]string, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(fields))
	for _, f := range fields {
		v, err := p.eval(f.node, env)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue
		}
		s, err := scalarString(v)
		if err != nil {
			return nil, &EvalError{Transformer: p.ref, Path: f.node.path, Expr: f.node.src, Err: err}
		}
		out[f.key] = s
		key := f.key
		if lowerKey {
			key = strings.ToLower(key)
		}
		if f.node.usesCreds {
			credTargets[section+"."+key] = true
		}
	}
	return out, nil
}

func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("must evaluate to a string, number or bool, got %T", v)
}
