# Load and soak test report (M8: HUB-235, HUB-236)

| | |
| --- | --- |
| Date | 2026-11-07 |
| Code | branch `perf/HUB-235-load-soak`, merged with `main` at `93168e0` (runs 4 and 5); runs 1 to 3 on the same branch before that merge |
| Machine | 2 vCPU, 7 GB RAM Linux container, **shared** by Postgres 16, Redis 7, mockvendor, the load generator, the Hub roles and other workstreams |
| Tool | `make load` → `deploy/load/run.sh` + `backend/cmd/hubload` (ADR-0016); raw results per step as JSON, CPU profiles via `/debug/pprof` |
| Rule | Before every run, wait until the 1-minute load average is below 1.0; the value at the start of each step is in the tables |

**No real vendor was called.** This machine cannot reach third-party APIs (egress allowlist), and our tests must never call real vendors. A programmable fake vendor (`mockvendor`) stands in for the vendor and answers every call after a fixed delay that we set: 0 ms, 50 ms or 200 ms. A real vendor adds its own response time and internet latency on top, and the Hub does not change those. What these numbers show is the time the Hub itself adds.

## 1. The short answer

**How much latency does the Hub add?** For the same 4 KB call (sync mode, 100 calls per second), here is the time from sending the request to getting the full answer:

| Vendor answers in | Direct to the vendor: typical / slow 1 in 20 / slow 1 in 100 | Through the Hub: typical / 1 in 20 / 1 in 100 | **The Hub adds** | The Hub's own measurement of its time |
| --- | --- | --- | --- | --- |
| 0 ms | 0.9 / 1.4 / 1.7 ms | 2.1 / 3.1 / 4.2 ms | **1.1 / 1.7 / 2.5 ms** | 0.6 / 1.1 / 1.8 ms |
| 50 ms | 51.4 / 52.3 / 52.5 ms | 52.8 / 54.7 / 72.8 ms | **1.4 / 2.4 / 20.3 ms** ¹ | 0.6 / 1.1 / 2.0 ms |
| 200 ms | 201.4 / 202.3 / 202.6 ms | 202.6 / 204.1 / 206.0 ms | **1.3 / 1.7 / 3.5 ms** | 0.6 / 0.9 / 1.8 ms |

"Typical" is the median (p50). "Slow 1 in 20" is p95 and "slow 1 in 100" is p99. "The Hub adds" is the through-the-Hub time minus the direct time. "The Hub's own measurement" is the overhead the Hub records for each request in its audit ledger (vendor time excluded).

¹ In this run a short stall in the Hub's Redis-based rate limiter delayed about 1 % of calls by up to 50 ms. In two earlier runs of the same row the slow 1-in-100 added 3.6 ms and 3.3 ms (section 3.1). The fix is ticket HUB-408.

**In plain words:** a typical call through the Hub takes about **1 to 1.5 ms longer** than the same call made directly, and the slowest 1 in 100 calls about **2.5 to 3.5 ms longer**. That holds whether the vendor answers in 0, 50 or 200 ms, because the Hub's share does not depend on the vendor's speed. The design target is ≤ 5 ms for the slowest 1 in 100 (DESIGN §1). The one exception above (20 ms) comes from a known weak spot that we have filed.

**How did the Hub behave?**
- **Sync (caller waits):** 500 calls per second with zero errors, Hub overhead within target (p99 3.8 ms), on a 2-CPU machine that also runs the database, the fake vendor and the load generator. Above about 600 calls per second this machine runs out of CPU and the slowest calls slow down. Still no errors up to 1,000 per second.
- **Async (fire and forget):** the Hub accepts up to about **900 requests per second** here, with requests at 250 per second answered in under 10 ms (p99). One worker process delivers about **360 to 400 per second** to the vendor, so above that a backlog builds and drains after the burst. The target of 2,000 per second needs more worker capacity (HUB-407) and bigger hardware.
- **Vendor outages:** over two soaks, 42,000 accepted requests went through 5 random outages (connection refused, connection reset, HTTP 503, timeouts; 31 % of the time down). **Every request completed and none was lost.** Each got **exactly one** callback. After each outage the parked backlog (2,000 to 4,000 requests) drained within about 20 to 45 s of the vendor coming back.
- **Found while testing:** two bugs, the Hub's latency metric itself (HUB-405) and a worker crash on restart (HUB-406), plus four performance limits. All are filed (section 6).

## 2. Setup

