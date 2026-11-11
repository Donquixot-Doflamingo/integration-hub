package roles

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadPrecedence(t *testing.T) {
	base := map[string]string{"HUB_PG_DSN": "postgres://x"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	tests := []struct {
		name  string
		role  Role
		env   map[string]string
		args  []string
		check func(t *testing.T, c Config)
	}{
		{"defaults gateway", Gateway, base, nil, func(t *testing.T, c Config) {
			eq(t, c.HTTPAddr, ":8080")
			eq(t, c.OpsAddr, "127.0.0.1:9090")
			eq(t, c.RedisAddr, "localhost:6379")
			eq(t, c.ShutdownGrace, 25*time.Second)
			eq(t, c.ReadyDrain, 5*time.Second)
			eq(t, c.LogLevel, slog.LevelInfo)
			eq(t, c.TraceSampleRatio, 0.05)
		}},
		{"admin default port", Admin, base, nil, func(t *testing.T, c Config) { eq(t, c.HTTPAddr, ":8081") }},
		{"env over default", Gateway, with("HUB_HTTP_ADDR", ":7000", "HUB_LOG_LEVEL", "debug"), nil, func(t *testing.T, c Config) {
			eq(t, c.HTTPAddr, ":7000")
			eq(t, c.LogLevel, slog.LevelDebug)
		}},
		{"flag over env", Gateway, with("HUB_HTTP_ADDR", ":7000", "HUB_SHUTDOWN_GRACE", "40s"), []string{"-http-addr", ":7001", "-shutdown-grace=10s"}, func(t *testing.T, c Config) {
			eq(t, c.HTTPAddr, ":7001")
			eq(t, c.ShutdownGrace, 10*time.Second)
		}},
		{"flag over default", Worker, base, []string{"-pool", "llm", "-ready-drain", "0s"}, func(t *testing.T, c Config) {
			eq(t, c.WorkerPool, "llm")
			eq(t, c.ReadyDrain, time.Duration(0))
		}},
		{"dsn only from env", Gateway, base, nil, func(t *testing.T, c Config) { eq(t, string(c.PGDSN), "postgres://x") }},
		{"registry defaults", Gateway, base, nil, func(t *testing.T, c Config) {
			eq(t, c.ConfigDir, "config")
			eq(t, c.ContractsDir, "../contracts")
			eq(t, c.ConfigWatch, false)
		}},
		{"config watch from env and flag", Admin, with("HUB_CONFIG_WATCH", "true", "HUB_CONFIG_DIR", "/etc/hub"), []string{"-contracts-dir", "/srv/contracts"}, func(t *testing.T, c Config) {
			eq(t, c.ConfigWatch, true)
			eq(t, c.ConfigDir, "/etc/hub")
			eq(t, c.ContractsDir, "/srv/contracts")
		}},
		{"traceparent goes to no vendor by default", Gateway, base, nil, func(t *testing.T, c Config) {
			eq(t, len(c.TraceForwardVendors), 0)
		}},
		{"worker limits default to the delivery defaults", Worker, base, nil, func(t *testing.T, c Config) {
			eq(t, c.WorkerConcurrency, 0)
			eq(t, c.WorkerClaimBatch, 0)
			eq(t, c.WorkerTargetCap, 0)
		}},
		{"worker limits from env and flag", Worker, with("HUB_WORKER_CONCURRENCY", "128", "HUB_WORKER_CLAIM_BATCH", "64", "HUB_WORKER_TARGET_CAP", "16"),
			[]string{"-worker-target-cap", "8"}, func(t *testing.T, c Config) {
				eq(t, c.WorkerConcurrency, 128)
				eq(t, c.WorkerClaimBatch, 64)
				eq(t, c.WorkerTargetCap, 8)
			}},
		{"trace forward vendors from env", Worker, with("HUB_TRACE_FORWARD_VENDORS", " mock, acme ,,"), nil, func(t *testing.T, c Config) {
			eq(t, strings.Join(c.TraceForwardVendors, "|"), "mock|acme")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(tt.role, tt.args, envMap(tt.env), io.Discard)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			eq(t, c.Role, tt.role)
			tt.check(t, c)
		})
	}
}

func TestLoadRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		role    Role
		env     map[string]string
		args    []string
		wantErr []string
	}{
		{"missing dsn", Gateway, nil, nil, []string{"HUB_PG_DSN is required"}},
		{"bad durations and level reported together", Gateway, map[string]string{"HUB_PG_DSN": "x"},
			[]string{"-shutdown-grace=abc", "-log-level=loud"}, []string{"shutdown-grace", "log-level"}},
		{"drain not shorter than grace", Gateway, map[string]string{"HUB_PG_DSN": "x"},
			[]string{"-shutdown-grace=5s", "-ready-drain=5s"}, []string{"must be shorter"}},
		{"sample ratio out of range", Gateway, map[string]string{"HUB_PG_DSN": "x", "HUB_TRACE_SAMPLE_RATIO": "1.5"}, nil, []string{"trace-sample-ratio"}},
		{"unknown worker pool", Worker, map[string]string{"HUB_PG_DSN": "x"}, []string{"-pool=gpu"}, []string{"pool"}},
		{"worker limits not integers", Worker, map[string]string{"HUB_PG_DSN": "x", "HUB_WORKER_CONCURRENCY": "many", "HUB_WORKER_CLAIM_BATCH": "-1"}, nil,
			[]string{"worker-concurrency", "worker-claim-batch"}},
		{"target cap above concurrency", Worker, map[string]string{"HUB_PG_DSN": "x"}, []string{"-worker-concurrency=8", "-worker-target-cap=9"},
			[]string{"must not exceed"}},
		{"unknown admin auth mode", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_ADMIN_AUTH_MODE": "saml"}, nil, []string{"admin-auth-mode"}},
		{"isfc mode needs issuer and console client", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_ADMIN_AUTH_MODE": "isfc"}, nil,
			[]string{"HUB_OIDC_ISSUER", "HUB_ISFC_CONSOLE_CLIENT_ID", "HUB_OPA_URL"}},
		{"OPA cannot be disabled outside tests", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_OPA_DISABLED": "true"}, nil, []string{"HUB_OPA_DISABLED=true is refused"}},
		{"OPA cannot be disabled in prod", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_OPA_DISABLED": "true", "HUB_ENV": "prod"}, nil, []string{"HUB_OPA_DISABLED=true is refused"}},
		{"bad OPA client id", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_OPA_CLIENT_ID": "a/b"}, nil, []string{"opa-client-id"}},
		{"bad OPA timeout", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_OPA_TIMEOUT": "fast"}, nil, []string{"opa-timeout"}},
		{"isfc mode refuses the dev issuer", Admin, map[string]string{"HUB_PG_DSN": "x", "HUB_ADMIN_AUTH_MODE": "isfc", "HUB_ADMIN_DEV_OIDC": "true",
			"HUB_ISFC_CONSOLE_CLIENT_ID": "c"}, nil, []string{"dev OIDC stub"}},
		{"unknown flag", Gateway, map[string]string{"HUB_PG_DSN": "x"}, []string{"-nope"}, []string{"parse flags"}},
		{"stray argument", Gateway, map[string]string{"HUB_PG_DSN": "x"}, []string{"extra"}, []string{"unexpected arguments"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(tt.role, tt.args, envMap(tt.env), io.Discard)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

// TestAdminAuthModeDefaultsToJWT: HUB_ADMIN_AUTH_MODE is jwt unless set, and isfc mode loads when
// its settings are present (HUB-473).
func TestAdminAuthModeDefaultsToJWT(t *testing.T) {
	c, err := Load(Admin, nil, envMap(map[string]string{"HUB_PG_DSN": "x"}), io.Discard)
	if err != nil || c.AdminAuthMode != "jwt" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = Load(Admin, nil, envMap(map[string]string{"HUB_PG_DSN": "x", "HUB_ADMIN_AUTH_MODE": "isfc", "HUB_OIDC_ISSUER": "https://hydra.test",
		"HUB_ISFC_CONSOLE_CLIENT_ID": "console", "HUB_ISFC_PLATFORM_TENANT_ID": "uuid-1", "HUB_OPA_URL": "http://localhost:8181", "HUB_OPA_TIMEOUT": "50ms", "HUB_OPA_CLIENT_ID": "client_x-y"}), io.Discard)
	if err != nil || c.AdminAuthMode != "isfc" || c.ISFCConsoleClientID != "console" || c.ISFCPlatformTenantID != "uuid-1" || c.OPAURL != "http://localhost:8181" || c.OPATimeout != 50*time.Millisecond || c.OPAClientID != "client_x-y" {
		t.Fatalf("%+v %v", c, err)
	}
	// HUB_OPA_DISABLED is accepted only with HUB_ENV=test, and then OPA needs no URL.
	c, err = Load(Admin, nil, envMap(map[string]string{"HUB_PG_DSN": "x", "HUB_ADMIN_AUTH_MODE": "isfc", "HUB_OIDC_ISSUER": "https://hydra.test",
		"HUB_ISFC_CONSOLE_CLIENT_ID": "console", "HUB_OPA_DISABLED": "true", "HUB_ENV": "test"}), io.Discard)
	if err != nil || !c.OPADisabled || c.OPATimeout != 200*time.Millisecond || c.OPAClientID != "integration-hub" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestSecretNeverPrints(t *testing.T) {
	s := Secret("postgres://user:pw@db")
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).InfoContext(t.Context(), "cfg", slog.Any("dsn", s))
	if strings.Contains(b.String(), "pw@db") || strings.Contains(s.String(), "pw") {
		t.Fatalf("secret leaked: %q", b.String())
	}
}

func TestParseRole(t *testing.T) {
	for _, r := range Roles {
		if got, err := ParseRole(string(r)); err != nil || got != r {
			t.Errorf("ParseRole(%q) = %q, %v", r, got, err)
		}
	}
	if _, err := ParseRole("proxy"); err == nil {
		t.Error("ParseRole(proxy) succeeded")
	}
}

func eq[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
