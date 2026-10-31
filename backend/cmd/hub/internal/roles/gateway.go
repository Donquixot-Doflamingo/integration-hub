package roles

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"log/slog"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/edge"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// gateway holds the sync path's collaborators (DESIGN §18.4): the warm credential cache, the
// per-account upstream client, the audit writer and the edge server built on them.
type gateway struct {
	log    *slog.Logger
	cache  *creds.Cache
	client *upstream.Client
	audit  *audit.Writer
	edge   *edge.Server
	wg     sync.WaitGroup
	stop   context.CancelFunc
}

func newGateway(ctx context.Context, cfg Config, m *telemetry.Metrics, log *slog.Logger, secrets *telemetry.SecretSet, tp trace.TracerProvider, pg *pgxpool.Pool, reg *roleRegistry) (*gateway, error) {
	g := &gateway{log: log}
	var loader creds.Loader = emptyLoader{}
	path := strings.TrimPrefix(cfg.CredSource, "localfile://")
	switch lf, err := creds.OpenLocalFile(path, cfg.DevKeyFile); {
	case err == nil:
		loader = lf
	case errors.Is(err, fs.ErrNotExist):
		// Dev without `hubctl creds init-key`: start, but every vendor call fails closed with
		// 503 credentials_unavailable until credentials exist.
		log.WarnContext(ctx, "no credential key file; vendor calls will fail until credentials are set (hubctl creds)",
			slog.String("key_file", cfg.DevKeyFile))
	default:
		return nil, err
	}
	cache, err := creds.NewCache(loader, secrets, m, creds.CacheOptions{Logger: log})
	if err != nil {
		return nil, err
	}
	client, err := upstream.New(m, upstream.Options{TracerProvider: tp})
	if err != nil {
		return nil, err
	}
	key := []byte(cfg.AuditMaskKey)
	if len(key) == 0 {
		key = make([]byte, 32)
		_, _ = rand.Read(key)
		log.WarnContext(ctx, "HUB_AUDIT_MASK_KEY is not set: masked identifiers change on every restart")
	}
	g.cache, g.client = cache, client
	g.audit = audit.NewWriter(audit.PGStore{Pool: pg}, m, audit.WriterOptions{Log: log})
	g.edge = edge.New(edge.Deps{
		Snapshots: &reg.store, Creds: cache, Upstream: client, Audit: g.audit,
		Masker: audit.NewMasker(key), Metrics: m, Log: log, DebugHeaders: cfg.DebugHeaders,
	})
	// Credentials of every account of a snapshot are loaded before it serves traffic; accounts
	// that fail to load miss (503) rather than block the swap.
	reg.beforeSwap = func(ctx context.Context, s *registry.Snapshot) {
		if err := cache.Warm(ctx, creds.AccountIDs(s)); err != nil {
			log.WarnContext(ctx, "credential warm-up incomplete", slog.String("error", err.Error()))
		}
	}
	reg.afterSwap = client.Prune
	return g, nil
}

// start runs the cache refresher and the audit writer until stop.
func (g *gateway) start(ctx context.Context) {
	ctx, g.stop = context.WithCancel(context.WithoutCancel(ctx))
	g.wg.Go(func() { g.cache.Run(ctx) })
	g.wg.Go(func() { g.audit.Run(ctx) })
}

// shutdown stops the background loops after the public listener has drained, so late audit
// events are still written, then closes upstream connections.
func (g *gateway) shutdown() {
	if g.stop != nil {
		g.stop()
	}
	g.wg.Wait()
	g.client.Close()
}

// emptyLoader serves no credentials.
type emptyLoader struct{}

func (emptyLoader) Load(context.Context, []string) (map[string]creds.AccountSecrets, error) {
	return map[string]creds.AccountSecrets{}, nil
}
