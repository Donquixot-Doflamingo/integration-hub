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

func provider(db *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, files)
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
