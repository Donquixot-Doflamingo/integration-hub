// Command mockvendor is the programmable fake vendor used by tests and local dev
// (DESIGN §19 M0, contracts/openapi/mockvendor.v1.yaml). For local and test use only: it has
// no auth and must never run next to production traffic.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/testkit/mockvendor"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	env := func(k, def string) string {
		if v := getenv(k); v != "" {
			return v
		}
		return def
	}
	fs := flag.NewFlagSet("mockvendor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", env("MOCKVENDOR_ADDR", "127.0.0.1:8099"), "vendor surface (control API also served here)")
	controlAddr := fs.String("control-addr", env("MOCKVENDOR_CONTROL_ADDR", "127.0.0.1:8098"), "dedicated control API listener (stays up while the vendor listener refuses)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil)).With(slog.String("service", "mockvendor"))
	inst, err := mockvendor.Listen(ctx, mockvendor.New(log), *addr, *controlAddr)
	if err != nil {
		fmt.Fprintln(stderr, "mockvendor:", err)
		return 1
	}
	log.InfoContext(ctx, "mockvendor listening", slog.String("vendor_url", inst.VendorURL), slog.String("control_url", inst.ControlURL))
	code := 0
	select {
	case <-ctx.Done():
	case err := <-inst.Err():
		log.ErrorContext(ctx, "serve failed", slog.String("error", err.Error()))
		code = 1
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := inst.Close(sctx); err != nil {
		log.ErrorContext(ctx, "shutdown", slog.String("error", err.Error()))
		code = 1
	}
	return code
}
