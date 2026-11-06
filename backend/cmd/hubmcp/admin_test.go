package main

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func adminSession(t *testing.T, hub *fakeHub) (*mcp.ClientSession, *app) {
	t.Helper()
	a := testApp(t, hub.srv.URL, true, nil)
	a.now = func() time.Time { return time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC) }
	return connect(t, a), a
}

func TestAdminSearchRequests(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		reply     func(h *fakeHub) reply
		wantQuery url.Values
		wantCode  string
	}{
		{
			name: "filters, multi-value as CSV, explicit range",
			args: map[string]any{"integration_id": "int_evt_brandA", "status": []string{"dead_lettered", "parked"}, "code": []string{"UPSTREAM_TIMEOUT"},
				"vendor_id": "clevertap", "from": "2026-10-31T09:00:00Z", "to": "2026-10-31T10:00:00Z", "limit": 20},
			reply: func(h *fakeHub) reply { return h.adm("searchRequests", 200, "") },
			wantQuery: url.Values{"integration_id": {"int_evt_brandA"}, "status": {"dead_lettered,parked"}, "code": {"UPSTREAM_TIMEOUT"},
				"vendor_id": {"clevertap"}, "from": {"2026-10-31T09:00:00Z"}, "to": {"2026-10-31T10:00:00Z"}, "limit": {"20"}},
		},
		{
			name:      "no range defaults to the last hour",
			args:      map[string]any{},
			reply:     func(h *fakeHub) reply { return h.adm("searchRequests", 200, "") },
			wantQuery: url.Values{"from": {"2026-10-31T09:00:00Z"}},
		},
		{
			name:      "only 'to' given: one hour before it",
			args:      map[string]any{"to": "2026-10-30T12:00:00Z"},
			reply:     func(h *fakeHub) reply { return h.adm("searchRequests", 200, "") },
			wantQuery: url.Values{"from": {"2026-10-30T11:00:00Z"}, "to": {"2026-10-30T12:00:00Z"}},
		},
		{name: "range too wide", args: map[string]any{}, reply: func(h *fakeHub) reply { return h.adm("searchRequests", 400, "") }, wantCode: "VALIDATION_FAILED"},
		{name: "outside tenant scope", args: map[string]any{"tenant_id": "billing"}, reply: func(h *fakeHub) reply { return h.adm("searchRequests", 403, "") }, wantCode: "FORBIDDEN"},
		{name: "bad timestamp refused locally", args: map[string]any{"from": "yesterday"}, wantCode: "VALIDATION_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			if tt.reply != nil {
				hub.on("searchRequests", tt.reply(hub))
			}
			cs, _ := adminSession(t, hub)
			out := callTool(t, cs, toolAdminSearch, tt.args)
			if tt.wantCode != "" {
				if !out.res.IsError || out.field(t, "error", "code") != tt.wantCode {
					t.Fatalf("want %s, got %s", tt.wantCode, out.json)
				}
				return
			}
			c := hub.callsTo("searchRequests")[0]
			if got, _ := url.ParseQuery(c.Query); got.Encode() != tt.wantQuery.Encode() {
				t.Errorf("query %s, want %s", got.Encode(), tt.wantQuery.Encode())
			}
			if c.Header.Get("Authorization") != "Bearer "+testAdminToken {
				t.Error("admin token not sent")
			}
			items, _ := out.field(t, "items").([]any)
			if len(items) == 0 || items[0].(map[string]any)["vendor_id"] != "clevertap" {
				t.Errorf("admin rows should keep control-plane fields: %s", out.json)
			}
		})
	}
}

func TestAdminSearchDropsPayloads(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("searchRequests", hub.adm("searchRequests", 200, "", func(m map[string]any) {
		m["items"].([]any)[0].(map[string]any)["result"] = map[string]any{"status": "ok"}
	}))
	cs, _ := adminSession(t, hub)
	out := callTool(t, cs, toolAdminSearch, map[string]any{})
	if strings.Contains(string(out.json), `"result"`) {
		t.Fatalf("payload in summary rows: %s", out.json)
	}
}

func TestAdminRequestTimeline(t *testing.T) {
	tests := []struct {
		name         string
		include      bool
		timeline     func(h *fakeHub) reply
		attempts     func(h *fakeHub) reply
		wantCode     string
		wantPayloads bool
	}{
		{name: "metadata only by default", timeline: func(h *fakeHub) reply { return h.adm("getRequestTimeline", 200, "") },
			attempts: func(h *fakeHub) reply { return h.adm("listRequestAttempts", 200, "") }},
		{name: "payloads on request", include: true, timeline: func(h *fakeHub) reply { return h.adm("getRequestTimeline", 200, "") },
			attempts: func(h *fakeHub) reply { return h.adm("listRequestAttempts", 200, "") }, wantPayloads: true},
		{name: "unknown request", timeline: func(h *fakeHub) reply { return h.adm("getRequestTimeline", 404, "") }, wantCode: "NOT_FOUND"},
		{name: "attempts fail", timeline: func(h *fakeHub) reply { return h.adm("getRequestTimeline", 200, "") },
			attempts: func(h *fakeHub) reply { return h.adm("listRequestAttempts", 403, "") }, wantCode: "FORBIDDEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.on("getRequestTimeline", tt.timeline(hub))
			if tt.attempts != nil {
				hub.on("listRequestAttempts", tt.attempts(hub))
			}
			cs, _ := adminSession(t, hub)
			out := callTool(t, cs, toolAdminTimeline, map[string]any{"request_id": "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6", "include_payloads": tt.include})
			if tt.wantCode != "" {
				if !out.res.IsError || out.field(t, "error", "code") != tt.wantCode {
					t.Fatalf("want %s, got %s", tt.wantCode, out.json)
				}
				return
			}
			events, _ := out.field(t, "events").([]any)
			attempts, _ := out.field(t, "attempts").([]any)
			if len(events) != 5 || len(attempts) != 1 || out.field(t, "payload_policy") != "masked" {
				t.Fatalf("got %s", out.json)
			}
			_, hasPayload := events[0].(map[string]any)["payload"]
			if hasPayload != tt.wantPayloads {
				t.Errorf("payload present=%v, want %v", hasPayload, tt.wantPayloads)
			}
			if events[0].(map[string]any)["sha256"] == nil || events[0].(map[string]any)["bytes"] == nil {
				t.Errorf("sha256/bytes missing: %s", out.json)
			}
		})
	}
}

