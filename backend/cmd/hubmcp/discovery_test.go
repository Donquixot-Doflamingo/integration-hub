package main

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestListIntegrations(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		reply     func(h *fakeHub) reply
		wantError string // canonical code; "" = success
		wantQuery url.Values
		wantCalls int
	}{
		{
			name:      "first page with filter",
			args:      map[string]any{"capability": "events.track@v1", "limit": 10},
			reply:     func(h *fakeHub) reply { return h.pub("listIntegrations", 200, "") },
			wantQuery: url.Values{"capability": {"events.track@v1"}, "limit": {"10"}},
			wantCalls: 1,
		},
		{
			name: "next page cursor and last page",
			args: map[string]any{"cursor": "eyJrIjoiMTIzIn0"},
			reply: func(h *fakeHub) reply {
				return h.pub("listIntegrations", 200, "", func(m map[string]any) { m["next_cursor"] = nil })
			},
			wantQuery: url.Values{"cursor": {"eyJrIjoiMTIzIn0"}},
			wantCalls: 1,
		},
		{
			name:      "hub rejects the filter",
			args:      map[string]any{"capability": "events.track@v1"},
			reply:     func(h *fakeHub) reply { return h.pub("listIntegrations", 400, "VALIDATION_FAILED") },
			wantError: "VALIDATION_FAILED",
			wantCalls: 1,
		},
		{
			name:      "hub internal error",
			args:      map[string]any{},
			reply:     func(h *fakeHub) reply { return h.pub("listIntegrations", 500, "INTERNAL_ERROR") },
			wantError: "INTERNAL_ERROR",
			wantCalls: 1,
		},
		{name: "limit out of range is refused locally", args: map[string]any{"limit": 500}, wantError: "VALIDATION_FAILED"},
		{name: "unknown argument is refused locally", args: map[string]any{"vendor": "clevertap"}, wantError: "VALIDATION_FAILED"},
		{name: "malformed capability is refused locally", args: map[string]any{"capability": "Events"}, wantError: "VALIDATION_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			if tt.reply != nil {
				hub.on("listIntegrations", tt.reply(hub))
			}
			cs := connect(t, testApp(t, hub.srv.URL, false, nil))
			out := callTool(t, cs, toolListIntegrations, tt.args)
			calls := hub.callsTo("")
			if len(calls) != tt.wantCalls {
				t.Fatalf("%d Hub calls, want %d", len(calls), tt.wantCalls)
			}
			if tt.wantError != "" {
				if !out.res.IsError || out.field(t, "error", "code") != tt.wantError {
					t.Fatalf("want error %s, got %s", tt.wantError, out.json)
				}
				return
			}
			if out.res.IsError {
				t.Fatalf("unexpected error: %s", out.json)
			}
			if got, _ := url.ParseQuery(calls[0].Query); got.Encode() != tt.wantQuery.Encode() {
				t.Errorf("query %q, want %q", got.Encode(), tt.wantQuery.Encode())
			}
			if items, _ := out.field(t, "items").([]any); len(items) == 0 {
				t.Errorf("no items: %s", out.json)
			}
		})
	}
}

func TestDescribeIntegration(t *testing.T) {
	tests := []struct {
		name      string
		replies   func(h *fakeHub)
		wantError string
		wantOps   []string
	}{
		{
			name: "integration and capability",
			replies: func(h *fakeHub) {
				h.on("getIntegration", h.pub("getIntegration", 200, ""))
				h.on("getCapability", h.pub("getCapability", 200, ""))
			},
			wantOps: []string{"getIntegration", "getCapability"},
		},
		{
			name:      "unknown integration",
			replies:   func(h *fakeHub) { h.on("getIntegration", h.pub("getIntegration", 404, "")) },
			wantError: "INTEGRATION_NOT_FOUND",
			wantOps:   []string{"getIntegration"},
		},
		{
			name: "capability missing",
			replies: func(h *fakeHub) {
				h.on("getIntegration", h.pub("getIntegration", 200, ""))
				h.on("getCapability", h.pub("getCapability", 404, ""))
			},
			wantError: "CAPABILITY_NOT_FOUND",
			wantOps:   []string{"getIntegration", "getCapability"},
		},
		{
			name:      "not granted",
			replies:   func(h *fakeHub) { h.on("getIntegration", h.pub("getIntegration", 403, "")) },
			wantError: "FORBIDDEN",
			wantOps:   []string{"getIntegration"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			tt.replies(hub)
			cs := connect(t, testApp(t, hub.srv.URL, false, nil))
			out := callTool(t, cs, toolDescribe, map[string]any{"integration_id": "int_evt_brandA"})
			var ops []string
			for _, c := range hub.callsTo("") {
				ops = append(ops, c.OperationID)
			}
			if strings.Join(ops, ",") != strings.Join(tt.wantOps, ",") {
				t.Fatalf("Hub operations %v, want %v", ops, tt.wantOps)
			}
			if tt.wantError != "" {
				if !out.res.IsError || out.field(t, "error", "code") != tt.wantError {
					t.Fatalf("want %s, got %s", tt.wantError, out.json)
				}
				if id, _ := out.field(t, "error", "request_id").(string); !strings.HasPrefix(id, "req_") {
					t.Errorf("error lacks the Hub request id: %s", out.json)
				}
				return
			}
			if out.field(t, "integration", "integration_id") != "int_evt_brandA" {
				t.Errorf("integration: %s", out.json)
			}
			ops0, _ := out.field(t, "capability", "operations").([]any)
			if len(ops0) == 0 {
				t.Fatalf("no operations: %s", out.json)
			}
			if rs, _ := ops0[0].(map[string]any)["request_schema"].(map[string]any); len(rs) == 0 {
				t.Errorf("request_schema missing: %s", out.json)
			}
		})
	}
	t.Run("invalid id is refused locally", func(t *testing.T) {
		hub := newFakeHub(t)
		cs := connect(t, testApp(t, hub.srv.URL, false, nil))
		out := callTool(t, cs, toolDescribe, map[string]any{"integration_id": "../admin"})
		if !out.res.IsError || out.field(t, "error", "code") != "VALIDATION_FAILED" || len(hub.callsTo("")) != 0 {
			t.Fatalf("got %s with %d Hub calls", out.json, len(hub.callsTo("")))
		}
	})
}

