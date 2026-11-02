package adminapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func init() { registrars = append(registrars, (*Server).registerIntegrationDrafts) }

func (s *Server) registerIntegrationDrafts() {
	s.handle("createIntegrationDraft", RoleEditor, true, "POST /integrations/{integration_id}/versions", s.createIntegrationDraft)
	s.handle("updateIntegrationDraft", RoleEditor, true, "PUT /integrations/{integration_id}/versions/{version}", s.updateIntegrationDraft)
	s.handle("validateIntegrationVersion", RoleEditor, false, "POST /integrations/{integration_id}/versions/{version}/validate", s.validateIntegrationVersion)
}

// decodeSpec reads a contract IntegrationSpec strictly and converts it (pointer prefix /spec).
func decodeSpec(raw json.RawMessage, c *catalog, in *registry.Integration) (*registry.IntegrationVersion, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var spec apiSpec
	if err := dec.Decode(&spec); err != nil {
		return nil, badRequest("/spec", "spec is not a valid IntegrationSpec: "+err.Error())
	}
	var ttl time.Duration
	if capability, ok := c.snap.Capabilities[in.Capability]; ok {
		ttl = capability.DefaultTTL
	}
	return fromAPISpec(spec, "/spec", ttl)
}

// mutationTarget loads the integration of a mutating call (no tenant scoping: editors and up).
func (s *Server) mutationTarget(r *http.Request) (*catalog, *registry.Integration, error) {
	if s.d.Pool == nil || s.d.Publisher == nil {
		return nil, nil, errors.New("no database or publisher")
	}
	c, err := s.catalog(r.Context())
	if err != nil {
		return nil, nil, err
	}
	in, ok := c.all[r.PathValue("integration_id")]
	if !ok {
		return c, nil, notFound("Integration")
	}
	return c, in, nil
}

func (s *Server) createIntegrationDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BasedOn *int            `json:"based_on"`
		Spec    json.RawMessage `json:"spec"`
		Reason  string          `json:"reason"`
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
	c, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	basedOn := body.BasedOn
	if basedOn == nil && in.ActiveVersion > 0 {
		basedOn = &in.ActiveVersion
	}
	if basedOn != nil {
		if _, ok := in.Versions[*basedOn]; !ok {
			s.fail(w, r, badRequest("/based_on", "based_on names no published version of this integration"))
			return
		}
	}
	var v *registry.IntegrationVersion
	switch {
	case len(body.Spec) > 0 && string(body.Spec) != "null":
		if v, err = decodeSpec(body.Spec, c, in); err != nil {
			s.fail(w, r, err)
			return
		}
	case basedOn == nil:
		s.fail(w, r, badRequest("/spec", "spec is required: the integration has no active version to copy"))
		return
	default:
		v = in.Versions[*basedOn]
	}
	doc, err := versionYAML(v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n := latestVersion(in) + 1
	d, changeID, err := control.CreateDraft(r.Context(), s.d.Pool,
		control.Draft{Integration: in.Integration, Spec: doc, BasedOn: basedOn, Author: principal(r).Actor(), Reason: reason},
		control.Change{Actor: principal(r).Actor(), Action: "createIntegrationDraft", ObjectType: "integration", ObjectID: in.Integration,
			Reason: reason, After: map[string]any{"version": n, "state": "draft", "based_on": basedOn, "spec": renderSpec(v)}})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	view, err := draftView(in, d)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"change_id": changeID, "integration_version": view})
}

// draftAt checks that version n of in is its draft: a published number is 409 (immutable),
// anything else 404.
func draftAt(c *catalog, in *registry.Integration, n int) (control.Draft, error) {
	if _, published := in.Versions[n]; published {
		return control.Draft{}, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: "Published versions are immutable: create a draft instead"}
	}
	d, ok := c.draft(in.Integration)
	if !ok || n != latestVersion(in)+1 {
		return control.Draft{}, notFound("Draft version")
	}
	return d, nil
}

