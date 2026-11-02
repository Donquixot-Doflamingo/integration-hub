//go:build integration

package adminapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func init() {
	mutationCases["publishIntegrationVersion"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/versions/3/publish",
		body: `{"expected_active_version":2,"reason":"canary"}`, setup: func(a *adminRig) {
			resetArchive(a)
			mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+apiSpecJSON(90, "ct-brandB-us")+`,"reason":"canary draft"}`, 201)
		}}
	mutationCases["rollbackIntegration"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/rollback",
		body: `{"to_version":1,"expected_active_version":2,"reason":"back to v1"}`, setup: resetArchive}
	mutationCases["updateIntegrationWeights"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/weights",
		body: `{"expected_active_version":3,"weights":[{"vendor_id":"clevertap","account_id":"ct-brandA-in","weight":50},{"vendor_id":"clevertap","account_id":"ct-brandB-us","weight":50}],"reason":"50/50"}`,
		setup: func(a *adminRig) {
			resetArchive(a)
			mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+apiSpecJSON(90, "ct-brandB-us")+`,"reason":"canary draft"}`, 201)
			mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/publish", `{"expected_active_version":2,"reason":"canary"}`, 200)
		}}
}

// resetArchive starts the rig over from the dev config tree with no drafts.
func resetArchive(a *adminRig) {
	a.t.Helper()
	if _, err := a.s.d.Pool.Exec(a.t.Context(), `TRUNCATE hub.config_snapshots, hub.integration_drafts, hub.integration_pending`); err != nil {
		a.t.Fatal(err)
	}
}

func mustDo(a *adminRig, role Role, method, path, body string, want int) map[string]any {
	a.t.Helper()
	resp, out := a.do(role, method, path, body)
	if resp.Code != want {
		a.t.Fatalf("%s %s: %d %s, want %d", method, path, resp.Code, resp.Body, want)
	}
	return out
}

// changeRow reads the before/after of a change.
func changeRow(t *testing.T, a *adminRig, id any) (action, before, after string) {
	t.Helper()
	if err := a.s.d.Pool.QueryRow(t.Context(), `SELECT action, coalesce(before::text, 'null'), coalesce(after::text, 'null') FROM hub.change_log WHERE change_id = $1`, id).
		Scan(&action, &before, &after); err != nil {
		t.Fatal(err)
	}
	return action, before, after
}

// TestPublishDraft is HUB-205: publishing the draft makes it the active version nodes run,
// expected_active_version guards against a concurrent publish (409), and the change log has the
// before/after.
func TestPublishDraft(t *testing.T) {
	a := newDraftRig(t)
	mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+apiSpecJSON(90, "ct-brandB-us")+`,"reason":"canary 10 %"}`, 201)
	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/publish", `{"expected_active_version":1,"reason":"stale"}`)
	if resp.Code != 409 || !strings.Contains(resp.Body.String(), `"actual_active_version":2`) {
		t.Fatalf("stale expected version: %d %v", resp.Code, body)
	}
	body = mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/publish", `{"expected_active_version":2,"reason":"canary 10 %"}`, 200)
	if body["active_version"] != 3.0 || body["previous_active_version"] != 2.0 || body["snapshot_version"] == nil {
		t.Fatalf("publish: %v", body)
	}
	_, snap, _ := a.pub.Current(t.Context())
	in := snap.Integrations["int_evt_brandA"]
	if in.ActiveVersion != 3 || len(in.Versions[3].Targets) != 2 || in.Versions[3].Targets[1].Account != "ct-brandB-us" {
		t.Fatalf("archived snapshot: active v%d %+v", in.ActiveVersion, in.Versions[3])
	}
	action, before, after := changeRow(t, a, body["change_id"])
	if action != "publishIntegrationVersion" || before != `{"active_version": 2}` || !strings.Contains(after, `"version": 3`) {
		t.Fatalf("change %s %s %s", action, before, after)
	}
	view := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions/3", "", 200)
	if view["state"] != "published" || view["based_on"] != 2.0 || view["author"] != "editor@example.com" || view["published_by"] != "editor@example.com" {
		t.Fatalf("published draft view %v", view)
	}
	if got := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA", "", 200); got["has_draft"] != false {
		t.Fatalf("draft survived its publish: %v", got)
	}
	// Publishing it again, or a version that does not exist.
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/publish", `{"expected_active_version":3,"reason":"again"}`); resp.Code != 409 {
		t.Fatalf("republish: %d", resp.Code)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/4/publish", `{"expected_active_version":3,"reason":"none"}`); resp.Code != 404 {
		t.Fatalf("no draft: %d", resp.Code)
	}
}

