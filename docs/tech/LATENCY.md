# Sync latency budget (HUB-413)

The user's requirement: *the caller calls the Hub and the Hub calls the vendor. If the vendor takes 100 ms, the Hub must not turn that into 200 ms; the maximum is 110 ms. On a sync call no latency is added; anything that can be async is async.*

This page says what the budget is, how it is measured and run, what I/O is still on the sync path and why, and the latest numbers. Related: DESIGN §1 (success metrics), §18.5 (hot-path rules), §18.6 (per-stage budget), `docs/tech/LOAD_TEST_REPORT.md` (throughput and the knee), ADR-0021 (account limiter leases).

## 1. The budget

For a vendor that answers in V ms, measured by a caller over HTTP with a 4 KB body on a warm node:

| Bound | Value | At V = 100 ms |
| --- | --- | --- |
| Caller p50 through the Hub | ≤ V + 2 ms | ≤ 102 ms |
| Caller p99 through the Hub | ≤ V + 10 ms | ≤ 110 ms |
| Hub adds, p50 (through the Hub − same call direct to the vendor) | ≤ 2 ms | ≤ 2 ms |
| Hub adds, p99 | ≤ 10 ms | ≤ 10 ms |

The first two bounds take the vendor's nominal latency (mockvendor `fixed_ms`), the last two the latency a caller measures calling mockvendor directly in the same run. DESIGN §1 keeps its stricter server-side target (Hub-added p99 ≤ 5 ms, vendor time excluded), measured by the `hub_overhead_ms` metric and the `HubOverheadP99High` alert. The gate here is the caller's view, end to end.

Scope: **sync** mode, with and without an `Idempotency-Key`. **Wait** mode (the D3 default) is measured and reported but not gated, see §4.

## 2. How it is measured

`backend/internal/edge/latency_budget_test.go`, `TestLatencyBudget` (build tags `integration latency`):

