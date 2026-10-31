// Command hub runs one Hub role: gateway, worker, scheduler or admin (DESIGN §17).
// `hub migrate` applies the embedded database migrations.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/migrations"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: hub <gateway|worker|scheduler|admin|migrate> [flags]")
		return 2
	}
	switch args[0] {
	case "migrate":
		if err := migrate(ctx, getenv("HUB_PG_DSN"), stdout); err != nil {
			fmt.Fprintln(stderr, "hub migrate:", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "hub: role %q not implemented yet\n", args[0])
		return 2
	}
}

func migrate(ctx context.Context, dsn string, stdout io.Writer) error {
	if dsn == "" {
		return fmt.Errorf("HUB_PG_DSN is required")
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
