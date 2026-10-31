# Leak and latency runbook

## A. Memory grows over time
1. **Confirm it is a leak, not load.** Plot `/memory/classes/heap/objects:bytes` against request rate. A rising *floor* after GC at flat load is a leak; growth that tracks traffic and returns is not.
2. **Check goroutines first** (`/sched/goroutines:goroutines`). Most Hub "memory leaks" are goroutine leaks holding buffers. If goroutines grow → section B.
3. **Take two heap profiles 10–30 min apart** from the same pod via the internal port:
   ```bash
   curl -s localhost:9090/debug/pprof/heap > heap1.pb.gz
   # wait
   curl -s localhost:9090/debug/pprof/heap > heap2.pb.gz
   go tool pprof -sample_index=inuse_space -base heap1.pb.gz -top heap2.pb.gz
   ```
4. **Read the growing frames.** Usual suspects here:
   - Maps keyed by something unbounded: limiter/bulkhead/breaker maps keyed by an id that never gets deleted (accounts retired, integrations removed) — prune on snapshot swap.
   - Old snapshots kept alive: something stored a `*Snapshot` (cache, closure, long-lived goroutine) instead of loading per request. Look for multiple snapshot versions' compiled programs in the profile.
   - Credential cache without TTL eviction.
   - Pool with no size cap holding multi-MB buffers (LLM bodies).
   - Audit buffer backing slices retained after drops.
5. **Fix, add a regression test** (a unit test that churns snapshot swaps / ids and asserts the map size or uses `testing.AllocsPerRun`), redeploy, re-check the floor.

## B. Goroutines grow
1. Dump: `curl -s 'localhost:9090/debug/pprof/goroutine?debug=1' > g.txt` (grouped by stack with counts). `debug=2` gives every goroutine with wait reason and duration.
2. Sort by count; the leaking stack has a count that grows between two dumps. Note the wait reason: `chan send`, `chan receive`, `select`, `IO wait`, `semacquire`, `sync.Mutex.Lock`.
3. Map to the leak table in SKILL.md:
   - `chan send` in a result-returning helper → unread result channel after caller timeout.
   - `select` in a loop with a ticker → missing ctx case or never-cancelled ctx.
   - `IO wait` in `net/http.(*persistConn).readLoop` growing → response bodies not closed, or a new `http.Transport` per request.
   - `semacquire` → WaitGroup/semaphore never released on an error path.
4. Reproduce in a test with `goleak.VerifyNone(t)` around the suspected code path; fix; keep the test.

## C. p99 latency / hub_overhead_ms regression
1. **Locate the stage.** Use the per-stage breakdown (`hub_stage_duration_ms{stage}`) — which middleware moved? If none moved but total did, suspect GC or scheduling.
2. **Exemplar → trace.** Open a slow-bucket exemplar of `hub_overhead_ms` to get a trace id; check which span is slow (`go-observability`).
3. **CPU profile** under realistic load (30 s): `go tool pprof -http=:0 'localhost:9090/debug/pprof/profile?seconds=30'`. Look at the flame graph under the edge handler.
4. **Alloc profile**: `-sample_index=alloc_space` on a heap profile; large allocators on the request path are GC fuel.
5. **GC / scheduler**: if GC CPU > 10 % or `/sched/latencies` p99 rose, it's allocation pressure or GOMEMLIMIT too tight. `GODEBUG=gctrace=1` on one pod for a few minutes shows heap goals and pause times.
6. **Contention**: enable mutex/block profiling temporarily (flag), take `/debug/pprof/mutex` and `/debug/pprof/block`. A global lock in a limiter/breaker map on the hot path is the classic culprit → copy-on-write map behind `atomic.Pointer` or sharded locks.
7. **Execution trace** for 5 s if still unexplained: `go tool trace trace.out` → "Scheduler latency profile" and "Network blocking profile".
8. Reproduce with a benchmark, fix, show benchstat before/after, keep the benchmark.

## D. Redis/Postgres adding latency to the sync path
- Only Redis idempotency is allowed on the sync path (§18.5.6). If Redis time shows up in traces for requests *without* an `Idempotency-Key`, something else is calling Redis on the path (rate limiter without local lease, breaker read not from cache) — fix the design violation, not the latency.
- Any Postgres span under a `sync`-mode request is a bug (wait/async persist the envelope; sync must not).

## E. Before you close the incident
- [ ] Root cause stack/frame identified and linked in the ticket.
- [ ] Regression test or benchmark added.
- [ ] Metric/alert that would have caught it earlier (goroutine growth, heap floor) exists in `deploy/alerts/`.
