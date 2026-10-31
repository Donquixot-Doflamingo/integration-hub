---
name: go-debugging
description: "Systematic debugging for the Integration Hub Go services - reproduce with mockvendor, isolate, Delve (dlv test, dlv attach, goroutine breakpoints), reading race detector output, goroutine dumps for hangs and deadlocks, GODEBUG and GOTRACEBACK knobs, httptrace for upstream latency breakdown, the per-request audit timeline, Postgres (EXPLAIN ANALYZE, SKIP LOCKED lock waits) and Redis (SLOWLOG, MONITOR caution) debugging, plus Hub-specific failure signatures. Use whenever a test fails mysteriously or flakes, a request returns the wrong canonical code, something hangs, a race is reported, requests are stuck in the outbox, or production behaviour differs from expectations."
---

# Debugging the Hub

## Method (do it in this order)
1. **Write down the symptom precisely**: request id, integration id, mode, canonical code, expected vs actual, first-seen time, which role/pod.
2. **Read the evidence before touching code**: the request's audit timeline, its attempts, logs by `request_id`/`trace_id`, the trace, breaker state, snapshot version on the pod. Most Hub bugs are visible there.
3. **Reproduce locally** with `deploy/docker-compose.yml` + `cmd/mockvendor` programmed to the vendor behaviour seen (status, latency, 429 + Retry-After, malformed body, connection refused). **Never** reproduce against a real vendor.
4. **Shrink to a failing test** (unit if possible, else `//go:build integration`). The test is the deliverable as much as the fix.
5. **Isolate**: bisect stages (validate → resolve → transform → upstream → map) by calling them directly; `git bisect run go test ./... -run TestX` for regressions.
6. **Fix the cause, not the symptom**; keep the test; check the failure-signature table below for siblings of the same bug.

## Audit timeline first
Every request has events (DESIGN §13.1) and attempts (`request_attempts`). Read them via the admin API (`/admin/v1/requests/{id}` timeline, once implemented) or SQL:
```sql
SELECT seq, type, at, bytes, sha256 FROM request_events WHERE request_id = $1 ORDER BY seq;
SELECT attempt_no, integration_version, account_id, vendor_status, duration_ms, error_code
FROM request_attempts WHERE request_id = $1 ORDER BY attempt_no;
SELECT status, attempt, next_attempt_at, expires_at, claimed_by, claimed_until FROM outbox WHERE request_id = $1;
```
(Partitioned tables: include the day bound if you know it, so only one partition is scanned.) Questions it answers: which version/account served it, what exact vendor request was sent (secrets redacted), what the vendor returned, which mapping rule fired, how long each wait took.

## Delve
```bash
go install github.com/go-delve/delve/cmd/dlv@latest
dlv test ./internal/delivery -- -test.run 'TestWorker/retry_on_503' -test.v
dlv debug ./cmd/hub -- gateway --http-addr :8080
dlv attach $(pgrep -f 'hub worker')          # local only; needs a binary built with -gcflags=all="-N -l" for good variables
```
Useful commands: `break internal/delivery/worker.go:120`, `condition 1 env.RequestID == "req_01J9..."`, `continue`, `goroutines` (list), `goroutines -t` (with stacks), `goroutine 42 bt`, `frame 3 locals`, `print env`, `stack`, `on 1 print attempt` (print on every hit). A conditional breakpoint on the request id is the fastest way into one request among many.

Never attach Delve to a production pod; use dumps and profiles there.

## Race detector
`go test -race` output has two stacks: the current access and the previous conflicting access, plus where each goroutine was created. Read **both creation stacks** — they tell you which two loops share the data. Typical Hub races: mutating a snapshot after publish, a limiter/breaker map written by the sync loop while read on the request path without the atomic swap, shared `bytes.Buffer` from a pool returned twice, a test helper writing a captured variable from a handler goroutine. Fix by ownership (copy-on-write + `atomic.Pointer`, or confine to one goroutine), not by sprinkling mutexes.

