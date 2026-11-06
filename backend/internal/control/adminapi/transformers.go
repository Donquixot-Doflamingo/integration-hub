package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/transform"
)

// Transformers (DESIGN §7, §16.1). A version is transformers/<id>/<n>.yaml of the config tree
// with its golden fixtures in transformers/<id>/fixtures/v<n>/ (registry.FixtureDir) and its
// publish metadata (author, reason, created_at) in admin/transformers/<id>/<n>.json. Versions
// are immutable: a create writes version n+1 and never touches an existing file. A create goes
// through the full snapshot build, which compiles, type-checks and runs every golden, so a
// transformer with failing goldens cannot be created (AGENTS §3.4).

func init() { registrars = append(registrars, (*Server).registerTransformers) }

func (s *Server) registerTransformers() {
	s.handle("listTransformers", RoleViewer, false, "GET /transformers", s.listTransformers)
	s.handle("dryRunTransformer", RoleEditor, false, "POST /transformers/dry-run", s.dryRunTransformer)
	s.handle("runTransformerGoldens", RoleEditor, false, "POST /transformers/run-goldens", s.runTransformerGoldens)
	s.handle("getTransformer", RoleViewer, false, "GET /transformers/{transformer_id}", s.getTransformer)
	s.handle("listTransformerVersions", RoleViewer, false, "GET /transformers/{transformer_id}/versions", s.listTransformerVersions)
	s.handle("createTransformerVersion", RoleEditor, true, "POST /transformers/{transformer_id}/versions", s.createTransformerVersion)
	s.handle("getTransformerVersion", RoleViewer, false, "GET /transformers/{transformer_id}/versions/{version}", s.getTransformerVersion)
}

var (
	transformerIDRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	fixtureNameRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	vendorEndpointRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{2,63}\.[a-z][a-z0-9_]{0,63}@[1-9][0-9]*$`)
)

type transformerMeta struct {
	Author    string    `json:"author"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

func transformerFile(ref registry.TransformerRef) string {
	return fmt.Sprintf("transformers/%s/%d.yaml", ref.ID, ref.Version)
}

func transformerMetaFile(ref registry.TransformerRef) string {
	return fmt.Sprintf("admin/transformers/%s/%d.json", ref.ID, ref.Version)
}

// fixture is the contract's GoldenFixture. input and vendor_response are the fixture documents
// as stored (DESIGN §7.6), "_context" included; account_config, when given, becomes
// _context.account of the case.
type fixture struct {
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input,omitempty"`
	AccountConfig    json.RawMessage `json:"account_config,omitempty"`
	ExpectedRequest  json.RawMessage `json:"expected_request,omitempty"`
	VendorResponse   json.RawMessage `json:"vendor_response,omitempty"`
	ExpectedResponse json.RawMessage `json:"expected_response,omitempty"`
	ExpectedError    json.RawMessage `json:"expected_error,omitempty"`
}

func isObject(raw json.RawMessage) bool {
	var m map[string]any
	return len(raw) > 0 && json.Unmarshal(raw, &m) == nil && m != nil
}

// withContext sets _context.account of doc from accountConfig unless doc already sets it.
func withContext(doc, accountConfig json.RawMessage) (json.RawMessage, error) {
	if len(accountConfig) == 0 {
		return doc, nil
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, err
	}
	ctx, _ := m["_context"].(map[string]any)
	if ctx == nil {
		ctx = map[string]any{}
	}
	if _, set := ctx["account"]; !set {
		var acc any
		if err := json.Unmarshal(accountConfig, &acc); err != nil {
			return nil, err
		}
		ctx["account"] = acc
	}
	m["_context"] = ctx
	return json.Marshal(m)
}

func indentJSON(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return raw
	}
	return append(buf.Bytes(), '\n')
}

