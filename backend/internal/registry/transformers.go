package registry

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// CompileTransformer compiles one transformer. It knows no caller schema (ADR-0023): the fields
// it reads are checked against each integration operation that uses it (CheckFieldRefs).
func (c *Config) CompileTransformer(ref TransformerRef) (*transform.Program, error) {
	t, ok := c.Transformers[ref]
	if !ok {
		return nil, fmt.Errorf("unknown transformer %s", ref)
	}
	return CompileFile(t)
}

// CompileFile compiles a transformer file (the snapshot build and admin drafts).
func CompileFile(t *TransformerFile) (*transform.Program, error) {
	spec := transform.Spec{
		ID: t.Transformer, Version: t.Version, Kind: string(t.Kind),
		Request: t.Request, Response: t.Response, Errors: t.Errors, Batch: t.Batch, Session: t.Session, AllowCredsInBody: t.AllowCredsInBody,
	}
	return transform.Compile(spec, transform.Options{})
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

// Endpoint resolves "clevertap.upload@1" in the snapshot.
func (s *Snapshot) Endpoint(ref string) (*Endpoint, bool) {
	vendor, name, ok := strings.Cut(ref, ".")
	if !ok {
		return nil, false
	}
	v, ok := s.Vendors[vendor]
	if !ok {
		return nil, false
	}
	ep, ok := v.Endpoints[name]
	return &ep, ok
}

// CheckEndpointRequest validates a produced vendor request (credential values already removed,
// transform.Program.BuildSample) against the endpoint's request schema: {path, query, headers,
// body}, where path holds the {name} segments of the endpoint path. A part the schema does not
// declare is not checked. Field pointers start with the part (/query/name). It runs on samples
// only, never on live calls (ADR-0023).
func CheckEndpointRequest(ep *Endpoint, req core.VendorRequest) []Violation {
	if ep == nil || ep.RequestSchema == nil {
		return nil
	}
	rs := ep.RequestSchema
	var out []Violation
	check := func(s *JSONSchema, v any, prefix string) {
		if s.Compiled() == nil {
			return
		}
		jv, err := ToJSONValue(v)
		if err != nil {
			out = append(out, Violation{Field: prefix, Message: err.Error()})
			return
		}
		out = append(out, Violations(s.Compiled(), jv, prefix)...)
	}
	check(rs.Path, pathParams(ep.Path, req.Path), "/path")
	check(rs.Query, stringObject(req.Query), "/query")
	check(rs.Headers, stringObject(req.Headers), "/headers")
	if rs.Body.Compiled() != nil {
		if req.Body == nil {
			out = append(out, Violation{Field: "/body", Keyword: "required", Message: "the endpoint expects a body"})
		} else {
			check(rs.Body, req.Body, "/body")
		}
	}
	if len(out) > MaxViolations {
		out = out[:MaxViolations]
	}
	return out
}

// pathParams matches the endpoint path template (/v1/users/{id}) against a produced path and
// returns the {name} segments; an empty object when nothing matches.
func pathParams(template, actual string) map[string]any {
	out := map[string]any{}
	ts := strings.Split(strings.Trim(template, "/"), "/")
	as := strings.Split(strings.Trim(strings.SplitN(actual, "?", 2)[0], "/"), "/")
	if len(ts) != len(as) {
		return out
	}
	for i, seg := range ts {
		if name, ok := strings.CutPrefix(seg, "{"); ok && strings.HasSuffix(name, "}") {
			out[strings.TrimSuffix(name, "}")] = as[i]
		}
	}
	return out
}

func stringObject(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// CheckFieldRefs checks the req.* and hdr.* reads of a transformer against an operation's
// request schema (check schemas_compile, ADR-0023): req.* must be declared in the body schema,
// hdr.* in the headers schema (case-insensitive), apart from hdr.idempotency_key, which the Hub
// always provides. Each problem carries the field (/body/user/id, /headers/X-Source).
func CheckFieldRefs(prog *transform.Program, op *Operation) []Violation {
	var out []Violation
	for _, r := range prog.FieldRefs() {
		switch r.Root {
		case "req":
			if op.RequestSchema.Body == nil {
				continue
			}
			if err := CheckFieldPath(op.RequestSchema.Body.Doc, r.Segs); err != nil {
				out = append(out, Violation{Field: "/body/" + pointerOf(r.Segs), Keyword: "properties",
					Message: fmt.Sprintf("%s (%s) reads req.%s, which operation %s's request_schema.body does not declare", prog.Ref(), r.Path, strings.Join(r.Segs, "."), op.Name)})
			}
		case "hdr":
			name := r.Segs[0]
			if name == HdrIdempotencyKey || containsFold(op.headers, name) {
				continue
			}
			out = append(out, Violation{Field: "/headers/" + EscapePointer(name), Keyword: "properties",
				Message: fmt.Sprintf("%s (%s) reads hdr.%s, which operation %s's request_schema.headers does not declare", prog.Ref(), r.Path, name, op.Name)})
		}
	}
	return out
}

// CheckSampleInput validates a request sample's caller input against an operation's request
// schema (check input_schema): the body, and the headers when the operation declares them.
// Field pointers start with /body or /headers.
func CheckSampleInput(op *Operation, body, headers map[string]any) []Violation {
	var out []Violation
	if s := op.RequestSchema.Body.Compiled(); s != nil {
		if b := body; b != nil {
			jv, err := ToJSONValue(b)
			if err == nil {
				out = append(out, Violations(s, jv, "/body")...)
			}
		}
	}
	if s := op.RequestSchema.Headers.Compiled(); s != nil {
		h := map[string]any{}
		for _, name := range op.headers { // declared names, matched case-insensitively
			for k, v := range headers {
				if strings.EqualFold(k, name) {
					h[name] = v
				}
			}
		}
		jv, err := ToJSONValue(h)
		if err == nil {
			out = append(out, Violations(s, jv, "/headers")...)
		}
	}
	return out
}

func pointerOf(segs []string) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = EscapePointer(s)
	}
	return strings.Join(parts, "/")
}

