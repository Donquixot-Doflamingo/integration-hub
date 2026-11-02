//go:build integration

package adminapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func init() {
	mutationCases["createIntegration"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/integrations",
		body: `{"integration_id":"int_evt_brandC","owner_tenant":"orders-service","capability":"events.track@v1","reason":"brand C pilot"}`, setup: resetArchive}
	mutationCases["updateIntegration"] = mutationCase{role: RoleEditor, method: http.MethodPatch, path: "/integrations/int_evt_brandA",
		body: `{"description":"Brand A events (renamed)","reason":"clearer name"}`, setup: resetArchive}
}

const brandC = `{"integration_id":"int_evt_brandC","owner_tenant":"orders-service","capability":"events.track@v1","description":"Brand C","grants":[],"reason":"brand C pilot"}`

// TestCreateAndFirstPublish is HUB-205: a created integration has no active version and is not
// in the snapshot; its first draft publishes integration.yaml and v1 together
// (expected_active_version null).
func TestCreateAndFirstPublish(t *testing.T) {
	a := newDraftRig(t)
	resp, body := a.do(RoleEditor, http.MethodPost, "/integrations", brandC)
	in, _ := body["integration"].(map[string]any)
	if resp.Code != 201 || resp.Header().Get("ETag") == "" || in["active_version"] != nil || in["latest_version"] != nil || in["has_draft"] != false {
		t.Fatalf("create: %d %v", resp.Code, body)
	}
	_, snap, _ := a.pub.Current(t.Context())
	if _, ok := snap.Integrations["int_evt_brandC"]; ok {
		t.Fatal("an unpublished integration reached the snapshot")
	}
	for _, tc := range []struct{ body, want string }{
		{brandC, "409"},
		{strings.Replace(strings.Replace(brandC, "int_evt_brandC", "int_evt_brandE", 1), `"orders-service"`, `"nobody-here"`, 1), "/owner_tenant"},
		{strings.Replace(strings.Replace(brandC, "int_evt_brandC", "int_evt_brandD", 1), "events.track@v1", "events.nope@v1", 1), "/capability"},
	} {
		resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations", tc.body)
		if !strings.Contains(resp.Body.String(), tc.want) && !strings.Contains(resp.Result().Status, tc.want) {
			t.Errorf("want %s: %d %s", tc.want, resp.Code, resp.Body)
		}
	}
	if got := mustDo(a, RoleViewer, http.MethodGet, "/integrations?has_active_version=false", "", 200); len(got["items"].([]any)) != 1 {
		t.Fatalf("never-published filter: %v", got)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandC/versions", `{"reason":"copy nothing"}`); resp.Code != 400 {
		t.Fatalf("draft without spec or active version: %d", resp.Code)
	}
	brandCSpec := strings.ReplaceAll(apiSpecJSON(100, ""), "ct-brandA-in", "ct-brandB-us")
	mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandC/versions", `{"spec":`+brandCSpec+`,"reason":"first version"}`, 201)
	if v := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandC/versions/1", "", 200); v["state"] != "draft" || v["based_on"] != nil {
		t.Fatalf("first draft %v", v)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/integrations/int_evt_brandC/versions/1/publish", `{"expected_active_version":1,"reason":"go now"}`); resp.Code != 409 {
		t.Fatalf("expected 1 for a never-published integration: %d", resp.Code)
	}
	body = mustDo(a, RoleEditor, http.MethodPost, "/integrations/int_evt_brandC/versions/1/publish", `{"expected_active_version":null,"reason":"go live"}`, 200)
	if body["active_version"] != 1.0 || body["previous_active_version"] != nil {
		t.Fatalf("first publish %v", body)
	}
	_, snap, _ = a.pub.Current(t.Context())
	if c := snap.Integrations["int_evt_brandC"]; c == nil || c.ActiveVersion != 1 || c.Description != "Brand C" {
		t.Fatalf("published integration %+v", c)
	}
	got := mustDo(a, RoleViewer, http.MethodGet, "/integrations/int_evt_brandC", "", 200)
	if got["active_version"] != 1.0 || got["has_draft"] != false || got["active"] == nil {
		t.Fatalf("after first publish %v", got)
	}
	var pending int
	_ = a.s.d.Pool.QueryRow(t.Context(), `SELECT count(*) FROM hub.integration_pending`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("%d pending row(s) left after the first publish", pending)
	}
}

// TestUpdateIntegrationIfMatch is HUB-205 / contract Concurrency: PATCH merges description,
// grants and moves_money_or_quota through the pipeline, honours If-Match (412 with the current
// ETag), and logs before/after.
func TestUpdateIntegrationIfMatch(t *testing.T) {
	a := newDraftRig(t)
	resp, _ := a.do(RoleViewer, http.MethodGet, "/integrations/int_evt_brandA", "")
	etag := resp.Header().Get("ETag")
	patch := func(tag, body string) (int, map[string]any, string) {
		req := a.request(RoleEditor, http.MethodPatch, "/integrations/int_evt_brandA", body)
		if tag != "" {
			req.Header.Set("If-Match", tag)
		}
		rec := a.spec.Serve(t, a.h, req)
		var out map[string]any
		_ = jsonUnmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out, rec.Header().Get("ETag")
	}
	code, body, newTag := patch(etag, `{"moves_money_or_quota":true,"description":null,"reason":"quota-bearing"}`)
	in, _ := body["integration"].(map[string]any)
	if code != 200 || in["moves_money_or_quota"] != true || in["description"] != nil || newTag == "" || newTag == etag {
		t.Fatalf("patch: %d %v (etag %s → %s)", code, body, etag, newTag)
	}
	_, before, after := changeRow(t, a, body["change_id"])
	if !strings.Contains(before, `"moves_money_or_quota": false`) || !strings.Contains(after, `"moves_money_or_quota": true`) {
		t.Fatalf("change before %s after %s", before, after)
	}
	_, snap, _ := a.pub.Current(t.Context())
	if f := snap.Integrations["int_evt_brandA"]; !f.MovesMoneyOrQuota || f.Description != "" || f.ActiveVersion != 2 {
		t.Fatalf("snapshot integration %+v", f.IntegrationFile)
	}
	code, body, _ = patch(etag, `{"description":"stale write","reason":"stale"}`)
	if meta, _ := body["error"].(map[string]any)["meta"].(map[string]any); code != 412 || meta["current_etag"] != newTag {
		t.Fatalf("stale If-Match: %d %v", code, body)
	}
	if code, body, _ := patch(newTag, `{"grants":["nobody-here"],"reason":"grant"}`); code != 400 {
		t.Fatalf("unknown grantee: %d %v", code, body)
	}
	if code, _, _ := patch("", `{"description":"no precondition","reason":"blind write"}`); code != 200 {
		t.Fatalf("PATCH without If-Match: %d", code)
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
