//go:build integration

package adminapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/trackops"
)

func init() {
	mutationCases["createIntegrationDraft"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/integrations/int_evt_brandA/versions",
		body: `{"reason":"canary draft"}`, setup: resetArchive}
	mutationCases["updateIntegrationDraft"] = mutationCase{role: RoleEditor, method: http.MethodPut, path: "/integrations/int_evt_brandA/versions/3",
		body: `{"spec":` + apiSpecJSON(80, "ct-brandB-us") + `,"reason":"canary 20 %"}`, setup: func(a *adminRig) {
			resetArchive(a)
			if resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"reason":"canary draft"}`); resp.Code != 201 {
				a.t.Fatalf("draft: %d %v", resp.Code, body)
			}
		}}
}

// clearDrafts empties the draft and pending-integration tables (shared dev database).
func clearDrafts(a *adminRig) {
	a.t.Helper()
	if _, err := a.s.d.Pool.Exec(a.t.Context(), `TRUNCATE hub.integration_drafts, hub.integration_pending`); err != nil {
		a.t.Fatal(err)
	}
}

func newDraftRig(t *testing.T) *adminRig {
	t.Helper()
	a := newAdminRig(t)
	clearDrafts(a)
	return a
}

// apiSpecJSON is a contract IntegrationSpec of int_evt_brandA: weight on ct-brandA-in, the rest
// on second (when weight < 100).
func apiSpecJSON(weight int, second string) string {
	targets := fmt.Sprintf(`{"vendor_id":"clevertap","account_id":"ct-brandA-in","transformer":{"request":"T1@7","response":"R1@2"},"weight":%d}`, weight)
	if weight < 100 {
		targets += fmt.Sprintf(`,{"vendor_id":"clevertap","account_id":%q,"transformer":{"request":"T1@7","response":"R1@2"},"weight":%d}`, second, 100-weight)
	}
	return `{` + trackops.Member + `,"targets":[` + targets + `],"failover":[],"delivery":{"mode":"wait","wait_ms":3000,"ttl_ms":21600000,"on_circuit_open":"fail_fast","pending":"re_resolve","polling":true},` +
		`"webhooks":[],"audit":{"payloads":"masked","retention_ms":2592000000}}`
}

// TestDrafts is HUB-205: a draft is the next version, visible to the admin (has_draft, version
// list, diff) but not to nodes (the archive does not change); one draft at a time; published
// versions are immutable; every draft change is logged with before/after.
func TestDrafts(t *testing.T) {
	a := newDraftRig(t)
	_, snapBefore, _ := a.pub.Current(t.Context())

	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions",
		`{"spec":`+apiSpecJSON(90, "ct-brandB-us")+`,"reason":"canary 10 % to the US workspace"}`)
	iv, _ := body["integration_version"].(map[string]any)
	if resp.Code != 201 || iv["version"] != 3.0 || iv["state"] != "draft" || iv["based_on"] != 2.0 || iv["published_at"] != nil {
		t.Fatalf("create draft: %d %v", resp.Code, body)
	}
	if resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"reason":"second draft"}`); resp.Code != 409 {
		t.Fatalf("second draft: %d %v", resp.Code, body)
	}
	_, snapAfter, _ := a.pub.Current(t.Context())
	if snapAfter.Checksum != snapBefore.Checksum {
		t.Fatal("a draft changed the snapshot nodes run")
	}

	_, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA", "")
	if body["has_draft"] != true || body["latest_version"] != 3.0 || body["active_version"] != 2.0 {
		t.Fatalf("integration with a draft: %v", body)
	}
	_, body = a.do(RoleViewer, http.MethodGet, "/integrations?active_version_behind=true", "")
	if items := body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["has_draft"] != true {
		t.Fatalf("behind filter: %v", body)
	}
	_, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/versions?state=draft", "")
	if items := body["items"].([]any); len(items) != 1 || items[0].(map[string]any)["version"] != 3.0 {
		t.Fatalf("draft list: %v", body)
	}
	_, body = a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA/diff?from=2&to=3", "")
	if got := fmt.Sprint(body["changes"]); !strings.Contains(got, "/targets/0/weight") || !strings.Contains(got, "/targets/1") {
		t.Fatalf("draft diff %s", got)
	}

	resp, body = a.do(RoleEditor, http.MethodPut, "/integrations/int_evt_brandA/versions/3",
		`{"spec":`+apiSpecJSON(80, "ct-brandB-us")+`,"reason":"canary 20 % instead"}`)
	iv, _ = body["integration_version"].(map[string]any)
	if resp.Code != 200 || iv["reason"] != "canary 20 % instead" ||
		iv["spec"].(map[string]any)["targets"].([]any)[0].(map[string]any)["weight"] != 80.0 {
		t.Fatalf("update draft: %d %v", resp.Code, body)
	}
	var before, after string
	if err := a.s.d.Pool.QueryRow(t.Context(), `SELECT before::text, after::text FROM hub.change_log WHERE change_id = $1`, body["change_id"]).Scan(&before, &after); err != nil ||
		!strings.Contains(before, `"weight": 90`) || !strings.Contains(after, `"weight": 80`) {
		t.Fatalf("change row before %s after %s (%v)", before, after, err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"/integrations/int_evt_brandA/versions/2", 409}, {"/integrations/int_evt_brandA/versions/7", 404}, {"/integrations/int_nope/versions/1", 404}} {
		if resp, body := a.do(RoleEditor, http.MethodPut, tc.path, `{"spec":`+apiSpecJSON(100, "")+`,"reason":"edit"}`); resp.Code != tc.want {
			t.Errorf("PUT %s: %d %v, want %d", tc.path, resp.Code, body, tc.want)
		}
	}
	// Unsupported contract members are refused at their pointer.
	bad := strings.Replace(apiSpecJSON(100, ""), `"polling":true`, `"polling":true,"retry":{"max_attempts":3}`, 1)
	if resp, body := a.do(RoleEditor, http.MethodPut, "/integrations/int_evt_brandA/versions/3", `{"spec":`+bad+`,"reason":"retry"}`); resp.Code != 400 ||
		!strings.Contains(resp.Body.String(), `"pointer":"/spec/delivery/retry"`) {
		t.Fatalf("unsupported retry: %d %v", resp.Code, body)
	}
}