func (s *Server) updateIntegrationDraft(w http.ResponseWriter, r *http.Request) {
	n, err := pathVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var body struct {
		Spec   json.RawMessage `json:"spec"`
		Reason string          `json:"reason"`
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
	if len(body.Spec) == 0 || string(body.Spec) == "null" {
		s.fail(w, r, badRequest("/spec", "spec is required"))
		return
	}
	c, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := draftAt(c, in, n); err != nil {
		s.fail(w, r, err)
		return
	}
	v, err := decodeSpec(body.Spec, c, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	doc, err := versionYAML(v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, changeID, err := control.UpdateDraft(r.Context(), s.d.Pool, in.Integration, doc, reason, func(before control.Draft) control.Change {
		ch := control.Change{Actor: principal(r).Actor(), Action: "updateIntegrationDraft", ObjectType: "integration", ObjectID: in.Integration,
			Reason: reason, After: map[string]any{"version": n, "state": "draft", "spec": renderSpec(v)}}
		if old, err := parseVersionYAML(before.Spec); err == nil {
			ch.Before = map[string]any{"version": n, "state": "draft", "spec": renderSpec(old)}
		}
		return ch
	})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	view, err := draftView(in, d)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": changeID, "integration_version": view})
}

// knownChecks are the DESIGN §6.4 checks a validation report lists (contract ValidationCheck).
var knownChecks = []string{"schemas_compile", "expressions_typecheck", "goldens_pass", "response_conforms", "references_exist",
	"accounts_not_retired", "interactive_sync_only", "weights_sum_100", "delivery_path_declared"}

// validationReport lists every known check: failed ones once per problem, the others as pass.
func validationReport(problems registry.ConfigErrors) map[string]any {
	failed := map[string][]map[string]any{}
	for _, p := range problems {
		check, msg := checkOf(p), p.Message
		if p.File != "" {
			msg = p.File + ": " + msg
		}
		failed[check] = append(failed[check], map[string]any{"check": check, "status": "fail", "message": msg, "pointer": nullable(pointer(p.Path))})
	}
	checks := []map[string]any{}
	for _, name := range knownChecks {
		if f, ok := failed[name]; ok { // checkOf only names known checks
			checks = append(checks, f...)
			continue
		}
		checks = append(checks, map[string]any{"check": name, "status": "pass", "message": nil, "pointer": nil})
	}
	return map[string]any{"valid": len(problems) == 0, "checks": checks}
}

// versionRequest is the publish request of version n of in: its draft, or a copy of a published
// version (re-activating it), with integration.yaml when the integration is not published yet.
func versionRequest(c *catalog, in *registry.Integration, n int) (control.VersionRequest, *control.Draft, error) {
	req := control.VersionRequest{Integration: in.Integration, Activate: true}
	if p, ok := c.pending[in.Integration]; ok {
		req.Header = p.Header
	}
	if v, ok := in.Versions[n]; ok {
		doc, err := versionYAML(v)
		req.Spec = doc
		return req, nil, err
	}
	d, ok := c.draft(in.Integration)
	if !ok || n != latestVersion(in)+1 {
		return req, nil, notFound("Integration version")
	}
	req.Spec, req.Version = d.Spec, n
	return req, &d, nil
}

// validateIntegrationVersion runs the publish checks on version n (a draft, or a published
// version as if it were re-activated) and returns the report without publishing anything.
func (s *Server) validateIntegrationVersion(w http.ResponseWriter, r *http.Request) {
	n, err := pathVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req, _, err := versionRequest(c, in, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, published := in.Versions[n]; published {
		req.Version = 0
	}
	err = s.d.Publisher.Check(r.Context(), req)
	var problems registry.ConfigErrors
	switch {
	case errors.As(err, &problems):
		writeJSON(w, http.StatusOK, validationReport(specProblems(problems, in.Integration)))
	case err != nil:
		s.fail(w, r, controlError(err))
	default:
		writeJSON(w, http.StatusOK, validationReport(nil))
	}
}
