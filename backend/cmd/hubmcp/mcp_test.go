package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolCase sets up the fake Hub and the arguments for one tool call.
type toolCase struct {
	setup func(h *fakeHub)
	args  map[string]any
}

// toolMatrix has, for every tool of the contract, one success and one error scenario (HUB-306).
func toolMatrix() map[string][2]toolCase {
	invokeArgs := map[string]any{"integration_id": "int_evt_brandA", "body": trackBody, "idempotency_key": "order-8821-created"}
	getInt := func(h *fakeHub) { h.on("getIntegration", h.pub("getIntegration", 200, "")) }
	return map[string][2]toolCase{
		toolListIntegrations: {
			{func(h *fakeHub) { h.on("listIntegrations", h.pub("listIntegrations", 200, "")) }, map[string]any{}},
			{func(h *fakeHub) { h.on("listIntegrations", h.pub("listIntegrations", 500, "INTERNAL_ERROR")) }, map[string]any{}},
		},
		toolDescribe: {
			{func(h *fakeHub) { getInt(h); h.on("getCapability", h.pub("getCapability", 200, "")) }, map[string]any{"integration_id": "int_evt_brandA"}},
			{func(h *fakeHub) { h.on("getIntegration", h.pub("getIntegration", 404, "")) }, map[string]any{"integration_id": "int_evt_brandZ"}},
		},
		toolDryRun: {
			{func(h *fakeHub) { h.on("dryRunIntegration", h.pub("dryRunIntegration", 200, "")) }, map[string]any{"integration_id": "int_evt_brandA", "body": trackBody}},
			{func(h *fakeHub) { h.on("dryRunIntegration", h.pub("dryRunIntegration", 403, "")) }, map[string]any{"integration_id": "int_evt_brandA", "body": trackBody}},
		},
		toolInvoke: {
			{func(h *fakeHub) {
				getInt(h)
				h.on("invokeIntegration", h.pub("invokeIntegration", 200, "events.track@v1"))
			}, invokeArgs},
			{func(h *fakeHub) { getInt(h); h.on("invokeIntegration", h.pub("invokeIntegration", 422, "")) }, invokeArgs},
		},
		toolGetRequest: {
			{func(h *fakeHub) { h.on("getRequestStatus", h.pub("getRequestStatus", 200, "succeeded")) }, map[string]any{"request_id": fakeRequestID}},
			{func(h *fakeHub) { h.on("getRequestStatus", h.pub("getRequestStatus", 404, "")) }, map[string]any{"request_id": fakeRequestID}},
		},
		toolAdminSearch: {
			{func(h *fakeHub) { h.on("searchRequests", h.adm("searchRequests", 200, "")) }, map[string]any{}},
			{func(h *fakeHub) { h.on("searchRequests", h.adm("searchRequests", 403, "")) }, map[string]any{}},
		},
		toolAdminTimeline: {
			{func(h *fakeHub) {
				h.on("getRequestTimeline", h.adm("getRequestTimeline", 200, ""))
				h.on("listRequestAttempts", h.adm("listRequestAttempts", 200, ""))
			}, map[string]any{"request_id": fakeRequestID}},
			{func(h *fakeHub) { h.on("getRequestTimeline", h.adm("getRequestTimeline", 404, "")) }, map[string]any{"request_id": fakeRequestID}},
		},
		toolAdminBreakerStates: {
			{func(h *fakeHub) { h.on("listBreakers", h.adm("listBreakers", 200, "")) }, map[string]any{}},
			{func(h *fakeHub) { h.on("listBreakers", h.adm("listBreakers", 400, "")) }, map[string]any{"state": "open"}},
		},
	}
}

