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
	Role             Role
	HTTPAddr         string // public listener (gateway /v1, admin /admin/v1); unused by worker and scheduler
	OpsAddr          string // /healthz, /readyz, /metrics, /debug/pprof; never exposed outside the cluster
	PGDSN            Secret
	RedisAddr        string
	AdminURL         string // where data-plane roles fetch snapshots
	WorkerPool       string
	ShutdownGrace    time.Duration // total budget for graceful shutdown
	ReadyDrain       time.Duration // time between /readyz=503 and closing listeners
	LogLevel         slog.Level
	OTLPEndpoint     string // empty disables trace export
	TraceSampleRatio float64
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
		c                                      = Config{Role: role, PGDSN: Secret(getenv("HUB_PG_DSN"))}
		shutdownGrace, readyDrain, level, rate string
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
	if err := fs.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}
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
	if role == Worker && c.WorkerPool != "messaging" && c.WorkerPool != "llm" {
		errs = append(errs, fmt.Errorf("pool: want messaging or llm, got %q", c.WorkerPool))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return c, nil
}