- **Roles:** `hub gateway`, `hub worker` (1 process), `hub scheduler`, each its own process on the same host, with the real Postgres (database `hub_load`) and Redis (`:6385`). Registry: the dev config plus `deploy/load/config/`:
  - tenant `load-tenant`
  - account `ct-load`, with quotas set high so the Hub, not a quota, is measured
  - `int_load_sync` (sync), `int_load_async` (async + polling), `int_load_cb` (async_callback)
- **Vendor:** mockvendor `POST /1/upload`, fixed latency (0/50/200 ms for the comparison, 20 ms for the ramps and the soak, as in OBSERVABILITY §12 L1). Production circuit-breaker defaults (30 s window, 20 calls, 50 %, 30 s open, doubling).
- **Request:** a 4 KB `events.track@v1` body, transformed by T1@7/R1@2, audit policy `masked`.
- **Generator:** open loop. Each arrival is sent at its scheduled time whether or not earlier ones have answered, and latency is measured from the scheduled time, so a stalled Hub shows up as latency rather than as fewer requests. Arrivals beyond 4,000 in flight are counted as dropped. Comparison steps are 20 s after a 3 s warm-up (2,000 calls each); ramp steps are 20 s (sync) or 15 s (async).
- **Overhead sources** (all three are in the JSON results):
  1. *Hub adds*, measured by the client: through-the-Hub minus direct. Includes the extra local hop and the account limiter's Redis call.
  2. *Audit ledger*: `hub.requests.hub_overhead_ms` of every request in the window. Server side, vendor time excluded, and also excludes the account limiter's Redis call, which the Hub counts as a deliberate wait. The column "incl. limiter wait" uses `completed_at - created_at - vendor_ms` from the same rows.
  3. *Metric* `hub_overhead_ms`: **broken**, see 5.1. Shown only as "negative share".

## 3. Results

### 3.1 Direct vs through the Hub: detail and repeat runs

Run 4 (the table in section 1). All steps ran with the machine 3 to 13 % busy, a 1-minute load average of 0.19 to 0.78 at the start, and a generator send lag p99 under 1.2 ms:

| Vendor answers in | Calls/s | Direct p50 / p95 / p99 (ms) | Through the Hub p50 / p95 / p99 (ms) | Hub adds p50 / p95 / p99 (ms) | Audit ledger p50 / p95 / p99 (ms) | Ledger incl. limiter wait p50 / p95 / p99 (ms) | Errors |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 ms | 100 | 0.9 / 1.4 / 1.7 | 2.1 / 3.1 / 4.2 | 1.1 / 1.7 / 2.5 | 0.6 / 1.1 / 1.8 | 0.8 / 1.6 / 2.4 | 0 |
| 50 ms | 100 | 51.4 / 52.3 / 52.5 | 52.8 / 54.7 / 72.8 | 1.4 / 2.4 / 20.3 | 0.6 / 1.1 / 2.0 | 0.8 / 1.7 / 10.4 | 0 |
| 200 ms | 100 | 201.4 / 202.3 / 202.6 | 202.6 / 204.1 / 206.0 | 1.3 / 1.7 / 3.5 | 0.6 / 0.9 / 1.8 | 0.8 / 1.4 / 3.3 | 0 |

The 50 ms row's tail is the account limiter. Its stage p99 was 9.5 ms in this step, against 1.5 ms in runs 1 and 2. The gateway logged 15 `account limiter unavailable: letting traffic through` warnings within one second (each is a 50 ms Redis timeout, after which the limiter fails open).

"Hub adds" p50 / p95 / p99 in the repeat runs (same settings):

| Vendor | Run 1 | Run 2 | Run 4 |
| --- | --- | --- | --- |
| 0 ms | 1.1 / 1.7 / not usable ² | 1.0 / 3.0 / 5.0 ³ | 1.1 / 1.7 / 2.5 |
| 50 ms | 1.3 / 2.2 / 3.6 | 1.4 / 2.2 / 3.3 | 1.4 / 2.4 / 20.3 |
| 200 ms | 1.3 / 1.6 / 2.9 | 1.3 / 1.8 / 3.2 | 1.3 / 1.7 / 3.5 |

