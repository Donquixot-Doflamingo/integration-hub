package roles

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/core"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/delivery"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/edge"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/idempotency"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/queue"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/registry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream"
)

// dataPlane holds the delivery collaborators of the gateway, worker and scheduler roles
// (DESIGN §17): warm credential cache and upstream client (gateway, worker), audit writer,
// guarded transitions, and the role's own loop or server.
type dataPlane struct {
	log         *slog.Logger
	name        string // owns this process's queue claims
	cache       *creds.Cache
	client      *upstream.Client
	audit       *audit.Writer
	transitions *delivery.Transitions
	edge        *edge.Server        // gateway
	worker      *delivery.Worker    // worker
	scheduler   *delivery.Scheduler // scheduler
	wg          sync.WaitGroup
	stop        context.CancelFunc
}

// nodeName identifies this process's claims in hub.outbox.claimed_by.
func nodeName(role Role) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%s:%d:%s", role, host, os.Getpid(), core.NewULID(time.Now())[20:])
}

func newDataPlane(ctx context.Context, cfg Config, m *telemetry.Metrics, log *slog.Logger, secrets *telemetry.SecretSet, tp trace.TracerProvider, st *stores, reg *roleRegistry) (*dataPlane, error) {
	dp := &dataPlane{log: log, name: nodeName(cfg.Role)}
	q := &queue.Outbox{}
	dp.transitions = &delivery.Transitions{DB: st.pg, Queue: q} // OnTerminal: callbacks (HUB-120)
	if cfg.Role == Scheduler {
		dp.scheduler = &delivery.Scheduler{DB: st.pg, Queue: q, Transitions: dp.transitions, Log: log}
		dp.scheduler.RegisterMetrics(m.Registry)
		return dp, nil
	}

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
	dp.cache, dp.client = cache, client
	dp.audit = audit.NewWriter(audit.PGStore{Pool: st.pg}, m, audit.WriterOptions{Log: log})
	masker := audit.NewMasker(key)
	attempter := &delivery.Attempter{Creds: cache, Upstream: client, Log: log}
	budget := delivery.NewBudget(m.Registry)
	settler := delivery.Settler{Transitions: dp.transitions, Budget: budget}

	switch cfg.Role {
	case Gateway:
		idem := idempotency.New(st.rdb, m, idempotency.Options{})
		if err := idem.Warm(ctx); err != nil {
			log.WarnContext(ctx, "idempotency scripts not preloaded (falls back to EVAL)", slog.String("error", err.Error()))
		}
		dp.edge = edge.New(edge.Deps{
			Snapshots: &reg.store, Creds: cache, Upstream: client, Audit: dp.audit, Masker: masker, Metrics: m, Log: log,
			DebugHeaders: cfg.DebugHeaders, Idempotency: idem,
			Queueing: &edge.Queueing{DB: st.pg, Queue: q, Settler: settler, Name: dp.name},
		})
	case Worker:
		dp.worker = &delivery.Worker{
			Name: dp.name, Pool: cfg.WorkerPool, DB: st.pg, Queue: q, Transitions: dp.transitions, Snapshots: &reg.store,
			Attempter: attempter, Audit: dp.audit, Masker: masker, Budget: budget, Metrics: m, Log: log,
		}
	case Admin, Scheduler:
	}
	// Credentials of every account of a snapshot are loaded before it serves traffic; accounts
	// that fail to load miss (503 / retry) rather than block the swap.
	reg.beforeSwap = func(ctx context.Context, s *registry.Snapshot) {
		if err := cache.Warm(ctx, creds.AccountIDs(s)); err != nil {
			log.WarnContext(ctx, "credential warm-up incomplete", slog.String("error", err.Error()))
		}
	}
	reg.afterSwap = client.Prune
	return dp, nil
}

// start runs the background loops until shutdown.
func (dp *dataPlane) start(ctx context.Context) {
	ctx, dp.stop = context.WithCancel(context.WithoutCancel(ctx))
	if dp.cache != nil {
		dp.wg.Go(func() { dp.cache.Run(ctx) })
	}
	if dp.audit != nil {
		dp.wg.Go(func() { dp.audit.Run(ctx) })
	}
	if dp.worker != nil {
		dp.wg.Go(func() { dp.worker.Run(ctx) })
	}
	if dp.scheduler != nil {
		dp.wg.Go(func() { dp.scheduler.Run(ctx) })
	}
}

// shutdown stops the loops after the public listener has drained: the worker finishes its
// in-flight attempts (unfinished claims expire and are reclaimed), late audit events are
// written, then upstream connections close.
func (dp *dataPlane) shutdown() {
	if dp.stop != nil {
		dp.stop()
	}
	dp.wg.Wait()
	if dp.client != nil {
		dp.client.Close()
	}
}

// emptyLoader serves no credentials.
type emptyLoader struct{}

func (emptyLoader) Load(context.Context, []string) (map[string]creds.AccountSecrets, error) {
	return map[string]creds.AccountSecrets{}, nil
}
