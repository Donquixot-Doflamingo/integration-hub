package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestEmbeddedContractIsTheFrozenOne(t *testing.T) {
	if !bytes.Equal(toolContractJSON, frozenContract(t)) {
		t.Fatal("cmd/hubmcp/hubmcp-tools.json differs from contracts/mcp/hubmcp-tools.json; run `go generate ./cmd/hubmcp`")
	}
}

// wantTool is the advertised shape of one tool as the frozen contract defines it.
type wantTool struct {
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Audience     string          `json:"audience"`
	Description  string          `json:"description"`
	Annotations  json.RawMessage `json:"annotations"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

func frozenTools(t *testing.T) []wantTool {
	t.Helper()
	var c struct {
		Tools []wantTool `json:"tools"`
	}
	if err := json.Unmarshal(frozenContract(t), &c); err != nil {
		t.Fatal(err)
	}
	return c.Tools
}

// TestToolListEqualsFrozenContract proves the advertised tools are exactly the frozen
// definitions: names, titles, descriptions, annotations and both schemas (HUB-300, HUB-306).
func TestToolListEqualsFrozenContract(t *testing.T) {
	for _, withAdmin := range []bool{false, true} {
		t.Run(fmt.Sprintf("admin=%v", withAdmin), func(t *testing.T) {
			cs := connect(t, testApp(t, "http://127.0.0.1:1", withAdmin, nil))
			res, err := cs.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]*mcp.Tool{}
			for _, tool := range res.Tools {
				got[tool.Name] = tool
			}
			var want []string
			for _, w := range frozenTools(t) {
				if w.Audience == audienceAdmin && !withAdmin {
					if _, ok := got[w.Name]; ok {
						t.Errorf("%s is registered without HUB_ADMIN_TOKEN", w.Name)
					}
					continue
				}
				want = append(want, w.Name)
				g, ok := got[w.Name]
				if !ok {
					t.Errorf("contract tool %s is not advertised", w.Name)
					continue
				}
				if g.Title != w.Title || g.Description != w.Description {
					t.Errorf("%s: title/description differ from the contract", w.Name)
				}
				if !jsonEqual(t, mustJSON(t, g.InputSchema), w.InputSchema) {
					t.Errorf("%s: inputSchema differs from the contract:\n got %s\nwant %s", w.Name, mustJSON(t, g.InputSchema), w.InputSchema)
				}
				if !jsonEqual(t, mustJSON(t, g.OutputSchema), w.OutputSchema) {
					t.Errorf("%s: outputSchema differs from the contract", w.Name)
				}
				if !jsonEqual(t, mustJSON(t, g.Annotations), w.Annotations) {
					t.Errorf("%s: annotations %s, contract %s", w.Name, mustJSON(t, g.Annotations), w.Annotations)
				}
			}
			if len(res.Tools) != len(want) {
				t.Errorf("advertised %d tools, want %d (%v)", len(res.Tools), len(want), want)
			}
		})
	}
}

// TestStdioServesContractTools runs the real entry point over stdio pipes, lists the tools and
// checks the tenant key never reaches the logs (HUB-300).
func TestStdioServesContractTools(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var logs syncBuffer
	env := map[string]string{"HUB_BASE_URL": "http://127.0.0.1:1", "HUB_API_KEY": testKey}
	done := make(chan int, 1)
	go func() {
		done <- run(t.Context(), []string{"-transport", "stdio", "-log-level", "debug"}, func(k string) string { return env[k] }, inR, outW, &logs)
		_ = outW.Close()
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "stdio-test", Version: "1"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{toolDescribe, toolDryRun, toolGetRequest, toolInvoke, toolListIntegrations}
	if !slices.Equal(names, want) {
		t.Errorf("tools %v, want %v", names, want)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code %d, logs: %s", code, logs.String())
		}
	case <-ctx.Done():
		t.Fatal("hubmcp did not stop after stdin closed")
	}
	if strings.Contains(logs.String(), testKey) {
		t.Fatalf("tenant key leaked into logs: %s", logs.String())
	}
	for _, line := range []string{`"msg":"hubmcp started"`, `"msg":"admin tools disabled: HUB_ADMIN_TOKEN is not set"`} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("logs lack %s: %s", line, logs.String())
		}
	}
}

func TestLoadConfig(t *testing.T) {
	base := map[string]string{"HUB_BASE_URL": "http://hub:8080/", "HUB_API_KEY": testKey}
	tests := []struct {
		name      string
		env       map[string]string
		args      []string
		wantErr   string
		wantAdmin string
	}{
		{name: "ok, admin URL defaults to base + /admin/v1", env: base, wantAdmin: "http://hub:8080/admin/v1"},
		{name: "admin URL override", env: map[string]string{"HUB_BASE_URL": "http://hub:8080", "HUB_API_KEY": testKey, "HUB_ADMIN_BASE_URL": "http://adm:8081/admin/v1"}, wantAdmin: "http://adm:8081/admin/v1"},
		{name: "no base URL", env: map[string]string{"HUB_API_KEY": testKey}, wantErr: "HUB_BASE_URL is required"},
		{name: "no key", env: map[string]string{"HUB_BASE_URL": "http://hub"}, wantErr: "HUB_API_KEY is required"},
		{name: "bad transport", env: base, args: []string{"-transport", "sse"}, wantErr: "transport"},
		{name: "no key flag exists", env: base, args: []string{"-api-key", "x"}, wantErr: "flag provided but not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := loadConfig(tt.args, func(k string) string { return tt.env[k] }, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.adminBaseURL != tt.wantAdmin {
				t.Errorf("admin base URL %q, want %q", c.adminBaseURL, tt.wantAdmin)
			}
			if s := fmt.Sprintf("%v %+v %#v", c, c, c); strings.Contains(s, testKey) {
				t.Errorf("formatted config shows the key: %s", s)
			}
		})
	}
}

func TestSecretsAreRedactedInLogsAndOutput(t *testing.T) {
	var logs syncBuffer
	secrets := newSecretSet(testKey, testAdminToken, "short")
	log := newLogger(&logs, 0, secrets)
	log.InfoContext(t.Context(), "calling with "+testKey, slog.Any("key", secret(testKey)), slog.String("raw", testAdminToken))
	if s := logs.String(); strings.Contains(s, testKey) || strings.Contains(s, testAdminToken) || !strings.Contains(s, "***") {
		t.Fatalf("logs not redacted: %s", s)
	}
	a := &app{secrets: secrets}
	out, err := a.render(toolResult{structured: map[string]string{"echo": "Bearer " + testKey}, note: "token " + testAdminToken})
	if err != nil {
		t.Fatal(err)
	}
	b := mustJSON(t, out)
	if bytes.Contains(b, []byte(testKey)) || bytes.Contains(b, []byte(testAdminToken)) {
		t.Fatalf("tool output not redacted: %s", b)
	}
}
