//go:build integration

package adminapi

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
)

const newCapability = `{"name":"notify.send","major":1,"description":"Send a notification.","interactive":false,"operations":{"default":{
"description":"Send one notification.","idempotent":true,
"request_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["to"],"additionalProperties":false,"properties":{"to":{"type":"string","x-pii":"identifier"}}},
"response_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["status"],"properties":{"status":{"type":"string"}}}}},
"reason":"First notification capability"}`

// eventsTrackMinor is a createCapabilityVersion body for events.track@v1 from the repo's schemas,
// with edit applied to the request schema.
func eventsTrackMinor(t *testing.T, expected int, edit func(req map[string]any)) string {
	t.Helper()
	read := func(name string) map[string]any {
		b, err := os.ReadFile("../../../../contracts/schemas/capabilities/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	req, resp := read("events.track.v1.request.json"), read("events.track.v1.response.json")
	if edit != nil {
		edit(req)
	}
	b, _ := json.Marshal(map[string]any{"expected_latest_minor": expected, "summary": "Add optional context.channel", "reason": "Channel attribution",
		"operations": map[string]any{"default": map[string]any{"description": "Track a single event for a user.", "idempotent": true,
			"request_schema": req, "response_schema": resp}}})
	return string(b)
}

func addChannel(req map[string]any) {
	ctx := req["properties"].(map[string]any)["context"].(map[string]any)["properties"].(map[string]any)
	ctx["channel"] = map[string]any{"type": "string", "maxLength": 64}
}

func init() {
	mutationCases["createCapability"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/capabilities", body: newCapability}
	b, err := os.ReadFile("../../../../contracts/schemas/capabilities/events.track.v1.request.json")
	var req map[string]any
	if err == nil && json.Unmarshal(b, &req) == nil {
		mutationCases["createCapabilityVersion"] = mutationCase{role: RoleEditor, method: http.MethodPost, path: "/capabilities/events.track@v1/versions",
			body: eventsTrackMinorFrom(req)}
	}
}

func eventsTrackMinorFrom(req map[string]any) string {
	addChannel(req)
	b, _ := os.ReadFile("../../../../contracts/schemas/capabilities/events.track.v1.response.json")
	var resp map[string]any
	_ = json.Unmarshal(b, &resp)
	out, _ := json.Marshal(map[string]any{"expected_latest_minor": 0, "reason": "Channel attribution",
		"operations": map[string]any{"default": map[string]any{"idempotent": true, "request_schema": req, "response_schema": resp}}})
	return string(out)
}

func TestCapabilities(t *testing.T) {
	a := newAdminRig(t)

	resp, body := a.do(RoleViewer, http.MethodGet, "/capabilities", "")
	items, _ := body["items"].([]any)
	if resp.Code != 200 || len(items) != 1 || items[0].(map[string]any)["integrations_using"] != 3.0 {
		t.Fatalf("list: %d %v", resp.Code, body)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/capabilities/events.track@v1", "")
	if resp.Code != 200 || body["minor"] != 0.0 || body["author"] != bootstrapAuthor {
		t.Fatalf("get bootstrap: %d %v", resp.Code, body)
	}
	if resp, _ := a.do(RoleViewer, http.MethodGet, "/capabilities/nope.nope@v1", ""); resp.Code != 404 {
		t.Fatalf("unknown capability: %d", resp.Code)
	}

	// Create a capability: its schemas compile in the full build and travel with the archive.
	resp, body = a.do(RoleEditor, http.MethodPost, "/capabilities", newCapability)
	cv, _ := body["capability_version"].(map[string]any)
	if resp.Code != 201 || cv["capability"] != "notify.send@v1" || cv["minor"] != 0.0 || cv["author"] != "editor@example.com" {
		t.Fatalf("create: %d %v", resp.Code, body)
	}
	_, snap, err := a.pub.Current(t.Context()) // rebuilt from the archived blob
	if err != nil || snap.Capabilities["notify.send@v1"] == nil || len(snap.Capabilities["notify.send@v1"].Operations["default"].PII) != 1 {
		t.Fatalf("archived snapshot: %v", err)
	}
	if resp, _ := a.do(RoleEditor, http.MethodPost, "/capabilities", newCapability); resp.Code != 409 {
		t.Fatalf("duplicate: %d", resp.Code)
	}
	bad := strings.Replace(strings.Replace(newCapability, `"major":1`, `"major":2`, 1), `"to":{"type":"string"`, `"to":{"type":"strung"`, 1)
	if resp, body := a.do(RoleEditor, http.MethodPost, "/capabilities", bad); resp.Code != 422 {
		t.Fatalf("schema that does not compile: %d %v", resp.Code, body)
	}

	// Minors: additive only, immutable history, newest first.
	refusedCases := []struct {
		name   string
		body   string
		status int
	}{
		{"stale expected minor", eventsTrackMinor(t, 3, addChannel), 409},
		{"removed field", eventsTrackMinor(t, 0, func(req map[string]any) { delete(req["properties"].(map[string]any), "context") }), 422},
		{"newly required", eventsTrackMinor(t, 0, func(req map[string]any) { req["required"] = []any{"user", "event", "context"} }), 422},
	}
	for _, c := range refusedCases {
		resp, body := a.do(RoleEditor, http.MethodPost, "/capabilities/events.track@v1/versions", c.body)
		if resp.Code != c.status {
			t.Errorf("%s: %d %v", c.name, resp.Code, body)
		}
		if c.status == 422 && !strings.Contains(resp.Body.String(), "additive_change") {
			t.Errorf("%s: no additive_change check in %s", c.name, resp.Body)
		}
	}
	resp, body = a.do(RoleEditor, http.MethodPost, "/capabilities/events.track@v1/versions", eventsTrackMinor(t, 0, addChannel))
	if cv, _ := body["capability_version"].(map[string]any); resp.Code != 201 || cv["minor"] != 1.0 {
		t.Fatalf("add minor: %d %v", resp.Code, body)
	}
	_, snap, err = a.pub.Current(t.Context())
	if err != nil || snap.Capabilities["events.track@v1"].Minor != 1 {
		t.Fatalf("snapshot minor: %v", err)
	}
	if err := snap.Capabilities["events.track@v1"].Operations["default"].Request.Validate(map[string]any{
		"user": map[string]any{"id": "u1"}, "event": map[string]any{"name": "x"}, "context": map[string]any{"channel": "web"}}); err != nil {
		t.Fatalf("minor 1 schema does not accept context.channel: %v", err)
	}
	resp, body = a.do(RoleViewer, http.MethodGet, "/capabilities/events.track@v1/versions", "")
	vs, _ := body["items"].([]any)
	if resp.Code != 200 || len(vs) != 2 || vs[0].(map[string]any)["minor"] != 1.0 || vs[1].(map[string]any)["minor"] != 0.0 ||
		vs[0].(map[string]any)["summary"] != "Add optional context.channel" {
		t.Fatalf("versions: %v", body)
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/capabilities/events.track@v1/versions?limit=1", ""); body["next_cursor"] == nil {
		t.Fatalf("versions page: %v", body)
	}
	if _, body := a.do(RoleViewer, http.MethodGet, "/capabilities/events.track@v1", ""); body["minor"] != 1.0 {
		t.Fatalf("get latest: %v", body)
	}
}
