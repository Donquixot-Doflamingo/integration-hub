package roles

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// stores are the shared Postgres and Redis clients of a role. Both connect lazily, so a role
// starts (and reports not-ready) while a dependency is down.
type stores struct {
	pg  *pgxpool.Pool
	rdb *redis.Client
}

func openStores(ctx context.Context, cfg Config) (*stores, error) {
	pcfg, err := pgxpool.ParseConfig(string(cfg.PGDSN))
	if err != nil {
		return nil, fmt.Errorf("HUB_PG_DSN: %w", redactParseErr(err))
	}
	pg, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("postgres pool: %w", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	return &stores{pg: pg, rdb: rdb}, nil
}

func (s *stores) checks() []Check {
	return []Check{
		{Name: "postgres", Fn: s.pg.Ping},
		{Name: "redis", Fn: func(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }},
	}
}

// close releases Redis first, then Postgres last (go-architecture shutdown order).
func (s *stores) close() {
	_ = s.rdb.Close()
	s.pg.Close()
}

// redactParseErr drops the DSN text that pgx may echo back in a parse error.
func redactParseErr(error) error { return fmt.Errorf("invalid connection string") }