// fixtureFiles turns fixtures into fixture-directory files (name → content) for kind.
func fixtureFiles(kind string, fixtures []fixture, pointer string) (map[string][]byte, error) {
	if len(fixtures) == 0 {
		return nil, badRequest(pointer, "at least one golden fixture is required (AGENTS §3.4)")
	}
	files := map[string][]byte{}
	seen := map[string]bool{}
	for i, f := range fixtures {
		at := fmt.Sprintf("%s/%d", pointer, i)
		if !fixtureNameRE.MatchString(f.Name) {
			return nil, badRequest(at+"/name", "fixture names must match "+fixtureNameRE.String())
		}
		if seen[f.Name] {
			return nil, badRequest(at+"/name", "duplicate fixture "+f.Name)
		}
		seen[f.Name] = true
		if len(f.AccountConfig) > 0 && !isObject(f.AccountConfig) {
			return nil, badRequest(at+"/account_config", "account_config must be an object")
		}
		switch kind {
		case string(registry.KindRequest):
			if !isObject(f.Input) || !isObject(f.ExpectedRequest) {
				return nil, badRequest(at, "request fixtures need input and expected_request objects")
			}
			in, err := withContext(f.Input, f.AccountConfig)
			if err != nil {
				return nil, badRequest(at+"/input", err.Error())
			}
			files[f.Name+".input.json"] = indentJSON(in)
			files[f.Name+".expected_request.json"] = indentJSON(f.ExpectedRequest)
		case string(registry.KindResponse):
			hasResp, hasErr := isObject(f.ExpectedResponse), isObject(f.ExpectedError)
			if !isObject(f.VendorResponse) || hasResp == hasErr {
				return nil, badRequest(at, "response fixtures need vendor_response and exactly one of expected_response or expected_error")
			}
			vr, err := withContext(f.VendorResponse, f.AccountConfig)
			if err != nil {
				return nil, badRequest(at+"/vendor_response", err.Error())
			}
			want := map[string]json.RawMessage{"response": f.ExpectedResponse}
			if hasErr {
				want = map[string]json.RawMessage{"error": f.ExpectedError}
			}
			b, _ := json.Marshal(want)
			files[f.Name+".vendor_response.json"] = indentJSON(vr)
			files[f.Name+".expected_response.json"] = indentJSON(b)
		default:
			return nil, badRequest("/kind", "kind must be request or response (inbound transformers are not supported by this build)")
		}
	}
	return files, nil
}

// readFixtures reads a fixture directory back into the contract's fixtures.
func readFixtures(fsys fs.FS, dir, kind string) ([]fixture, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []fixture{}, nil
	}
	if err != nil {
		return nil, err
	}
	read := func(name string) (json.RawMessage, bool) {
		b, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, false
		}
		return json.RawMessage(bytes.TrimSpace(b)), true
	}
	suffix := ".input.json"
	if kind == string(registry.KindResponse) {
		suffix = ".vendor_response.json"
	}
	out := []fixture{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), suffix)
		if !ok || e.IsDir() {
			continue
		}
		f := fixture{Name: name}
		if kind == string(registry.KindResponse) {
			f.VendorResponse, _ = read(e.Name())
			if want, ok := read(name + ".expected_response.json"); ok {
				var m map[string]json.RawMessage
				if json.Unmarshal(want, &m) == nil {
					f.ExpectedResponse, f.ExpectedError = m["response"], m["error"]
				}
			}
		} else {
			f.Input, _ = read(e.Name())
			f.ExpectedRequest, _ = read(name + ".expected_request.json")
		}
		out = append(out, f)
	}
	return out, nil
}

// ---- golden runs ----

type goldenCase struct {
	Fixture string   `json:"fixture"`
	Passed  bool     `json:"passed"`
	Diff    []diffOp `json:"diff"`
	Error   *string  `json:"error"`
}

type goldensResult struct {
	Passed     bool         `json:"passed"`
	Total      int          `json:"total"`
	Failed     int          `json:"failed"`
	Results    []goldenCase `json:"results"`
	DurationMS int64        `json:"duration_ms"`
}

