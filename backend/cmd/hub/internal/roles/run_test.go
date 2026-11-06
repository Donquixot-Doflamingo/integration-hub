package roles

import (
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

func testConfig(tb testing.TB, role Role) Config {
	return Config{
		Role: role, HTTPAddr: "127.0.0.1:0", OpsAddr: "127.0.0.1:0", PGDSN: "postgres://unused",
		RedisAddr: "127.0.0.1:1", ShutdownGrace: 5 * time.Second, ReadyDrain: 50 * time.Millisecond,
		ConfigDir: "../../../../config", ContractsDir: "../../../../../contracts",
		DataDir: tb.TempDir(), BlobURL: "file://" + tb.TempDir(),
	}
}

func TestEveryRoleStartsAndStopsCleanly(t *testing.T) {
	for _, role := range Roles {
		t.Run(string(role), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			addrs := map[string]string{}
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- Run(ctx, testConfig(t, role), slog.New(slog.DiscardHandler), Options{
					OnListen: func(name, addr string) {
						mu.Lock()
						defer mu.Unlock()
						addrs[name] = addr
						if name == "ops" {
							close(started)
						}
					},
				})
			}()
			select {
			case <-started:
			case err := <-done:
				t.Fatalf("Run returned before listening: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("role did not start")
			}

			mu.Lock()
			_, hasHTTP := addrs["http"]
			opsAddr := addrs["ops"]
			mu.Unlock()
			if hasHTTP != role.servesPublicHTTP() {
				t.Errorf("public listener present = %v, want %v", hasHTTP, role.servesPublicHTTP())
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+opsAddr+"/", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("ops listener not serving: %v", err)
			}
			_ = resp.Body.Close()

			stopAt := time.Now()
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if d := time.Since(stopAt); d > 10*time.Second {
					t.Fatalf("shutdown took %s, want < 10s", d)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("role did not stop within 10s")
			}
		})
	}
}

func TestRunFailsWhenPortTaken(t *testing.T) {
	cfg := testConfig(t, Gateway)
	first := make(chan string, 2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg, slog.New(slog.DiscardHandler), Options{OnListen: func(name, addr string) {
			if name == "http" {
				first <- addr
			}
		}})
	}()
	taken := <-first
	cfg.HTTPAddr = taken
	if err := Run(t.Context(), cfg, slog.New(slog.DiscardHandler), Options{}); err == nil {
		t.Fatal("second Run on a taken port succeeded")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("first Run: %v", err)
	}
}

// startRole runs role with the unit-test config until the test ends.
func startRole(t *testing.T, role Role, opts Options) map[string]string {
	t.Helper()
	return startRoleWith(t, testConfig(t, role), opts)
}

// startRoleWith runs a role with cfg until the test ends and returns its bound addresses.
func startRoleWith(t *testing.T, cfg Config, opts Options) map[string]string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	addrs := map[string]string{}
	var mu sync.Mutex
	started := make(chan struct{})
	opts.OnListen = func(name, addr string) {
		mu.Lock()
		defer mu.Unlock()
		addrs[name] = addr
		if name == "ops" {
			close(started)
		}
	}
	if opts.Secrets == nil { // the logger and the role share one redaction set, as in main
		opts.Secrets = telemetry.NewSecretSet()
	}
	log := newRoleLogger(cfg.Role, opts.Secrets) // captured for the secret grep (logscan_test.go)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, log, opts) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	return maps.Clone(addrs)
}

func get(t *testing.T, url string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestOpsServesMetrics(t *testing.T) {
	addrs := startRole(t, Worker, Options{Version: "v-test"})
	code, body := get(t, "http://"+addrs["ops"]+"/metrics", nil)
	if code != http.StatusOK {
		t.Fatalf("/metrics status %d", code)
	}
	for _, want := range []string{"go_goroutines", `hub_build_info{role="worker",version="v-test"} 1`} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %s", want)
		}
	}
}

func TestGatewayContinuesCallerTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	addrs := startRole(t, Gateway, Options{SpanProcessor: rec})
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	get(t, "http://"+addrs["http"]+"/v1/anything", map[string]string{"traceparent": "00-" + traceID + "-00f067aa0ba902b7-01"})
	spans := rec.Ended()
	if len(spans) == 0 {
		t.Fatal("no server span recorded")
	}
	if got := spans[0].SpanContext().TraceID().String(); got != traceID {
		t.Fatalf("trace id = %s, want %s", got, traceID)
	}
}

// pprof is served on the ops port only, never on the public listeners (OBSERVABILITY §8.1).
func TestPprofOnlyOnOpsPort(t *testing.T) {
	for _, role := range []Role{Gateway, Admin, Worker} {
		t.Run(string(role), func(t *testing.T) {
			addrs := startRole(t, role, Options{})
			for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/goroutine?debug=1"} {
				code, body := get(t, "http://"+addrs["ops"]+path, nil)
				if code != http.StatusOK || body == "" {
					t.Errorf("ops %s: status %d, body %d bytes; want 200 with a body", path, code, len(body))
				}
			}
			if pub, ok := addrs["http"]; ok {
				if code, _ := get(t, "http://"+pub+"/debug/pprof/", nil); code != http.StatusNotFound {
					t.Errorf("public /debug/pprof/: status %d, want 404", code)
				}
			}
		})
	}
}

// TestRunRegistersConfigSecrets: Run adds the DSN password and the audit mask key to the
// process's redaction set before anything logs, whichever entry point built the logger.
func TestRunRegistersConfigSecrets(t *testing.T) {
	const pw, mask = "pg-password-canary-0001", "audit-mask-key-canary-0123456789abcdef"
	forbid("postgres password", pw)
	forbid("audit mask key", mask)
	cfg := testConfig(t, Worker)
	cfg.PGDSN = Secret("postgres://hub:" + pw + "@127.0.0.1:1/hub")
	cfg.AuditMaskKey = Secret(mask)
	secrets := telemetry.NewSecretSet()
	startRoleWith(t, cfg, Options{Secrets: secrets})
	for _, v := range []string{pw, mask} {
		if got := secrets.Redact("x " + v + " y"); got != "x "+telemetry.Redacted+" y" {
			t.Errorf("not registered for redaction: %q", got)
		}
	}
}