func containsFold(names []string, name string) bool {
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// compiled is the result of compiling every transformer of a config set.
type compiled struct {
	programs map[TransformerRef]*transform.Program // every transformer that compiled
	goldens  map[TransformerRef][]transform.GoldenResult
}

// compileAll compiles every transformer once and runs its samples (DESIGN §6.4, AGENTS §3.4,
// ADR-0023): a type error, a failing golden or a produced request that breaks its endpoint's
// request schema refuses the snapshot, naming the transformer and the sample. The programs that
// compiled are returned with the problems, so the integration checks still run on them.
func (c *Config) compileAll(config fs.FS) (compiled, ConfigErrors) {
	out := compiled{programs: make(map[TransformerRef]*transform.Program, len(c.Transformers)), goldens: map[TransformerRef][]transform.GoldenResult{}}
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
		var ep *Endpoint
		if tf := c.Transformers[ref]; tf.Kind == KindRequest {
			if e, err := c.endpoint(tf.VendorEndpoint); err == nil {
				ep = e
			}
		}
		results := transform.RunGoldens(prog, fixtures)
		for _, r := range results {
			at := path.Join(FixtureDir(config, ref), r.Case)
			if r.Case == "" {
				at = FixtureDir(config, ref)
			}
			if !r.Passed() {
				msg, field := r.Err, ""
				if len(r.Diffs) > 0 {
					msg = "golden mismatch: " + strings.Join(r.Diffs, "; ")
					field, _, _ = strings.Cut(r.Diffs[0], ": ")
				}
				ps = append(ps, Problem{File: at, Message: fmt.Sprintf("%s: %s", ref, msg), Check: CheckGoldensPass, Sample: r.Case, Field: field})
			}
			if r.Checked != nil && ep != nil {
				for _, v := range CheckEndpointRequest(ep, *r.Checked) {
					ps = append(ps, Problem{File: at, Path: "vendor_endpoint", Check: CheckEndpointSchema, Sample: r.Case, Field: v.Field,
						Message: fmt.Sprintf("%s: sample %s: the produced request breaks %s's request_schema at %s: %s", ref, r.Case, c.Transformers[ref].VendorEndpoint, v.Field, v.Message)})
				}
			}
		}
		out.goldens[ref] = results
		out.programs[ref] = prog
	}
	return out, ps
}

// checkIntegrations runs the per-operation checks of every integration version once every
// transformer compiled (ADR-0023): the fields each pair's transformers read must be declared in
// the operation's request schema (schemas_compile), and every sample input of the request
// transformer must fit it (input_schema).
func (c *Config) checkIntegrations(cc compiled) ConfigErrors {
	var ps ConfigErrors
	for _, id := range sortedKeys(c.Integrations) {
		in := c.Integrations[id]
		for _, n := range sortedKeys(in.Versions) {
			v, file := in.Versions[n], in.VersionFiles[n]
			type key struct {
				op  string
				ref TransformerRef
			}
			seen := map[key]bool{}
			for _, ta := range targetsAt(v) {
				for _, opName := range sortedKeys(v.Operations) {
					op := v.Operations[opName]
					if op == nil || op.RequestSchema.Body.Compiled() == nil {
						continue
					}
					pair := ta.t.Pair(opName)
					at := ta.at + ".transformer"
					if _, own := ta.t.Operations[opName]; own {
						at = ta.at + ".operations." + opName
					}
					for _, slot := range []struct {
						name string
						ref  TransformerRef
					}{{"request", pair.Request}, {"response", pair.Response}} {
						prog := cc.programs[slot.ref]
						if prog == nil || seen[key{opName, slot.ref}] {
							continue
						}
						seen[key{opName, slot.ref}] = true
						for _, viol := range CheckFieldRefs(prog, op) {
							ps = append(ps, Problem{File: file, Path: at + "." + slot.name, Check: CheckSchemasCompile, Field: viol.Field, Message: viol.Message})
						}
						if slot.name != "request" {
							continue
						}
						for _, r := range cc.goldens[slot.ref] {
							if r.Input == nil {
								continue
							}
							for _, viol := range CheckSampleInput(op, r.Input, r.InputHeaders) {
								ps = append(ps, Problem{File: file, Path: at + ".request", Check: CheckInputSchema, Sample: r.Case, Field: viol.Field,
									Message: fmt.Sprintf("sample %s of %s does not fit operation %s's request_schema at %s: %s", r.Case, slot.ref, opName, viol.Field, viol.Message)})
							}
						}
					}
				}
			}
		}
	}
	return ps
}

type targetAt struct {
	t  Target
	at string
}

// targetsAt lists every target of a version with its YAML path: targets, failover, shadow.
func targetsAt(v *IntegrationVersion) []targetAt {
	var out []targetAt
	for i, t := range v.Targets {
		out = append(out, targetAt{t, fmt.Sprintf("targets[%d]", i)})
	}
	for i, t := range v.Failover {
		out = append(out, targetAt{t, fmt.Sprintf("failover[%d]", i)})
	}
	if v.Shadow != nil {
		out = append(out, targetAt{v.Shadow.Target, "shadow.target"})
	}
	return out
}
