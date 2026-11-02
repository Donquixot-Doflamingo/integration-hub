package adminapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
)

func init() { registrars = append(registrars, (*Server).registerIntegrationPublish) }

func (s *Server) registerIntegrationPublish() {
	s.handle("publishIntegrationVersion", RoleEditor, true, "POST /integrations/{integration_id}/versions/{version}/publish", s.publishIntegrationVersion)
	s.handle("rollbackIntegration", RoleEditor, true, "POST /integrations/{integration_id}/rollback", s.rollbackIntegration)
	s.handle("updateIntegrationWeights", RoleEditor, true, "POST /integrations/{integration_id}/weights", s.updateIntegrationWeights)
}

func versionOrNull(n int) *int {
	if n == 0 {
		return nil
	}
	return &n
}

// publishError maps a publish refusal: another publish first (409 with both versions in meta),
// failed checks (422 with contract pointers), the rest through controlError.
func publishError(err error, integration string) error {
	var conflict *control.ActiveVersionConflictError
	var problems registry.ConfigErrors
	switch {
	case errors.As(err, &conflict):
		return &Error{Status: http.StatusConflict, Code: CodeConflict, Details: []Detail{},
			Message: fmt.Sprintf("Active version is %d, expected %d", conflict.Actual, conflict.Expected),
			Meta:    map[string]any{"expected_active_version": versionOrNull(conflict.Expected), "actual_active_version": versionOrNull(conflict.Actual)}}
	case errors.As(err, &problems):
		return controlError(specProblems(problems, integration))
	}
	return controlError(err)
}

// publishResult is the contract's PublishResult.
func publishResult(res control.Result, integration string, previous int, copiedFrom *int) map[string]any {
	return map[string]any{"change_id": res.ChangeID, "integration_id": integration, "previous_active_version": versionOrNull(previous),
		"active_version": res.Version, "created_version": res.Version, "copied_from_version": copiedFrom,
		"snapshot_version": res.ArchiveVersion, "published_at": time.Now().UTC()}
}

