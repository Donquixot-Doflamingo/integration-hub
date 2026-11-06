// Command hubmcp is the MCP server that lets Hub users discover, dry-run and invoke integrations from an AI assistant (F-20, contracts/mcp/hubmcp-tools.json).
//
// It talks to the Hub only through the contracted HTTP API (pkg/hubclient for the caller API, a
// GET-only client for the admin API), never internal/.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient"
)

// version is set at build time (-ldflags "-X main.version=..."); it goes into the MCP server
// info and the User-Agent (hubmcp/<version>).
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// config is the process configuration. Credentials come from the environment only, so they
// never show up in process listings (HUB-300).
type config struct {
	transport    string
	logLevel     slog.Level
	baseURL      string
	apiKey       secret
	adminToken   secret
	adminBaseURL string
}

const (
	transportStdio = "stdio"
)

// String describes the configuration without its credentials (fmt does not call String on
// unexported fields, so the secret type alone would not protect %v of a config).
func (c config) String() string {
	return fmt.Sprintf("hubmcp config{transport: %s, base_url: %s, admin_base_url: %s, api_key: ***, admin_token: %s}",
		c.transport, c.baseURL, c.adminBaseURL, map[bool]string{true: "***", false: "(unset)"}[c.adminToken != ""])
}

// GoString is String, so %#v hides the credentials too.
func (c config) GoString() string { return c.String() }

func loadConfig(args []string, getenv func(string) string, stderr io.Writer) (config, error) {
	var c config
	var level string
	fs := flag.NewFlagSet("hubmcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.transport, "transport", transportStdio, "MCP transport: stdio")
	fs.StringVar(&level, "log-level", "info", "debug, info, warn or error (logs go to stderr)")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	c.baseURL = strings.TrimRight(getenv("HUB_BASE_URL"), "/")
	c.apiKey = secret(getenv("HUB_API_KEY"))
	c.adminToken = secret(getenv("HUB_ADMIN_TOKEN"))
	c.adminBaseURL = getenv("HUB_ADMIN_BASE_URL")
	if c.adminBaseURL == "" && c.baseURL != "" {
		c.adminBaseURL = c.baseURL + "/admin/v1"
	}
	var errs []error
	if c.baseURL == "" {
		errs = append(errs, errors.New("HUB_BASE_URL is required"))
	}
	if strings.TrimSpace(string(c.apiKey)) == "" {
		errs = append(errs, errors.New("HUB_API_KEY is required (environment only)"))
	}
	if c.transport != transportStdio {
		errs = append(errs, fmt.Errorf("transport: want stdio, got %q", c.transport))
	}
	if err := c.logLevel.UnmarshalText([]byte(level)); err != nil {
		errs = append(errs, fmt.Errorf("log-level: %w", err))
	}
	return c, errors.Join(errs...)
}

// newApp builds the tool dependencies from the configuration.
func newApp(c config, log *slog.Logger, secrets *secretSet, hc *http.Client) (*app, error) {
	ua := "hubmcp/" + version
	hub, err := hubclient.New(c.baseURL, string(c.apiKey), hubclient.WithHTTPClient(hc), hubclient.WithUserAgent(ua))
	if err != nil {
		return nil, err
	}
	a := &app{hub: hub, log: log, secrets: secrets, now: time.Now}
	if c.adminToken != "" {
		if a.admin, err = newAdminClient(c.adminBaseURL, c.adminToken, hc, ua); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := loadConfig(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "hubmcp:", err)
		return 2
	}
	secrets := newSecretSet(cfg.apiKey, cfg.adminToken)
	log := newLogger(stderr, cfg.logLevel, secrets)
	contract, err := loadContract(toolContractJSON)
	if err != nil {
		log.ErrorContext(ctx, "load tool contract", slog.String("error", err.Error()))
		return 1
	}
	a, err := newApp(cfg, log, secrets, &http.Client{})
	if err != nil {
		log.ErrorContext(ctx, "configure hubmcp", slog.String("error", err.Error()))
		return 2
	}
	srv, err := newMCPServer(contract, a, version)
	if err != nil {
		log.ErrorContext(ctx, "build MCP server", slog.String("error", err.Error()))
		return 1
	}
	log.InfoContext(ctx, "hubmcp started", slog.String("transport", cfg.transport), slog.String("hub_base_url", cfg.baseURL),
		slog.Bool("admin_tools", a.admin != nil), slog.String("contract_version", contract.Version), slog.String("version", version))
	if a.admin == nil {
		log.InfoContext(ctx, "admin tools disabled: HUB_ADMIN_TOKEN is not set")
	}
	err = srv.Run(ctx, &mcp.IOTransport{Reader: nopReadCloser{stdin}, Writer: nopWriteCloser{stdout}})
	if err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
		log.ErrorContext(ctx, "MCP session ended", slog.String("error", err.Error()))
		return 1
	}
	log.InfoContext(ctx, "hubmcp stopped")
	return 0
}

type nopReadCloser struct{ io.Reader }

func (nopReadCloser) Close() error { return nil }

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
