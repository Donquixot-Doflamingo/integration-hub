//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	// HUB-315: a team onboards end to end through the write tools, signed in as an admin user.
	var minted string
	t.Run("admin write tools onboard a team as the signed-in user", func(t *testing.T) {
		minted = onboardThroughMCP(t, st, cs, a, &outputs)
	})

	t.Run("a viewer token gets FORBIDDEN on a write tool", func(t *testing.T) {
		venv := map[string]string{"HUB_BASE_URL": st.gatewayURL, "HUB_API_KEY": devKey,
			"HUB_ADMIN_TOKEN": st.viewerToken, "HUB_ADMIN_BASE_URL": st.adminURL + "/admin/v1"}
		vcfg, err := loadConfig(nil, func(k string) string { return venv[k] }, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		vsec := newSecretSet(vcfg.apiKey, vcfg.adminToken)
		va, err := newApp(vcfg, tc, newLogger(&logs, 0, vsec), vsec, &http.Client{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		vcs := connect(t, va)
		out := callTool(t, vcs, "hub_admin_create_tenant", map[string]any{"tenant_id": "viewer-made", "display_name": "Viewer made", "reason": "should be refused"})
		if !out.res.IsError || out.field(t, "error", "code") != "FORBIDDEN" {
			t.Fatalf("viewer create_tenant: %s", out.json)
		}
		if g := callTool(t, vcs, "hub_admin_get_tenant", map[string]any{"tenant_id": "viewer-made"}); !g.res.IsError || g.field(t, "error", "code") != "NOT_FOUND" {
			t.Fatalf("the refused tenant exists: %s", g.json)
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
	if strings.Contains(logs.String(), devKey) || strings.Contains(logs.String(), st.adminToken) || strings.Contains(logs.String(), st.viewerToken) {
		t.Error("credential in hubmcp logs")
	}
	if minted == "" || strings.Contains(logs.String(), minted) {
		t.Errorf("minted API key missing (%t) or in hubmcp logs", minted == "")
	}
}

// e2eIDs are the objects the onboarding run creates. The run starts from a fresh config archive
// (startHubStack), so fixed ids do not collide across runs.
const (
	e2eTenant      = "mcp-team"
	e2eVendor      = "mcpvendor"
	e2eIntegration = "int_mcp_events"
)

// onboardThroughMCP is the HUB-315 e2e: create tenant → API key (secret returned once) → vendor +
// endpoint → transformer version → integration → draft → validate → publish (refused without
// confirm, then confirmed) → hub_invoke with the minted key → set weights → rollback. Each change
// is in the change log under the admin user with the " (via hubmcp)" reason, and nothing is
// deleted. It returns the minted key.
func onboardThroughMCP(t *testing.T, st *hubStack, cs *mcp.ClientSession, a *app, outputs *[]string) string {
	t.Helper()
	start := time.Now().UTC().Add(-time.Second)
	var changes []string
	write := func(tool string, args map[string]any) toolOutcome {
		t.Helper()
		out := callTool(t, cs, tool, args)
		if out.res.IsError {
			t.Fatalf("%s: %s", tool, out.json)
		}
		id, _ := out.field(t, "change_id").(string)
		if id == "" {
			t.Fatalf("%s: no change_id: %s", tool, out.json)
		}
		changes = append(changes, id)
		return out
	}

	write("hub_admin_create_tenant", map[string]any{"tenant_id": e2eTenant, "display_name": "MCP team", "contact": "mcp-team@example.com",
		"rate_limit": map[string]any{"rps": 50, "burst": 100}, "reason": "Onboard the MCP team, OPS-315"})

	keyArgs := map[string]any{"tenant_id": e2eTenant, "scopes": []any{"invoke"}, "description": "mcp e2e", "reason": "Key for the MCP team"}
	if out := callTool(t, cs, "hub_admin_create_api_key", keyArgs); !out.res.IsError || out.field(t, "error", "code") != codeConfirmationRequired {
		t.Fatalf("create_api_key without confirm: %s", out.json)
	}
	keyArgs["confirm"] = true
	key := write("hub_admin_create_api_key", keyArgs)
	secret, _ := key.field(t, "secret").(string)
	if !strings.HasPrefix(secret, "hk_") || !strings.Contains(key.res.Content[0].(*mcp.TextContent).Text, secret) {
		t.Fatalf("minted key not returned: %s", key.json)
	}
	keyID := key.field(t, "api_key", "key_id").(string)
	// Returned once: a later read shows the key without its secret, which is now redacted by value.
	tenant := callTool(t, cs, "hub_admin_get_tenant", map[string]any{"tenant_id": e2eTenant})
	if tenant.res.IsError || strings.Contains(string(tenant.json), secret) || tenant.field(t, "etag") == nil {
		t.Fatalf("get_tenant after minting: %s", tenant.json)
	}
	if out := callTool(t, cs, "hub_admin_get_tenant", map[string]any{"tenant_id": secret}); !out.res.IsError || out.field(t, "error", "code") != codeSecretInInput {
		t.Fatalf("minted key passed back as input: %s", out.json)
	}

	write("hub_admin_create_vendor", map[string]any{"vendor_id": e2eVendor, "display_name": "MCP vendor", "auth_scheme": "static_headers",
		"regions": map[string]any{"in": st.vendorURL}, "docs_url": "https://developer.clevertap.com/docs/upload-events-api", "reason": "New analytics vendor"})
	ep := write("hub_admin_create_vendor_endpoint", map[string]any{"vendor_id": e2eVendor, "name": "upload", "method": "POST", "path": "/1/upload",
		"idempotent": false, "reason": "Upload endpoint"})
	if ep.field(t, "endpoint", "endpoint_ref") != "upload@1" {
		t.Fatalf("endpoint: %s", ep.json)
	}

	tv := write("hub_admin_create_transformer_version", map[string]any{"transformer_id": "T1", "kind": "request", "capability": "events.track@v1",
		"vendor_endpoint": "clevertap.upload@1", "expected_latest_version": 7, "yaml": configFile(t, "transformers/T1/7.yaml"),
		"fixtures": []any{map[string]any{"name": "minimal", "input": configJSON(t, "transformers/T1/fixtures/minimal.input.json"),
			"expected_request": configJSON(t, "transformers/T1/fixtures/minimal.expected_request.json")}},
		"docs_url": "https://developer.clevertap.com/docs/upload-events-api", "reason": "T1 for the MCP team"})
	if tv.field(t, "transformer_version", "ref") != "T1@8" || tv.field(t, "transformer_version", "yaml") != nil {
		t.Fatalf("transformer version: %s", tv.json)
	}

	write("hub_admin_create_integration", map[string]any{"integration_id": e2eIntegration, "owner_tenant": e2eTenant, "capability": "events.track@v1",
		"description": "MCP team engagement events", "reason": "MCP team events"})
	spec := map[string]any{
		"targets": []any{map[string]any{"vendor_id": "clevertap", "account_id": "ct-brandA-in",
			"transformer": map[string]any{"request": "T1@8", "response": "R1@2"}, "weight": 100}},
		"failover": []any{},
		"delivery": map[string]any{"mode": "wait", "wait_ms": 3000, "ttl_ms": 21600000, "on_circuit_open": "fail_fast", "pending": "re_resolve", "polling": true},
		"webhooks": []any{},
		"audit":    map[string]any{"payloads": "masked", "retention_ms": 2592000000},
	}
	draft := write("hub_admin_create_integration_draft", map[string]any{"integration_id": e2eIntegration, "spec": spec, "reason": "First version"})
	if draft.field(t, "integration_version", "version") != 1.0 || draft.field(t, "integration_version", "state") != "draft" {
		t.Fatalf("draft: %s", draft.json)
	}
	if v := callTool(t, cs, "hub_admin_validate_integration_version", map[string]any{"integration_id": e2eIntegration, "version": 1}); v.res.IsError || v.field(t, "valid") != true {
		t.Fatalf("validate: %s", v.json)
	}

	publish := map[string]any{"integration_id": e2eIntegration, "version": 1, "expected_active_version": nil, "reason": "Go live"}
	if out := callTool(t, cs, "hub_admin_publish_integration_version", publish); !out.res.IsError || out.field(t, "error", "code") != codeConfirmationRequired {
		t.Fatalf("publish without confirm: %s", out.json)
	}
	if g := callTool(t, cs, "hub_admin_get_integration", map[string]any{"integration_id": e2eIntegration}); g.field(t, "active_version") != nil || g.field(t, "has_draft") != true {
		t.Fatalf("a refused publish changed the integration: %s", g.json)
	}
	publish["confirm"] = true
	if p := write("hub_admin_publish_integration_version", publish); p.field(t, "active_version") != 1.0 {
		t.Fatalf("publish: %s", p.json)
	}

	// The team's own service calls it with the minted key (a second hubmcp, as the team runs it).
	tenv := map[string]string{"HUB_BASE_URL": st.gatewayURL, "HUB_API_KEY": secret}
	tcfg, err := loadConfig(nil, func(k string) string { return tenv[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := loadContract(toolContractJSON)
	if err != nil {
		t.Fatal(err)
	}
	tsec := newSecretSet(tcfg.apiKey)
	ta, err := newApp(tcfg, tc, a.log, tsec, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tcs := connect(t, ta)
	body := map[string]any{"user": map[string]any{"id": "u_315"}, "event": map[string]any{"name": "app_opened", "timestamp": "2026-11-07T09:15:00Z"}}
	deadline := time.Now().Add(30 * time.Second)
	for { // the gateway applies the new snapshot (tenant, key, integration) asynchronously
		out := callTool(t, tcs, toolInvoke, map[string]any{"integration_id": e2eIntegration, "body": body, "idempotency_key": "e2e-mcp-" + st.runID()})
		*outputs = append(*outputs, string(out.json))
		if out.field(t, "outcome") == "completed" && out.field(t, "result", "status") == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub_invoke with the minted key: %s", out.json)
		}
		waitFor(t, 300*time.Millisecond)
	}

	weights := map[string]any{"integration_id": e2eIntegration, "expected_active_version": 1,
		"weights": []any{map[string]any{"vendor_id": "clevertap", "account_id": "ct-brandA-in", "weight": 100}}, "reason": "Pin weights", "confirm": true}
	if w := write("hub_admin_set_integration_weights", weights); w.field(t, "active_version") != 2.0 || w.field(t, "copied_from_version") != 1.0 {
		t.Fatalf("weights: %s", w.json)
	}
	rb := write("hub_admin_rollback_integration", map[string]any{"integration_id": e2eIntegration, "to_version": 1, "expected_active_version": 2,
		"reason": "Back to v1", "confirm": true})
	if rb.field(t, "active_version") != 3.0 || rb.field(t, "copied_from_version") != 1.0 {
		t.Fatalf("rollback: %s", rb.json)
	}

	// Change log: every write under the admin user, with the MCP reason suffix.
	if len(changes) != 10 {
		t.Fatalf("%d changes, want 10", len(changes))
	}
	for _, id := range changes {
		var e struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Reason string `json:"reason"`
		}
		adminGet(t, st, "/change-log/"+id, &e)
		if e.Actor != e2eActor || !strings.HasSuffix(e.Reason, reasonSuffix) {
			t.Errorf("change %s: actor %q reason %q", id, e.Actor, e.Reason)
		}
	}
	// Nothing deleted: every change of this run is one of the writes above; the objects remain.
	var log struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	adminGet(t, st, "/change-log?limit=200&actor="+e2eActor+"&from="+start.Format(time.RFC3339), &log)
	if len(log.Items) != len(changes) {
		t.Errorf("%d change-log entries for %s, want %d", len(log.Items), e2eActor, len(changes))
	}
	for _, it := range log.Items {
		if strings.Contains(strings.ToLower(it.Action), "delete") || strings.Contains(strings.ToLower(it.Action), "revoke") {
			t.Errorf("change-log action %s", it.Action)
		}
	}
	for tool, args := range map[string]map[string]any{
		"hub_admin_get_tenant":          {"tenant_id": e2eTenant},
		"hub_admin_get_vendor":          {"vendor_id": e2eVendor},
		"hub_admin_get_vendor_endpoint": {"vendor_id": e2eVendor, "endpoint_ref": "upload@1"},
		"hub_admin_get_integration":     {"integration_id": e2eIntegration},
	} {
		if out := callTool(t, cs, tool, args); out.res.IsError {
			t.Errorf("%s after the run: %s", tool, out.json)
		}
	}
	var k struct {
		State string `json:"state"`
	}
	adminGet(t, st, "/api-keys/"+keyID, &k)
	if k.State != "active" {
		t.Errorf("minted key state %q", k.State)
	}
	return secret
}

// adminGet reads the control plane directly with the admin user's token (test verification only).
func adminGet(t *testing.T, st *hubStack, path string, out any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, st.adminURL+"/admin/v1"+path, nil)
	req.Header.Set("Authorization", "Bearer "+st.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: HTTP %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

// configFile reads a file of the bootstrap config tree (backend/config).
func configFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := contract.Dir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "backend", "config", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func configJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(configFile(t, name)), &v); err != nil {
		t.Fatal(err)
	}
	return v
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