func shown(s string) any {
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

// diffOps turns transform.Diff lines ("<ptr>: expected X, got Y", "<ptr>: expected X, missing",
// "<ptr>: unexpected Y") into DiffOps from expected (before) to actual (after).
func diffOps(lines []string) []diffOp {
	out := make([]diffOp, 0, len(lines))
	for _, l := range lines {
		ptr, rest, _ := strings.Cut(l, ": ")
		switch {
		case strings.HasPrefix(rest, "unexpected "):
			out = append(out, diffOp{Op: "add", Path: ptr, After: shown(strings.TrimPrefix(rest, "unexpected "))})
		case strings.HasPrefix(rest, "expected ") && strings.HasSuffix(rest, ", missing"):
			out = append(out, diffOp{Op: "remove", Path: ptr, Before: shown(strings.TrimSuffix(strings.TrimPrefix(rest, "expected "), ", missing"))})
		default:
			body := strings.TrimPrefix(rest, "expected ")
			if i := strings.LastIndex(body, ", got "); i >= 0 {
				out = append(out, diffOp{Op: "replace", Path: ptr, Before: shown(body[:i]), After: shown(body[i+len(", got "):])})
			} else {
				out = append(out, diffOp{Op: "replace", Path: ptr, After: rest})
			}
		}
	}
	return out
}

func runGoldens(prog *transform.Program, fixtures fs.FS) goldensResult {
	start := time.Now()
	res := goldensResult{Results: []goldenCase{}}
	for _, r := range transform.RunGoldens(prog, fixtures) {
		c := goldenCase{Fixture: r.Case, Passed: r.Passed(), Diff: diffOps(r.Diffs), Error: nullable(r.Err)}
		if c.Fixture == "" {
			c.Fixture = "(none)"
		}
		res.Total++
		if !c.Passed {
			res.Failed++
		}
		res.Results = append(res.Results, c)
	}
	res.Passed = res.Total > 0 && res.Failed == 0
	res.DurationMS = time.Since(start).Milliseconds()
	return res
}

// ---- compiling drafts ----

type draft struct {
	Kind           string `json:"kind"`
	Capability     string `json:"capability"`
	VendorEndpoint string `json:"vendor_endpoint"`
	YAML           string `json:"yaml"`
}

type compileProblem struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// normalise sets the header fields of a transformer document the pipeline owns (id, version,
// kind, capability, vendor endpoint, docs URL), keeping the author's comments and layout.
func normalise(src string, id string, version int, d draft, docsURL *string) ([]byte, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(src), &m); err != nil || m == nil {
		return nil, badRequest("/yaml", "yaml must be a YAML mapping (DESIGN §7)")
	}
	t := control.NewScratchTree(map[string][]byte{"t.yaml": []byte(src)})
	set := []struct{ key, value string }{{"transformer", id}, {"version", strconv.Itoa(version)}, {"kind", d.Kind}, {"capability", d.Capability}}
	if d.VendorEndpoint != "" {
		set = append(set, struct{ key, value string }{"vendor_endpoint", d.VendorEndpoint})
	}
	for _, kv := range set {
		if err := t.SetScalar("t.yaml", kv.key, kv.value); err != nil {
			return nil, badRequest("/yaml", err.Error())
		}
	}
	if docsURL != nil {
		if err := t.SetString("t.yaml", "docs_url", *docsURL); err != nil {
			return nil, badRequest("/yaml", err.Error())
		}
	}
	b, _ := t.Get("t.yaml")
	return b, nil
}