// TestPublishValidationFails is HUB-205's acceptance: a failing publish answers 422
// PUBLISH_VALIDATION_FAILED with path-qualified details and meta.report, and changes nothing.
func TestPublishValidationFails(t *testing.T) {
	a := newDraftRig(t)
	spec := strings.Replace(apiSpecJSON(90, "ct-nowhere"), `"weight":10`, `"weight":20`, 1)
	mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+spec+`,"reason":"broken"}`, 201)
	resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/publish", `{"expected_active_version":2,"reason":"broken"}`)
	var env struct {
		Error struct {
			Code    string   `json:"code"`
			Details []Detail `json:"details"`
			Meta    struct {
				Report struct {
					Valid  bool             `json:"valid"`
					Checks []map[string]any `json:"checks"`
				} `json:"report"`
			} `json:"meta"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &env)
	pointers := map[string]string{}
	for _, d := range env.Error.Details {
		pointers[d.Pointer] = d.Keyword
	}
	if resp.Code != 422 || env.Error.Code != CodePublishValidationFailed || env.Error.Meta.Report.Valid || len(env.Error.Meta.Report.Checks) == 0 ||
		pointers["/targets/1/account_id"] != "references_exist" || pointers["/targets"] != "weights_sum_100" {
		t.Fatalf("422: %d %s", resp.Code, resp.Body)
	}
	if n := changesOf(t, a, resp.Header().Get("X-Hub-Request-Id")); n != 0 {
		t.Fatalf("a refused publish wrote %d change(s)", n)
	}
	if got := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA", "", 200); got["active_version"] != 2.0 || got["has_draft"] != true {
		t.Fatalf("after a refused publish: %v", got)
	}
}

// TestRollbackAndWeights is HUB-205: rollback publishes a copy (v4 = v1), weights creates a
// new version (published, or left as the draft), stale expected versions are 409, and each
// change carries before/after.
func TestRollbackAndWeights(t *testing.T) {
	a := newDraftRig(t)
	body := mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/rollback", `{"to_version":1,"expected_active_version":2,"reason":"R1 breaks support"}`, 200)
	if body["active_version"] != 3.0 || body["copied_from_version"] != 1.0 || body["previous_active_version"] != 2.0 {
		t.Fatalf("rollback: %v", body)
	}
	diff := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=1&to=3", "", 200)
	if len(diff["changes"].([]any)) != 0 {
		t.Fatalf("v3 is not a copy of v1: %v", diff["changes"])
	}
	if v := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions/3", "", 200); v["copied_from_version"] != 1.0 {
		t.Fatalf("rollback version %v", v)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/rollback", `{"to_version":2,"expected_active_version":2,"reason":"stale"}`); resp.Code != 409 {
		t.Fatalf("stale rollback: %d", resp.Code)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/rollback", `{"to_version":42,"expected_active_version":3,"reason":"nowhere"}`); resp.Code != 404 {
		t.Fatalf("rollback to a missing version: %d", resp.Code)
	}

	// Two targets so weights have something to move: publish a 90/10 draft as v4.
	mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+apiSpecJSON(90, "ct-brandB-us")+`,"reason":"canary"}`, 201)
	mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/4/publish", `{"expected_active_version":3,"reason":"canary"}`, 200)
	weights := func(a1, b1 int, extra string) string {
		return `{"expected_active_version":4,"weights":[{"vendor_id":"clevertap","account_id":"ct-brandA-in","weight":` + strconv.Itoa(a1) +
			`},{"vendor_id":"clevertap","account_id":"ct-brandB-us","weight":` + strconv.Itoa(b1) + `}]` + extra + `,"reason":"go 50/50"}`
	}
	resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/weights", weights(50, 60, ""))
	if resp.Code != 422 || !strings.Contains(resp.Body.String(), "weights_sum_100") {
		t.Fatalf("weights 110: %d %s", resp.Code, resp.Body)
	}
	resp, _ = a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/weights",
		`{"expected_active_version":4,"weights":[{"vendor_id":"clevertap","account_id":"ct-brandA-in","weight":100}],"reason":"one target"}`)
	if resp.Code != 400 || !strings.Contains(resp.Body.String(), `"pointer":"/weights"`) {
		t.Fatalf("partial weights: %d %s", resp.Code, resp.Body)
	}
	// publish: false → the new version is the draft.
	body = mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/weights", weights(70, 30, `,"publish":false`), 200)
	if body["active_version"] != 4.0 || body["created_version"] != 5.0 || body["snapshot_version"] != nil {
		t.Fatalf("weights draft: %v", body)
	}
	if v := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions/5", "", 200); v["state"] != "draft" || v["copied_from_version"] != 4.0 {
		t.Fatalf("weights draft view %v", v)
	}
	a.s.d.Pool.Exec(t.Context(), `TRUNCATE hub.integration_drafts`) //nolint:errcheck // test cleanup
	body = mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/weights", weights(50, 50, ""), 200)
	if body["active_version"] != 5.0 || body["copied_from_version"] != 4.0 || body["snapshot_version"] == nil {
		t.Fatalf("weights: %v", body)
	}
	action, before, after := changeRow(t, a, body["change_id"])
	if action != "updateIntegrationWeights" || !strings.Contains(before, `"active_version": 4`) || !strings.Contains(after, `"weight": 50`) {
		t.Fatalf("weights change %s %s %s", action, before, after)
	}
	diff = mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=4&to=5", "", 200)
	var paths []string
	for _, c := range diff["changes"].([]any) {
		paths = append(paths, c.(map[string]any)["path"].(string))
	}
	if strings.Join(paths, ",") != "/targets/0/weight,/targets/1/weight" {
		t.Fatalf("weights diff %v", paths)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/weights", weights(50, 50, "")); resp.Code != 409 {
		t.Fatalf("stale weights: %d", resp.Code)
	}
}