² The generator itself stalled during the direct step (send lag p99 47 ms; another workstream's CPU burners had just stopped), so the direct p99 (51 ms) is not a vendor number.
³ The machine was 86 % busy during the through-the-Hub step while our processes used about 11 % (another workstream).

### 3.2 Sync ramp (L1 shape: mockvendor 20 ms, 4 KB)

Runs 4 (100 to 500 RPS) and 5 (600 to 1,000 RPS):

| Target RPS | Achieved 2xx/s | Client p50 / p99 / max (ms) | Ledger overhead p50 / p95 / p99 (ms) | > 5 ms | Incl. limiter wait p50 / p95 / p99 (ms) | > 5 ms | Errors | CPU cores: gateway / Postgres / mockvendor / generator | Machine busy | Load avg (start) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 100 | 99.9 | 22.8 / 25.9 / 32.3 | 0.66 / 1.06 / 1.88 | 0.0 % | 0.84 / 1.62 / 3.20 | 0.1 % | 0 | 0.17 / 0.03 / 0.04 / 0.05 | 15 % | 0.28 |
| 200 | 199.8 | 22.6 / 26.3 / 31.9 | 0.59 / 1.06 / 2.30 | 0.2 % | 0.76 / 1.79 / 3.25 | 0.3 % | 0 | 0.29 / 0.04 / 0.07 / 0.08 | 25 % | 0.48 |
| 300 | 299.7 | 22.6 / 27.2 / 42.2 | 0.60 / 1.17 / 2.08 | 0.1 % | 0.77 / 1.88 / 3.38 | 0.3 % | 0 | 0.43 / 0.06 / 0.10 / 0.11 | 35 % | 0.91 |
| 400 | 399.6 | 22.7 / 29.0 / 38.4 | 0.61 / 1.36 / 2.76 | 0.2 % | 0.78 / 2.32 / 4.33 | 0.6 % | 0 | 0.56 / 0.08 / 0.12 / 0.13 | 46 % | 1.16 |
| **500** | **499.4** | 22.9 / 32.6 / 45.8 | 0.66 / 1.86 / **3.79** | 0.5 % | 0.85 / 3.32 / **6.51** | 2.1 % | 0 | 0.68 / 0.10 / 0.14 / 0.14 | 58 % | 1.50 |
| 600 | 599.4 | 24.2 / 38.9 / 63.5 | 0.81 / 2.70 / 5.44 | 1.3 % | 1.09 / 5.58 / 10.54 | 6.2 % | 0 | 0.83 / 0.14 / 0.15 / 0.15 | 67 % | 0.98 |
| 800 | 799.2 | 25.2 / 45.5 / 89.1 | 0.75 / 2.49 / 4.80 | 0.9 % | 1.12 / 6.33 / 11.76 | 8.0 % | 0 | 0.97 / 0.17 / 0.17 / 0.16 | 78 % | 1.13 |
| 1000 | 998.7 | 28.7 / 121.2 / 236.5 | 0.74 / 2.76 / 7.10 | 1.8 % | 1.47 / 13.57 / 30.93 | 19.4 % | 0 | 1.08 / 0.20 / 0.18 / 0.16 | 88 % | 2.23 |

Gateway stage p99 at 500 RPS (`hub_stage_duration_ms`, own time): **admission/wait 3.96 ms**, body_limit 0.60, response_map 0.28, transform 0.07, validate 0.07, auth 0.04, audit 0.05, resolve 0.003, rate_limit 0.002. At 1,000 RPS the admission/wait p99 is 27.5 ms.

Run 1, on the code before the merge, gave the same picture: 500 RPS, ledger p99 3.38 ms, 6.22 ms including the limiter wait, 0 errors.

**Knee:** the ledger overhead stays under 5 ms at p99 up to 500 RPS. It crosses at 600, where the gateway uses 0.83 cores and the machine is 67 % busy. Counting the limiter's Redis call, p99 already exceeds 5 ms at 500 RPS (6.5 ms), and the excess is that one stage (DESIGN §18.6 budgets 0.1 ms for it). Throughput keeps up to 1,000 RPS without errors, while the tail grows because the gateway, mockvendor (vendor p99 70 ms at 1,000 RPS), Redis and the generator compete for 2 CPUs.

### 3.3 Async ramp (L2 shape: async + polling, mockvendor 20 ms)

Run 4. Each step is 15 s after a 3 s warm-up; the queue drains (up to 100 s) before the next step.

| Target msg/s | Accepted 202/s | Accept p50 / p99 / max (ms) | Worker finished/s | Max queue depth | Max oldest age (s) | Drained after stop (s) | Generator drops | CPU cores: gateway / worker / Postgres / generator | Machine busy | Load avg (start) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 250 | 250.0 | 2.5 / **9.1** / 26.8 | 245.7 | 15 | 0.0 | 0.3 | 0 | 0.26 / 0.24 / 0.32 / 0.09 | 48 % | 1.86 |
| 500 | 500.0 | 4.1 / 292.9 / 369.1 | 378.1 | 2,244 | 4.7 | 4.8 | 0 | 0.45 / 0.33 / 0.52 / 0.12 | 78 % | 1.47 |
| 1000 | 840.6 | 2,715 / 4,157 / 4,178 | 356.7 | 9,620 | 26.2 | 23.9 | 166 | 0.69 / 0.28 / 0.60 / 0.15 | 93 % | 2.18 |
| 2000 | 959.4 | 4,084 / 4,512 / 4,584 | 357.9 | 11,502 | 31.2 | 29.1 | 13,375 | 0.73 / 0.27 / 0.65 / 0.13 | 95 % | 2.91 |

The worker's overhead per delivery (ledger) is p50 0.4 ms and p99 4 to 5.5 ms at every step. Run 2 gave the same knee: 250 msg/s accept p99 23 ms, 500 msg/s p99 39 ms, worker 354 to 404/s, at most 956 accepted per second.

**Knees:** two different limits.
- **Accepting** (gateway → outbox commit) tops out at about 850 to 950 per second. There the machine is 93 to 96 % busy: gateway 0.7 cores, Postgres 0.6, worker 0.3, generator 0.15.
- **Delivering** (worker → vendor) tops out at about 360 to 400 per second per worker process, while that worker uses only **0.25 to 0.35 cores**. It is not short of CPU: it runs at most 16 attempts at once and waits for each batch of 32 to finish (HUB-407). 16 / (20 ms vendor + about 25 ms of Hub and database time) ≈ 355/s, which matches.

At 1,000 msg/s and above, the gateway's audit writer also falls behind Postgres. `audit_events_dropped_total{reason="buffer_full"}` counted 19,158 dropped events in a 15 s step at 930 accepted/s (run 3), all in the gateway. Requests themselves are not affected (outbox rows commit before the 202), but their audit events are lost by design (§18.5 rule 3). That is HUB-411. Separately, gateway audit writes timed out during overload (three in run 1, one in run 2), and each lost its whole batch of up to 504 events (HUB-410).

### 3.4 Soak with random vendor outages (HUB-236)

Async_callback traffic at 100 msg/s, mockvendor 20 ms, one worker. A chaos loop takes the vendor down at random (healthy for 30 to 60 s, then down for 10 to 40 s). Afterwards every accepted request id (from the 202 bodies) is reconciled against `hub.requests`, and every callback received by the test receiver is counted per request.

| | Soak A (run 3) | Soak B (run 4, merged code) |
| --- | --- | --- |
| Traffic | 4 min, 24,000 accepted (all 202) | 3 min, 18,000 accepted (all 202) |
| Outages (start s, length s) | refused (33, 34), reset (116, 21), refused (179, 18) | HTTP 503 (45, 21), timeout > 3 s (117, 34) |
| Vendor down | 73 s (31 %) | 55 s (31 %) |
| Accept p50 / p99 / max | 2.5 / 10.3 / 123 ms | 2.6 / 12.4 / 183 ms |
| Peak backlog (parked + retrying + accepted) | 4,359 | 4,217 |
| Backlog back to ~0 after recovery | 33 s; 32 s (the reset outage overlapped the next one) | 44 s, 19 s |
| Reconciled after traffic stopped | 103 s | 5 s |
| **Final state of the accepted requests** | **succeeded 24,000** | **succeeded 18,000** |
| **Lost** (accepted, no `hub.requests` row) | **0** | **0** |
| Non-terminal at the end | 0 | 0 |
| Requests called back / callback rows delivered | 24,000 / 24,000 | 18,000 / 18,000 |
| **Duplicate callbacks** (more than one terminal event) | **0** | **0** |
| Redeliveries of the same event | 0 | 0 |
| Attempts per request: mean / max | 1.23 / 6 | 1.09 / 5 |
| Dead-lettered | 0 | 0 |

Totals: 42,000 accepted, 42,000 succeeded, 0 lost, 0 duplicated.

**Worker restarts are not in these soaks.** The harness has a restart hook (SIGTERM, then SIGKILL). In a trial run it exposed HUB-406: a worker restarted while a backlog exists claims envelopes before its config snapshot is loaded and panics (`delivery.(*Worker).trail`, nil snapshot). The soak runs with `SOAK_RESTARTS=0` until that is fixed.

## 4. Targets vs results

| Target (DESIGN §1, §19 M8; OBSERVABILITY §12) | Result on this machine | Met? |
| --- | --- | --- |
| Hub overhead p99 ≤ 5 ms at 500 sync RPS (HUB-235 AC 1) | Ledger p99 3.79 ms at 500 RPS (run 1: 3.38 ms); 6.51 ms if the limiter's Redis call is counted; the exported metric is broken (HUB-405) | **Not ticked.** The server-side value meets it, but the official metric cannot show it until HUB-405, and the limiter stage alone breaks the budget (HUB-408). 20 s steps, not 10 min |
| 2,000 async msg/s sustained for 10 min, queue age < 2 min (HUB-235 AC 2) | At most about 950 accepted/s, about 360 to 400/s delivered per worker; queue age grows without bound above that | **No.** Limited by 2 CPUs and by worker concurrency (HUB-407) |
| Report with pprof findings under `docs/tech/` (HUB-235 AC 3) | This document | **Yes** |
| Async accept p99 ≤ 10 ms (DESIGN §1) | 9.1 ms at 250 msg/s (run 4), 23 ms in run 2; 39 to 293 ms at 500 msg/s | Only at low load |
| Zero lost async requests (HUB-236 AC 1) | 0 of 42,000 lost, 5 outages, 4 outage kinds | **Yes** (vendor outages; worker restarts blocked by HUB-406) |
| No duplicate callbacks (HUB-236 AC 2) | 0 duplicates, 0 redeliveries, 42,000 callbacks for 42,000 requests | **Yes** |
| Results with counts per terminal state (HUB-236 AC 3) | Section 3.4 | **Yes** |
| Parked backlog drains ≤ 60 s after recovery (DESIGN §1) | Backlog back to about 0 within 19 to 44 s of recovery, except one overlapping outage | Yes, informally (5 s samples) |

## 5. pprof findings

CPU profiles were taken for 20 s from `/debug/pprof/profile` on the ops port (mounted by this branch, HUB-403). Percentages are shares of the profile's samples. Absolute sample totals under-count on this VM: for example, 4.3 s sampled in 20 s for a gateway that `/proc` shows at 0.68 cores. The proportions are what to read.

### 5.1 Gateway, sync, 500 RPS (run 4)

| Path | Share of gateway CPU |
| --- | --- |
| `edge.(*Server).deliver` (transform, vendor call, response map) | 30 % |
| `edge.(*Server).prepare` (auth, body read, JSON decode, validate, resolve) | 22 % |
| **`audit.(*Trail).masked`**, on the request goroutine | **20 %** (27 % in run 1) |
| GC (`gcBgMarkWorker`) + allocation (`mallocgc`) | 19 % + 12 % |
| `upstream.(*Client).Attempt` (HTTP to the vendor) | 9 % |
| `edge.readJSONBody` | 5 % |

- **Audit masking is the largest single cost on the request path.** For each trail event, `Trail.masked` runs `json.Unmarshal` on bytes the edge has already parsed (55 % of masking time), then `json.Marshal` and a SHA-256 per summarised value. That is against DESIGN §18.5 rules 2 and 3 (one parse in; audit off the request path). `audit/policy.go` also builds a `strings.Replacer` per JSON-pointer segment per request (2 %). Filed as HUB-409.
- The allocation rate is high (GC about 19 %), mostly from that re-parse and the body read, so it is the same fix.
- **Not CPU, but the largest stage by time: the account limiter.** A Redis `EVALSHA` per vendor call, p99 3.96 ms at 500 RPS and 27 ms at 1,000. Its 50 ms timeout produced the 20 ms p99 tail in 3.1. DESIGN §18.5 rule 6 allows only the idempotency check on the sync path, and the system-design skill says to lease tokens locally. Filed as HUB-408.
- **The `hub_overhead_ms` metric is wrong.** `edge.(*Server).invoke` runs `defer s.metrics.RecordTimings(&p.timings, s.deps.Now(), ...)`, and Go evaluates `s.deps.Now()` when the `defer` statement runs, right after `prepare`. The metric therefore records "prepare time minus vendor time" and is negative for 100 % of requests at any vendor latency above about 1 ms. The audit ledger (computed in `record`) and the stage histograms are correct. This invalidates the S1 SLO, the `HubOverheadHigh` alert and the M3 overhead evidence until HUB-405 is fixed.

### 5.2 Gateway, async accept, 1,000 msg/s

`prepare` 46 % (masking 24 %, `readJSONBody` 12 %), `accept` (outbox transaction, `pgx.BeginFunc`) 19 %, allocation 18 %, GC 9 %. Accepting is about half Hub CPU and half Postgres commit. On 2 CPUs the two share the machine and cap acceptance at about 900/s.

### 5.3 Worker, async, saturated (backlog of 16,000)

`Worker.handle` 65 %, masking 14 %, the terminal transition transaction (`Transitions.terminal`, `pgx.BeginFunc`) 11 to 12 %, vendor HTTP 8 %, account limiter 7 %, claim 6 %. The process uses only 0.27 cores at 364 envelopes/s, so the profile shows little contention: the worker spends most of its time waiting. With 16 attempts in flight and a 32-envelope batch barrier, its throughput is about 16 / (vendor latency + Hub and database time). That is HUB-407. Adding worker processes scales it today; HUB-403 makes the 16 and 32 configurable; HUB-407 changes the claim model to keep the bulkhead full.

## 6. What limited this machine, and recommendations

| Limit | Evidence | Recommendation | Ticket |
| --- | --- | --- | --- |
| 2 shared CPUs | Machine ≥ 88 % busy at 1,000 sync RPS and ≥ 93 % at about 900 async accepts/s; generator, mockvendor and Postgres take about 0.5 cores of it | Run the full-length scenarios on separate hosts or pinned cores (`deploy/load/README.md`) | — |
| Overhead metric | 100 % negative observations | Record at return time, not at `defer` | HUB-405 |
| Account limiter on the sync path | Largest stage: p99 3.96 ms at 500 RPS; a Redis stall gives a 50 ms tail | Local token lease; count only real waiting as a wait | HUB-408 |
| Audit masking on the request goroutine | 20 to 27 % of gateway CPU, re-parses JSON | Mask from the parsed tree or in the writer goroutine | HUB-409 |
| Audit writer throughput | 19,158 to 82,782 events dropped (`buffer_full`) per step at ≥ 900 accepts/s | Batched `COPY`, more than one writer, configurable buffer | HUB-411 |
| Audit write errors | 5 s write timeouts (3 in run 1, 1 in run 2) each dropped a whole batch of up to 504 events | Retry with backoff before dropping | HUB-410 |
| Worker concurrency | 360 to 400 msg/s per process at 0.25 to 0.35 cores | Continuous claiming up to the bulkhead; meanwhile more worker processes | HUB-407 (+ HUB-403) |
| Worker restart with a backlog | Nil-snapshot panic | Gate claiming on a loaded snapshot | HUB-406 |
| Postgres pool size (not tested) | pgx defaults to `max(4, NumCPU)` connections, 4 here; not isolated as a limit in these runs | Set `pool_max_conns` in the DSN on real hardware and include it in the full-length run | — |

No production default was changed to make a number pass. The load registry raises only the test account's quota and the test tenant's rate limit, so that quotas do not hide the Hub's own cost.

## 7. How to reproduce, and the full-length run

```bash
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub_load?sslmode=disable' HUB_REDIS_ADDR=localhost:6385
cd backend && make load                       # about 25 min on 2 CPUs; results in deploy/load/results/<time>/
PHASES=soak make load                         # soak only
```

`report.md` in the run directory holds these tables (`hubload report -dir <run> [-pg-dsn …]`). The JSON per step holds every quantile, outcome, metric delta and CPU figure.

Full length on proper hardware, as in `deploy/load/README.md`: L1 500 RPS for 10 min, L2 2,000 msg/s for 10 min with `WORKERS=4` or more, and a 1 h soak at 1,000 msg/s with worker restarts after HUB-406. HUB-235's open boxes need that run after HUB-405, HUB-407 and HUB-408 have landed.

### Runs in this report

| Run | Phases | Notes |
| --- | --- | --- |
| 1 | compare, sync 100–500, async | Before the merge with main; async phase contaminated by another workstream (machine 98 % busy at 250 msg/s), not used |
| 2 | compare, async | Before the merge; repeat-run columns in 3.1 |
| 3 | async 1,000 (audit-drop breakdown), soak A | Before the merge |
| 4 | compare, sync 100–500, async, soak B | Merged code; primary numbers |
| 5 | sync 600–1,000 | Merged code; knee |
