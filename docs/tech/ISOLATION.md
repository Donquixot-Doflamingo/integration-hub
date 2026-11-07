# What happens to other vendors when one vendor is down or slow

Short answer: **nothing that the others can notice.** Each vendor account is isolated at every layer a request passes through: its own HTTP connection pool, its own circuit breaker, its own rate limit, its own share of the async worker, its own parked backlog and drain. A vendor that is down, hanging or slow uses up only its own resources. Its own callers see its outage (fast 503s on sync, queued requests on async), and its queued requests are not lost: they retry or park and drain after recovery.

The unit of isolation is the **(account, endpoint)** pair, the breaker key of DESIGN §10.4 (D5). Two integrations on the same vendor account share it on purpose: they talk to the same vendor quota.

Every claim below names the test that proves it. All tests use mockvendor (never a real vendor) with real Postgres and Redis.

## The proof: noisy-neighbour chaos tests (HUB-412)
`TestNoisyNeighbour` (`backend/internal/edge/isolation_integration_test.go`) wires gateway, worker and scheduler as `cmd/hub` does. Account A and account B are served by two separate mockvendor instances. Callers keep sending A 20 async requests/s while it misbehaves. B carries 20 async/s and 10 sync/s throughout. Baseline: 3 s with A healthy (20 ms answers). Then 6 s with A misbehaving. Then A recovers.

| Scenario | What A does |
| --- | --- |
| (a) `a_slow_but_successful` | answers 200 OK after 1.3 s, under its 1.5 s timeout (never counts as a failure, never trips the breaker) |
| (b) `b_hangs_until_timeout` | accepts the connection and never answers (each attempt times out at 1.5 s) |
| (c) `c_refuses_connections` | connection refused |
| (c) `c_answers_503` | 503 at once |

Asserted for each scenario:
- **B async** (accept → `succeeded`, from `hub.requests`): p99 while A misbehaves ≤ B's baseline p99 + 300 ms. The baseline term is capped at 200 ms, so a machine stall during the baseline cannot loosen the bound. The bound therefore never exceeds 500 ms, less than half of A's slowest answer. Waiting behind even one of A's calls breaks it. The 300 ms covers scheduling noise on a shared 2-CPU runner under `-race`.
- **B sync** (gateway round trip): p99 ≤ baseline p99 (capped at 100 ms) + 100 ms.
- **B loses nothing**: every accepted B request ends `succeeded`, and B's vendor received at least one call per request.
- **A loses nothing**: after A recovers, every A request accepted during the incident ends `succeeded` (retried, or parked and drained). None is dead-lettered or expired.

### Before and after (same test, same machine; worker with 16 slots, claims of 32)
B's latency while A misbehaves, from the final runs on 2026-11-07 (shared 2-CPU machine, `-race`). Baseline B async p99 (A healthy) was 48–89 ms in these runs.

| Scenario | B async p99, before | B async p99, after | B sync p99, before → after |
| --- | --- | --- | --- |
| (a) A slow (1.3 s) | **3.83 s** (p50 2.29 s), fails | 51 ms (baseline 83 ms) | 31 → 26 ms |
| (b) A hangs | **2.33 s** (p50 332 ms), fails | 129 ms (baseline 48 ms) | 31 → 41 ms |
| (c) A refuses | 102 ms, passes | 92 ms (baseline 52 ms) | 29 → 37 ms |
| (c) A 503 | 52 ms, passes | 54 ms (baseline 89 ms) | 28 → 30 ms |

"Before" is `main` before HUB-412 (`44c401c`) running the same test. The worker claimed a batch of 32, ran 16 at a time and waited for the whole batch before claiming again. In (a) and (b), B's envelopes waited behind A's 1.3–1.5 s calls (`logSlowest`: 3.86 s of queue wait, 27 ms at the vendor). In (c), A's calls fail fast and the breaker parks A within about a second, so the old worker was not measurably hurt in the final run. (c) is therefore a regression guard, not proof of the fix. Sync latency was never affected: it does not go through the worker. With HUB-412, B had 0 envelopes put back by the cap in all four scenarios; A had 213 (a), 73 (b), 10 and 13 (c).

