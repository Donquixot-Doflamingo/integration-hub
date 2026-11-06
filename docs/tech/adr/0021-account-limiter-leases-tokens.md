# ADR-0021: The account limiter leases tokens off the request path

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-408, HUB-413 |
| DESIGN refs | §10.8, §18.5 rule 6, §18.6; go-system-design skill §6 |

## Context
`upstream.AccountLimiter.Wait` ran a Redis Lua script for every vendor call, sync ones included. DESIGN §18.5 rule 6 allows only the idempotency check as a non-vendor network call on the sync path, and the go-system-design skill says to cache a local token lease. The HUB-235 load test measured the cost: the `admission` stage p99 was 3.8 ms at 500 RPS on 2 CPUs (budget 0.1 ms), and a Redis stall made the limiter's 50 ms timeout fire 15 times in one second, a 20 ms p99 tail on the "Hub adds" number (LOAD_TEST_REPORT §3.1). The whole call was also counted as a deliberate wait (`StageWait`), so `hub_overhead_ms` left it out.

The account quota must still hold across every node and worker (DESIGN §10.8), live traffic must keep priority over retries and drain (§10.5), and a Redis outage must not block requests.

## Options considered
1. **Redis call per request (status quo).** Exact, but a network round trip and a Redis-stall tail on every sync call.
2. **Static per-node share (rate / node count).** No Redis at all, but needs a reliable node count, wastes quota on idle nodes and over-admits during scale-out.
3. **Leased tokens from the same Redis bucket, refilled in the background.** Each node takes a small block of tokens from the shared bucket and serves calls from it; the request only touches local memory.

## Decision
Option 3. Per `(account, live|retry)` each node keeps a lease:
- **Lease size** is the node's recent use over `LeaseHorizon` (200 ms), between 1 token and `LeaseShare` (10 %) of the account rate. A busy node takes bigger blocks, a quiet one single tokens, so idle nodes do not hold quota others need.
- **Refill** starts in a background goroutine when the lease falls to half its last size. The Lua script grants up to the number asked (partial grants allowed) and, when it grants none, the ms until a token is due. Leases are serialised per pool and bounded by `LeaseTimeout` (50 ms); `Close` cancels and waits for them.
- **No token, not at the limit:** the call passes at once and the node owes one token, paid from its next lease. A cold or idle node therefore never waits for Redis. A second call while the debt is outstanding waits for the lease reply (counted as `admission` overhead, not `wait`).
- **At the limit** (the last lease granted nothing): calls wait until a token is due, up to their `maxWait` (250 ms live, 5 s otherwise); only that sleep is a deliberate wait (`StageWait`, `hub_wait_ms`), else 429 `account_quota`.
- **Expiry:** leased tokens unused for `LeaseTTL` (1 s) are dropped, never returned. Dropping only lowers throughput; it can never over-admit.
- **Reserve:** retry/drain pools lease with the 20 % reserve as the floor, live pools with none, exactly as before.
- **Redis down:** a failed lease lets that pool's traffic through for `FailOpenFor` (1 s) without further Redis calls, then leases again. Before, every request paid a 50 ms timeout before failing open.

Every token a node uses comes from the Redis bucket, so the fleet-wide rate is the bucket's rate. The only over-admission is the borrowed token: at most one per node and traffic class at any instant, paid back on the next lease.

## Consequences
- Positive: no Redis round trip on a sync request in steady state (unit test with a 20 ms fake Redis, itest with a 100 ms real one); the limiter's share of `admission` is a mutex and a map lookup; Redis stalls no longer reach callers; the fleet rate still holds (unit test with two nodes in synthetic time ± 5 %, itest with two gateways ± 10 %).
- Negative: at the limit a node's waiters are woken by its own lease timers, so a node serves slightly fewer of a contended account's tokens than one that polls Redis per call. A node can briefly hold up to 10 % of the account rate that another node could have used (at most 1 s, then dropped). The fleet can be ahead of the bucket by one token per node and class.
- The bucket's Redis key and fields (`hub:rl:acct:<account>`, `t`, `ts`) are unchanged, so old and new nodes share one bucket during a rolling deploy.
- New metric `account_limiter_leases_total{result}` and `redis_op_duration_seconds{op="limiter_lease"}` (OBSERVABILITY §3.3); the stage split is in TECH_SPEC §11 and F-13.

## Deviation from DESIGN.md
Additive interpretation of §10.8 ("shared Redis bucket"): the bucket stays shared and authoritative, but nodes draw from it in leases rather than per call, and may be one token ahead per node and class. §18.5 rule 6 is now met for the account limiter.