## Hangs and deadlocks
- Test hung: `go test -timeout 60s` prints all goroutine stacks on timeout. Or send `SIGQUIT` (Ctrl-\) to a running binary to dump goroutines and exit.
- Running pod: `curl -s 'localhost:9090/debug/pprof/goroutine?debug=2'` on the internal port — shows each goroutine's wait reason and minutes blocked. Look for many goroutines in `semacquire` (bulkhead/WaitGroup), `chan send` (full bounded channel with a dead consumer), `select` with no ctx case, `sync.Mutex.Lock` (lock held across I/O).
- "all goroutines are asleep - deadlock!" in tests: an unbuffered channel send without a receiver, or `wg.Wait()` before `wg.Go`/`Add`.

## Runtime knobs
| Variable | Use |
| --- | --- |
| `GOTRACEBACK=all` (default single) | panic dumps all goroutines; `crash` adds a core dump; set `all` in prod |
| `GODEBUG=gctrace=1` | one line per GC: heap sizes, pause, CPU % |
| `GODEBUG=schedtrace=1000,scheddetail=1` | scheduler state every second (noisy; short bursts only) |
| `GODEBUG=http2debug=1` | HTTP/2 frame logging to vendors (may print headers: **local only**, never in prod since headers carry credentials) |
| `GODEBUG=netdns=go+1` | which DNS resolver and lookups |
| `GODEBUG=inittrace=1` | slow package init |
| `GOMEMLIMIT`, `GOGC` | see `go-memory-performance` |

## Upstream latency breakdown (httptrace)
When `vendor_latency_ms` is high, find out whether it's DNS, connect, TLS, pool wait or the vendor's server time:
```go
var t0, gotConn, firstByte time.Time
var reused bool
trace := &httptrace.ClientTrace{
	GetConn:              func(string) { t0 = time.Now() },
	GotConn:              func(i httptrace.GotConnInfo) { gotConn = time.Now(); reused = i.Reused },
	GotFirstResponseByte: func() { firstByte = time.Now() },
}
req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
// after Do: pool+dial+TLS = gotConn.Sub(t0); server time ≈ firstByte.Sub(gotConn); reused tells if the pool works
```
Also available: `DNSStart/DNSDone`, `ConnectStart/ConnectDone`, `TLSHandshakeStart/TLSHandshakeDone`, `WroteRequest`. The audit `vendor.sent` event records conn reuse and waits (§13.1), so production cases often don't need code changes. `reused=false` on most calls → transport per request, bodies not drained, or `MaxIdleConnsPerHost` too low.

## Postgres
- Plans: `EXPLAIN (ANALYZE, BUFFERS) <query>`. `ANALYZE` **executes** the statement: wrap UPDATE/DELETE in `BEGIN; ... ROLLBACK;`, and never run it on the prod primary for a claim query.
- Claim query slow or skipping everything: check it uses the partial index on `(status, next_attempt_at)`; a seq scan on `outbox` means stats are stale or the predicate doesn't match the index.
- Lock waits:
```sql
SELECT pid, wait_event_type, wait_event, state, now() - xact_start AS xact_age, left(query, 120)
FROM pg_stat_activity WHERE datname = current_database() AND state <> 'idle' ORDER BY xact_age DESC;
SELECT pid, pg_blocking_pids(pid) AS blocked_by, left(query, 80) FROM pg_stat_activity WHERE cardinality(pg_blocking_pids(pid)) > 0;
```
`SKIP LOCKED` claims never wait on row locks; if claimers show `Lock` waits, something else holds a table-level lock (migration, `VACUUM FULL`, `LOCK TABLE`) or a long transaction keeps rows locked (a transaction spanning a vendor call — a bug). `idle in transaction` sessions with old `xact_age` are the usual culprit.
- Advisory lock holder: `SELECT * FROM pg_locks WHERE locktype = 'advisory';`.

## Redis
- `SLOWLOG GET 20`, `LATENCY DOCTOR`, `INFO stats|memory|clients`, `redis-cli --bigkeys` (scans; off-peak).
- `MONITOR` streams **every command including values** (idempotency payloads, possibly sensitive) and costs throughput: local/dev only, never in prod.
- Inspect a breaker or bucket: `HGETALL hub:breaker:{acct}:{endpoint}`; TTLs with `PTTL key`.

## Hub failure signatures
See [references/signatures.md](references/signatures.md) for symptom → likely cause → where to look.