## Layer by layer

### 1. Sync path (gateway → vendor)
- **Own connection pool per account.** Every account has its own `http.Transport` with `MaxConnsPerHost` = 64 (the bulkhead, DESIGN §10.8). A slow vendor fills only its own pool; calls to other accounts never wait for its connections. Proof: `TestPerAccountPoolsAndBulkhead` (`internal/upstream`, a full account does not borrow another's connections), `TestBulkheadPerAccount` (`internal/upstream`, integration).
- **Bounded wait per call.** Each call has its endpoint's timeout (`timeout_ms`, `connect_timeout_ms`) and the caller's `X-Request-Deadline`, so a hanging vendor costs its caller at most the timeout. Proof: `TestEndpointTimeout`, `TestConnectTimeout`, `TestCallerDeadlineBeatsEndpointTimeout`.
- **No shared queue.** A sync request is served on its own goroutine and reads only the in-memory snapshot (DESIGN §18.5). It never touches the outbox or the worker. Proof: B sync latency in `TestNoisyNeighbour` (all four scenarios).

### 2. Async path (outbox → worker → vendor)
- **Continuous claiming (HUB-412, ADR-0019).** The worker claims `min(HUB_WORKER_CLAIM_BATCH, free slots)` as soon as a slot frees. No envelope waits for another envelope's attempt to finish. Proof: `TestWorkerClaimsContinuously` (B delivered while A's 1.5 s attempt is still in flight; on the old loop B took 1.50 s and finished after A).
- **Per-(account, endpoint) worker cap (HUB-412).** One target may hold at most `HUB_WORKER_TARGET_CAP` of the worker's `HUB_WORKER_CONCURRENCY` slots (defaults: 64 of 256 per process, 16 of 64 in the LLM pool). Claims are room-aware: a claim takes no more of a target's backlog than that target's free slots, and none while it is at its cap (its rows are left out before the claim's `LIMIT`), so a slow vendor's backlog never fills a claim ahead of other vendors' envelopes. An envelope that still finds its target full when it starts (a first claim before the target is known) is put back unattempted: no attempt counted, no breaker outcome, no retry-budget token. Proof: `TestWorkerTargetCap` (`internal/delivery`), `TestClaimWithinTargetRoom` and `TestDeferTarget` (`internal/queue`), `TestWorkerTargetSlots`, `TestWorkerTargetSlotsConcurrent` (unit), and `TestNoisyNeighbour` (a) and (b).
- **Separate LLM pool.** Envelopes in the `llm` pool run in their own worker pool and claim from their own `pool` in the outbox, so slow model calls never take messaging slots. Proof: `TestPoolsAreSeparate` (`internal/queue`). Since ADR-0023 new requests are all queued as `messaging` until an explicit integration setting exists ([ADR-0024](adr/0024-llm-body-limit-and-pool-without-capability-names.md), HUB-439); envelopes already in `llm` stay there.
- **Nothing lost.** An envelope is accepted only after its outbox row commits. Claims expire and are reclaimed if a worker dies, and every state change is guarded (DESIGN §11, §10.9). Shutdown stops claiming and lets the attempts in flight finish. Proof: `TestWorkerShutdownFinishesInFlight`; `TestWorkerCrashDeliveredOnceToTheCaller` (M4 worker kill, `internal/edge`); "A loses nothing" in `TestNoisyNeighbour`.

