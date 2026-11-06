package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HUB-315: the admin write tools of hubmcp-tools 1.1.0 and their safety rules.

// contractExample returns the first example input of a tool in the frozen contract (a fresh copy
// on every call). check.mjs validates every example input against the tool's input schema.
func contractExample(t testing.TB, tool string) map[string]any {
	t.Helper()
	var c struct {
		Tools []struct {
			Name     string `json:"name"`
			Examples []struct {
				Input map[string]any `json:"input"`
			} `json:"examples"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(toolContractJSON, &c); err != nil {
		t.Fatal(err)
	}
	for _, tl := range c.Tools {
		if tl.Name == tool && len(tl.Examples) > 0 {
			return tl.Examples[0].Input
		}
	}
	t.Fatalf("tool %s has no example input", tool)
	return nil
}

// adminErrorStatus is, per hub-admin operation of a contract-driven tool, one non-403 error the
// operation declares (the reply body is the contract's example for it).
var adminErrorStatus = map[string]int{
	"listTenants": 400, "getTenant": 404, "createTenant": 409, "updateTenant": 412, "createApiKey": 404,
	"listVendors": 400, "getVendor": 404, "createVendor": 409, "updateVendor": 412,
	"listVendorEndpoints": 404, "getVendorEndpoint": 404, "createVendorEndpoint": 404,
	"listAccounts": 400, "getAccount": 404, "updateAccount": 412, "listCapabilities": 400,
	"getTransformer": 404, "createTransformerVersion": 422, "runTransformerGoldens": 400, "dryRunTransformer": 400,
	"listIntegrations": 400, "getIntegration": 404, "createIntegration": 409, "getIntegrationVersion": 404,
	"diffIntegrationVersions": 404, "createIntegrationDraft": 409, "updateIntegrationDraft": 409,
	"validateIntegrationVersion": 404, "publishIntegrationVersion": 409, "updateIntegrationWeights": 409,
	"rollbackIntegration": 422, "getRegistrarEndpoint": 404, "createRegistrarEndpoint": 409, "updateRegistrarEndpoint": 412,
}

// successExample names the 2xx example of operations that have named examples only.
var successExample = map[string]string{"getIntegrationVersion": "single_operation"}

// successMutate adjusts 2xx examples the testkit's validator cannot take verbatim: kin-openapi
// applies OpenAPI 3.0 nullability, so the diff example's `before: null` on an "add" change fails
// an untyped schema; an add has no before, so it is dropped.
var successMutate = map[string]func(map[string]any){
	"diffIntegrationVersions": func(m map[string]any) {
		for _, ch := range m["changes"].([]any) {
			c := ch.(map[string]any)
			for _, k := range []string{"before", "after"} {
				if v, ok := c[k]; ok && v == nil {
					delete(c, k)
				}
			}
		}
	},
}

// adminSuccess is the operation's 2xx example reply.
func adminSuccess(h *fakeHub, op string) reply {
	if f := successMutate[op]; f != nil {
		return h.adm(op, successStatus(op), successExample[op], f)
	}
	return h.adm(op, successStatus(op), successExample[op])
}

func successStatus(op string) int {
	if strings.HasPrefix(op, "create") {
		return http.StatusCreated
	}
	return http.StatusOK
}

// adminToolCases is the success and error scenario of a contract-driven admin tool: the
// contract's example input, answered with the operation's 2xx example or its error example.
func adminToolCases(t testing.TB, tool *toolSpec) [2]toolCase {
	t.Helper()
	op := tool.MapsTo[0].OperationID
	errStatus, ok := adminErrorStatus[op]
	if !ok {
		t.Fatalf("%s: no error status for %s", tool.Name, op)
	}
	return [2]toolCase{
		{func(h *fakeHub) { h.on(op, adminSuccess(h, op)) }, contractExample(t, tool.Name)},
		{func(h *fakeHub) { h.on(op, h.adm(op, errStatus, "")) }, contractExample(t, tool.Name)},
	}
}

// viewerCase is the FORBIDDEN scenario of an admin tool called with the viewer token. Operations
// needing editor or admin are refused by the fake's RBAC; for read operations the viewer may call,
// the Hub's 403 for an object outside the user's scope is queued instead.
func viewerCase(tool *toolSpec, success toolCase) toolCase {
	op := tool.MapsTo[0].OperationID
	return toolCase{args: success.args, setup: func(h *fakeHub) {
		if r := h.roles[op]; r != "editor" && r != "admin" {
			h.on(op, h.adm(op, http.StatusForbidden, ""))
		}
	}}
}

// TestToolRegistrationByTransport: tenant tools always; admin tools only with HUB_ADMIN_TOKEN;
// write tools (requires.transport ["stdio"]) only on stdio, never on Streamable HTTP, loopback
// included. Over HTTP the admin client's allowlist holds no write operation either.
func TestToolRegistrationByTransport(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	var tenant, adminRead, adminWrite []string
	for _, tool := range c.Tools {
		switch {
		case tool.Audience == audienceTenant:
			tenant = append(tenant, tool.Name)
		case tool.mutating():
			adminWrite = append(adminWrite, tool.Name)
		default:
			adminRead = append(adminRead, tool.Name)
		}
	}
	if len(tenant) != 5 || len(adminRead) != 21 || len(adminWrite) != 16 {
		t.Fatalf("contract: %d tenant, %d admin read, %d admin write tools; want 5, 21, 16", len(tenant), len(adminRead), len(adminWrite))
	}
	tests := []struct {
		name      string
		transport string
		token     string
		want      [][]string
		instr     string
	}{
		{name: "stdio with token", transport: transportStdio, token: testAdminToken, want: [][]string{tenant, adminRead, adminWrite}},
		{name: "stdio without token", transport: transportStdio, want: [][]string{tenant}, instr: "HUB_ADMIN_TOKEN is not set"},
		{name: "http loopback with token", transport: transportHTTP, token: testAdminToken, want: [][]string{tenant, adminRead}, instr: "not available over Streamable HTTP"},
		{name: "http without token", transport: transportHTTP, want: [][]string{tenant}, instr: "HUB_ADMIN_TOKEN is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t)
			a := newTestApp(t, hub.srv.URL, appOpts{transport: tt.transport, adminToken: tt.token})
			cs := connect(t, a)
			want := slices.Concat(tt.want...)
			slices.Sort(want)
			if got := toolNames(t, cs); !slices.Equal(got, want) {
				t.Fatalf("%d tools %v\nwant %d %v", len(got), got, len(want), want)
			}
			if tt.instr != "" && !strings.Contains(cs.InitializeResult().Instructions, tt.instr) {
				t.Errorf("instructions lack %q: %s", tt.instr, cs.InitializeResult().Instructions)
			}
			if a.admin == nil {
				return
			}
			for _, tool := range c.Tools {
				if !tool.mutating() {
					continue
				}
				op := tool.MapsTo[0].OperationID
				_, allowed := a.admin.allowed[op]
				if allowed != (tt.transport == transportStdio) {
					t.Errorf("%s in the admin allowlist: %v", op, allowed)
				}
				if tt.transport == transportHTTP {
					_, err := a.admin.do(t.Context(), adminCall{op: op, path: map[string]string{}, body: []byte("{}")})
					if err == nil || !strings.Contains(err.Error(), errOperationNotAllowed.Error()) {
						t.Errorf("%s over http: %v", op, err)
					}
				}
			}
			if n := len(hub.callsTo("")); n != 0 {
				t.Errorf("%d Hub calls", n)
			}
		})
	}
}

// TestNoToolMapsToDeleteOrForbidden (replaces TestAdminToolsAreReadOnly): driven by the contract.
// No registered tool maps to an HTTP DELETE or to a forbidden operation; every forbidden
// operationId is absent from all maps_to; every maps_to http_method equals the method the
// contract-validated fake admin API declares; every DELETE of hub-admin is forbidden; and only
// tools that map to a mutating operation are write tools.
func TestNoToolMapsToDeleteOrForbidden(t *testing.T) {
	hub := newFakeHub(t)
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	methodOf := map[string]map[string]string{"hub-admin.v1.yaml": {}, "hub-public.v1.yaml": {}}
	for _, op := range hub.admin.Operations() {
		methodOf["hub-admin.v1.yaml"][op.ID] = op.Method
	}
	for _, op := range hub.public.Operations() {
		methodOf["hub-public.v1.yaml"][op.ID] = op.Method
	}
	forbidden := map[string]bool{}
	for _, f := range c.ForbiddenOperations {
		forbidden[f.OperationID] = true
		if methodOf[adminSpec][f.OperationID] != f.HTTPMethod {
			t.Errorf("forbidden %s: method %s, spec %s", f.OperationID, f.HTTPMethod, methodOf[adminSpec][f.OperationID])
		}
	}
	if len(forbidden) != 17 {
		t.Errorf("%d forbidden operations, want 17", len(forbidden))
	}
	for id, m := range methodOf[adminSpec] {
		if m == http.MethodDelete && !forbidden[id] {
			t.Errorf("hub-admin DELETE %s is not in forbidden_operations", id)
		}
	}
	mapped := map[string]bool{}
	a := testApp(t, hub.srv.URL, true, nil) // stdio with token: the superset of registered tools
	registered := registeredTools(c, a.transport, true)
	if len(registered) != 42 {
		t.Fatalf("%d registered tools, want 42", len(registered))
	}
	for _, tool := range registered {
		mutatingOp := false
		for _, m := range tool.MapsTo {
			spec := methodOf[m.Spec]
			if spec == nil {
				t.Errorf("%s maps to unknown spec %s", tool.Name, m.Spec)
				continue
			}
			got, ok := spec[m.OperationID]
			switch {
			case !ok:
				t.Errorf("%s maps to %s#%s, not an operation", tool.Name, m.Spec, m.OperationID)
			case got != m.HTTPMethod:
				t.Errorf("%s maps to %s as %s; the fake admin API declares %s", tool.Name, m.OperationID, m.HTTPMethod, got)
			}
			if got == http.MethodDelete || m.HTTPMethod == http.MethodDelete {
				t.Errorf("%s maps to an HTTP DELETE (%s)", tool.Name, m.OperationID)
			}
			if m.Spec == adminSpec {
				if forbidden[m.OperationID] {
					t.Errorf("%s maps to forbidden operation %s", tool.Name, m.OperationID)
				}
				mapped[m.OperationID] = true
				nonMutating := got == http.MethodGet || slices.Contains([]string{"validateIntegrationVersion", "runTransformerGoldens", "dryRunTransformer"}, m.OperationID)
				mutatingOp = mutatingOp || !nonMutating
			}
		}
		if tool.Audience == audienceAdmin {
			if tool.mutating() != mutatingOp {
				t.Errorf("%s: readOnlyHint=%v but maps to a mutating operation: %v", tool.Name, tool.Annotations.ReadOnlyHint, mutatingOp)
			}
			if tool.Annotations.DestructiveHint {
				t.Errorf("%s: destructiveHint on an admin tool", tool.Name)
			}
		}
	}
	for id := range forbidden {
		if mapped[id] {
			t.Errorf("forbidden %s appears in a maps_to", id)
		}
		if _, ok := a.admin.allowed[id]; ok {
			t.Errorf("forbidden %s is in the admin client's allowlist", id)
		}
	}
	// Every hub-admin operation is classified exactly once (mapped, forbidden or not exposed).
	seen := map[string]int{}
	for id := range mapped {
		seen[id]++
	}
	for _, l := range [][]classifiedOp{c.ForbiddenOperations, c.NotExposedOperations} {
		for _, o := range l {
			seen[o.OperationID]++
		}
	}
	for id := range methodOf[adminSpec] {
		if seen[id] != 1 {
			t.Errorf("hub-admin %s classified %d times", id, seen[id])
		}
	}
}

// TestLoadContractRefusesUnsafeTools: the server refuses to start from a contract copy in which a
// tool maps to a DELETE or a forbidden operation, a write tool lacks reason or is not stdio-only.
func TestLoadContractRefusesUnsafeTools(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(tools []any, find func(string) map[string]any)
		wantErr string
	}{
		{name: "maps to a DELETE", wantErr: "HTTP method", mutate: func(_ []any, find func(string) map[string]any) {
			find("hub_admin_update_registrar_endpoint")["maps_to"] = []any{map[string]any{"spec": adminSpec, "operationId": "deleteRegistrarEndpoint", "http_method": "DELETE"}}
		}},
		{name: "maps to a forbidden POST", wantErr: "forbidden operation purgeDeadLetters", mutate: func(_ []any, find func(string) map[string]any) {
			find("hub_admin_create_tenant")["maps_to"] = []any{map[string]any{"spec": adminSpec, "operationId": "purgeDeadLetters", "http_method": "POST"}}
		}},
		{name: "write tool without required reason", wantErr: "must require reason", mutate: func(_ []any, find func(string) map[string]any) {
			in := find("hub_admin_create_vendor")["inputSchema"].(map[string]any)
			in["required"] = []any{"vendor_id", "display_name", "auth_scheme", "regions"}
		}},
		{name: "write tool offered over http", wantErr: "stdio-only", mutate: func(_ []any, find func(string) map[string]any) {
			find("hub_admin_publish_integration_version")["requires"].(map[string]any)["transport"] = []any{"stdio", "streamable-http"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(toolContractJSON, &doc); err != nil {
				t.Fatal(err)
			}
			tools := doc["tools"].([]any)
			find := func(name string) map[string]any {
				for _, x := range tools {
					if m := x.(map[string]any); m["name"] == name {
						return m
					}
				}
				t.Fatalf("no tool %s", name)
				return nil
			}
			tt.mutate(tools, find)
			_, err := loadContract(mustJSON(t, doc))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestAdminRoutesMatchTheContract: hubmcp's route table is exactly the admin operations the tool
// contract maps, each with the OpenAPI document's method and path.
func TestAdminRoutesMatchTheContract(t *testing.T) {
	hub := newFakeHub(t)
	spec := map[string]adminRoute{}
	for _, op := range hub.admin.Operations() {
		spec[op.ID] = adminRoute{method: op.Method, path: op.Path}
	}
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	all := make([]*toolSpec, 0, len(c.Tools))
	for i := range c.Tools {
		all = append(all, &c.Tools[i])
	}
	ops := adminOperations(all)
	if len(ops) != len(adminRoutes) {
		t.Errorf("%d routes, %d mapped admin operations", len(adminRoutes), len(ops))
	}
	for id, r := range adminRoutes {
		if _, ok := ops[id]; !ok {
			t.Errorf("route %s is not mapped by any tool", id)
		}
		if spec[id] != r {
			t.Errorf("route %s = %v, contract %v", id, r, spec[id])
		}
		if !allowedMethod(r.method) {
			t.Errorf("route %s uses %s", id, r.method)
		}
	}
}

// TestAdminClientCannotDelete: a DELETE is impossible by construction. The admin client refuses
// an operation outside its allowlist, refuses to be built with a DELETE route, and every HTTP
// client hubmcp uses refuses the method before anything leaves the process.
func TestAdminClientCannotDelete(t *testing.T) {
	hub := newFakeHub(t)
	a := testApp(t, hub.srv.URL, true, nil)
	base := hub.srv.URL + "/admin/v1"

	t.Run("no route for a DELETE operation", func(t *testing.T) {
		_, err := newAdminClient(base, testAdminToken, a.http, "t", map[string]string{"deleteRegistrarEndpoint": http.MethodDelete})
		if err == nil {
			t.Fatal("built a client with a DELETE operation")
		}
	})
	t.Run("a DELETE route is refused at construction", func(t *testing.T) {
		adminRoutes["deleteRegistrarEndpoint"] = adminRoute{http.MethodDelete, "/registrar-endpoints/{endpoint_id}"}
		t.Cleanup(func() { delete(adminRoutes, "deleteRegistrarEndpoint") })
		_, err := newAdminClient(base, testAdminToken, a.http, "t", map[string]string{"deleteRegistrarEndpoint": http.MethodDelete})
		if err == nil || !strings.Contains(err.Error(), "never sent") {
			t.Fatalf("error %v", err)
		}
	})
	t.Run("a DELETE route smuggled into the allowlist is refused at send time", func(t *testing.T) {
		c := &adminClient{base: base, token: testAdminToken, http: a.http, allowed: map[string]adminRoute{
			"deleteRegistrarEndpoint": {http.MethodDelete, "/registrar-endpoints/{endpoint_id}"}}}
		_, err := c.do(t.Context(), adminCall{op: "deleteRegistrarEndpoint", path: map[string]string{"endpoint_id": "reg_orders_cb"}})
		if err == nil {
			t.Fatal("sent a DELETE")
		}
	})
	t.Run("forbidden and unknown operations are not in the allowlist", func(t *testing.T) {
		c, err := loadContract(toolContractJSON)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range append(c.ForbiddenOperations, c.NotExposedOperations...) {
			_, err := a.admin.do(t.Context(), adminCall{op: f.OperationID})
			if err == nil || !strings.Contains(err.Error(), errOperationNotAllowed.Error()) {
				t.Errorf("%s: %v", f.OperationID, err)
			}
		}
	})
	t.Run("the guarded HTTP client never sends DELETE", func(t *testing.T) {
		for _, target := range []string{hub.srv.URL + "/admin/v1/registrar-endpoints/reg_orders_cb", hub.srv.URL + "/v1/integrations"} {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := a.http.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatalf("DELETE %s was sent", target)
			}
		}
	})
	if n := len(hub.callsTo("")); n != 0 {
		t.Fatalf("%d requests reached the Hub", n)
	}
}

// TestSourceHasNoDeletePath: no non-test source file of hubmcp names the DELETE method (no
// http.MethodDelete, no "DELETE" literal), so no code path can construct such a request; the
// method guard above covers pkg/hubclient too.
func TestSourceHasNoDeletePath(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "MethodDelete" {
					t.Errorf("%s: http.MethodDelete", fset.Position(x.Pos()))
				}
			case *ast.BasicLit:
				if x.Kind == token.STRING {
					if v, err := strconv.Unquote(x.Value); err == nil && strings.EqualFold(strings.TrimSpace(v), "delete") {
						t.Errorf("%s: literal %s", fset.Position(x.Pos()), x.Value)
					}
				}
			}
			return true
		})
	}
	if files < 10 {
		t.Fatalf("scanned only %d files", files)
	}
}

// TestWriteToolConfirmGuard: every requires_confirm tool without confirm=true (absent or false)
// returns CONFIRMATION_REQUIRED and makes no Hub call; with confirm=true it calls the Hub once.
func TestWriteToolConfirmGuard(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range c.Tools {
		tool := &c.Tools[i]
		if tool.RequiresConfirm == "" {
			continue
		}
		n++
		op := tool.MapsTo[0].OperationID
		for _, confirm := range []any{nil, false, true} {
			t.Run(tool.Name+"/confirm="+strings.ToLower(strconv.Quote(jsonText(confirm))), func(t *testing.T) {
				hub := newFakeHub(t)
				hub.on(op, adminSuccess(hub, op))
				cs := connect(t, testApp(t, hub.srv.URL, true, nil))
				args := contractExample(t, tool.Name)
				delete(args, "confirm")
				if confirm != nil {
					args["confirm"] = confirm
				}
				out := callTool(t, cs, tool.Name, args)
				calls := len(hub.callsTo(""))
				if confirm == true {
					if out.res.IsError || calls != 1 {
						t.Fatalf("confirmed: isError=%v, %d calls: %s", out.res.IsError, calls, out.json)
					}
					return
				}
				if !out.res.IsError || out.field(t, "error", "code") != codeConfirmationRequired || calls != 0 {
					t.Fatalf("unconfirmed: %d calls: %s", calls, out.json)
				}
				if text := out.res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "the Hub was not called") || !strings.Contains(text, "confirm=true") {
					t.Errorf("refusal text: %s", text)
				}
			})
		}
	}
	if n != 9 {
		t.Errorf("%d requires_confirm tools, want 9", n)
	}
}

func jsonText(v any) string {
	if v == nil {
		return "absent"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// TestNoSecretInputProperties: no tool input has a property named in forbidden_input_properties,
// and no mutating tool input one in forbidden_mutation_input_properties (walked recursively).
func TestNoSecretInputProperties(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ForbiddenInputProperties) == 0 || len(c.ForbiddenMutationInputProperties) == 0 {
		t.Fatal("the contract lists no forbidden input properties")
	}
	var walk func(v any, out map[string]bool)
	walk = func(v any, out map[string]bool) {
		switch x := v.(type) {
		case map[string]any:
			if props, ok := x["properties"].(map[string]any); ok {
				for k := range props {
					out[k] = true
				}
			}
			for _, vv := range x {
				walk(vv, out)
			}
		case []any:
			for _, vv := range x {
				walk(vv, out)
			}
		}
	}
	for _, tool := range c.Tools {
		var schema any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		names := map[string]bool{}
		walk(schema, names)
		for _, p := range c.ForbiddenInputProperties {
			if names[p] {
				t.Errorf("%s: input property %q is forbidden", tool.Name, p)
			}
		}
		if tool.mutating() {
			for _, p := range c.ForbiddenMutationInputProperties {
				if names[p] {
					t.Errorf("%s: mutating tool has input property %q", tool.Name, p)
				}
			}
		}
	}
}

// Secrets the tests plant: they match the contract's secret_patterns.
const (
	plantedAPIKey        = "hk_live_7Qm2EXAMPLEexampleEXAMPLE0000"
	plantedSigningSecret = "whsec_EXAMPLEexampleEXAMPLEexample00"
)

// TestSecretInInputIsRefused: for every tool (all 42 on stdio with a token), arguments carrying
// a string that matches secret_patterns are refused with SECRET_IN_INPUT (hub_invoke: outcome
// refused), the secret is not echoed, and the Hub is never called.
func TestSecretInInputIsRefused(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	matrix := toolMatrix()
	for i := range c.Tools {
		tool := &c.Tools[i]
		var base map[string]any
		if cases, ok := matrix[tool.Name]; ok {
			base = cases[0].args
		} else {
			base = contractExample(t, tool.Name)
		}
		for _, planted := range []string{plantedAPIKey, plantedSigningSecret} {
			t.Run(tool.Name+"/"+planted[:5], func(t *testing.T) {
				args := mustCopy(t, base)
				injected := false
				for k, v := range args {
					if _, ok := v.(string); ok && k != "confirm" {
						args[k] = "see " + planted
						injected = true
						break
					}
				}
				if !injected {
					args["context"] = map[string]any{"note": []any{planted}}
				}
				hub := newFakeHub(t)
				cs := connect(t, testApp(t, hub.srv.URL, true, nil))
				out := callTool(t, cs, tool.Name, args)
				if !out.res.IsError || out.field(t, "error", "code") != codeSecretInInput {
					t.Fatalf("not refused: %s", out.json)
				}
				if tool.Name == toolInvoke && out.field(t, "outcome") != "refused" {
					t.Errorf("hub_invoke outcome %v", out.field(t, "outcome"))
				}
				if strings.Contains(string(out.json), planted) || strings.Contains(out.res.Content[0].(*mcp.TextContent).Text, planted) {
					t.Errorf("secret echoed: %s", out.json)
				}
				if n := len(hub.callsTo("")); n != 0 {
					t.Errorf("%d Hub calls", n)
				}
			})
		}
	}
}

func mustCopy(t testing.TB, m map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(mustJSON(t, m), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSensitiveOutputIsReturnedOnceAndNeverLogged: the minted API key and signing secret appear in
// the minting result only (with the SENSITIVE OUTPUT note), never in any log line (including
// tool_called), and are redacted by value from every later result and error text.
func TestSensitiveOutputIsReturnedOnceAndNeverLogged(t *testing.T) {
	tests := []struct {
		tool, op, field, minted string
	}{
		{hubAdminCreateAPIKey, "createApiKey", "secret", plantedAPIKey},
		{"hub_admin_create_registrar_endpoint", "createRegistrarEndpoint", "signing_secret", plantedSigningSecret},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			hub := newFakeHub(t)
			var logs syncBuffer
			cs := connect(t, newTestApp(t, hub.srv.URL, appOpts{adminToken: testAdminToken, logs: &logs}))
			hub.on(tt.op, hub.adm(tt.op, http.StatusCreated, ""))
			out := callTool(t, cs, tt.tool, contractExample(t, tt.tool))
			if out.res.IsError || out.field(t, tt.field) != tt.minted {
				t.Fatalf("minting result: %s", out.json)
			}
			if text := out.res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "SENSITIVE OUTPUT") || !strings.Contains(text, tt.minted) {
				t.Errorf("text content: %s", text)
			}
			// A later Hub answer that echoes the secret, in a result and in an error message.
			hub.on("getTenant", hub.adm("getTenant", http.StatusOK, "", func(m map[string]any) { m["description"] = "copy of " + tt.minted }))
			later := callTool(t, cs, "hub_admin_get_tenant", map[string]any{"tenant_id": "orders-service"})
			hub.on("createTenant", hub.adm("createTenant", http.StatusConflict, "", func(m map[string]any) {
				m["error"].(map[string]any)["message"] = "conflict with " + tt.minted
			}))
			failed := callTool(t, cs, "hub_admin_create_tenant", map[string]any{"tenant_id": "orders-service", "display_name": "Orders", "reason": "onboarding"})
			for _, o := range []toolOutcome{later, failed} {
				if strings.Contains(string(o.json), tt.minted) || strings.Contains(o.res.Content[0].(*mcp.TextContent).Text, tt.minted) {
					t.Errorf("minted secret in a later result: %s", o.json)
				}
				if !strings.Contains(string(o.json), "***") {
					t.Errorf("not redacted: %s", o.json)
				}
			}
			l := logs.String()
			if strings.Count(l, `"msg":"tool_called"`) != 3 || !strings.Contains(l, `"tool":"`+tt.tool+`"`) {
				t.Fatalf("tool_called lines: %s", l)
			}
			if strings.Contains(l, tt.minted) {
				t.Fatalf("minted secret in logs: %s", l)
			}
		})
	}
}

const hubAdminCreateAPIKey = "hub_admin_create_api_key"

// TestReasonIfMatchAndETag: every write tool forwards reason with " (via hubmcp)" appended and
// never sends confirm or if_match in the body; if_match is sent as If-Match; get tools return etag
// from the ETag header (null without one); the tool_called line carries mutating and change_id.
func TestReasonIfMatchAndETag(t *testing.T) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("reason suffix on every write tool", func(t *testing.T) {
		n := 0
		for i := range c.Tools {
			tool := &c.Tools[i]
			if !tool.mutating() {
				continue
			}
			n++
			op := tool.MapsTo[0].OperationID
			hub := newFakeHub(t)
			hub.on(op, adminSuccess(hub, op))
			var logs syncBuffer
			cs := connect(t, newTestApp(t, hub.srv.URL, appOpts{adminToken: testAdminToken, logs: &logs}))
			args := contractExample(t, tool.Name)
			out := callTool(t, cs, tool.Name, args)
			if out.res.IsError {
				t.Fatalf("%s: %s", tool.Name, out.json)
			}
			var body map[string]any
			if err := json.Unmarshal(hub.callsTo(op)[0].Body, &body); err != nil {
				t.Fatalf("%s body: %v", tool.Name, err)
			}
			if body["reason"] != args["reason"].(string)+reasonSuffix {
				t.Errorf("%s: reason %q", tool.Name, body["reason"])
			}
			for _, k := range []string{"confirm", "if_match"} {
				if _, ok := body[k]; ok {
					t.Errorf("%s: %s sent in the body", tool.Name, k)
				}
			}
			changeID, _ := out.field(t, "change_id").(string)
			if l := logs.String(); changeID == "" || !strings.Contains(l, `"mutating":true`) || !strings.Contains(l, `"change_id":"`+changeID+`"`) {
				t.Errorf("%s: change_id %q, log %s", tool.Name, changeID, l)
			}
		}
		if n != 16 {
			t.Errorf("%d write tools, want 16", n)
		}
	})
	t.Run("if_match is sent as If-Match", func(t *testing.T) {
		for _, ifMatch := range []string{`"3"`, ""} {
			hub := newFakeHub(t)
			hub.on("updateTenant", hub.adm("updateTenant", http.StatusOK, ""))
			cs := connect(t, testApp(t, hub.srv.URL, true, nil))
			args := map[string]any{"tenant_id": "orders-service", "rate_limit": map[string]any{"rps": 400, "burst": 800}, "reason": "Peak sale", "confirm": true}
			if ifMatch != "" {
				args["if_match"] = ifMatch
			}
			if out := callTool(t, cs, "hub_admin_update_tenant", args); out.res.IsError {
				t.Fatal(string(out.json))
			}
			call := hub.callsTo("updateTenant")[0]
			if got := call.Header.Get("If-Match"); got != ifMatch {
				t.Errorf("If-Match %q, want %q", got, ifMatch)
			}
			if call.Path != "/admin/v1/tenants/orders-service" || call.Method != http.MethodPatch {
				t.Errorf("sent %s %s", call.Method, call.Path)
			}
		}
	})
	t.Run("a stale if_match surfaces PRECONDITION_FAILED", func(t *testing.T) {
		hub := newFakeHub(t)
		hub.on("updateTenant", hub.adm("updateTenant", http.StatusPreconditionFailed, ""))
		cs := connect(t, testApp(t, hub.srv.URL, true, nil))
		out := callTool(t, cs, "hub_admin_update_tenant", map[string]any{"tenant_id": "orders-service", "display_name": "Orders", "reason": "rename", "if_match": `"2"`, "confirm": true})
		if !out.res.IsError || out.field(t, "error", "code") != "PRECONDITION_FAILED" {
			t.Fatal(string(out.json))
		}
	})
	t.Run("get tools return etag", func(t *testing.T) {
		gets := 0
		for i := range c.Tools {
			tool := &c.Tools[i]
			if !tool.outputETag {
				continue
			}
			gets++
			op := tool.MapsTo[0].OperationID
			for _, etag := range []string{`"7"`, ""} {
				hub := newFakeHub(t)
				r := hub.adm(op, http.StatusOK, successExample[op])
				if etag != "" {
					r.header = map[string]string{"ETag": etag}
				}
				hub.on(op, r)
				cs := connect(t, testApp(t, hub.srv.URL, true, nil))
				out := callTool(t, cs, tool.Name, contractExample(t, tool.Name))
				var want any
				if etag != "" {
					want = etag
				}
				if got := out.field(t, "etag"); got != want {
					t.Errorf("%s: etag %v, want %v", tool.Name, got, want)
				}
			}
		}
		if gets != 4 {
			t.Errorf("%d tools return etag, want 4", gets)
		}
	})
}

// TestCreateTransformerVersionDropsSource: the result does not repeat the yaml and fixtures the
// model just sent.
func TestCreateTransformerVersionDropsSource(t *testing.T) {
	hub := newFakeHub(t)
	hub.on("createTransformerVersion", hub.adm("createTransformerVersion", http.StatusCreated, ""))
	cs := connect(t, testApp(t, hub.srv.URL, true, nil))
	out := callTool(t, cs, toolAdminCreateTransformerVersion, contractExample(t, toolAdminCreateTransformerVersion))
	tv, _ := out.field(t, "transformer_version").(map[string]any)
	if out.res.IsError || tv["ref"] == nil {
		t.Fatal(string(out.json))
	}
	if _, ok := tv["yaml"]; ok {
		t.Errorf("yaml echoed: %s", out.json)
	}
	if _, ok := tv["fixtures"]; ok {
		t.Errorf("fixtures echoed: %s", out.json)
	}
}

// TestAdminClientRefusesDotSegments: a path value of "." or ".." (which url.PathEscape keeps)
// would leave the route; the client refuses it without sending anything.
func TestAdminClientRefusesDotSegments(t *testing.T) {
	hub := newFakeHub(t)
	a := testApp(t, hub.srv.URL, true, nil)
	for _, v := range []string{".", ".."} {
		_, err := a.admin.do(t.Context(), adminCall{op: "getTenant", path: map[string]string{"tenant_id": v}})
		if err == nil || !strings.Contains(err.Error(), "invalid path parameter") {
			t.Errorf("%q: %v", v, err)
		}
	}
	if n := len(hub.callsTo("")); n != 0 {
		t.Fatalf("%d requests sent", n)
	}
}