// compileDraft compiles a draft against snap's capabilities the way the snapshot build does.
func compileDraft(snap *registry.Snapshot, d draft) (*transform.Program, []compileProblem, error) {
	if err := checkDraft(d, "/draft"); err != nil {
		return nil, nil, err
	}
	doc, err := normalise(d.YAML, "draft", 1, d, nil)
	if err != nil {
		return nil, nil, err
	}
	var tf registry.TransformerFile
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&tf); err != nil {
		return nil, []compileProblem{{Message: strings.TrimPrefix(err.Error(), "yaml: ")}}, nil //nolint:nilerr // a draft that does not compile is a result, not a failure
	}
	c, ok := snap.Capabilities[tf.Capability]
	if !ok {
		return nil, []compileProblem{{Path: "capability", Message: "unknown capability " + tf.Capability}}, nil
	}
	var op *registry.Operation
	switch {
	case tf.Operation != "":
		if op = c.Operations[tf.Operation]; op == nil {
			return nil, []compileProblem{{Path: "operation", Message: "capability " + c.Key + " has no operation " + tf.Operation}}, nil
		}
	case len(c.Operations) == 1:
		for _, o := range c.Operations {
			op = o
		}
	default:
		return nil, []compileProblem{{Path: "operation", Message: fmt.Sprintf("capability %s has %d operations: set operation", c.Key, len(c.Operations))}}, nil
	}
	spec := transform.Spec{ID: "draft", Version: 1, Kind: string(tf.Kind), Capability: tf.Capability, Request: tf.Request, Response: tf.Response,
		Errors: tf.Errors, Batch: tf.Batch, Session: tf.Session, AllowCredsInBody: tf.AllowCredsInBody}
	prog, err := transform.Compile(spec, transform.Options{RequestSchema: op.RequestSchema, ValidateResponse: op.Response.Validate})
	var ce *transform.CompileError
	if errors.As(err, &ce) {
		ps := make([]compileProblem, 0, len(ce.Problems))
		for _, p := range ce.Problems {
			ps = append(ps, compileProblem{Path: p.Path, Message: p.Message})
		}
		return nil, ps, nil
	}
	if err != nil {
		return nil, []compileProblem{{Message: err.Error()}}, nil //nolint:nilerr // a draft that does not compile is a result, not a failure
	}
	return prog, nil, nil
}

func checkDraft(d draft, at string) error {
	switch {
	case d.Kind != string(registry.KindRequest) && d.Kind != string(registry.KindResponse):
		return badRequest(at+"/kind", "kind must be request or response (inbound transformers are not supported by this build)")
	case !capKeyRE.MatchString(d.Capability):
		return badRequest(at+"/capability", "capability must be <name>@v<major>")
	case !vendorEndpointRE.MatchString(d.VendorEndpoint):
		return badRequest(at+"/vendor_endpoint", "vendor_endpoint must be <vendor>.<endpoint>@<version>")
	case strings.TrimSpace(d.YAML) == "":
		return badRequest(at+"/yaml", "yaml is required")
	}
	return nil
}

// ---- views ----

// pairedEndpoint is the vendor endpoint of a response transformer: its own, else the endpoint
// of the request transformer it is paired with in an integration target.
func pairedEndpoint(snap *registry.Snapshot, ref registry.TransformerRef) string {
	tf := snap.Transformers[ref]
	if tf.VendorEndpoint != "" {
		return tf.VendorEndpoint
	}
	for _, id := range slices.Sorted(maps.Keys(snap.Integrations)) {
		in := snap.Integrations[id]
		for _, n := range slices.Sorted(maps.Keys(in.Versions)) {
			for _, t := range targetsOf(in.Versions[n]) {
				for _, pair := range t.Pairs() {
					if pair.Response == ref {
						if req := snap.Transformers[pair.Request]; req != nil && req.VendorEndpoint != "" {
							return req.VendorEndpoint
						}
					}
				}
			}
		}
	}
	return ""
}

func targetsOf(v *registry.IntegrationVersion) []registry.Target {
	ts := append(slices.Clone(v.Targets), v.Failover...)
	if v.Shadow != nil {
		ts = append(ts, v.Shadow.Target)
	}
	return ts
}

// usedByActive counts integrations whose active version references transformer id.
func usedByActive(snap *registry.Snapshot, id string) int {
	n := 0
	for _, in := range snap.Integrations {
		v := in.Active()
		if v == nil {
			continue
		}
		if slices.ContainsFunc(targetsOf(v), func(t registry.Target) bool {
			return slices.ContainsFunc(t.Pairs(), func(p registry.TransformerPair) bool { return p.Request.ID == id || p.Response.ID == id })
		}) {
			n++
		}
	}
	return n
}