- **Stack.** The gateway wired as `cmd/hub` wires it (`roles/dataplane.go`): API-key auth, tenant limit, body parse, JSON Schema validation, resolve from the snapshot, transform, warm credential cache (with its background refresher), upstream client, Redis-shared breakers, the leasing account limiter (quota raised so it never has to wait), idempotency store on Redis, audit writer on Postgres with masked payloads (the dev config's policy), metrics, the OTel HTTP handler, outbox queueing for wait mode. Served by a real loopback HTTP server; the caller is a keep-alive `http.Client`.
- **Vendor.** In-process mockvendor, route `POST /1/upload`, fixed latency V (default 100 ms).
- **Pairs.** Every sample is a pair: one call through the Hub, then the same body straight to mockvendor. Slow moments of the machine hit both sides alike, and "Hub adds" is the difference of the quantiles of the two sides.
- **Phases.** Warm-up (40 pairs over the three scenarios, plus one round of concurrent connections, then a GC), then measured: 150 sequential pairs, 8 concurrent callers × 30 pairs (together the `sync` scenario, 390 samples), 100 sequential pairs with a fresh `Idempotency-Key` each (`sync+idempotency_key`), 100 pairs on a wait-mode integration (`wait_mode`, reported only).
- **Quantiles.** Nearest rank over all samples of a scenario.
- **Stall guard (2-CPU runners).** A goroutine sleeps 1 ms in a loop for the whole measurement and records how late it wakes. If it ever wakes more than 20 ms late, the process was frozen (another process had the CPUs, or a long GC); every call in flight then waits, direct ones included, and a 10 ms budget cannot be measured through it. That attempt is logged as `(discarded)`, the test waits for the load average to drop below the CPU count (≤ 90 s) and measures again, up to 3 attempts; with no clean attempt the gate **fails**. A clean attempt is always judged against the full budget: nothing is relaxed on a slow machine.
- **Diagnostics.** Each attempt logs a `DIAG` line: the Hub's own time from the audit ledger (`hub_overhead_ms` of each sync request), how late the 1 ms sleeper woke, GC cycles and pause, load average.
- **No `-race`.** The race detector multiplies CPU time; it would be what the gate measures. The gate has its own make target and CI job; the regular `make test` / `make itest` keep `-race` and exclude it (build tag `latency`).

## 3. How to run it

```sh
cd backend
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub?sslmode=disable' HUB_REDIS_ADDR=localhost:6379
make up                                   # or any Postgres 16 + Redis 7
make latency-budget                       # V = 100 ms, about 90 s
LATENCY_VENDOR_MS=0,50,100,200 make latency-budget   # the table in §5
```

CI: job `latency-budget` in `.github/workflows/backend.yml` (compose Postgres + Redis, then `make latency-budget`). Output lines: `LATENCY vendor=… scenario=… direct p50/p95/p99=… hub=… added=…` per scenario and the `DIAG` line per attempt.

## 4. I/O left on the sync path, and why

| I/O | When | Why it stays | Cost |
| --- | --- | --- | --- |
| The vendor call | every request | it is the request | V |
| Redis `Begin` (idempotency claim) | only with an `Idempotency-Key` | DESIGN §18.5 rule 6: the one allowed non-vendor call; it is what stops a duplicate reaching the vendor twice | one Redis round trip |
| Redis `Complete` / `Release` | only with an `Idempotency-Key`, after the vendor answered, before the response is flushed | the response is stored before the caller (or a duplicate right after it) can see it, so a retry gets the replay instead of a 409 `in_progress` (edge `invoke`, `idemClaim.settle`) | one Redis round trip |
| Log line to stdout | failed requests only (`request failed`) | operators need it; successful requests log nothing | a `write(2)` |

Off the sync path (proved by tests):

| Work | Where it runs | Proof |
| --- | --- | --- |
| Audit ledger and trail writes | `audit.Writer` goroutine; the request does a non-blocking channel send and drops (counted) when the buffer is full | `TestSyncRequestDoesNotWaitForAudit`, `audit.TestEmitNeverBlocksOnStalledStore`, `TestE2ENoDatabaseOnTheRequestPath` (itest) |
| Account limiter Redis calls | background lease refills (ADR-0021) | `TestSyncPathMakesNoRedisCallPerRequest`, `TestLimiterSteadyStateMakesNoRedisCallOnTheRequest` |
| Breaker state sharing | local state machine; transitions published and peers' adopted by `Breakers.Run` | `upstream` breaker tests |
| Credential loading and decryption | warm cache, refreshed in the background; a miss never calls the loader | `creds` cache tests |
| Async and async_callback delivery | worker, after a 202 | `TestAsyncModesAcceptWithoutWaitingForTheVendor` |
| Callback delivery | callback dispatcher, from the outbox | `TestCallbackDeliveryIsNotOnTheCallersRequest` |
| Traces | OTel batch span processor | — |

CPU work that is still on the request goroutine but could move: audit **masking** re-parses and re-marshals every trail payload on the caller's goroutine. It is about half of the Hub's own CPU per request in the gate's profile and is filed as HUB-409.

**Wait mode** (the D3 default) is not sync: it persists the request in Postgres before the vendor call (so the request survives a crash and can fall back to 202) and settles it after, two transactions on the caller's path. A caller still waits for the answer, so to the caller it looks like a sync call; see §5 for what that costs. Integrations that need the sync budget should use `mode: sync`.

## 5. Current numbers

2026-11-07, branch `perf/HUB-413-latency-budget` (HUB-408 limiter leases included), shared 2-CPU / 7 GB container that also ran other workstreams' builds and tests (load average 0.5 to 5 during the runs). Milliseconds, p50 / p95 / p99. "Clean" means the stall guard passed; rows marked *stalled* are the best attempt of a run in which every attempt saw the process frozen for more than 20 ms (value in brackets), shown for completeness.

**Sync, no `Idempotency-Key`** (390 samples: 150 sequential + 8 × 30 concurrent)

| Vendor | Direct | Through the Hub | Hub adds | Hub own time (ledger) | Run |
| --- | --- | --- | --- | --- | --- |
| 0 ms | 0.3 / 3.3 / 5.4 | 1.9 / 7.7 / 12.8 | 1.5 / 4.4 / 7.5 | 0.6 / 1.5 / 3.2 | clean |
| 50 ms | 50.8 / 51.5 / 52.1 | 52.1 / 54.7 / 56.4 | 1.3 / 3.2 / 4.2 | 0.8 / 1.6 / 2.3 | clean |
| 100 ms | 100.9 / 102.3 / 104.1 | 102.1 / 106.7 / 109.6 | 1.2 / 4.4 / 5.5 | 0.8 / 2.3 / 4.5 | clean |
| 200 ms | 200.8 / 202.1 / 203.5 | 201.9 / 205.0 / 207.8 | 1.1 / 2.9 / 4.3 | 0.8 / 2.9 / 5.4 | *stalled* (46 ms) |

**Sync with a fresh `Idempotency-Key`** (100 sequential samples)

| Vendor | Direct | Through the Hub | Hub adds | Run |
| --- | --- | --- | --- | --- |
| 0 ms | 0.1 / 0.2 / 0.3 | 1.1 / 3.8 / 4.6 | 1.0 / 3.6 / 4.3 | clean |
| 50 ms | 50.9 / 51.5 / 51.9 | 52.9 / 54.0 / 55.5 | 2.0 / 2.5 / 3.6 | clean |
| 100 ms | 100.8 / 101.4 / 101.7 | 102.9 / 106.0 / 106.3 | 2.0 / 4.6 / 4.6 | clean |
| 200 ms | 200.7 / 201.2 / 201.7 | 202.8 / 206.7 / 209.9 | 2.1 / 5.4 / 8.2 | *stalled* (46 ms) |

**Wait mode** (D3 default; 100 sequential samples; reported, not gated)

| Vendor | Direct | Through the Hub | Hub adds | Run |
| --- | --- | --- | --- | --- |
| 0 ms | 0.1 / 0.2 / 1.8 | 3.2 / 4.3 / 7.6 | 3.1 / 4.1 / 5.8 | clean |
| 50 ms | 50.9 / 51.4 / 51.5 | 56.0 / 58.7 / 60.8 | 5.1 / 7.3 / 9.3 | clean |
| 100 ms | 100.8 / 101.5 / 103.1 | 106.1 / 110.8 / 114.1 | 5.3 / 9.4 / 11.0 | clean |
| 200 ms | 200.8 / 201.7 / 203.6 | 206.1 / 212.2 / 218.6 | 5.3 / 10.5 / 15.0 | *stalled* (46 ms) |

What the numbers say:

- **Never "100 becomes 200".** At a 100 ms vendor a sync call through the Hub takes about 102 ms typically and 106 to 110 ms for the slowest 1 in 100; the Hub's share does not grow with the vendor's latency. The p99 bounds (≤ V + 10 ms, adds ≤ 10 ms) hold in every clean run.
- **The p50 bound does not hold yet**, by 0.1 ms without a key and about 0.9 ms with one. Two reasons:
  1. mockvendor at `fixed_ms: 100` answers a direct caller at 100.8 to 100.9 ms p50 on this machine (the wake-up after a 100 ms idle sleep; at 0 ms the direct p50 is 0.1 to 0.3 ms). That leaves the Hub 1.1 ms of the 2 ms p50 allowance.
  2. The Hub adds 1.1 to 1.5 ms p50 (2.0 ms with a key). About half of the Hub's own CPU is audit masking on the request goroutine (HUB-409). An experiment with the sync integration's audit policy set to `full` (not committed) gave own time p50 0.4 ms instead of 0.8 ms and sync 101.8 ms p50 (passes); with a key it was still 102.5 ms because the key costs two Redis round trips before the response (§4).
- **Wait mode adds about 5.5 ms p50 and 10 to 15 ms p99**: two Postgres transactions on the caller's path. It is outside this budget by design today (§4).

## 6. Gate status

`make latency-budget` is **red** on this branch: the p50 bound fails (above) and, on the shared container, most attempts are discarded by the stall guard. It turns green when (a) HUB-409 takes masking off the request goroutine and (b) for keyed calls, either the idempotency `Complete` moves after the response is flushed (a semantic change: a duplicate in that window would get 409 `in_progress` instead of the replay; needs a decision and an ADR) or keyed calls are reported rather than gated. Whether the absolute p50 bound should use the vendor's nominal latency or the direct-call p50 of the same run is also open: with the direct p50 as the reference the bound is the same as "Hub adds ≤ 2 ms p50", which already holds without a key.
