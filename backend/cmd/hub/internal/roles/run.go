package roles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfcsync"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Options are test and wiring hooks for Run.
type Options struct {
	// OnListen is called once per listener after it is bound, with "http" or "ops" and the
	// bound address. Tests pass ":0" addresses and use it to learn the ports.
	OnListen func(name, addr string)
	// Version is the build version reported in hub_build_info and trace resources.
	Version string
	// SpanProcessor replaces the OTLP exporter (tests record spans in memory).
	SpanProcessor sdktrace.SpanProcessor
	// Metrics, when set, is used instead of a fresh registry (tests scrape it).
	Metrics *telemetry.Metrics
	// ExtraChecks are added to the readiness checks (later milestones: snapshot loaded).
	ExtraChecks []Check
	// WrapListener, when set, wraps each bound listener before it serves (tests inject a listener
	// that fails at runtime).
	WrapListener func(name string, ln net.Listener) net.Listener
	// Secrets is the process's redaction set (the logger's). Run adds the secrets of cfg (the
	// Postgres password, the audit mask key) and the data plane adds decrypted credentials. nil
	// means a fresh set.
	Secrets *telemetry.SecretSet
}

type endpoint struct {
	name, addr string
	h          http.Handler
}

type listener struct {
	name string
	srv  *http.Server
	ln   net.Listener
}