### 3. Circuit breaker (DESIGN §10.4)
- Per (account, endpoint), shared by every node through Redis. Only the failing pair opens; other accounts, even on the same vendor, keep their own closed breaker. On the sync path the breaker is read from a local cache, so an open breaker answers in microseconds (`fail_fast` 503 with `retry_after_ms`, or failover), and nothing is sent. Proof: `TestChaos1VendorDown`, `TestChaos4SharedAccountAndPause` (two integrations on one account trip together; pausing one leaves the other untouched), `TestNodesConverge`, `TestRedisDownNeverBlocks` (Redis down never blocks requests).
- **What it does not cover:** a vendor that is slow but answers 2xx never trips it, and a hanging vendor trips only after 5 consecutive timeouts (or > 50 % failures over ≥ 20 calls). In those windows the worker cap above is what protects others. Proof: `TestNoisyNeighbour` (a), and (b) before the breaker opens.

### 4. Parking
While A's breaker is open, A's async envelopes are parked under their (account, endpoint) instead of attempted. They hold no worker slot and no connection, and are never dropped (TTL still applies). Proof: `TestChaos1VendorDown` (async parks, no vendor call), `TestNoisyNeighbour` (b) and (c) (A's requests all succeed after recovery).

### 5. Ramped drain (DESIGN §10.5)
After A recovers, the scheduler releases A's parked backlog at 20 % of A's account rate, doubling every ramp step up to 100 %. Live traffic keeps priority on A's limiter. The drain is per (account, endpoint) and uses A's rate, so A's backlog never competes with B's quota. If A fails again mid-drain, the rest stay parked. Before its first registry snapshot the drain releases nothing, because it would not know A's rate (HUB-406). Proof: `TestChaos2RecoveryAndRampedDrain`, `TestChaos3ReopenMidDrain`, `TestDrainWaitsForSnapshot`.

### 6. Rate limits
- **Account limiter** (shared Redis token bucket per account, DESIGN §10.8): each account spends only its own quota. Workers wait for A's tokens (at most 5 s, 250 ms for live traffic) instead of collecting 429s. That wait holds at most A's capped worker slots. Proof: `TestAccountRateAcrossWorkers`, `TestLiveTrafficFirst`.
- **Tenant limiter** at the edge (per tenant + integration): a tenant hammering one integration gets 429 for that integration only. Proof: `TestTenantLimiter`.
- **Retry budget** (per account, retries ≤ 20 % of live traffic): A's retries during an incident are throttled against A's own traffic, never B's. Proof: `TestBudget`.

### 7. Startup
A worker restarted while the outbox has a backlog claims nothing until its first registry snapshot is loaded. It used to panic and count a crash per claim, which could poison healthy envelopes. Proof: `TestWorkerWaitsForSnapshot`, `TestWorkerReleasesClaimWithoutSnapshot` (both panicked before HUB-406).

## What is shared, and the limits of the guarantee
- **The process, Postgres and Redis.** Every request uses them, whichever vendor it goes to. A vendor outage adds a little load (A's retries, parks and put-backs are short transactions; put-backs are counted in `worker_target_cap_deferred_total`), but none of it holds a connection while waiting on a vendor. No transaction ever spans a vendor call.
- **Caps are per worker process.** With N worker processes, a misbehaving target can hold N × cap slots fleet-wide, which is always a quarter of the fleet's slots by default. The connection bulkhead is per process as well.
- **Several bad vendors at once.** With the default quarter cap, three misbehaving targets together still leave a quarter of every worker's slots to everyone else. More than that needs more workers or a lower `HUB_WORKER_TARGET_CAP`.
- **The misbehaving vendor's own callers** do see it: sync calls fail fast or fail over (`on_circuit_open`), and async requests take longer. While A is slow, A's backlog is delivered at most `cap / A's latency` per second per worker process. That is the intended trade: A gets slower, nobody else does.

## Watching it in production
`worker_target_inflight{account, endpoint}` at the cap and a rising `worker_target_cap_deferred_total` name the vendor being contained. `breaker_state`, `parked_count` and `parked_oldest_age_s` show parking. `vendor_latency_ms` shows the slowness itself. Other accounts' `hub_requests_total` and `queue_oldest_age_s` stay flat. Details: `OBSERVABILITY.md` §3.3; design record: ADR-0019.