// expectedActive reads a required expected_active_version that may be null (0).
func expectedActive(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, badRequest("/expected_active_version", "expected_active_version is required (null for the first version)")
	}
	var v *int
	if err := json.Unmarshal(raw, &v); err != nil || (v != nil && *v < 1) {
		return 0, badRequest("/expected_active_version", "expected_active_version must be a version number or null")
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

// publishIntegrationVersion publishes the draft (version n) and makes it active.
func (s *Server) publishIntegrationVersion(w http.ResponseWriter, r *http.Request) {
	n, err := pathVersion(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var body struct {
		ExpectedActiveVersion json.RawMessage `json:"expected_active_version"`
		Reason                string          `json:"reason"`
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
	expected, err := expectedActive(body.ExpectedActiveVersion)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, published := in.Versions[n]; published {
		s.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: fmt.Sprintf("Version %d is already published: roll back to it instead", n)})
		return
	}
	req, draft, err := versionRequest(c, in, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	req.ExpectActive, req.Author, req.Reason = &expected, principal(r).Actor(), reason
	req.After = map[string]any{"based_on": draft.BasedOn, "copied_from_version": draft.CopiedFrom, "created_at": draft.CreatedAt, "author": draft.Author}
	res, err := s.d.Publisher.Publish(r.Context(), req)
	if err != nil {
		s.fail(w, r, publishError(err, in.Integration))
		return
	}
	// The version is in the archive now; the draft (and a pending integration) row is spent.
	// A failed delete leaves a stale row that the next publish or draft write replaces.
	ctx := r.Context()
	if err := control.DeleteDraft(ctx, s.d.Pool, in.Integration); err != nil {
		s.d.Log.WarnContext(ctx, "published draft not removed", slog.String("integration", in.Integration), slog.String("error", err.Error()))
	}
	if req.Header != nil {
		if err := control.DeletePending(ctx, s.d.Pool, in.Integration); err != nil {
			s.d.Log.WarnContext(ctx, "published integration still pending", slog.String("integration", in.Integration), slog.String("error", err.Error()))
		}
	}
	writeJSON(w, http.StatusOK, publishResult(res, in.Integration, expected, draft.CopiedFrom))
}

// rollbackIntegration publishes a copy of to_version as the next version (v9 = v7).
func (s *Server) rollbackIntegration(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ToVersion             int    `json:"to_version"`
		ExpectedActiveVersion int    `json:"expected_active_version"`
		Reason                string `json:"reason"`
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
	case body.ToVersion < 1:
		s.fail(w, r, badRequest("/to_version", "to_version must be a version number"))
		return
	case body.ExpectedActiveVersion < 1:
		s.fail(w, r, badRequest("/expected_active_version", "expected_active_version must be a version number"))
		return
	}
	_, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, ok := in.Versions[body.ToVersion]; !ok {
		s.fail(w, r, notFound(fmt.Sprintf("Version %d", body.ToVersion)))
		return
	}
	to := body.ToVersion
	res, err := s.d.Publisher.Publish(r.Context(), control.VersionRequest{Integration: in.Integration, Derive: control.CopyVersion(in.Integration, to),
		Activate: true, ExpectActive: &body.ExpectedActiveVersion, Action: "rollbackIntegration", Author: principal(r).Actor(), Reason: reason,
		After: map[string]any{"copied_from_version": to, "based_on": body.ExpectedActiveVersion}})
	if err != nil {
		s.fail(w, r, publishError(err, in.Integration))
		return
	}
	writeJSON(w, http.StatusOK, publishResult(res, in.Integration, body.ExpectedActiveVersion, &to))
}

type weightEntry struct {
	VendorID  string `json:"vendor_id"`
	AccountID string `json:"account_id"`
	Weight    int    `json:"weight"`
}

// reweigh copies v with new target weights; the (vendor, account) set must equal v's targets.
func reweigh(v *registry.IntegrationVersion, weights []weightEntry) ([]byte, error) {
	if len(weights) != len(v.Targets) {
		return nil, badRequest("/weights", fmt.Sprintf("weights must name exactly the active version's %d target(s)", len(v.Targets)))
	}
	cp := *v
	cp.Targets = slices.Clone(v.Targets)
	seen := make([]bool, len(cp.Targets))
	for i, w := range weights {
		at := slices.IndexFunc(cp.Targets, func(t registry.Target) bool { return t.Vendor == w.VendorID && t.Account == w.AccountID })
		if at < 0 || seen[at] {
			return nil, badRequest(fmt.Sprintf("/weights/%d", i), fmt.Sprintf("%s/%s is not a target of the active version (or is named twice)", w.VendorID, w.AccountID))
		}
		seen[at] = true
		cp.Targets[at].Weight = w.Weight
	}
	return yaml.Marshal(&cp)
}

// updateIntegrationWeights creates version n+1 from the active version with new weights, and
// publishes it unless publish is false (then it is the draft).
func (s *Server) updateIntegrationWeights(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedActiveVersion int           `json:"expected_active_version"`
		Weights               []weightEntry `json:"weights"`
		Publish               *bool         `json:"publish"`
		Reason                string        `json:"reason"`
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
	case body.ExpectedActiveVersion < 1:
		s.fail(w, r, badRequest("/expected_active_version", "expected_active_version must be a version number"))
		return
	case len(body.Weights) == 0:
		s.fail(w, r, badRequest("/weights", "weights must name every target"))
		return
	}
	_, in, err := s.mutationTarget(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expected := body.ExpectedActiveVersion
	actor := principal(r).Actor()
	if body.Publish == nil || *body.Publish {
		res, err := s.d.Publisher.Publish(r.Context(), control.VersionRequest{Integration: in.Integration, Activate: true, ExpectActive: &expected,
			Derive: func(cur *registry.Integration) ([]byte, error) {
				if cur == nil || cur.Active() == nil {
					return nil, &Error{Status: http.StatusConflict, Code: CodeConflict, Message: "The integration has no active version to reweigh"}
				}
				return reweigh(cur.Active(), body.Weights)
			},
			Action: "updateIntegrationWeights", Author: actor, Reason: reason,
			After: map[string]any{"copied_from_version": expected, "based_on": expected, "weights": body.Weights}})
		if err != nil {
			s.fail(w, r, publishError(err, in.Integration))
			return
		}
		writeJSON(w, http.StatusOK, publishResult(res, in.Integration, expected, &expected))
		return
	}
	// publish: false leaves the new version as the draft (not visible to nodes).
	if in.ActiveVersion != expected {
		s.fail(w, r, publishError(&control.ActiveVersionConflictError{Integration: in.Integration, Expected: expected, Actual: in.ActiveVersion}, in.Integration))
		return
	}
	doc, err := reweigh(in.Active(), body.Weights)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n := latestVersion(in) + 1
	v, err := parseVersionYAML(doc)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_, changeID, err := control.CreateDraft(r.Context(), s.d.Pool,
		control.Draft{Integration: in.Integration, Spec: doc, BasedOn: &expected, CopiedFrom: &expected, Author: actor, Reason: reason},
		control.Change{Actor: actor, Action: "updateIntegrationWeights", ObjectType: "integration", ObjectID: in.Integration, Reason: reason,
			Before: map[string]any{"active_version": expected, "weights": activeWeights(in.Active())},
			After:  map[string]any{"version": n, "state": "draft", "copied_from_version": expected, "weights": body.Weights, "spec": renderSpec(v)}})
	if err != nil {
		s.fail(w, r, controlError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"change_id": changeID, "integration_id": in.Integration, "previous_active_version": expected,
		"active_version": expected, "created_version": n, "copied_from_version": expected, "snapshot_version": nil, "published_at": nil})
}

func activeWeights(v *registry.IntegrationVersion) []weightEntry {
	out := make([]weightEntry, 0, len(v.Targets))
	for _, t := range v.Targets {
		w := t.Weight
		if len(v.Targets) == 1 && w == 0 {
			w = 100
		}
		out = append(out, weightEntry{VendorID: t.Vendor, AccountID: t.Account, Weight: w})
	}
	return out
}
