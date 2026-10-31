// Command hub runs one Hub role: gateway, worker, scheduler or admin (DESIGN §17).
// `hub migrate` applies the embedded database migrations.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/cmd/hub/internal/roles"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

// version is set at build time: -ldflags "-X main.version=<git sha or tag>".
var version = "dev"

const usage = `usage: hub <role> [flags]

roles:
  gateway     public caller API (/v1), inbound webhooks
  worker      async delivery (--pool=messaging|llm)
  scheduler   retries, drain, TTL expiry, health probes (leader-elected)
  admin       control plane (/admin/v1)
  migrate     apply database migrations and exit

Run "hub <role> -h" for flags. Configuration: flags > HUB_* env > defaults (TECH_SPEC §8.1).`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if args[0] == "migrate" {
		if err := migrate(ctx, getenv("HUB_PG_DSN"), stdout); err != nil {
			fmt.Fprintln(stderr, "hub migrate:", err)
			return 1
		}
		return 0
	}

	role, err := roles.ParseRole(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "hub: %v\n\n%s\n", err, usage)
		return 2
	}
	cfg, err := roles.Load(role, args[1:], getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "hub %s: invalid configuration:\n%v\n", role, err)
		return 2
	}
	secrets := telemetry.NewSecretSet()
	if u, err := url.Parse(string(cfg.PGDSN)); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			secrets.Add(pw)
		}
	}
	log := telemetry.NewLogger(stderr, cfg.LogLevel, secrets, string(role))
	if err := roles.Run(ctx, cfg, log, roles.Options{Version: version, Secrets: secrets}); err != nil {
		log.ErrorContext(ctx, "role exited with error", slog.String("error", err.Error()))
		return 1
	}
	return 0
}

func migrate(ctx context.Context, dsn string, stdout io.Writer) error {
	if dsn == "" {
		return errors.New("HUB_PG_DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer func() { _ = db.Close() }()
	n, err := migrations.Up(ctx, db)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "applied %d migration(s)\n", n)
	return nil
}
