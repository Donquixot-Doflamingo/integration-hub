// Command mockisfc runs the fake ISFC auth-service (internal/isfc/mockisfc) for local runs and
// scripts. It is a test double: it has no persistence and no real security. Never point a real
// deployment at it.
//
//	mockisfc -addr :8199 -hub-client integration-hub -hub-secret dev-secret
//
// Environment (flags win): MOCKISFC_ADDR, MOCKISFC_HUB_CLIENT, MOCKISFC_HUB_SECRET,
// MOCKISFC_CONSOLE_CLIENT, MOCKISFC_PLATFORM_KEY. Drive state through /_mock/* (see package doc).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/isfc/mockisfc"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() { os.Exit(run()) }

func run() int {
	fs := flag.NewFlagSet("mockisfc", flag.ExitOnError)
	addr := fs.String("addr", env("MOCKISFC_ADDR", "127.0.0.1:8199"), "listen address")
	hub := fs.String("hub-client", env("MOCKISFC_HUB_CLIENT", "integration-hub"), "the Hub backend client id")
	secret := fs.String("hub-secret", env("MOCKISFC_HUB_SECRET", "dev-secret"), "the Hub backend client secret")
	console := fs.String("console-client", env("MOCKISFC_CONSOLE_CLIENT", "integration-hub-console"), "the Hub console client id")
	_ = fs.Parse(os.Args[1:])

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m := mockisfc.New(mockisfc.Options{Issuer: "http://" + *addr})
	m.Seed(*hub, *secret, *console)
	srv := &http.Server{Addr: *addr, Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx) //nolint:contextcheck // a fresh context: ctx has ended
	}()
	log.InfoContext(ctx, "mockisfc listening (test double, no real security)", slog.String("addr", *addr), slog.String("hub_client", *hub))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "mockisfc:", err)
		return 1
	}
	return 0
}
