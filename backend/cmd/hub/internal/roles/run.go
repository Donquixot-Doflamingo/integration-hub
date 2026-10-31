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
)

// Options are test and wiring hooks for Run.
type Options struct {
	// OnListen is called once per listener after it is bound, with "http" or "ops" and the
	// bound address. Tests pass ":0" addresses and use it to learn the ports.
	OnListen func(name, addr string)
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
	log = log.With(slog.String("role", string(cfg.Role)))
	var ready atomic.Bool

	// ops is last so it is closed last: /readyz answers 503 while the public listener drains.
	endpoints := []endpoint{{"ops", cfg.OpsAddr, opsHandler(&ready)}}
	if cfg.Role.servesPublicHTTP() {
		endpoints = append([]endpoint{{"http", cfg.HTTPAddr, publicHandler(cfg.Role)}}, endpoints...)
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
	log.InfoContext(ctx, "role stopped", slog.Duration("shutdown", time.Since(start)))
	return runErr
}

// publicHandler is the role's public surface. Edge (gateway) and control (admin) handlers are
// mounted here by later tickets; until then every path is 404.
func publicHandler(Role) http.Handler { return http.NotFoundHandler() }

// opsHandler serves the ops port. /healthz and /readyz arrive in HUB-025, /metrics in HUB-027.
func opsHandler(*atomic.Bool) http.Handler { return http.NotFoundHandler() }