// TestValidate is HUB-205's acceptance for validation: the report names each failing check with
// a contract pointer, and validating publishes nothing.
func TestValidate(t *testing.T) {
	a := newDraftRig(t)
	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/2/validate", "")
	if resp.Code != 200 || body["valid"] != true || len(body["checks"].([]any)) != len(knownChecks) {
		t.Fatalf("valid published version: %d %v", resp.Code, body)
	}
	// A draft whose weights total 110 and whose second target is an unknown account.
	spec := strings.Replace(apiSpecJSON(90, "ct-nowhere"), `"weight":10`, `"weight":20`, 1)
	if resp, body := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions", `{"spec":`+spec+`,"reason":"broken draft"}`); resp.Code != 201 {
		t.Fatalf("draft: %d %v", resp.Code, body)
	}
	var archived int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.config_snapshots`).Scan(&archived)
	resp, body = a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/3/validate", "{}")
	if resp.Code != 200 || body["valid"] != false {
		t.Fatalf("invalid draft: %d %v", resp.Code, body)
	}
	fails := map[string]string{}
	for _, c := range body["checks"].([]any) {
		m := c.(map[string]any)
		if m["status"] == "fail" {
			fails[m["check"].(string)+" "+fmt.Sprint(m["pointer"])] = m["message"].(string)
		}
	}
	if _, ok := fails["weights_sum_100 /targets"]; !ok {
		t.Errorf("no weights_sum_100 failure: %v", fails)
	}
	if _, ok := fails["references_exist /targets/1/account_id"]; !ok {
		t.Errorf("no references_exist failure at /targets/1/account_id: %v", fails)
	}
	var after int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.config_snapshots`).Scan(&after)
	if after != archived {
		t.Fatalf("validate archived a snapshot (%d → %d)", archived, after)
	}
	if n := changesOf(t, a, resp.Header().Get("X-Hub-Request-Id")); n != 0 {
		t.Fatalf("validate wrote %d change(s)", n)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandA/versions/9/validate", ""); resp.Code != 404 {
		t.Fatalf("unknown version: %d", resp.Code)
	}
}