// TestEveryToolSuccessAndError runs, for every contract tool (all 42), a success and a Hub-error
// scenario against the contract-validated fake Hub, plus for every admin tool a FORBIDDEN answer
// to a viewer token surfaced as an isError result. Outputs are validated against the contract
// (callTool), and every request a tool sends is one of its maps_to operations with exactly the
// declared http_method (HUB-306, HUB-315).
func TestEveryToolSuccessAndError(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	matrix := toolMatrix()
	covered := 0
	for i := range c.Tools {
		tool := &c.Tools[i]
		cases, ok := matrix[tool.Name]
		if !ok && tool.Audience == audienceAdmin {
			cases, ok = adminToolCases(t, tool), true
		}
		if !ok {
			t.Errorf("contract tool %s has no success/error scenario", tool.Name)
			continue
		}
		covered++
		declared := map[string]string{} // operationId -> http_method
		for _, m := range tool.MapsTo {
			declared[m.OperationID] = m.HTTPMethod
		}
		type scenario struct {
			name    string
			tc      toolCase
			token   string
			wantErr bool
			code    string
		}
		scenarios := []scenario{{name: "success", tc: cases[0]}, {name: "error", tc: cases[1], wantErr: true}}
		if tool.Audience == audienceAdmin {
			scenarios[0].token, scenarios[1].token = testAdminToken, testAdminToken
			scenarios = append(scenarios, scenario{name: "forbidden", tc: viewerCase(tool, cases[0]), token: testViewerToken, wantErr: true, code: "FORBIDDEN"})
		}
		for _, sc := range scenarios {
			t.Run(tool.Name+"/"+sc.name, func(t *testing.T) {
				hub := newFakeHub(t)
				sc.tc.setup(hub)
				cs := connect(t, newTestApp(t, hub.srv.URL, appOpts{adminToken: sc.token}))
				out := callTool(t, cs, tool.Name, sc.tc.args)
				if out.res.IsError != sc.wantErr {
					t.Fatalf("isError=%v, want %v: %s", out.res.IsError, sc.wantErr, out.json)
				}
				if sc.code != "" && out.field(t, "error", "code") != sc.code {
					t.Fatalf("error code %v, want %s: %s", out.field(t, "error", "code"), sc.code, out.json)
				}
				calls := hub.callsTo("")
				if len(calls) == 0 {
					t.Fatal("no Hub call")
				}
				for _, call := range calls {
					method, ok := declared[call.OperationID]
					if !ok {
						t.Errorf("%s called %s, not in its maps_to %v", tool.Name, call.OperationID, declared)
					} else if call.Method != method {
						t.Errorf("%s sent %s %s for %s, the contract declares %s", tool.Name, call.Method, call.Path, call.OperationID, method)
					}
				}
			})
		}
	}
	if covered != 42 {
		t.Errorf("%d tools covered, want 42", covered)
	}
}

// TestSpawnedBinaryOverStdio is the protocol smoke run: the hubmcp binary (this test binary in
// exec-main mode) is spawned as an MCP client would, then initialize, tools/list and tools/call
// go over its real stdin/stdout.
func TestSpawnedBinaryOverStdio(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("listIntegrations", hub.pub("listIntegrations", 200, ""))
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-transport", "stdio")
	cmd.Env = append(os.Environ(), "HUBMCP_TEST_EXEC_MAIN=1", "HUB_BASE_URL="+hub.srv.URL, "HUB_API_KEY="+testKey)
	var stderr syncBuffer
	cmd.Stderr = &stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "spawn-test", Version: "1"}, nil).Connect(t.Context(), &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v\n%s", err, stderr.String())
	}
	if info := cs.InitializeResult().ServerInfo; info.Name != "hubmcp" || info.Title != "Integration Hub" {
		t.Errorf("server info %+v", info)
	}
	if n := len(toolNames(t, cs)); n != 5 {
		t.Errorf("%d tools, want 5", n)
	}
	out := callTool(t, cs, toolListIntegrations, map[string]any{"limit": 5})
	if out.res.IsError {
		t.Fatalf("tool call failed: %s", out.json)
	}
	if err := cs.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	logs := stderr.String()
	if !strings.Contains(logs, `"msg":"tool_called"`) || !strings.Contains(logs, `"tool":"hub_list_integrations"`) {
		t.Errorf("no tool_called log: %s", logs)
	}
	if strings.Contains(logs, testKey) {
		t.Errorf("key in logs: %s", logs)
	}
}
