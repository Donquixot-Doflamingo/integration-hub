---
name: go-memory-performance
description: "Find and prevent memory leaks, goroutine leaks and latency regressions in the Integration Hub Go services - pprof on the internal port, bench with -benchmem and profiles, escape analysis, sync.Pool for buffers, allocation-free hot path (DESIGN 18.5/18.6), GOMEMLIMIT and GOGC, runtime/metrics, goroutine leak patterns, benchstat regression gating, runtime/trace, plus a step-by-step leak runbook. Use whenever memory or goroutine counts grow, p99 or hub_overhead_ms regresses, a benchmark budget fails, you touch the sync request path, or you are optimising allocations."
---

# Memory and performance

Budgets (DESIGN §18.6): Hub overhead ≤ 5 ms p99 sync (≈ 3.4 ms planned), transform ≤ 0.5 ms p99 / 4 KB, middleware < 100 µs p99 each, async accept ≤ 10 ms p99. Measure before optimising; keep the profile that justified a change in the PR description.

## Hot-path rules (DESIGN §18.5) in practice
- One `json` decode in, one encode out. Transforms operate on the parsed tree; never marshal → unmarshal between stages. Carry bodies as `json.RawMessage` where untouched.
- No per-request: regex compile, schema compile, expression compile, `fmt.Sprintf` for keys (use `strings.Builder` or precomputed keys on the snapshot), `time.Format` in logs, map allocation for headers you could look up, closures capturing large state, `defer` in tight loops.
- Snapshot pointer loaded once per request. Precompute everything per integration at snapshot build (route tables, header allowlists, metric attribute sets).
- Metrics: pre-build `attribute.Set`/`metric.WithAttributeSet` per (tenant, integration, …) combination at snapshot build instead of building attributes per request.
- Audit: non-blocking send into a bounded channel; drop + counter when full.
- Logging on the hot path: only behind `Enabled` checks or sampling (`go-logging`).

## sync.Pool: when and how
Use for short-lived, frequently allocated, size-bounded buffers: request body read buffers, JSON encode buffers, vendor request builders.
```go
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }} // package-level pool is fine: no shared state semantics

func getBuf() *bytes.Buffer { b := bufPool.Get().(*bytes.Buffer); b.Reset(); return b }

func putBuf(b *bytes.Buffer) {
	if b.Cap() > 64<<10 { // don't let one 4 MB LLM body pin memory forever
		return
	}
	bufPool.Put(b)
}
```
Do not pool: objects that escape the request (stored in audit channel, returned to caller), objects with complex reset logic, anything where a missed reset leaks data across tenants (a reset bug here is a security bug). Measure: a pool that doesn't move `allocs/op` is complexity for nothing.

## Benchmark and profile workflow
```bash
cd backend
go test -run='^$' -bench=BenchmarkTransform4KB -benchmem -count=10 ./internal/transform | tee new.txt
git stash && go test -run='^$' -bench=BenchmarkTransform4KB -benchmem -count=10 ./internal/transform > old.txt; git stash pop
benchstat old.txt new.txt          # golang.org/x/perf/cmd/benchstat
go test -run='^$' -bench=BenchmarkTransform4KB -cpuprofile cpu.out -memprofile mem.out ./internal/transform
go tool pprof -http=:0 cpu.out
go tool pprof -sample_index=alloc_space -http=:0 mem.out
```
- Regression gate: CI runs the hot-path benchmarks with `-count=10` against `main`; a statistically significant (benchstat p < 0.05) regression > 10 % in `sec/op` or any increase in `allocs/op` on hot-path benches fails the job unless the PR explains it.
- Also keep the p99 budget tests (`go-testing`, `//go:build perf`).

## Escape analysis
```bash
go build -gcflags='-m' ./internal/transform 2>&1 | grep -E 'escapes|moved to heap'
go build -gcflags='-m -m' ./internal/edge 2>&1 | less   # reasons
```
Common causes on our path: storing a pointer in an interface (`any`, `slog.Any`), closures captured by goroutines, slices whose size is not constant, returning pointers to locals. Fix only where a profile shows it matters.

## Runtime tuning
- Go 1.25 sets `GOMAXPROCS` from the container CPU limit automatically; do not add automaxprocs.
- Set `GOMEMLIMIT` to ~85–90 % of the container memory limit (env in the k8s manifest), keep `GOGC=100` unless profiles say otherwise. A soft limit avoids OOM kills from GC lag; it does not fix a leak (a leak under GOMEMLIMIT shows as GC CPU climbing to the cap).
- Never `runtime.GC()` / `debug.FreeOSMemory()` in production code.

