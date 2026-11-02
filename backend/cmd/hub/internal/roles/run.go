package roles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

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
	// Secrets is the process's redaction set (the logger's); decrypted credentials are added to
	// it. nil means a fresh set.
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
		secrets := opts.Secrets
		if secrets == nil {
			secrets = telemetry.NewSecretSet()
		}
		if dp, err = newDataPlane(ctx, cfg, metrics, log, secrets, tp, st, reg); err != nil {
			_ = tp.Shutdown(ctx)
			return err
		}
		dp.start(ctx)
		defer dp.shutdown() // after the listeners closed (deferred calls run before st.close)
	}
	reg.load(ctx)
	checks := append(append(st.checks(), reg.check()), opts.ExtraChecks...)

	// ops is last so it is closed last: /readyz answers 503 while the public listener drains.
	endpoints := []endpoint{{"ops", cfg.OpsAddr, opsHandler(cfg.Role, &ready, checks, metrics, log)}}
	if cfg.Role.servesPublicHTTP() {
		h := publicHandler(cfg.Role, dp)
		if cfg.Role == Admin {
			if h, err = adminHandler(ctx, cfg, metrics, log, st, reg); err != nil {
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
		lns = append(lns, listener{name: h.name, srv: srv, ln: ln})
		if opts.OnListen != nil {
			opts.OnListen(h.name, ln.Addr().String())
		}
	}

	serveErr := make(chan error, len(lns))
	var wg sync.WaitGroup
	if cfg.ConfigWatch {
		wg.Go(func() { reg.watch(ctx) })
	}
	wg.Go(func() { reg.follow(ctx) }) // every role follows published snapshots (HUB-171)
	if cfg.Role == Scheduler {
		wg.Go(func() { reg.pruneLoop(ctx) })
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
