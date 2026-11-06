package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