func TestAdminBreakerStates(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listBreakers", hub.adm("listBreakers", 200, ""))
	cs, _ := adminSession(t, hub)
	out := callTool(t, cs, toolAdminBreakerStates, map[string]any{"state": "open", "vendor_id": "clevertap"})
	if out.res.IsError {
		t.Fatal(string(out.json))
	}
	if q, _ := url.ParseQuery(hub.callsTo("listBreakers")[0].Query); q.Get("state") != "open" || q.Get("vendor_id") != "clevertap" {
		t.Errorf("query %v", q)
	}
	t.Run("rejected admin token", func(t *testing.T) {
		hub := newFakeHub(t)
		a := testApp(t, hub.srv.URL, true, nil)
		a.admin.token = "a-revoked-admin-token-000"
		out := callTool(t, connect(t, a), toolAdminBreakerStates, map[string]any{})
		if !out.res.IsError || out.field(t, "error", "code") != "UNAUTHORIZED" {
			t.Fatalf("got %s", out.json)
		}
	})
	t.Run("admin API unreachable", func(t *testing.T) {
		a := testApp(t, "http://127.0.0.1:1", true, nil)
		out := callTool(t, connect(t, a), toolAdminBreakerStates, map[string]any{})
		if !out.res.IsError || !strings.Contains(out.field(t, "error", "message").(string), "HUB_ADMIN_BASE_URL") {
			t.Fatalf("got %s", out.json)
		}
	})
}

// TestAdminToolsAreReadOnly (HUB-305): every Hub operation an admin tool maps to is a GET in
// hub-admin.v1.yaml, and exercising every admin tool sends only GETs to operations in its
// maps_to (never unseal, replay, purge, pause or force).
func TestAdminToolsAreReadOnly(t *testing.T) {
	hub := newFakeHub(t)
	methodOf := map[string]string{}
	for _, op := range hub.admin.Operations() {
		methodOf[op.ID] = op.Method
	}
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string][]string{}
	for _, tool := range c.Tools {
		if tool.Audience != audienceAdmin {
			continue
		}
		for _, m := range tool.MapsTo {
			if m.Spec != "hub-admin.v1.yaml" || methodOf[m.OperationID] != http.MethodGet {
				t.Errorf("%s maps to %s#%s (%s), want a hub-admin GET", tool.Name, m.Spec, m.OperationID, methodOf[m.OperationID])
			}
			allowed[tool.Name] = append(allowed[tool.Name], m.OperationID)
		}
		if !tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint {
			t.Errorf("%s annotations are not read-only", tool.Name)
		}
	}
	if len(allowed) != 3 {
		t.Fatalf("%d admin tools, want 3", len(allowed))
	}

	hub.on("searchRequests", hub.adm("searchRequests", 200, ""))
	hub.on("getRequestTimeline", hub.adm("getRequestTimeline", 200, ""))
	hub.on("listRequestAttempts", hub.adm("listRequestAttempts", 200, ""))
	hub.on("listBreakers", hub.adm("listBreakers", 200, ""))
	cs, _ := adminSession(t, hub)
	for name, args := range map[string]map[string]any{
		toolAdminSearch:        {},
		toolAdminTimeline:      {"request_id": "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6", "include_payloads": true},
		toolAdminBreakerStates: {},
	} {
		before := len(hub.callsTo(""))
		callTool(t, cs, name, args)
		for _, call := range hub.callsTo("")[before:] {
			if call.Method != http.MethodGet || !slices.Contains(allowed[name], call.OperationID) {
				t.Errorf("%s sent %s %s (%s), not a GET in its maps_to", name, call.Method, call.Path, call.OperationID)
			}
		}
	}
	// Mutating admin operations exist in the contract; none was ever called.
	for _, call := range hub.callsTo("") {
		if methodOf[call.OperationID] != http.MethodGet {
			t.Errorf("mutating operation %s called", call.OperationID)
		}
	}
}

// TestAdminToolsWithoutToken (HUB-305): without HUB_ADMIN_TOKEN the admin tools do not exist, a
// call to one fails with a clear "unknown tool" error, and the server instructions say why.
func TestAdminToolsWithoutToken(t *testing.T) {
	hub := newFakeHub(t)
	cs := connect(t, testApp(t, hub.srv.URL, false, nil))
	for _, name := range toolNames(t, cs) {
		if strings.HasPrefix(name, "hub_admin_") {
			t.Errorf("%s registered without an admin token", name)
		}
	}
	_, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: toolAdminBreakerStates, Arguments: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), toolAdminBreakerStates) {
		t.Fatalf("calling a disabled admin tool: %v", err)
	}
	if ins := cs.InitializeResult().Instructions; !strings.Contains(ins, "HUB_ADMIN_TOKEN is not set") {
		t.Errorf("instructions do not explain the missing admin tools: %q", ins)
	}
	if len(hub.callsTo("")) != 0 {
		t.Error("the Hub was called")
	}
}