func versionsOf(snap *registry.Snapshot, id string) []int {
	var out []int
	for ref := range snap.Transformers {
		if ref.ID == id {
			out = append(out, ref.Version)
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) transformerMeta(ctx context.Context, src registry.Sources, snap *registry.Snapshot, ref registry.TransformerRef) (transformerMeta, error) {
	var m transformerMeta
	if err := readMeta(srcGet(src), transformerMetaFile(ref), &m); err != nil {
		return m, err
	}
	if m.Author == "" {
		created, _ := s.objectTimes(ctx, "transformer", ref.String(), snap.BuiltAt)
		m = transformerMeta{Author: bootstrapAuthor, Reason: "Bootstrap config tree", CreatedAt: created}
	}
	return m, nil
}

// versionView is the contract's TransformerVersion (full=false: TransformerVersionSummary).
func (s *Server) versionView(ctx context.Context, src registry.Sources, snap *registry.Snapshot, ref registry.TransformerRef, full bool) (map[string]any, error) {
	tf := snap.Transformers[ref]
	m, err := s.transformerMeta(ctx, src, snap, ref)
	if err != nil {
		return nil, err
	}
	dir := registry.FixtureDir(src.Config, ref)
	var goldens goldensResult
	if prog := snap.Programs[ref]; prog != nil {
		sub, err := fs.Sub(src.Config, dir)
		if err != nil {
			return nil, err
		}
		goldens = runGoldens(prog, sub)
	}
	out := map[string]any{"transformer_id": ref.ID, "version": ref.Version, "ref": ref.String(), "author": m.Author, "reason": m.Reason,
		"created_at": m.CreatedAt.UTC()}
	if !full {
		out["goldens_passed"] = goldens.Passed
		return out, nil
	}
	doc, err := fs.ReadFile(src.Config, transformerFile(ref))
	if err != nil {
		return nil, err
	}
	fixtures, err := readFixtures(src.Config, dir, string(tf.Kind))
	if err != nil {
		return nil, err
	}
	out["kind"], out["capability"], out["vendor_endpoint"] = string(tf.Kind), tf.Capability, pairedEndpoint(snap, ref)
	out["yaml"], out["fixtures"], out["docs_url"] = string(doc), fixtures, nullable(tf.DocsURL)
	out["goldens"] = map[string]any{"passed": goldens.Passed, "total": goldens.Total, "failed": goldens.Failed}
	return out, nil
}

func (s *Server) summaryView(ctx context.Context, src registry.Sources, snap *registry.Snapshot, id string) (map[string]any, registry.TransformerRef, error) {
	vs := versionsOf(snap, id)
	ref := registry.TransformerRef{ID: id, Version: vs[len(vs)-1]}
	tf := snap.Transformers[ref]
	m, err := s.transformerMeta(ctx, src, snap, ref)
	if err != nil {
		return nil, ref, err
	}
	return map[string]any{"transformer_id": id, "kind": string(tf.Kind), "capability": tf.Capability, "vendor_endpoint": pairedEndpoint(snap, ref),
		"latest_version": ref.Version, "used_by_active_versions": usedByActive(snap, id), "updated_at": m.CreatedAt.UTC()}, ref, nil
}

// ---- read handlers ----

func (s *Server) listTransformers(w http.ResponseWriter, r *http.Request) {
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	ids := map[string]bool{}
	for ref := range snap.Transformers {
		ids[ref.ID] = true
	}
	var keep []string
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		vs := versionsOf(snap, id)
		ref := registry.TransformerRef{ID: id, Version: vs[len(vs)-1]}
		tf := snap.Transformers[ref]
		ep := pairedEndpoint(snap, ref)
		vendor, _, _ := strings.Cut(ep, ".")
		if (q.Get("kind") != "" && q.Get("kind") != string(tf.Kind)) || (q.Get("capability") != "" && q.Get("capability") != tf.Capability) ||
			(q.Get("vendor_id") != "" && q.Get("vendor_id") != vendor) || !matchQ(r, id) {
			continue
		}
		keep = append(keep, id)
	}
	pg, next, err := pageOf(r, keep)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(pg))
	for _, id := range pg {
		v, _, err := s.summaryView(r.Context(), src, snap, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getTransformer(w http.ResponseWriter, r *http.Request) {
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("transformer_id")
	if len(versionsOf(snap, id)) == 0 {
		s.fail(w, r, notFound("Transformer "+id))
		return
	}
	out, ref, err := s.summaryView(r.Context(), src, snap, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if out["latest"], err = s.versionView(r.Context(), src, snap, ref, true); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listTransformerVersions(w http.ResponseWriter, r *http.Request) {
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("transformer_id")
	vs := versionsOf(snap, id)
	if len(vs) == 0 {
		s.fail(w, r, notFound("Transformer "+id))
		return
	}
	keys := make([]string, 0, len(vs))
	for _, v := range slices.Backward(vs) {
		keys = append(keys, strconv.Itoa(v))
	}
	pg, next, err := pageOf(r, keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(pg))
	for _, k := range pg {
		n, _ := strconv.Atoi(k)
		v, err := s.versionView(r.Context(), src, snap, registry.TransformerRef{ID: id, Version: n}, false)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items = append(items, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getTransformerVersion(w http.ResponseWriter, r *http.Request) {
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := strconv.Atoi(r.PathValue("version"))
	ref := registry.TransformerRef{ID: r.PathValue("transformer_id"), Version: n}
	if _, ok := snap.Transformers[ref]; err != nil || !ok {
		s.fail(w, r, notFound("Transformer version "+ref.ID+"@"+r.PathValue("version")))
		return
	}
	out, err := s.versionView(r.Context(), src, snap, ref, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- create ----

func (s *Server) createTransformerVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		draft
		ExpectedLatestVersion *int      `json:"expected_latest_version"`
		Fixtures              []fixture `json:"fixtures"`
		DocsURL               *string   `json:"docs_url"`
		Reason                string    `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	reason, err := reasonOf(r, body.Reason)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := r.PathValue("transformer_id")
	if !transformerIDRE.MatchString(id) {
		s.fail(w, r, badRequest("", "transformer_id must match "+transformerIDRE.String()))
		return
	}
	if err := checkDraft(body.draft, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.DocsURL == nil || !httpURL(*body.DocsURL) {
		s.fail(w, r, badRequest("/docs_url", "docs_url must cite the vendor documentation the mapping was checked against (AGENTS §3.3)"))
		return
	}
	files, err := fixtureFiles(body.Kind, body.Fixtures, "/fixtures")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Publisher == nil {
		s.fail(w, r, errors.New("no publisher"))
		return
	}
	var ref registry.TransformerRef
	meta := transformerMeta{Author: principal(r).Actor(), Reason: reason, CreatedAt: s.d.Now().UTC().Truncate(time.Millisecond)}
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		vs := versionsOf(snap, id)
		latest := 0
		if len(vs) > 0 {
			latest = vs[len(vs)-1]
		}
		switch {
		case latest == 0 && body.ExpectedLatestVersion != nil:
			return control.Change{}, fmt.Errorf("%w: transformer %s does not exist: omit expected_latest_version to create version 1", control.ErrConflict, id)
		case latest > 0 && (body.ExpectedLatestVersion == nil || *body.ExpectedLatestVersion != latest):
			return control.Change{}, fmt.Errorf("%w: the latest version of %s is %d: send expected_latest_version %d", control.ErrConflict, id, latest, latest)
		case latest > 0 && string(snap.Transformers[registry.TransformerRef{ID: id, Version: latest}].Kind) != body.Kind:
			return control.Change{}, fmt.Errorf("%w: transformer %s is a %s transformer", control.ErrConflict, id, snap.Transformers[registry.TransformerRef{ID: id, Version: latest}].Kind)
		}
		ref = registry.TransformerRef{ID: id, Version: latest + 1}
		file := transformerFile(ref)
		dir := fmt.Sprintf("transformers/%s/fixtures/v%d", ref.ID, ref.Version)
		if _, exists := t.Get(file); exists || len(t.Paths(dir)) > 0 {
			return control.Change{}, fmt.Errorf("%w: %s exists: versions are immutable", control.ErrConflict, file)
		}
		doc, err := normalise(body.YAML, id, ref.Version, body.draft, body.DocsURL)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(file, doc)
		for name, b := range files {
			t.Put(dir+"/"+name, b)
		}
		if err := putJSON(t, transformerMetaFile(ref), meta); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: meta.Author, Action: "createTransformerVersion", ObjectType: "transformer", ObjectID: ref.String(), Reason: reason,
			After: map[string]any{"version": ref.Version, "kind": body.Kind, "capability": body.Capability, "vendor_endpoint": body.VendorEndpoint,
				"fixtures": len(body.Fixtures)}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if snap.Checksum != res.Snapshot.Checksum { // a later publish already landed: show the version as built here
		snap = res.Snapshot
	}
	v, err := s.versionView(r.Context(), src, snap, ref, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "transformer_version": v})
}

// ---- dry-run and goldens ----

// resolve returns the program of a stored ref or a compiled draft; problems are draft compile
// problems (the request is still answered).
func (s *Server) resolve(src registry.Sources, snap *registry.Snapshot, refStr string, d *draft) (prog *transform.Program, tf draft, problems []compileProblem, err error) {
	switch {
	case (refStr == "") == (d == nil):
		return nil, draft{}, nil, badRequest("", "give exactly one of ref or draft")
	case d != nil:
		prog, problems, err = compileDraft(snap, *d)
		return prog, *d, problems, err
	}
	ref, perr := registry.ParseTransformerRef(refStr)
	if perr != nil {
		return nil, draft{}, nil, badRequest("/ref", perr.Error())
	}
	stored, ok := snap.Transformers[ref]
	if !ok || snap.Programs[ref] == nil {
		return nil, draft{}, nil, notFound("Transformer version " + refStr)
	}
	doc, _ := fs.ReadFile(src.Config, transformerFile(ref))
	return snap.Programs[ref], draft{Kind: string(stored.Kind), Capability: stored.Capability, VendorEndpoint: pairedEndpoint(snap, ref), YAML: string(doc)}, nil, nil
}

func (s *Server) dryRunTransformer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref            string            `json:"ref"`
		Draft          *draft            `json:"draft"`
		AccountID      string            `json:"account_id"`
		Input          map[string]any    `json:"input"`
		Headers        map[string]string `json:"headers"`
		Session        map[string]any    `json:"session"`
		VendorResponse *struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    any               `json:"body"`
		} `json:"vendor_response"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prog, d, problems, err := s.resolve(src, snap, body.Ref, body.Draft)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := map[string]any{"ok": false, "kind": d.Kind, "vendor_request": nil, "canonical_response": nil, "canonical_error": nil,
		"compile_errors": []compileProblem{}, "duration_us": 0}
	if len(problems) > 0 {
		out["compile_errors"] = problems
		writeJSON(w, http.StatusOK, out)
		return
	}
	vendorID, _, _ := strings.Cut(d.VendorEndpoint, ".")
	vendor := snap.Vendors[vendorID]
	// Credentials are never read here: every creds.<key> the transformer names gets a
	// placeholder, and DryRun masks it in the output.
	in := transform.Input{Req: body.Input, Session: body.Session, Creds: placeholderCreds(d.YAML), Now: s.d.Now(),
		UUID: func() string { return "00000000-0000-4000-8000-000000000000" }}
	if len(body.Headers) > 0 {
		in.Hdr = map[string]any{}
		for k, v := range body.Headers {
			in.Hdr[strings.ToLower(k)] = v
		}
	}
	base := ""
	if body.AccountID != "" {
		a, ok := snap.Accounts[body.AccountID]
		if !ok {
			s.fail(w, r, notFound("Account "+body.AccountID))
			return
		}
		cfg := make(map[string]any, len(a.Config))
		for k, v := range a.Config {
			cfg[k] = v
		}
		in.Account = map[string]any{"id": a.Account, "region": a.Region, "config": cfg}
		if v := snap.Vendors[a.Vendor]; v != nil {
			base = v.Regions[a.Region]
		}
	}
	start := time.Now()
	runErr := func(err error) {
		p := compileProblem{Message: err.Error()}
		var ee *transform.EvalError
		if errors.As(err, &ee) {
			p.Path = ee.Path
			if ee.Detail() != "" { // the caller's own dry-run input, never stored or logged
				p.Message += " (" + ee.Detail() + ")"
			}
		}
		out["compile_errors"] = []compileProblem{p}
	}
	if d.Kind == string(registry.KindRequest) {
		if body.Input == nil {
			s.fail(w, r, badRequest("/input", "input (the canonical request) is required for a request transformer"))
			return
		}
		req, err := prog.DryRun(in)
		if err != nil {
			runErr(err)
		} else {
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			if vendor != nil && (vendor.Auth == creds.SchemeBearer || vendor.Auth == creds.SchemeBasic) {
				req.Headers["Authorization"] = transform.Redacted // the vendor auth scheme adds it after the transformer
			}
			out["ok"] = true
			out["vendor_request"] = map[string]any{"method": req.Method, "url": joinURL(base, req), "headers": req.Headers, "body": req.Body}
		}
	} else {
		if body.VendorResponse == nil {
			s.fail(w, r, badRequest("/vendor_response", "vendor_response is required for a response transformer"))
			return
		}
		resp := core.VendorResponse{Status: body.VendorResponse.Status, Headers: map[string][]string{}, Body: body.VendorResponse.Body}
		for k, v := range body.VendorResponse.Headers {
			resp.Headers[k] = []string{v}
		}
		mapped, _, ce, err := prog.MapResponseSession(resp, in)
		switch {
		case err != nil:
			runErr(err)
		case ce != nil:
			out["ok"] = true
			out["canonical_error"] = ce
		default:
			out["ok"] = true
			if m, isObj := mapped.(map[string]any); isObj {
				out["canonical_response"] = m
			} else {
				out["ok"] = false
				out["compile_errors"] = []compileProblem{{Path: "response", Message: "the mapped response is not an object"}}
			}
		}
	}
	out["duration_us"] = time.Since(start).Microseconds()
	writeJSON(w, http.StatusOK, out)
}

var credsRef = regexp.MustCompile(`\bcreds\.([A-Za-z_][A-Za-z0-9_]*)`)

// placeholderCreds gives every credential key a transformer document reads a fake value.
func placeholderCreds(doc string) map[string]string {
	out := map[string]string{}
	for _, m := range credsRef.FindAllStringSubmatch(doc, -1) {
		out[m[1]] = "dry-run-placeholder-" + m[1]
	}
	return out
}

// joinURL is the vendor URL of req on base (path only without an account).
func joinURL(base string, req core.VendorRequest) string {
	u := strings.TrimSuffix(base, "/") + req.Path
	if len(req.Query) > 0 {
		q := url.Values{}
		for k, v := range req.Query {
			q.Set(k, v)
		}
		u += "?" + q.Encode()
	}
	return u
}

func (s *Server) runTransformerGoldens(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref      string    `json:"ref"`
		Draft    *draft    `json:"draft"`
		Fixtures []fixture `json:"fixtures"`
	}
	if err := decode(w, r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	if body.Ref != "" && body.Fixtures != nil {
		s.fail(w, r, badRequest("/fixtures", "fixtures go with a draft; a stored ref runs its own fixtures"))
		return
	}
	src, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	prog, d, problems, err := s.resolve(src, snap, body.Ref, body.Draft)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		e := badRequest("/draft/yaml", "the draft does not compile")
		e.Details = e.Details[:0]
		for _, p := range problems {
			e.Details = append(e.Details, Detail{Pointer: "/draft/yaml", Keyword: "expressions_typecheck", Message: strings.TrimPrefix(p.Path+": "+p.Message, ": ")})
		}
		s.fail(w, r, e)
		return
	}
	var fixtures fs.FS
	if body.Draft != nil {
		files, err := fixtureFiles(d.Kind, body.Fixtures, "/fixtures")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		m := fstest.MapFS{}
		for name, b := range files {
			m[name] = &fstest.MapFile{Data: b}
		}
		fixtures = m
	} else {
		ref, _ := registry.ParseTransformerRef(body.Ref)
		if fixtures, err = registry.TransformerFixtures(src.Config, ref); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, runGoldens(prog, fixtures))
}
