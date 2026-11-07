// Package migrations embeds the goose SQL migrations and applies them.
// Migrations are append-only: never edit one that has been applied (postgres-patterns skill).
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// versionTable is schema-qualified on purpose. Postgres resolves unqualified names through
// search_path ("$user", public); the dev and CI database user is named "hub", so once the
// baseline creates schema hub an unqualified goose_db_version would resolve to a new, empty
// hub.goose_db_version and every migration would run again. Application tables are always
// schema-qualified (hub.<table>) for the same reason.
const versionTable = "public.goose_db_version"

func provider(db *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithTableName(versionTable))
	if err != nil {
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	return p, nil
}

// Up applies every pending migration and returns how many were applied.
func Up(ctx context.Context, db *sql.DB) (int, error) {
	p, err := provider(db)
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	if err != nil {
		return len(res), fmt.Errorf("apply migrations: %w", err)
	}
	return len(res), nil
}

// UpTo applies pending migrations up to and including version (tests that need an old schema).
func UpTo(ctx context.Context, db *sql.DB, version int64) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.UpTo(ctx, version); err != nil {
		return fmt.Errorf("apply migrations up to %d: %w", version, err)
	}
	return nil
}

// Status reports, per migration version, whether it has been applied.
func Status(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration status: %w", err)
	}
	out := make(map[int64]bool, len(st))
	for _, s := range st {
		out[s.Source.Version] = s.State == goose.StateApplied
	}
	return out, nil
}

// DownTo rolls back migrations until version is the newest applied (0 = everything).
// For tests and local recovery only; production never runs down migrations.
func DownTo(ctx context.Context, db *sql.DB, version int64) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	if _, err := p.DownTo(ctx, version); err != nil {
		return fmt.Errorf("roll back to %d: %w", version, err)
	}
	return nil
}