// Run starts cfg.Role and blocks until ctx is cancelled (SIGINT/SIGTERM in main) or a server
// fails, then shuts down gracefully (go-architecture skill, "Graceful shutdown"):
// readiness flips to false, the role keeps serving for ReadyDrain so load balancers stop
// routing, then the public listener and finally the ops listener are closed within
// ShutdownGrace.
func Run(ctx context.Context, cfg Config, log *slog.Logger, opts Options) error {
	secrets := opts.Secrets
	if secrets == nil {
		secrets = telemetry.NewSecretSet()
	}
	registerConfigSecrets(secrets, cfg) // before anything can log
	var ready atomic.Bool
	metrics := opts.Metrics
	if metrics == nil {
		metrics = telemetry.NewMetrics(string(cfg.Role), opts.Version)
	}
	tp, err := telemetry.NewTracerProvider(ctx, telemetry.TraceConfig{
		Role: string(cfg.Role), Version: opts.Version, Endpoint: cfg.OTLPEndpoint,
		SampleRatio: cfg.TraceSampleRatio, Insecure: true, SpanProcessor: opts.SpanProcessor,
	})
	if err != nil {
		return err
	}

	st, err := openStores(ctx, cfg)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return err
	}
	defer st.close() // last: everything above may still use them while shutting down

	reg, err := newRegistry(cfg, metrics, log, st.pg, st.rdb)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return err
	}
	var dp *dataPlane
	if cfg.Role == Gateway || cfg.Role == Worker || cfg.Role == Scheduler {
		if dp, err = newDataPlane(ctx, cfg, metrics, log, secrets, tp, st, reg); err != nil {
			_ = tp.Shutdown(ctx)
			return err
		}
		dp.start(ctx)
		defer dp.shutdown() // after the listeners closed (deferred calls run before st.close)
	}
	if cfg.Role == Admin {
		reg.upgradeLegacy(ctx, st.pg)
	}
	reg.load(ctx)
	var isync *isfcSync // the worker's ISFC reconciler (HUB_TENANT_SOURCE=isfc)
	if cfg.Role == Worker {
		if isync, err = newISFCSync(cfg, metrics, log, secrets, st, reg, isfcsync.NewMetrics(metrics.Registry, nil)); err != nil {
			_ = tp.Shutdown(ctx)
			return err
		}
	}
	checks := append(append(st.checks(), reg.check()), opts.ExtraChecks...)

	// ops is last so it is closed last: /readyz answers 503 while the public listener drains.
	endpoints := []endpoint{{"ops", cfg.OpsAddr, opsHandler(cfg.Role, &ready, checks, metrics, log)}}
	if cfg.Role.servesPublicHTTP() {
		h := publicHandler(cfg.Role, dp)
		if cfg.Role == Admin {
			if h, err = adminHandler(ctx, cfg, metrics, log, secrets, tp, st, reg); err != nil {
				_ = tp.Shutdown(ctx)
				return err
			}
		}
		pub := telemetry.HTTPHandler(h, tp, string(cfg.Role))
		endpoints = append([]endpoint{{"http", cfg.HTTPAddr, pub}}, endpoints...)
	}

	var lns []listener
	closeAll := func() {
		for _, l := range lns {
			_ = l.ln.Close()
		}
	}
	for _, h := range endpoints {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", h.addr)
		if err != nil {
			closeAll()
			return fmt.Errorf("listen %s on %s: %w", h.name, h.addr, err)
		}
		srv := &http.Server{
			Handler:           h.h,
			ReadHeaderTimeout: 5 * time.Second,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
			BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
		}
		if opts.WrapListener != nil {
			ln = opts.WrapListener(h.name, ln)
		}
		lns = append(lns, listener{name: h.name, srv: srv, ln: ln})
		if opts.OnListen != nil {
			opts.OnListen(h.name, ln.Addr().String())
		}
	}

	// loopCtx ends when ctx does or when a listener fails: either way the watchers below must
	// stop, or wg.Wait never returns (HUB-454).
	loopCtx, cancelLoops := context.WithCancel(ctx)
	defer cancelLoops()
	serveErr := make(chan error, len(lns))
	var wg sync.WaitGroup
	if cfg.ConfigWatch {
		wg.Go(func() { reg.watch(loopCtx) })
	}
	wg.Go(func() { reg.follow(loopCtx) }) // every role follows published snapshots (HUB-171)
	if cfg.Role == Scheduler {
		wg.Go(func() { reg.pruneLoop(loopCtx) })
	}
	if isync != nil {
		wg.Go(func() { isync.run(loopCtx, st.rdb) }) // leader-only inside (AUTH_PLAN §6.2)
	}
	if dp != nil && dp.isfcKeys != nil {
		wg.Go(func() { runCallerKeyLoader(loopCtx, st, dp.isfcKeys, log) })
	}
	for _, l := range lns {
		wg.Go(func() {
			if err := l.srv.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("%s server: %w", l.name, err)
			}
		})
	}
	ready.Store(true)
	log.InfoContext(ctx, "role started", slog.String("http_addr", cfg.HTTPAddr), slog.String("ops_addr", cfg.OpsAddr))

	var runErr error
	select {
	case <-ctx.Done():
		log.InfoContext(ctx, "shutdown requested")
	case runErr = <-serveErr:
		log.ErrorContext(ctx, "server failed, shutting down", slog.String("error", runErr.Error()))
	}
	cancelLoops()

	start := time.Now()
	ready.Store(false)
	drain := time.NewTimer(cfg.ReadyDrain)
	<-drain.C

	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownGrace-cfg.ReadyDrain)
	defer cancel()
	for _, l := range lns {
		if err := l.srv.Shutdown(sctx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shutdown %s server: %w", l.name, err))
		}
	}
	wg.Wait()
	if err := tp.Shutdown(sctx); err != nil { // flush spans after the last request finished
		runErr = errors.Join(runErr, fmt.Errorf("shutdown tracer provider: %w", err))
	}
	log.InfoContext(ctx, "role stopped", slog.Duration("shutdown", time.Since(start)))
	return runErr
}

// publicHandler is the gateway's public surface (the edge); the admin role uses adminHandler.
func publicHandler(role Role, dp *dataPlane) http.Handler {
	if role == Gateway && dp != nil && dp.edge != nil {
		return dp.edge.Handler()
	}
	return http.NotFoundHandler()
}

// registerConfigSecrets adds the secret values of cfg to the redaction set (SECURITY.md §3): the
// Postgres password of the DSN and the audit mask key. Both are Secret-typed, so they already
// log as "***" when logged as config; this catches them inside error strings and payloads too.
func registerConfigSecrets(secrets *telemetry.SecretSet, cfg Config) {
	if u, err := url.Parse(string(cfg.PGDSN)); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			secrets.Add(pw)
		}
	}
	secrets.Add(string(cfg.AuditMaskKey))
}
