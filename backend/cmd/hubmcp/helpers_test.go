package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.uber.org/goleak"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// Test credentials: canaries that must never show up in logs or tool output.
const (
	testKey        = "hk_test_mcp_canary_7c1e93EXAMPLEnotreal"
	testAdminToken = "eyJ.admin-token-canary-4b8d.notreal"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// frozenContract reads contracts/mcp/hubmcp-tools.json from the repository (not the embedded copy).
func frozenContract(t testing.TB) []byte {
	t.Helper()
	dir, err := contract.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "mcp", "hubmcp-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testApp builds the app against baseURL with the test credentials. withAdmin configures the
// admin client (base baseURL + /admin/v1).
func testApp(t testing.TB, baseURL string, withAdmin bool, logs io.Writer) *app {
	t.Helper()
	env := map[string]string{"HUB_BASE_URL": baseURL, "HUB_API_KEY": testKey}
	if withAdmin {
		env["HUB_ADMIN_TOKEN"] = testAdminToken
	}
	cfg, err := loadConfig(nil, func(k string) string { return env[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if logs == nil {
		logs = io.Discard
	}
	secrets := newSecretSet(cfg.apiKey, cfg.adminToken)
	a, err := newApp(cfg, newLogger(logs, 0, secrets), secrets, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// connect starts the MCP server for a over an in-memory transport and returns a client session.
func connect(t testing.TB, a *app) *mcp.ClientSession {
	t.Helper()
	c, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := newMCPServer(c, a, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

// jsonEqual compares two JSON documents semantically.
func jsonEqual(t testing.TB, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("unmarshal %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return reflect.DeepEqual(va, vb)
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// toolOutcome is a tool call's result: the raw MCP result and its structured content as JSON.
type toolOutcome struct {
	res  *mcp.CallToolResult
	json []byte
}

func (o toolOutcome) field(t testing.TB, path ...string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(o.json, &v); err != nil {
		t.Fatal(err)
	}
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: not an object at %q in %s", path, p, o.json)
		}
		v = m[p]
	}
	return v
}

// callTool calls a tool and checks the result against the frozen contract: success results must
// match the tool's outputSchema; hub_invoke results always must; other error results carry a
// canonical error object. Every result is also scanned for vendor identity (except hub_dry_run).
func callTool(t testing.TB, cs *mcp.ClientSession, name string, args any) toolOutcome {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v (tool failures must be results)", name, err)
	}
	b := mustJSON(t, res.StructuredContent)
	if len(res.Content) != 1 {
		t.Fatalf("%s: %d content items, want 1", name, len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "{") {
		t.Fatalf("%s: content is not the JSON text of the result: %#v", name, res.Content[0])
	}
	if !res.IsError || name == toolInvoke {
		validateOutput(t, name, b)
	}
	if res.IsError {
		var env struct {
			Error map[string]any `json:"error"`
		}
		_ = json.Unmarshal(b, &env)
		if env.Error["code"] == nil || env.Error["message"] == nil || env.Error["retryable"] == nil {
			t.Fatalf("%s: error result lacks a canonical error object: %s", name, b)
		}
	}
	if name != toolDryRun && !strings.HasPrefix(name, "hub_admin_") {
		assertNoVendorIdentity(t, name, b)
		assertNoVendorIdentity(t, name, []byte(text.Text))
	}
	if bytes.Contains(b, []byte(testKey)) || bytes.Contains(b, []byte(testAdminToken)) {
		t.Fatalf("%s: credential in tool output: %s", name, b)
	}
	return toolOutcome{res: res, json: b}
}

var outputSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	c, err := loadContract(toolContractJSON)
	if err != nil {
		return nil, err
	}
	out := map[string]*jsonschema.Schema{}
	for _, tool := range c.Tools {
		s, err := compileSchema(tool.Name+"/output", tool.OutputSchema)
		if err != nil {
			return nil, err
		}
		out[tool.Name] = s
	}
	return out, nil
})

func validateOutput(t testing.TB, tool string, b []byte) {
	t.Helper()
	schemas, err := outputSchemas()
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := schemas[tool].Validate(v); err != nil {
		t.Fatalf("%s output violates the contract outputSchema: %v\n%s", tool, err, b)
	}
}

// Vendor identity planted by tests in Hub responses (vendor/account names, version fields and
// debug headers); none of it may reach a tenant tool's output.
var (
	leakKeys   = []string{"vendor", "vendor_id", "account", "account_id", "integration_version", "final_version", "version", "transformer", "transformers", "served_by", "endpoint_ref"}
	leakValues = []string{"clevertap", "ct-brandA-in", "ct-brandB-us", "failover:1", "T1@7"}
)

func assertNoVendorIdentity(t testing.TB, tool string, b []byte) {
	t.Helper()
	low := strings.ToLower(string(b))
	for _, v := range leakValues {
		if strings.Contains(low, strings.ToLower(v)) {
			t.Fatalf("%s output leaks vendor identity %q: %s", tool, v, b)
		}
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				if slices.Contains(leakKeys, k) {
					t.Fatalf("%s output has vendor/version field %q: %s", tool, k, b)
				}
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	var v any
	if json.Unmarshal(b, &v) == nil {
		walk(v)
	}
}

// plantVendorIdentity adds vendor/account/version fields a broken Hub might send.
func plantVendorIdentity(m map[string]any) {
	m["vendor_id"] = "clevertap"
	m["account_id"] = "ct-brandA-in"
	m["integration_version"] = 7
	m["served_by"] = "failover:1"
}

// leakHeaders are debug headers hubmcp must drop.
var leakHeaders = map[string]string{"X-Hub-Integration-Version": "7", "X-Hub-Served-By": "failover:1"}
