// Package roles loads process configuration and runs one Hub role (DESIGN §17, TECH_SPEC §8.1).
package roles

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/authz"
)

// Role is one of the Hub's runtime roles. All roles share one binary and one code base.
type Role string

const (
	Gateway   Role = "gateway"
	Worker    Role = "worker"
	Scheduler Role = "scheduler"
	Admin     Role = "admin"
)

// Roles lists every valid role in display order.
var Roles = []Role{Gateway, Worker, Scheduler, Admin}

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	for _, r := range Roles {
		if string(r) == s {
			return r, nil
		}
	}
	return "", fmt.Errorf("unknown role %q (want one of %s)", s, roleList())
}

func roleList() string {
	names := make([]string, len(Roles))
	for i, r := range Roles {
		names[i] = string(r)
	}
	return strings.Join(names, ", ")
}

// servesPublicHTTP reports whether the role listens on HUB_HTTP_ADDR besides the ops port.
func (r Role) servesPublicHTTP() bool { return r == Gateway || r == Admin }

// Secret is a config value that must never be logged.
type Secret string

// LogValue implements slog.LogValuer so a Secret always logs as "***".
func (Secret) LogValue() slog.Value { return slog.StringValue("***") }

// String hides the value from fmt as well.
func (Secret) String() string { return "***" }

// Config is the immutable process configuration of one role.
// Registry config (vendors, integrations, ...) never comes from here; it comes from snapshots.
type Config struct {
	Role       Role
	HTTPAddr   string // public listener (gateway /v1, admin /admin/v1); unused by worker and scheduler
	OpsAddr    string // /healthz, /readyz, /metrics, /debug/pprof; never exposed outside the cluster
	PGDSN      Secret
	RedisAddr  string
	AdminURL   string // where data-plane roles fetch snapshots
	WorkerPool string
	// Worker limits (TECH_SPEC §8.1, HUB-412). 0 means the delivery package default:
	// concurrency 256 (messaging) or 64 (llm), claim batch 32, target cap a quarter of the
	// concurrency up to the 64-connection bulkhead.
	WorkerConcurrency int           // attempts in flight
	WorkerClaimBatch  int           // most envelopes per claim
	WorkerTargetCap   int           // most attempts in flight per (account, endpoint)
	ShutdownGrace     time.Duration // total budget for graceful shutdown
	ReadyDrain        time.Duration // time between /readyz=503 and closing listeners
	LogLevel          slog.Level
	OTLPEndpoint      string // empty disables trace export
	TraceSampleRatio  float64
	// TraceForwardVendors names the vendors (registry vendor names) that receive the Hub's W3C
	// traceparent; every other vendor gets none (DESIGN §16.4, ADR-0013).
	TraceForwardVendors []string
	ConfigDir           string // dev registry (backend/config); production nodes fetch snapshots instead (M7)
	ContractsDir        string // unused since ADR-0023 (schemas live in the config tree); the flag stays for old scripts
	ConfigWatch         bool   // dev only: rebuild and swap the snapshot when ConfigDir changes
	DataDir             string // local cache of the last good snapshot (boot fallback)
	BlobURL             string // snapshot blob store (file:// in phase 1)
	CredSource          string // credential source: localfile://<path> in phase 1 (TECH_SPEC §8.1)
	DevKeyFile          string // key of the localfile credential source
	AuditMaskKey        Secret // master key for x-pii masking (environment only; random per process when unset)
	DebugHeaders        bool   // gateway: add the debug-only X-Hub-Integration-Version header
	// CallbackAllowLoopback lets callbacks reach loopback registrar endpoints over http (dev
	// receivers only; production leaves it false, DESIGN §12.1 SSRF guard).
	CallbackAllowLoopback bool
	// Admin auth (DESIGN §16.1): OIDCIssuer verifies real tokens; AdminDevOIDC runs the dev
	// issuer stub instead (never in production: it mints tokens for anyone). Neither: 401 always.
	OIDCIssuer     string
	OIDCAudience   string
	AdminDevOIDC   bool
	AdminPublicURL string // the admin's external base URL (the dev issuer's iss)
	// AdminAuthMode is HUB_ADMIN_AUTH_MODE: "jwt" (default; OIDCIssuer or the dev issuer, claims
	// hub_roles / hub_tenants) or "isfc" (ISFC gateway identity, AUTH_PLAN §7). In isfc mode
	// OIDCIssuer is the Hydra issuer whose RS256 tokens are verified.
	AdminAuthMode string
	// ISFCConsoleClientID is the ISFC client id of the Hub console (isfc mode: token client_id and
	// x-isfc-client-id must equal it). ISFCPlatformTenantID is the ISFC UUID of the platform
	// tenant: a request in it has platform scope (AUTH_PLAN §2.2).
	ISFCConsoleClientID  string
	ISFCPlatformTenantID string
	// OPAURL is the OPA sidecar base URL of the admin authorization (HUB_OPA_URL, isfc mode);
	// OPATimeout bounds one decision (HUB_OPA_TIMEOUT, default 200ms). OPADisabled
	// (HUB_OPA_DISABLED) skips authorization and is refused unless Env is "test".
	OPAURL      string
	OPATimeout  time.Duration
	OPADisabled bool
	// Env is HUB_ENV ("test" in tests only; anything else is a real deployment).
	Env string
	// CallbackHosts allowlists registrar endpoint hosts at the admin ("h" or ".suffix"); empty
	// accepts any public host (the SSRF guard still applies).
	CallbackHosts []string
}