// TestDiscoveryNeverLeaksVendorIdentity plants vendor, account and version fields and debug
// headers in every Hub response; the tool outputs must not contain any of them (HUB-302).
func TestDiscoveryNeverLeaksVendorIdentity(t *testing.T) {
	hub := newFakeHub(t)
	plantItems := func(m map[string]any) {
		plantVendorIdentity(m)
		for _, it := range m["items"].([]any) {
			plantVendorIdentity(it.(map[string]any))
		}
	}
	list := hub.pub("listIntegrations", 200, "", plantItems)
	list.header = leakHeaders
	get := hub.pub("getIntegration", 200, "", plantVendorIdentity)
	get.header = leakHeaders
	cp := hub.pub("getCapability", 200, "", plantVendorIdentity)
	cp.header = leakHeaders
	hub.on("listIntegrations", list)
	hub.on("getIntegration", get)
	hub.on("getCapability", cp)
	cs := connect(t, testApp(t, hub.srv.URL, false, nil))
	// callTool scans every output for the planted values and field names.
	callTool(t, cs, toolListIntegrations, map[string]any{})
	callTool(t, cs, toolDescribe, map[string]any{"integration_id": "int_evt_brandA"})
}

// TestDiscoveryUsesHubclient: calls carry the tenant key and the hubclient + hubmcp User-Agent
// (HUB-300: calls go through pkg/hubclient).
func TestDiscoveryUsesHubclient(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", http.StatusOK, ""))
	cs := connect(t, testApp(t, hub.srv.URL, false, nil))
	callTool(t, cs, toolListIntegrations, map[string]any{})
	c := hub.callsTo("listIntegrations")[0]
	if ua := c.Header.Get("User-Agent"); !strings.HasPrefix(ua, "hubclient-go/") || !strings.Contains(ua, " hubmcp/") {
		t.Errorf("User-Agent %q", ua)
	}
	if c.Header.Get("Authorization") != "Bearer "+testKey {
		t.Error("tenant key not sent as bearer")
	}
}

// TestUnauthorizedKeyIsAToolError: a key the Hub rejects becomes an isError result naming the
// env var to check, never echoing the key.
func TestUnauthorizedKeyIsAToolError(t *testing.T) {
	hub := newFakeHub(t)
	hub.keys = map[string]bool{"some-other-key-000000": true}
	cs := connect(t, testApp(t, hub.srv.URL, false, nil))
	out := callTool(t, cs, toolListIntegrations, map[string]any{})
	if out.field(t, "error", "code") != "UNAUTHORIZED" || !strings.Contains(out.field(t, "error", "message").(string), "HUB_API_KEY") {
		t.Fatalf("got %s", out.json)
	}
}

// TestHubUnreachableIsAToolError: a transport failure is a result naming HUB_BASE_URL.
func TestHubUnreachableIsAToolError(t *testing.T) {
	cs := connect(t, testApp(t, "http://127.0.0.1:1", false, nil))
	out := callTool(t, cs, toolListIntegrations, map[string]any{})
	if !out.res.IsError || !strings.Contains(out.field(t, "error", "message").(string), "HUB_BASE_URL") {
		t.Fatalf("got %s", out.json)
	}
}
