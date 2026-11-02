package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

// Capabilities (DESIGN §3.1, §8.1, §16.1). A capability major is capabilities/<name>.v<major>.yaml
// of the config tree, pointing at its JSON Schemas on the contracts side
// (schemas/capabilities/<name>.v<major>[.<op>].{request,response}.json, the repo's naming). The
// schemas are written through Tree.PutContract and travel in the snapshot archive. Each minor
// is also recorded, immutably, in admin/capabilities/<name>.v<major>/<minor>.json: the live
// schema files only hold the latest minor, the records keep every one.

func init() { registrars = append(registrars, (*Server).registerCapabilities) }

func (s *Server) registerCapabilities() {
	s.handle("listCapabilities", RoleViewer, false, "GET /capabilities", s.listCapabilities)
	s.handle("createCapability", RoleEditor, true, "POST /capabilities", s.createCapability)
	s.handle("getCapability", RoleViewer, false, "GET /capabilities/{capability}", s.getCapability)
	s.handle("listCapabilityVersions", RoleViewer, false, "GET /capabilities/{capability}/versions", s.listCapabilityVersions)
	s.handle("createCapabilityVersion", RoleEditor, true, "POST /capabilities/{capability}/versions", s.createCapabilityVersion)
}

var (
	capNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	capKeyRE  = regexp.MustCompile(`^([a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+)@v([1-9][0-9]*)$`)
	opNameRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// bootstrapAuthor is the author of objects that predate the admin API (the bootstrap tree).
const bootstrapAuthor = "bootstrap"

type capOperation struct {
	Description    *string         `json:"description"`
	Idempotent     *bool           `json:"idempotent"`
	RequestSchema  json.RawMessage `json:"request_schema"`
	ResponseSchema json.RawMessage `json:"response_schema"`
}

// capRecord is one immutable capability minor (the contract's CapabilityVersion, plus summary).
type capRecord struct {
	Capability  string                  `json:"capability"`
	Name        string                  `json:"name"`
	Major       int                     `json:"major"`
	Minor       int                     `json:"minor"`
	Description *string                 `json:"description"`
	Interactive bool                    `json:"interactive"`
	Operations  map[string]capOperation `json:"operations"`
	Author      string                  `json:"author"`
	CreatedAt   time.Time               `json:"created_at"`
	Summary     *string                 `json:"summary,omitempty"`
}

func (c capRecord) view() map[string]any {
	ops := map[string]any{}
	for name, op := range c.Operations {
		ops[name] = map[string]any{"description": op.Description, "idempotent": op.Idempotent != nil && *op.Idempotent,
			"request_schema": op.RequestSchema, "response_schema": op.ResponseSchema}
	}
	return map[string]any{"capability": c.Capability, "name": c.Name, "major": c.Major, "minor": c.Minor, "description": c.Description,
		"interactive": c.Interactive, "operations": ops, "author": c.Author, "created_at": c.CreatedAt.UTC()}
}

func splitCapKey(key string) (name string, major int, ok bool) {
	m := capKeyRE.FindStringSubmatch(key)
	if m == nil {
		return "", 0, false
	}
	major, _ = strconv.Atoi(m[2])
	return m[1], major, true
}

func capFile(name string, major int) string {
	return fmt.Sprintf("capabilities/%s.v%d.yaml", name, major)
}

func capRecordDir(key string) string {
	name, major, _ := splitCapKey(key)
	return fmt.Sprintf("admin/capabilities/%s.v%d", name, major)
}

func capRecordFile(key string, minor int) string {
	return capRecordDir(key) + "/" + strconv.Itoa(minor) + ".json"
}

// schemaFile is the contracts-relative schema file of one operation side (repo naming).
func schemaFile(name string, major int, op, side string) string {
	if op == "default" {
		return fmt.Sprintf("schemas/capabilities/%s.v%d.%s.json", name, major, side)
	}
	return fmt.Sprintf("schemas/capabilities/%s.v%d.%s.%s.json", name, major, op, side)
}

// rootOp is the operation whose request file carries the capability header (schema_ref).
func rootOp(ops map[string]capOperation) string {
	if _, ok := ops["default"]; ok {
		return "default"
	}
	return slices.Sorted(maps.Keys(ops))[0]
}

// synthesized is the record of a capability minor that predates the admin API, from the snapshot.
func (s *Server) synthesized(ctx context.Context, snap *registry.Snapshot, c *registry.Capability) capRecord {
	name, major, _ := splitCapKey(c.Key)
	created, _ := s.objectTimes(ctx, "capability", c.Key, snap.BuiltAt)
	ops := map[string]capOperation{}
	for n, op := range c.Operations {
		idem := op.Idempotent
		ops[n] = capOperation{Description: nullable(op.Description), Idempotent: &idem, RequestSchema: op.RequestSchema, ResponseSchema: op.ResponseSchema}
	}
	return capRecord{Capability: c.Key, Name: name, Major: major, Minor: c.Minor, Interactive: c.Interactive, Operations: ops,
		Author: bootstrapAuthor, CreatedAt: created}
}

// capRecords returns every recorded minor of key (plus the synthesized latest when it has no
// record), newest first.
func (s *Server) capRecords(ctx context.Context, get func(string) ([]byte, bool), paths []string, snap *registry.Snapshot, c *registry.Capability) ([]capRecord, error) {
	byMinor := map[int]capRecord{}
	for _, p := range paths {
		if !strings.HasPrefix(p, capRecordDir(c.Key)+"/") || !strings.HasSuffix(p, ".json") {
			continue
		}
		var rec capRecord
		if err := readMeta(get, p, &rec); err != nil {
			return nil, err
		}
		byMinor[rec.Minor] = rec
	}
	if _, ok := byMinor[c.Minor]; !ok {
		byMinor[c.Minor] = s.synthesized(ctx, snap, c)
	}
	out := make([]capRecord, 0, len(byMinor))
	for _, m := range slices.Backward(slices.Sorted(maps.Keys(byMinor))) {
		out = append(out, byMinor[m])
	}
	return out, nil
}

func configPaths(src registry.Sources, dir string) []string {
	var out []string
	if src.Config == nil {
		return nil
	}
	_ = fs.WalkDir(src.Config, dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (s *Server) loadCapability(ctx context.Context, key string) ([]capRecord, error) {
	src, snap, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := snap.Capabilities[key]
	if !ok {
		return nil, notFound("Capability " + key)
	}
	return s.capRecords(ctx, srcGet(src), configPaths(src, capRecordDir(key)), snap, c)
}

// ---- reads ----

func (s *Server) listCapabilities(w http.ResponseWriter, r *http.Request) {
	_, snap, err := s.current(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var keys []string
	for _, k := range slices.Sorted(maps.Keys(snap.Capabilities)) {
		if matchQ(r, k) {
			keys = append(keys, k)
		}
	}
	pg, next, err := pageOf(r, keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	using := map[string]int{}
	for _, in := range snap.Integrations {
		using[in.Capability]++
	}
	items := make([]map[string]any, 0, len(pg))
	for _, k := range pg {
		c := snap.Capabilities[k]
		name, major, _ := splitCapKey(k)
		_, updated := s.objectTimes(r.Context(), "capability", k, snap.BuiltAt)
		items = append(items, map[string]any{"capability": k, "name": name, "major": major, "latest_minor": c.Minor,
			"interactive": c.Interactive, "operations": slices.Sorted(maps.Keys(c.Operations)), "integrations_using": using[k], "updated_at": updated})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getCapability(w http.ResponseWriter, r *http.Request) {
	recs, err := s.loadCapability(r.Context(), r.PathValue("capability"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recs[0].view())
}

func (s *Server) listCapabilityVersions(w http.ResponseWriter, r *http.Request) {
	recs, err := s.loadCapability(r.Context(), r.PathValue("capability"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	keys := make([]string, len(recs))
	for i, rec := range recs {
		keys[i] = strconv.Itoa(rec.Minor)
	}
	pg, next, err := pageOf(r, keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(pg))
	for _, rec := range recs {
		if !slices.Contains(pg, strconv.Itoa(rec.Minor)) {
			continue
		}
		items = append(items, map[string]any{"capability": rec.Capability, "minor": rec.Minor, "author": rec.Author,
			"created_at": rec.CreatedAt.UTC(), "summary": rec.Summary})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// ---- writes ----

// checkOperations validates the operations of a create request.
func checkOperations(ops map[string]capOperation) error {
	if len(ops) == 0 {
		return badRequest("/operations", "at least one operation is required")
	}
	for _, name := range slices.Sorted(maps.Keys(ops)) {
		op, at := ops[name], "/operations/"+name
		if !opNameRE.MatchString(name) {
			return badRequest(at, "operation names must match "+opNameRE.String())
		}
		if op.Idempotent == nil {
			return badRequest(at+"/idempotent", "idempotent is required")
		}
		for side, raw := range map[string]json.RawMessage{"request_schema": op.RequestSchema, "response_schema": op.ResponseSchema} {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil || m == nil {
				return badRequest(at+"/"+side, side+" must be a JSON Schema object")
			}
		}
	}
	return nil
}

// writeSchemas writes the live schema files of rec on the contracts side and points the
// capability file at the root request file.
func writeSchemas(t control.Tree, rec capRecord) (schemaRef string, err error) {
	root := rootOp(rec.Operations)
	header := map[string]any{}
	for name, op := range rec.Operations {
		h := map[string]any{"idempotent": *op.Idempotent,
			"request_schema":  path.Base(schemaFile(rec.Name, rec.Major, name, "request")),
			"response_schema": path.Base(schemaFile(rec.Name, rec.Major, name, "response"))}
		if op.Description != nil {
			h["description"] = *op.Description
		}
		header[name] = h
	}
	for name, op := range rec.Operations {
		for side, raw := range map[string]json.RawMessage{"request": op.RequestSchema, "response": op.ResponseSchema} {
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				return "", err
			}
			doc["$id"] = fmt.Sprintf("urn:hub:capability:%s:%s:%s", rec.Capability, name, side)
			doc["x-capability"], doc["x-capability-minor"], doc["interactive"] = rec.Capability, rec.Minor, rec.Interactive
			if side == "request" && name == root {
				doc["operations"] = header
			}
			b, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return "", err
			}
			t.PutContract(schemaFile(rec.Name, rec.Major, name, side), append(b, '\n'))
		}
	}
	return schemaFile(rec.Name, rec.Major, root, "request"), nil
}

func (s *Server) createCapability(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name        string                  `json:"name"`
		Major       int                     `json:"major"`
		Description *string                 `json:"description"`
		Interactive *bool                   `json:"interactive"`
		Operations  map[string]capOperation `json:"operations"`
		Reason      string                  `json:"reason"`
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
	switch {
	case !capNameRE.MatchString(body.Name):
		err = badRequest("/name", "name must match "+capNameRE.String())
	case body.Major < 1:
		err = badRequest("/major", "major must be at least 1")
	case body.Interactive == nil:
		err = badRequest("/interactive", "interactive is required")
	default:
		err = checkOperations(body.Operations)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Publisher == nil {
		s.fail(w, r, errors.New("no publisher"))
		return
	}
	key := fmt.Sprintf("%s@v%d", body.Name, body.Major)
	rec := capRecord{Capability: key, Name: body.Name, Major: body.Major, Description: body.Description, Interactive: *body.Interactive,
		Operations: body.Operations, Author: principal(r).Actor(), CreatedAt: s.d.Now().UTC().Truncate(time.Millisecond)}
	res, err := s.d.Publisher.Edit(r.Context(), func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		if _, exists := snap.Capabilities[key]; exists {
			return control.Change{}, fmt.Errorf("%w: capability %s exists: add a minor with createCapabilityVersion or create a new major", control.ErrConflict, key)
		}
		if _, exists := t.Get(capFile(body.Name, body.Major)); exists {
			return control.Change{}, fmt.Errorf("%w: %s exists", control.ErrConflict, capFile(body.Name, body.Major))
		}
		ref, err := writeSchemas(t, rec)
		if err != nil {
			return control.Change{}, err
		}
		t.Put(capFile(body.Name, body.Major), fmt.Appendf(nil,
			"# Created by the Hub admin API (DESIGN §16.1). Schemas: contracts side %s; minors: %s/.\ncapability: %s\nschema_ref: %s\n",
			ref, capRecordDir(key), key, ref))
		if err := putJSON(t, capRecordFile(key, 0), rec); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: rec.Author, Action: "createCapability", ObjectType: "capability", ObjectID: key, Reason: reason,
			After: map[string]any{"minor": 0, "interactive": rec.Interactive, "operations": slices.Sorted(maps.Keys(rec.Operations))}}, nil
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "capability_version": rec.view()})
}

func (s *Server) createCapabilityVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedLatestMinor *int                    `json:"expected_latest_minor"`
		Description         *string                 `json:"description"`
		Operations          map[string]capOperation `json:"operations"`
		Summary             *string                 `json:"summary"`
		Reason              string                  `json:"reason"`
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
	if body.ExpectedLatestMinor == nil || *body.ExpectedLatestMinor < 0 {
		s.fail(w, r, badRequest("/expected_latest_minor", "expected_latest_minor is required (≥ 0)"))
		return
	}
	if err := checkOperations(body.Operations); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.d.Publisher == nil {
		s.fail(w, r, errors.New("no publisher"))
		return
	}
	key := r.PathValue("capability")
	var rec capRecord
	ctx := r.Context()
	res, err := s.d.Publisher.Edit(ctx, func(t control.Tree, snap *registry.Snapshot) (control.Change, error) {
		c, ok := snap.Capabilities[key]
		if !ok {
			return control.Change{}, fmt.Errorf("%w: capability %q", control.ErrNotFound, key)
		}
		if c.Minor != *body.ExpectedLatestMinor {
			return control.Change{}, fmt.Errorf("%w: latest minor of %s is %d, not %d", control.ErrConflict, key, c.Minor, *body.ExpectedLatestMinor)
		}
		recs, err := s.capRecords(ctx, t.Get, t.Paths(capRecordDir(key)), snap, c)
		if err != nil {
			return control.Change{}, err
		}
		prev := recs[0]
		if problems := additive(prev.Operations, body.Operations); len(problems) > 0 {
			return control.Change{}, &additiveError{problems}
		}
		if _, exists := t.Get(capRecordFile(key, prev.Minor)); !exists { // keep the bootstrap minor's record too
			if err := putJSON(t, capRecordFile(key, prev.Minor), prev); err != nil {
				return control.Change{}, err
			}
		}
		desc := prev.Description
		if body.Description != nil {
			desc = body.Description
		}
		rec = capRecord{Capability: key, Name: prev.Name, Major: prev.Major, Minor: prev.Minor + 1, Description: desc, Interactive: prev.Interactive,
			Operations: body.Operations, Author: principal(r).Actor(), CreatedAt: s.d.Now().UTC().Truncate(time.Millisecond), Summary: body.Summary}
		if _, exists := t.Get(capRecordFile(key, rec.Minor)); exists {
			return control.Change{}, fmt.Errorf("%w: minor %d of %s exists: minors are immutable", control.ErrConflict, rec.Minor, key)
		}
		ref, err := writeSchemas(t, rec)
		if err != nil {
			return control.Change{}, err
		}
		if err := t.SetScalar(c.File, "schema_ref", ref); err != nil {
			return control.Change{}, err
		}
		if err := putJSON(t, capRecordFile(key, rec.Minor), rec); err != nil {
			return control.Change{}, err
		}
		return control.Change{Actor: rec.Author, Action: "createCapabilityVersion", ObjectType: "capability", ObjectID: key, Reason: reason,
			Before: map[string]any{"minor": prev.Minor}, After: map[string]any{"minor": rec.Minor, "operations": slices.Sorted(maps.Keys(rec.Operations))}}, nil
	})
	var ae *additiveError
	if errors.As(err, &ae) {
		s.fail(w, r, ae.adminError())
		return
	}
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": res.ChangeID, "capability_version": rec.view()})
}

// ---- additive-change check ----

type additiveError struct{ problems registry.ConfigErrors }

func (e *additiveError) Error() string { return e.problems.Error() }

// adminError is 422 PUBLISH_VALIDATION_FAILED whose checks are "additive_change".
func (e *additiveError) adminError() *Error {
	ae := validationFailed(e.problems)
	ae.Message = fmt.Sprintf("%d non-additive change(s): a breaking change needs a new major", len(e.problems))
	for i := range ae.Details {
		ae.Details[i].Keyword = "additive_change"
	}
	if report, ok := ae.Meta["report"].(map[string]any); ok {
		for _, c := range report["checks"].([]map[string]any) {
			c["check"] = "additive_change"
		}
	}
	return ae
}

// additive lists why next is not an additive change of prev: an operation removed, a field
// removed or renamed, a type changed, a request constraint tightened (newly required field,
// additionalProperties closed, narrower bounds, enum values removed, a pattern or format
// added or changed), a response field no longer required, or an x-pii tag removed.
func additive(prev, next map[string]capOperation) registry.ConfigErrors {
	var ps registry.ConfigErrors
	for _, name := range slices.Sorted(maps.Keys(prev)) {
		n, ok := next[name]
		if !ok {
			ps = append(ps, registry.Problem{Path: "operations." + name, Message: "operation " + name + " was removed"})
			continue
		}
		for _, side := range []string{"request_schema", "response_schema"} {
			var o, nn any
			if side == "request_schema" {
				_ = json.Unmarshal(prev[name].RequestSchema, &o)
				_ = json.Unmarshal(n.RequestSchema, &nn)
			} else {
				_ = json.Unmarshal(prev[name].ResponseSchema, &o)
				_ = json.Unmarshal(n.ResponseSchema, &nn)
			}
			compat("operations."+name+"."+side, o, nn, side == "request_schema", &ps)
		}
	}
	return ps
}

var (
	upperBounds = []string{"maxLength", "maximum", "exclusiveMaximum", "maxItems", "maxProperties"}
	lowerBounds = []string{"minLength", "minimum", "exclusiveMinimum", "minItems", "minProperties"}
	exactBounds = []string{"pattern", "format", "const", "multipleOf"}
)

func compat(at string, oldAny, newAny any, request bool, ps *registry.ConfigErrors) {
	o, _ := oldAny.(map[string]any)
	n, _ := newAny.(map[string]any)
	if o == nil || n == nil {
		return
	}
	add := func(p, format string, args ...any) {
		*ps = append(*ps, registry.Problem{Path: p, Message: fmt.Sprintf(format, args...)})
	}
	if ot, ok := o["type"]; ok && !reflect.DeepEqual(ot, n["type"]) {
		add(at+".type", "the schema type changed from %v to %v", ot, n["type"])
	}
	if pii, ok := o["x-pii"]; ok && !reflect.DeepEqual(pii, n["x-pii"]) {
		add(at+".x-pii", "the x-pii tag %v was removed or changed (audit masking depends on it)", pii)
	}
	oreq, nreq := strings2(o["required"]), strings2(n["required"])
	if request {
		for _, f := range nreq {
			if !slices.Contains(oreq, f) {
				add(at+".required", "field %q is newly required", f)
			}
		}
		for _, k := range upperBounds {
			if nv, ok := num(n[k]); ok {
				if ov, had := num(o[k]); !had || nv < ov {
					add(at+"."+k, "%s was tightened", k)
				}
			}
		}
		for _, k := range lowerBounds {
			if nv, ok := num(n[k]); ok {
				if ov, had := num(o[k]); !had || nv > ov {
					add(at+"."+k, "%s was tightened", k)
				}
			}
		}
		for _, k := range exactBounds {
			if nv, ok := n[k]; ok && !reflect.DeepEqual(nv, o[k]) {
				add(at+"."+k, "%s was added or changed", k)
			}
		}
		if ne, ok := n["enum"].([]any); ok {
			oe, had := o["enum"].([]any)
			for _, v := range oe {
				if !slices.ContainsFunc(ne, func(x any) bool { return reflect.DeepEqual(x, v) }) {
					add(at+".enum", "enum value %v was removed", v)
				}
			}
			if !had {
				add(at+".enum", "an enum was added")
			}
		}
	} else {
		for _, f := range oreq {
			if !slices.Contains(nreq, f) {
				add(at+".required", "response field %q is no longer required", f)
			}
		}
	}
	if n["additionalProperties"] == false && o["additionalProperties"] != false {
		add(at+".additionalProperties", "additionalProperties was closed")
	}
	op, _ := o["properties"].(map[string]any)
	np, _ := n["properties"].(map[string]any)
	for _, k := range slices.Sorted(maps.Keys(op)) {
		nv, ok := np[k]
		if !ok {
			add(at+".properties."+k, "field %q was removed or renamed", k)
			continue
		}
		compat(at+".properties."+k, op[k], nv, request, ps)
	}
	compat(at+".items", o["items"], n["items"], request, ps)
}

func strings2(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}