// Load builds the config for role from flags (highest precedence), then HUB_* environment
// variables, then defaults. Secrets come from the environment only, so they never appear in
// process listings. All problems are reported together.
func Load(role Role, args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	defHTTP := ":8080"
	if role == Admin {
		defHTTP = ":8081"
	}
	env := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}

	var (
		c                                      = Config{Role: role, PGDSN: Secret(getenv("HUB_PG_DSN")), AuditMaskKey: Secret(getenv("HUB_AUDIT_MASK_KEY"))}
		shutdownGrace, readyDrain, level, rate string
		watch, debugHeaders, cbLoopback, devID bool
	)
	fs := flag.NewFlagSet("hub "+string(role), flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.HTTPAddr, "http-addr", env("HUB_HTTP_ADDR", defHTTP), "public listen address (gateway, admin)")
	fs.StringVar(&c.OpsAddr, "ops-addr", env("HUB_OPS_ADDR", "127.0.0.1:9090"), "ops listen address: health, metrics, pprof")
	fs.StringVar(&c.RedisAddr, "redis-addr", env("HUB_REDIS_ADDR", "localhost:6379"), "Redis address")
	fs.StringVar(&c.AdminURL, "admin-url", env("HUB_ADMIN_URL", "http://localhost:8081"), "control plane URL for snapshot fetch")
	fs.StringVar(&c.WorkerPool, "pool", env("HUB_WORKER_POOL", "messaging"), "worker pool: messaging or llm")
	fs.StringVar(&shutdownGrace, "shutdown-grace", env("HUB_SHUTDOWN_GRACE", "25s"), "graceful shutdown budget")
	fs.StringVar(&readyDrain, "ready-drain", env("HUB_READY_DRAIN", "5s"), "delay between readyz=503 and closing listeners")
	fs.StringVar(&level, "log-level", env("HUB_LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.StringVar(&c.OTLPEndpoint, "otlp-endpoint", env("HUB_OTLP_ENDPOINT", ""), "OTLP/gRPC endpoint; empty disables trace export")
	fs.StringVar(&rate, "trace-sample-ratio", env("HUB_TRACE_SAMPLE_RATIO", "0.05"), "head sampling ratio 0..1")
	fs.StringVar(&c.ConfigDir, "config-dir", env("HUB_CONFIG_DIR", "config"), "registry config directory (dev)")
	fs.StringVar(&c.ContractsDir, "contracts-dir", env("HUB_CONTRACTS_DIR", "../contracts"), "unused since ADR-0023 (kept for old scripts)")
	fs.StringVar(&c.DataDir, "data-dir", env("HUB_DATA_DIR", ".data"), "local data directory (snapshot cache)")
	fs.StringVar(&c.BlobURL, "blob-url", env("HUB_BLOB_URL", "file://.data/blobs"), "snapshot blob store URL (file:// in phase 1)")
	fs.BoolVar(&watch, "config-watch", env("HUB_CONFIG_WATCH", "false") == "true", "dev only: watch config-dir and swap snapshots")
	fs.StringVar(&c.CredSource, "cred-source", env("HUB_CRED_SOURCE", "localfile://.data/dev-secrets.enc"), "credential source (localfile://<path> in phase 1)")
	fs.StringVar(&c.DevKeyFile, "dev-key-file", env("HUB_DEV_KEY_FILE", ".data/dev.key"), "key file of the localfile credential source")
	fs.BoolVar(&cbLoopback, "callback-allow-loopback", env("HUB_CALLBACK_ALLOW_LOOPBACK", "false") == "true", "dev only: allow callbacks to loopback http registrar endpoints")
	fs.BoolVar(&debugHeaders, "debug-headers", env("HUB_DEBUG_HEADERS", "false") == "true", "gateway: send the debug-only X-Hub-Integration-Version header")
	fs.StringVar(&c.OIDCIssuer, "oidc-issuer", env("HUB_OIDC_ISSUER", ""), "admin: OIDC issuer URL whose tokens are accepted")
	fs.StringVar(&c.OIDCAudience, "oidc-audience", env("HUB_OIDC_AUDIENCE", ""), "admin: required token audience (optional)")
	fs.BoolVar(&devID, "admin-dev-oidc", env("HUB_ADMIN_DEV_OIDC", "false") == "true", "dev only: run the admin OIDC issuer stub")
	fs.StringVar(&c.AdminAuthMode, "admin-auth-mode", env("HUB_ADMIN_AUTH_MODE", "jwt"), "admin: jwt (default) or isfc (ISFC gateway identity, AUTH_PLAN §7)")
	fs.StringVar(&c.ISFCConsoleClientID, "isfc-console-client-id", env("HUB_ISFC_CONSOLE_CLIENT_ID", ""), "admin (isfc mode): ISFC client id of the Hub console")
	fs.StringVar(&c.ISFCPlatformTenantID, "isfc-platform-tenant-id", env("HUB_ISFC_PLATFORM_TENANT_ID", ""), "admin (isfc mode): ISFC tenant UUID of the platform tenant")
	var opaTimeout string
	var opaDisabled bool
	fs.StringVar(&c.OPAURL, "opa-url", env("HUB_OPA_URL", ""), "admin (isfc mode): OPA sidecar base URL, e.g. http://localhost:8181")
	fs.StringVar(&opaTimeout, "opa-timeout", env("HUB_OPA_TIMEOUT", "200ms"), "admin (isfc mode): timeout of one OPA decision")
	fs.BoolVar(&opaDisabled, "opa-disabled", env("HUB_OPA_DISABLED", "false") == "true", "tests only: skip OPA (refused unless HUB_ENV=test)")
	fs.StringVar(&c.AdminPublicURL, "admin-public-url", env("HUB_ADMIN_PUBLIC_URL", "http://localhost:8081"), "admin: external base URL (dev issuer iss)")
	var callbackHosts, traceVendors, concurrency, claimBatch, targetCap string
	fs.StringVar(&concurrency, "worker-concurrency", env("HUB_WORKER_CONCURRENCY", "0"), "worker: attempts in flight (0: 256 messaging, 64 llm)")
	fs.StringVar(&claimBatch, "worker-claim-batch", env("HUB_WORKER_CLAIM_BATCH", "0"), "worker: most envelopes per claim (0: 32)")
	fs.StringVar(&targetCap, "worker-target-cap", env("HUB_WORKER_TARGET_CAP", "0"), "worker: most attempts in flight per account and endpoint (0: concurrency/4, at most 64)")
	fs.StringVar(&traceVendors, "trace-forward-vendors", env("HUB_TRACE_FORWARD_VENDORS", ""), "comma-separated vendor names that receive traceparent (default none)")
	fs.StringVar(&callbackHosts, "callback-hosts", env("HUB_CALLBACK_HOSTS", ""), "admin: comma-separated registrar host allowlist (host or .suffix)")
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
	c.ConfigWatch, c.DebugHeaders, c.CallbackAllowLoopback, c.AdminDevOIDC = watch, debugHeaders, cbLoopback, devID
	c.OPADisabled, c.Env = opaDisabled, getenv("HUB_ENV")
	c.CallbackHosts, c.TraceForwardVendors = splitList(callbackHosts), splitList(traceVendors)
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	var errs []error
	var err error
	if c.ShutdownGrace, err = time.ParseDuration(shutdownGrace); err != nil || c.ShutdownGrace <= 0 {
		errs = append(errs, fmt.Errorf("shutdown-grace: want a positive duration, got %q", shutdownGrace))
	}
	if c.ReadyDrain, err = time.ParseDuration(readyDrain); err != nil || c.ReadyDrain < 0 {
		errs = append(errs, fmt.Errorf("ready-drain: want a duration >= 0, got %q", readyDrain))
	}
	if c.ReadyDrain >= c.ShutdownGrace && c.ShutdownGrace > 0 {
		errs = append(errs, fmt.Errorf("ready-drain (%s) must be shorter than shutdown-grace (%s)", c.ReadyDrain, c.ShutdownGrace))
	}
	if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
		errs = append(errs, fmt.Errorf("log-level: %w", err))
	}
	if c.TraceSampleRatio, err = strconv.ParseFloat(rate, 64); err != nil || c.TraceSampleRatio < 0 || c.TraceSampleRatio > 1 {
		errs = append(errs, fmt.Errorf("trace-sample-ratio: want 0..1, got %q", rate))
	}
	if c.PGDSN == "" {
		errs = append(errs, errors.New("HUB_PG_DSN is required (environment only)"))
	}
	if !strings.HasPrefix(c.CredSource, "localfile://") || c.CredSource == "localfile://" {
		errs = append(errs, fmt.Errorf("cred-source: want localfile://<path> (phase 1), got %q", c.CredSource))
	}
	if k := len(c.AuditMaskKey); k > 0 && k < 32 {
		errs = append(errs, fmt.Errorf("HUB_AUDIT_MASK_KEY: want at least 32 bytes, got %d", k))
	}
	if role == Worker && c.WorkerPool != "messaging" && c.WorkerPool != "llm" {
		errs = append(errs, fmt.Errorf("pool: want messaging or llm, got %q", c.WorkerPool))
	}
	for _, f := range []struct {
		name string
		raw  string
		dst  *int
	}{{"worker-concurrency", concurrency, &c.WorkerConcurrency}, {"worker-claim-batch", claimBatch, &c.WorkerClaimBatch}, {"worker-target-cap", targetCap, &c.WorkerTargetCap}} {
		if *f.dst, err = strconv.Atoi(f.raw); err != nil || *f.dst < 0 {
			errs = append(errs, fmt.Errorf("%s: want an integer >= 0 (0 = default), got %q", f.name, f.raw))
		}
	}
	if c.WorkerConcurrency > 0 && c.WorkerTargetCap > c.WorkerConcurrency {
		errs = append(errs, fmt.Errorf("worker-target-cap (%d) must not exceed worker-concurrency (%d)", c.WorkerTargetCap, c.WorkerConcurrency))
	}
	if c.AdminDevOIDC && c.OIDCIssuer != "" {
		errs = append(errs, errors.New("admin-dev-oidc and oidc-issuer are exclusive: the stub would shadow the real issuer"))
	}
	if err := authz.CheckDisabled(c.OPADisabled, c.Env); err != nil {
		errs = append(errs, err)
	}
	if c.OPATimeout, err = time.ParseDuration(opaTimeout); err != nil || c.OPATimeout <= 0 {
		errs = append(errs, fmt.Errorf("opa-timeout: want a positive duration, got %q", opaTimeout))
	}
	switch c.AdminAuthMode {
	case "jwt":
	case "isfc":
		if role == Admin {
			errs = append(errs, validateISFCAuth(c)...)
		}
	default:
		errs = append(errs, fmt.Errorf("admin-auth-mode: want jwt or isfc, got %q", c.AdminAuthMode))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validateISFCAuth lists what the admin needs in HUB_ADMIN_AUTH_MODE=isfc.
func validateISFCAuth(c Config) []error {
	var errs []error
	if c.OIDCIssuer == "" {
		errs = append(errs, errors.New("HUB_ADMIN_AUTH_MODE=isfc needs HUB_OIDC_ISSUER (the Hydra issuer that signs the access tokens)"))
	}
	if c.AdminDevOIDC {
		errs = append(errs, errors.New("HUB_ADMIN_AUTH_MODE=isfc cannot run the dev OIDC stub (admin-dev-oidc)"))
	}
	if c.ISFCConsoleClientID == "" {
		errs = append(errs, errors.New("HUB_ADMIN_AUTH_MODE=isfc needs HUB_ISFC_CONSOLE_CLIENT_ID"))
	}
	if c.OPAURL == "" && !c.OPADisabled {
		errs = append(errs, errors.New("HUB_ADMIN_AUTH_MODE=isfc needs HUB_OPA_URL (the OPA sidecar that decides every admin request)"))
	}
	return errs
}

// splitList splits a comma-separated flag value, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}
