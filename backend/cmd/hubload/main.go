// Command hubload drives a running Hub (gateway + worker + scheduler) and mockvendor for the M8
// load and soak tests (DESIGN §19 M8, OBSERVABILITY §12, TESTING_STRATEGY §10). It is a test
// tool: it never runs next to production traffic and it only ever targets mockvendor.
//
//	hubload rate    open-loop constant-arrival run (sync, async or direct-to-mockvendor)
//	hubload soak    async traffic with random mockvendor outages and worker restarts, then
//	                reconciles accepted requests against hub.requests and received callbacks
//	hubload report  renders the JSON results of a run directory as Markdown tables
//	hubload settle  waits until the 1-minute load average drops below a threshold
//
// Latency is measured from each request's intended send time (open loop), so a stalled server
// shows up as latency instead of silently lowering the request rate (coordinated omission).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage: hubload <command> [flags]

commands:
  rate     open-loop run at a constant arrival rate (-mode sync|async|direct)
  soak     async soak with random mockvendor outages, then zero-loss reconciliation
  report   render a results directory as Markdown
  settle   wait until the 1-minute load average is below -below

Run "hubload <command> -h" for flags. The API key comes from HUBLOAD_API_KEY only.`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "rate":
		return rateCmd(ctx, args[1:], getenv, stdout, stderr)
	case "soak":
		return soakCmd(ctx, args[1:], getenv, stdout, stderr)
	case "report":
		return reportCmd(ctx, args[1:], stdout, stderr)
	case "settle":
		return settleCmd(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
}