## What to watch (runtime/metrics, exported via OTel runtime instrumentation)
| Metric | Meaning | Smells |
| --- | --- | --- |
| `/sched/goroutines:goroutines` | live goroutines | grows with uptime, not with load → leak |
| `/memory/classes/heap/objects:bytes` | live heap | sawtooth OK; rising floor → leak |
| `/gc/heap/allocs:bytes` (rate) | allocation rate | jumps after a change → hot-path alloc |
| `/sched/latencies:seconds` | runnable→running wait | high p99 → CPU starvation / too many goroutines |
| `/cpu/classes/gc/total:cpu-seconds` | GC CPU | > 10–15 % → alloc pressure or near GOMEMLIMIT |
| `/sync/mutex/wait/total:seconds` | mutex contention | rising → lock on the hot path |
Plus Hub metrics: `hub_overhead_ms` per stage, `audit_events_dropped_total`, `queue_depth`.

## pprof on the internal port only
Never import `net/http/pprof` for its side effect (it registers on `http.DefaultServeMux`). Mount explicitly on the internal mux bound to `HUB_INTERNAL_ADDR` (not exposed by the Service/Ingress):
```go
mux := http.NewServeMux()
mux.HandleFunc("/debug/pprof/", pprof.Index) // serves heap, goroutine, allocs, block, mutex, threadcreate
mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
mux.Handle("/metrics", promHandler)
internal := &http.Server{Addr: cfg.InternalAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
```
Block and mutex profiles are empty unless enabled: `runtime.SetBlockProfileRate(n)` / `runtime.SetMutexProfileFraction(n)` — enable via a flag, only while investigating (they cost CPU).
```bash
kubectl port-forward pod/hub-gateway-x 9090:9090
go tool pprof -http=:0 http://localhost:9090/debug/pprof/heap
go tool pprof -http=:0 'http://localhost:9090/debug/pprof/profile?seconds=30'
curl -s 'http://localhost:9090/debug/pprof/goroutine?debug=2' > goroutines.txt
curl -s 'http://localhost:9090/debug/pprof/trace?seconds=5' > trace.out && go tool trace trace.out
```
Heap diff between two points in time: `go tool pprof -base heap1.pb.gz heap2.pb.gz`.

## Goroutine leak patterns (and fixes)
| Pattern | Fix |
| --- | --- |
| Goroutine per request/item with no bound | Worker pool / semaphore; bounded channel |
| `ctx, cancel := context.WithTimeout(...)` without `cancel()` | `defer cancel()` on the next line (vet `lostcancel`) |
| Send on a channel nobody reads after the caller returned (timeouts) | Buffered channel of size 1 for the result, or select on `ctx.Done()` in the sender |
| `time.NewTicker` in a loop goroutine without `Stop` / exit path | `defer t.Stop()`, select on `ctx.Done()` |
| `resp.Body` not closed / not drained | `defer resp.Body.Close()`; `io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))` before close for reuse |
| Blocking read on a Redis pub/sub or long-poll without ctx | Pass ctx; close the subscription on shutdown |
| `errgroup`/`WaitGroup` goroutine stuck because a sibling failed | Derive ctx with cancel; every goroutine selects on it |
| `http.Client` without timeout to callbacks/vendors | Per-endpoint timeouts + ctx deadlines; transport `ResponseHeaderTimeout` |
`goleak.VerifyTestMain` catches most of these in tests; production uses the goroutine metric + runbook.

## Execution traces
`runtime/trace` (and `/debug/pprof/trace`) shows scheduling latency, GC pauses, blocking syscalls and network waits per goroutine — the tool for "p99 is high but CPU profile looks fine". Go 1.25 adds `trace.FlightRecorder` (keep the last few seconds in memory, write it out when a slow request is detected); consider it for capturing rare `hub_overhead_ms` spikes, behind a flag.

## Runbook
Step-by-step leak and latency investigation: [references/runbook.md](references/runbook.md).

## Checklist for hot-path changes
- [ ] Benchmark exists and `allocs/op` did not increase (benchstat attached).
- [ ] No new per-request compile/format/alloc in loops; profile checked.
- [ ] Every new goroutine bounded and stoppable; goleak green.
- [ ] Pooled objects reset fully and capped in size.
- [ ] New middleware stage metric present and < 100 µs p99.
