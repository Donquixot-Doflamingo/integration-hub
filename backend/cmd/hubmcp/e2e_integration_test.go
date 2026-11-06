//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/contract"
)

// HUB-307: hubmcp against a running Hub. The hub (gateway, worker, admin) and mockvendor run as
// built binaries, exactly as in the compose stack, on HUB_PG_DSN / HUB_REDIS_ADDR; hubmcp talks
// to them over HTTP only.

// devKey is the documented local-only dev tenant key (backend/config/README.md).
const devKey = "hk_test_dev_orders_service_local_only"

type hubStack struct {
	gatewayURL  string
	adminURL    string // admin base, without /admin/v1
	vendorURL   string
	adminToken  string // the signed-in user's own token: role admin (hubctl login -dev -role admin)
	viewerToken string // another user with role viewer
	logs        map[string]*syncBuffer
}

// e2eActor is the subject and e-mail of the admin token; the change log records it as the actor.
const e2eActor = "mcp-e2e@example.com"

func TestMCPEndToEnd(t *testing.T) {
	st := startHubStack(t)
	defer func() {
		if t.Failed() {
			for name, l := range st.logs {
				t.Logf("--- %s log ---\n%s", name, tail(l.String(), 4000))
			}
		}
	}()

	env := map[string]string{"HUB_BASE_URL": st.gatewayURL, "HUB_API_KEY": devKey,
		"HUB_ADMIN_TOKEN": st.adminToken, "HUB_ADMIN_BASE_URL": st.adminURL + "/admin/v1"}
	cfg, err := loadConfig(nil, func(k string) string { return env[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	tc, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newSecretSet(cfg.apiKey, cfg.adminToken)
	a, err := newApp(cfg, tc, newLogger(&logs, 0, secrets), secrets, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	cs := connect(t, a)
	var outputs []string // every tenant tool output except hub_dry_run, for the final grep
	call := func(tool string, args map[string]any) toolOutcome {
		t.Helper()
		out := callTool(t, cs, tool, args) // validates outputs and scans tenant tools for vendor identity
		if tool != toolDryRun && !strings.HasPrefix(tool, "hub_admin_") {
			outputs = append(outputs, string(out.json), out.res.Content[0].(*mcp.TextContent).Text)
		}
		return out
	}
	body := map[string]any{"user": map[string]any{"id": "u_48213"}, "event": map[string]any{"name": "order_placed", "timestamp": "2026-10-31T09:15:00Z"}}

	t.Run("discovery", func(t *testing.T) {
		out := call(toolListIntegrations, map[string]any{"capability": "events.track@v1"})
		ids := map[string]bool{}
		for _, it := range out.field(t, "items").([]any) {
			ids[it.(map[string]any)["integration_id"].(string)] = true
		}
		for _, id := range []string{"int_evt_brandA", "int_evt_brandB", "int_evt_brandB_min"} {
			if !ids[id] {
				t.Errorf("%s not listed: %s", id, out.json)
			}
		}
		d := call(toolDescribe, map[string]any{"integration_id": "int_evt_brandA"})
		if d.field(t, "integration", "delivery_mode") != "wait" || d.field(t, "capability", "capability") != "events.track@v1" {
			t.Errorf("describe: %s", d.json)
		}
	})

	t.Run("dry-run shows the upstream request (documented exception)", func(t *testing.T) {
		out := call(toolDryRun, map[string]any{"integration_id": "int_evt_brandA", "body": body})
		if out.res.IsError {
			t.Fatalf("dry-run: %s", out.json)
		}
		if url, _ := out.field(t, "upstream_request", "url").(string); !strings.HasPrefix(url, st.vendorURL) {
			t.Errorf("upstream url %q, want mockvendor %s", url, st.vendorURL)
		}
		if out.field(t, "upstream_request", "headers", "X-CleverTap-Passcode") != "***" {
			t.Errorf("secret not redacted: %s", out.json)
		}
		if strings.Contains(string(out.json), "e2e-passcode") {
			t.Fatalf("credential in dry-run output: %s", out.json)
		}
	})

	t.Run("sync invoke completes", func(t *testing.T) {
		out := call(toolInvoke, map[string]any{"integration_id": "int_evt_brandA", "body": body, "idempotency_key": "e2e-sync-" + st.runID()})
		if out.field(t, "outcome") != "completed" || out.field(t, "http_status") != 200.0 || out.field(t, "result", "status") != "ok" {
			t.Fatalf("sync invoke: %s", out.json)
		}
		id := out.field(t, "request_id").(string)
		status := call(toolGetRequest, map[string]any{"request_id": id})
		if status.field(t, "status") != "succeeded" {
			t.Errorf("status of the sync request: %s", status.json)
		}
	})

	var asyncID string
	t.Run("async invoke is queued, then succeeds", func(t *testing.T) {
		out := call(toolInvoke, map[string]any{"integration_id": "int_evt_brandB", "body": body, "idempotency_key": "e2e-async-" + st.runID()})
		if out.field(t, "outcome") != "queued" || out.field(t, "http_status") != 202.0 || out.res.IsError {
			t.Fatalf("async invoke: %s", out.json)
		}
		asyncID = out.field(t, "request_id").(string)
		deadline := time.Now().Add(30 * time.Second)
		for {
			s := call(toolGetRequest, map[string]any{"request_id": asyncID})
			if s.field(t, "status") == "succeeded" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("request still %v after 30 s: %s", s.field(t, "status"), s.json)
			}
			waitFor(t, 300*time.Millisecond)
		}
	})

	t.Run("money/quota integration needs confirm and key", func(t *testing.T) {
		args := map[string]any{"integration_id": "int_evt_brandB_min", "body": body}
		out := call(toolInvoke, args)
		if out.field(t, "outcome") != "refused" || out.field(t, "error", "code") != codeConfirmationRequired {
			t.Fatalf("no confirm: %s", out.json)
		}
		args["confirm"] = true
		out = call(toolInvoke, args)
		if out.field(t, "outcome") != "refused" || out.field(t, "error", "code") != codeIdempotencyKeyRequired {
			t.Fatalf("no key: %s", out.json)
		}
		args["idempotency_key"] = "e2e-money-" + st.runID()
		out = call(toolInvoke, args)
		if out.field(t, "outcome") != "completed" {
			t.Fatalf("confirmed: %s", out.json)
		}
	})

	t.Run("admin read-only tools", func(t *testing.T) {
		if asyncID == "" {
			t.Fatal("the async invoke step did not produce a request id")
		}
		s := call(toolAdminSearch, map[string]any{"integration_id": "int_evt_brandB"})
		if s.res.IsError || !strings.Contains(string(s.json), asyncID) {
			t.Fatalf("search: %s", s.json)
		}
		tl := call(toolAdminTimeline, map[string]any{"request_id": asyncID})
		if tl.res.IsError || len(tl.field(t, "events").([]any)) == 0 || len(tl.field(t, "attempts").([]any)) == 0 {
			t.Fatalf("timeline: %s", tl.json)
		}
		b := call(toolAdminBreakerStates, map[string]any{})
		if b.res.IsError {
			t.Fatalf("breakers: %s", b.json)
		}
	})

	t.Run("no vendor identity outside hub_dry_run (grep)", func(t *testing.T) {
		all := strings.Join(outputs, "\n")
		for _, needle := range []string{"clevertap", "CleverTap", "ct-brandA-in", "ct-brandB-us", st.vendorURL, "integration_version", "X-Hub-Integration-Version", "vendor_id", "account_id"} {
			if strings.Contains(all, needle) {
				t.Errorf("tenant tool output contains %q", needle)
			}
		}
		if len(outputs) < 10 {
			t.Errorf("only %d outputs collected", len(outputs))
		}
	})
	if strings.Contains(logs.String(), devKey) || strings.Contains(logs.String(), st.adminToken) {
		t.Error("credential in hubmcp logs")
	}
}

func (s *hubStack) runID() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

// startHubStack builds hub, hubctl and mockvendor, prepares a config copy pointed at mockvendor
// (int_evt_brandB_min flagged moves_money_or_quota), dev credentials and a fresh snapshot
// archive, and runs mockvendor, admin, gateway and worker until the test ends.
func startHubStack(t *testing.T) *hubStack {
	t.Helper()
	dsn, redis := os.Getenv("HUB_PG_DSN"), os.Getenv("HUB_REDIS_ADDR")
	if dsn == "" || redis == "" {
		t.Fatal("HUB_PG_DSN and HUB_REDIS_ADDR must be set")
	}
	contracts, err := contract.Dir()
	if err != nil {
		t.Fatal(err)
	}
	backend := filepath.Join(filepath.Dir(contracts), "backend")
	work := t.TempDir()
	bin := filepath.Join(work, "bin")
	for _, c := range []string{"hub", "hubctl", "mockvendor"} {
		cmd := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(bin, c), "./cmd/"+c)
		cmd.Dir = backend
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", c, err, out)
		}
	}
	baseEnv := append(os.Environ(), "HUB_PG_DSN="+dsn, "HUB_REDIS_ADDR="+redis)
	runTool := func(name string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), filepath.Join(bin, name), args...)
		cmd.Env = baseEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	runTool("hub", "migrate")
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	// The admin archives this run's config only into an archive without published changes
	// (ADR-0010); other integration tests share the database.
	if _, err := pool.Exec(t.Context(), "TRUNCATE hub.config_snapshots"); err != nil {
		t.Fatal(err)
	}
	pool.Close()

	st := &hubStack{logs: map[string]*syncBuffer{}}

	// mockvendor: every upload succeeds with CleverTap's success shape.
	vendorAddr, controlAddr := freeAddr(t), freeAddr(t)
	st.vendorURL = "http://" + vendorAddr
	startProc(t, st, "mockvendor", filepath.Join(bin, "mockvendor"), baseEnv, "-addr", vendorAddr, "-control-addr", controlAddr)
	waitHTTP(t, "http://"+controlAddr+"/__control/routes", 15*time.Second)
	route := `{"match":{"method":"POST","path":"/1/upload"},"behaviour":{"status":200,"body":{"status":"success","processed":1,"unprocessed":[]}}}`
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, "http://"+controlAddr+"/__control/routes/upload", strings.NewReader(route))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("mockvendor route: HTTP %d", resp.StatusCode)
	}

	// Registry config: a copy of backend/config pointed at mockvendor.
	cfgDir := filepath.Join(work, "config")
	copyTree(t, filepath.Join(backend, "config"), cfgDir)
	vendorFile := filepath.Join(cfgDir, "vendors", "clevertap.yaml")
	rewrite(t, vendorFile, regexp.MustCompile(`http://127\.0\.0\.1:8099`), st.vendorURL)
	minFile := filepath.Join(cfgDir, "integrations", "int_evt_brandB_min", "integration.yaml")
	appendLine(t, minFile, "moves_money_or_quota: true\n")

	// Dev credentials (encrypted local file), never in the config dir.
	keyFile, credFile := filepath.Join(work, "dev.key"), filepath.Join(work, "dev-secrets.enc")
	runTool("hubctl", "creds", "init-key", "-key-file", keyFile)
	for _, acct := range []string{"ct-brandA-in", "ct-brandB-us"} {
		runTool("hubctl", "creds", "set", "-file", credFile, "-key-file", keyFile, acct, "account_id=e2e-acct-"+acct, "passcode=e2e-passcode-"+acct)
	}
	runTool("hubctl", "creds", "set", "-file", credFile, "-key-file", keyFile, "reg_orders_cb", "signing_secret=e2e-signing-secret-0123456789")

	common := []string{"-config-dir", cfgDir, "-contracts-dir", contracts, "-blob-url", "file://" + filepath.Join(work, "blobs"),
		"-cred-source", "localfile://" + credFile, "-dev-key-file", keyFile, "-ready-drain", "50ms", "-shutdown-grace", "5s", "-log-level", "info"}
	role := func(name string, extra ...string) (httpAddr, opsAddr string) {
		httpAddr, opsAddr = freeAddr(t), freeAddr(t)
		args := append([]string{name, "-http-addr", httpAddr, "-ops-addr", opsAddr, "-data-dir", filepath.Join(work, "data-"+name)}, common...)
		startProc(t, st, name, filepath.Join(bin, "hub"), baseEnv, append(args, extra...)...)
		return httpAddr, opsAddr
	}
	adminHTTP, adminOps := freeAddr(t), freeAddr(t)
	st.adminURL = "http://" + adminHTTP
	adminArgs := append([]string{"admin", "-http-addr", adminHTTP, "-ops-addr", adminOps, "-data-dir", filepath.Join(work, "data-admin"),
		"-admin-dev-oidc", "-admin-public-url", st.adminURL}, common...)
	startProc(t, st, "admin", filepath.Join(bin, "hub"), baseEnv, adminArgs...)
	waitHTTP(t, "http://"+adminOps+"/readyz", 30*time.Second)
	gwHTTP, gwOps := role("gateway", "-debug-headers")
	_, wkOps := role("worker")
	waitHTTP(t, "http://"+gwOps+"/readyz", 30*time.Second)
	waitHTTP(t, "http://"+wkOps+"/readyz", 30*time.Second)
	st.gatewayURL = "http://" + gwHTTP

	// The users' own tokens, as a developer gets them locally: hubctl login -dev (dev OIDC stub).
	login := func(role, sub string) string {
		t.Helper()
		file := filepath.Join(work, "token-"+role)
		cmd := exec.CommandContext(t.Context(), filepath.Join(bin, "hubctl"), "login", "-dev", "-admin-url", st.adminURL, "-role", role, "-sub", sub, "-email", sub)
		cmd.Env = append(slices.Clone(baseEnv), "HUBCTL_TOKEN_FILE="+file)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hubctl login -role %s: %v\n%s", role, err, out)
		}
		b, err := os.ReadFile(file)
		if err != nil || len(bytes.TrimSpace(b)) == 0 {
			t.Fatalf("token file: %v", err)
		}
		return string(bytes.TrimSpace(b))
	}
	st.adminToken = login("admin", e2eActor)
	st.viewerToken = login("viewer", "oncall@example.com")
	return st
}

// startProc runs a process until the test ends, then stops it with SIGTERM.
func startProc(t *testing.T, st *hubStack, name, path string, env []string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	logs := &syncBuffer{}
	st.logs[name] = logs
	cmd.Stdout, cmd.Stderr = logs, logs
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().String()
}

// waitHTTP polls url until it answers 200.
func waitHTTP(t *testing.T, url string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: HTTP %d %s", url, resp.StatusCode, b)
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("%s: %v", url, err)
		}
		waitFor(t, 100*time.Millisecond)
	}
}

func waitFor(t *testing.T, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rewrite(t *testing.T, path string, re *regexp.Regexp, repl string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := re.ReplaceAll(b, []byte(repl))
	if bytes.Equal(out, b) {
		t.Fatalf("%s: nothing to rewrite", path)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte(line)...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
