package roles

import (
	"context"
	"fmt"
	"time"

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
	// The gateway's idempotency store sits on the request path: fail within its 50 ms op timeout
	// when Redis is down instead of go-redis' default dial retries (TECH_SPEC §8.1, §12.2).
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr, ContextTimeoutEnabled: true, DialerRetries: 1,
		DialTimeout: 250 * time.Millisecond, ReadTimeout: 100 * time.Millisecond, WriteTimeout: 100 * time.Millisecond,
		PoolSize: 50,
	})
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
