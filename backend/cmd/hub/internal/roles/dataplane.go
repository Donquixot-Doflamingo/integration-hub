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

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/callbacks"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
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
	edge        *edge.Server          // gateway
	worker      *delivery.Worker      // worker
	dispatcher  *callbacks.Dispatcher // worker: signed callbacks
	scheduler   *delivery.Scheduler   // scheduler
	breakers    *upstream.Breakers    // circuit breakers (gateway, worker, scheduler)
	rdb         *redis.Client         // hub:creds pings (credential rotation)
	shadow      *delivery.Shadower    // gateway: shadow copies (DESIGN §6.4)
	wg          sync.WaitGroup
	stop        context.CancelFunc
}

// nodeName identifies this process's claims in hub.outbox.claimed_by.
func nodeName(role Role) string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s:%s:%d:%s", role, host, os.Getpid(), core.NewULID(time.Now())[20:])
}

func newDataPlane(ctx context.Context, cfg Config, m *telemetry.Metrics, log *slog.Logger, secrets *telemetry.SecretSet, tp trace.TracerProvider, st *stores, reg *roleRegistry) (*dataPlane, error) {
	dp := &dataPlane{log: log, name: nodeName(cfg.Role), rdb: st.rdb}
	q := &queue.Outbox{}
	// Terminal callbacks are enqueued in the terminal transaction of whichever role settles the
	// request: the worker, the gateway (wait mode) or the scheduler (expiry).
	enq, err := callbacks.NewEnqueuer(&reg.store, m, callbacks.EnqueueOptions{Log: log})
	if err != nil {
		return nil, err
	}
	dp.transitions = &delivery.Transitions{DB: st.pg, Queue: q, OnTerminal: delivery.CallbackHook(enq), Metrics: m}
	dp.breakers = upstream.NewBreakers(st.rdb, m, log)
	if cfg.Role == Scheduler {
		// Probes need no credentials (DESIGN §10.4 synthetic probe; an auth error proves the vendor answers).
		probeClient, err := upstream.New(m, upstream.Options{TracerProvider: tp})
		if err != nil {
			return nil, err
		}
		dp.client = probeClient
		res := &delivery.Resilience{Breakers: dp.breakers, Upstream: probeClient, Snapshots: &reg.store, Queue: q, Log: log}
		res.RegisterMetrics(m.Registry)
		sampler := &delivery.QueueSampler{Queue: q, Log: log}
		sampler.RegisterMetrics(m.Registry)
		dp.scheduler = &delivery.Scheduler{DB: st.pg, Queue: q, Transitions: dp.transitions, Log: log, Resilience: res, Sampler: sampler}
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
	forward := map[string]bool{}
	for _, v := range cfg.TraceForwardVendors {
		forward[v] = true
	}
	client, err := upstream.New(m, upstream.Options{TracerProvider: tp, ForwardTraceparent: forward})
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
	sealer := auditSealer(ctx, cfg, log)
	limiter := upstream.NewAccountLimiter(st.rdb, m, log)
	attempter := &delivery.Attempter{Creds: cache, Upstream: client, Breakers: dp.breakers, Limiter: limiter, Log: log}
	budget := delivery.NewBudget(m.Registry)
	settler := delivery.Settler{Transitions: dp.transitions, Budget: budget}

	switch cfg.Role {
	case Gateway:
		dp.shadow = delivery.NewShadower(attempter, dp.audit, log, m.Registry)
		idem := idempotency.New(st.rdb, m, idempotency.Options{})
		if err := idem.Warm(ctx); err != nil {
			log.WarnContext(ctx, "idempotency scripts not preloaded (falls back to EVAL)", slog.String("error", err.Error()))
		}
		dp.edge = edge.New(edge.Deps{
			Snapshots: &reg.store, Creds: cache, Upstream: client, Breakers: dp.breakers, Limiter: limiter, Audit: dp.audit, Masker: masker, Sealer: sealer, Metrics: m, Log: log,
			DebugHeaders: cfg.DebugHeaders, Idempotency: idem, Shadow: dp.shadow,
			Queueing: &edge.Queueing{DB: st.pg, Queue: q, Settler: settler, Name: dp.name},
		})
	case Worker:
		dp.worker = &delivery.Worker{
			Name: dp.name, Pool: cfg.WorkerPool, DB: st.pg, Queue: q, Transitions: dp.transitions, Snapshots: &reg.store,
			Attempter: attempter, Audit: dp.audit, Masker: masker, Sealer: sealer, Budget: budget, Metrics: m, Log: log,
			TracerProvider: tp,
		}
		if dp.dispatcher, err = callbacks.NewDispatcher(callbacks.DispatcherDeps{
			Pool: st.pg, Snapshots: &reg.store, Secrets: creds.RegistrarSecrets{Cache: cache}, Audit: dp.audit,
			Metrics: m, Log: log, TracerProvider: tp,
		}, callbacks.DispatcherOptions{Guard: callbacks.GuardOptions{AllowLoopback: cfg.CallbackAllowLoopback}}); err != nil {
			return nil, err
		}
	case Admin, Scheduler:
	}
	// Credentials of every account of a snapshot are loaded before it serves traffic; accounts
	// that fail to load miss (503 / retry) rather than block the swap.
	secretIDs := creds.AccountIDs
	if cfg.Role == Worker { // the worker also signs callbacks
		secretIDs = creds.SnapshotSecretIDs
	}
	reg.beforeSwap = func(ctx context.Context, s *registry.Snapshot) {
		if err := cache.Warm(ctx, secretIDs(s)); err != nil {
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
		if dp.rdb != nil {
			dp.wg.Go(func() { dp.followCredentials(ctx) })
		}
	}
	if dp.breakers != nil {
		dp.wg.Go(func() { dp.breakers.Run(ctx) })
	}
	if dp.audit != nil {
		dp.wg.Go(func() { dp.audit.Run(ctx) })
	}
	if dp.worker != nil {
		dp.wg.Go(func() { dp.worker.Run(ctx) })
	}
	if dp.dispatcher != nil {
		dp.wg.Go(func() {
			if err := dp.dispatcher.Run(ctx); err != nil {
				dp.log.ErrorContext(ctx, "callback dispatcher stopped", slog.String("error", err.Error()))
			}
		})
	}
	if dp.scheduler != nil {
		dp.wg.Go(func() { dp.scheduler.Run(ctx) })
	}
}

// followCredentials reloads the credential cache on every hub:creds ping, so a promoted
// credential serves every later attempt (drains included) within a second (DESIGN §6.6). The
// cache's own periodic refresh is the fallback when a ping is missed.
func (dp *dataPlane) followCredentials(ctx context.Context) {
	sub := dp.rdb.Subscribe(ctx, control.CredentialsChannel)
	defer func() { _ = sub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel():
			if !ok {
				return
			}
			if err := dp.cache.Refresh(ctx); err != nil && ctx.Err() == nil {
				dp.log.WarnContext(ctx, "credential reload after rotation failed", slog.String("account", msg.Payload), slog.String("error", err.Error()))
			} else {
				dp.log.InfoContext(ctx, "credentials reloaded after rotation", slog.String("account", msg.Payload))
			}
		}
	}
}

// shutdown stops the loops after the public listener has drained: the worker finishes its
// in-flight attempts (unfinished claims expire and are reclaimed), late audit events are
// written, then upstream connections close.
func (dp *dataPlane) shutdown() {
	if dp.shadow != nil {
		dp.shadow.Wait() // copies in flight finish (bounded by their timeout) while audit still runs
	}
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
