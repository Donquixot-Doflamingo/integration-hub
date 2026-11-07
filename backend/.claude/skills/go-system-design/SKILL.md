---
name: go-system-design
description: "Distributed-systems patterns as implemented in the Integration Hub - transactional outbox with FOR UPDATE SKIP LOCKED and visibility timeout, at-least-once delivery with idempotency, compare-and-set state transitions, full-jitter retries with a retry budget, (account, endpoint) circuit breaker shared via Redis, token buckets, bulkheads, backpressure, TTL expiry, ramped drain, scheduler leader election, snapshot distribution and re-resolve vs pinned cutover, each with failure-mode tables. Use whenever you work on queue, delivery, upstream, scheduler, registry distribution or resilience code (DESIGN 5, 6, 10, 11), or reason about retries, duplicates, outages, races or ordering."
---

# System design patterns in the Hub

Read the matching DESIGN section first; this skill is the *how*. Code-level detail: [references/patterns.md](references/patterns.md). SQL: `postgres-patterns`.

## Guarantees we promise (and the ones we don't)
| Promise | Mechanism |
| --- | --- |
| Accepted async request is never lost | 202 only after the outbox row is **committed** (§11.1) |
| Delivered **at least once** to the vendor | Claim + visibility timeout + reclaim |
| Caller sees **one** outcome / one callback | CAS status transitions + idempotency store |
| No duplicate vendor side effects where possible | Vendor idempotency keys (`Idempotency-Key` → vendor header) when the vendor supports it |
| Not promised | Exactly-once delivery to vendors without idempotency support; global ordering (only per `ordering_key`, opt-in) |

## 1. Transactional outbox
- Write the envelope (canonical body + integration id, **not** the vendor payload) in the same transaction as the `requests` row. Commit → then respond 202.
- Workers claim with `FOR UPDATE SKIP LOCKED` in a CTE + `UPDATE ... RETURNING`, set `status='in_flight'`, `claimed_by`, `claimed_until = now() + visibility` (30 s). Claim and status flip are one statement, so the row lock is held for milliseconds, not for the vendor call.
- A row whose `claimed_until` has passed while `in_flight` is reclaimable: the worker died.
- Vendor call happens **outside** any DB transaction.

| Failure | What happens | Guard |
| --- | --- | --- |
| Gateway dies before commit | Caller gets error/timeout, nothing stored | Caller retries with same `Idempotency-Key` |
| Gateway dies after commit, before 202 | Request exists; caller retries | Idempotency returns stored state |
| Worker dies mid-call | Reclaimed after visibility timeout → second vendor call | Vendor idempotency key; poison counter |
| Vendor call slower than visibility | Two workers send concurrently | Visibility > max endpoint timeout + margin; worker heartbeats extend `claimed_until` for long calls; CAS on completion |
| Envelope crashes worker twice | Poison | `claim_count` ≥ 3 without a recorded attempt → dead-letter (§10.6) |
| DB down | Claims fail | Worker backs off with jitter, `/readyz` fails; gateway sync mode still works for `sync` integrations |

## 2. Guarded state transitions (CAS)
States and edges are DESIGN §10.9. Every transition is `UPDATE ... WHERE request_id=$1 AND status=$from [AND claimed_by=$worker]`; **0 rows affected = you lost the race; do nothing else** (no callback, no audit "completed"). Encode allowed edges in one Go table and test it exhaustively.
```go
var allowed = map[Status][]Status{
	Accepted:       {InFlight},
	InFlight:       {Succeeded, RetryScheduled, Parked, DeadLettered},
	RetryScheduled: {InFlight, Expired},
	Parked:         {RetryScheduled, Expired},
	DeadLettered:   {Accepted}, // manual replay
}
```
Side effects (callback enqueue, terminal audit record) are written **in the same transaction** as the winning transition, so they happen exactly once per transition.

## 3. Idempotency
- Key scope: `(tenant, integration_id, Idempotency-Key)`, 24 h (§4.2). Redis `SET key value NX PX` reserves; value = request id + state; completed results stored for replay with `X-Hub-Idempotent-Replay: true`.
- Only **completed** requests short-circuit. A retry of a failed request is a new attempt and re-resolves (§6.3).
- Concurrent duplicates: the loser of `NX` returns the in-progress request's 202/poll link (or waits within `wait_ms`), never a second vendor call.
- Redis down: fail **closed** for integrations where money/quota moves (mandatory key) → 503 retryable; fail open elsewhere with a metric. Decide per integration; document in the feature doc.

## 4. Retries, backoff, retry budget
- Async schedule §10.3: 1 s, 5 s, 30 s, 2 m, 10 m, 30 m, 1 h, 1 h → DLQ after 8 or at TTL. **Full jitter**: `delay = rand.N(schedule[n])`, then `max(delay, Retry-After)` for 429.
- Sync: at most one immediate retry, only for timeouts/resets on idempotent endpoints or with an idempotency key (§10.2). Never retry 4xx except 429 (async).
- **Retry budget**: per account, retries ≤ 20 % of first attempts over a rolling window (plus a small floor so low-traffic accounts can retry). Exhausted → do not send now; `Nack` to a later time and increment `retry_budget_exhausted_total`. Never drop.
- Retries count toward the breaker; a retry while Open is a park, not a send.

## 5. Circuit breaker (§10.4)
- Key `(account, endpoint)`. State machine: Closed → Open (> 50 % failures over 30 s with ≥ 20 calls, 429 at half weight, or 5 consecutive timeouts) → Half-open after open window (30 s doubling, cap 10 min) → 3 probes → Closed / Open.
- **Request path reads only the local cache** (§18.5.6). Outcomes are recorded locally and flushed in aggregate to Redis off the path (every ~250 ms); a Redis Lua script evaluates thresholds and transitions atomically and publishes a change; nodes subscribe and also poll every 1 s.
- Integration overrides tighten thresholds for *that integration's decision* only; the shared state belongs to the account.
- Synthetic health probe every 30 s while Open (scheduler, leader only).

| Failure | Behaviour |
| --- | --- |
| Redis down | Nodes keep a local breaker per node (degraded, not shared); metric `breaker_sync_errors`; never block requests |
| Pub/sub message lost | 1 s poll converges |
| Two nodes trip simultaneously | Lua script is the single writer; second transition is a no-op |
| Half-open thundering herd | Probe slots taken via Redis `INCR` with limit; others treat as Open |

## 6. Rate limits and bulkheads (§10.8)
- Tenant fairness at the edge: local `x/time/rate` limiter per (tenant, integration); approximate across nodes (limit / replicas) is acceptable.
- Account vendor quota: shared Redis token bucket (Lua) in upstream; workers **wait** for tokens instead of collecting 429s; sync path does not wait beyond its deadline. Cache a local token lease to avoid a Redis round trip per call.
- Bulkhead: per-account semaphore (default 64 in flight) + per-account `http.Transport`; LLM in a separate worker pool. A slow vendor fills only its own bulkhead.

## 7. Backpressure
- All in-memory queues are bounded channels. Producer behaviour when full is explicit: audit drops + counter (`audit_events_dropped_total`, alert); worker claim loop simply claims fewer; edge returns `UPSTREAM_RATE_LIMITED`/503 rather than queueing in memory.
- Workers claim `n = free bulkhead slots`, never more than they can start.

## 8. TTL
`expires_at` checked before each attempt and by a scheduler sweep for `retry_scheduled|parked`. Expire with CAS → `expired`, terminal audit, callback `request.expired`. Never send an expired envelope.

## 9. Park and ramped drain (§10.5)
- Breaker Open → `Park` (status `parked`, tagged account+endpoint, arrival order).
- On Close the scheduler releases at 20 % of the account rate for 30 s, doubling every 30 s to 100 %; live traffic keeps priority on the shared limiter (drain only uses tokens above a reserve). Re-open mid-drain → stop releasing; released-but-unsent envelopes park again on their next attempt.
- Release is `UPDATE ... SET status='retry_scheduled', next_attempt_at=now() WHERE ... ORDER BY created_at LIMIT n` in a CTE with SKIP LOCKED.

## 10. Scheduler leader election
Postgres session advisory lock (`pg_try_advisory_lock`) on a **dedicated connection** (not from the pool). Leader runs jobs; standby retries every few seconds. The leader pings its lock connection each tick and stops all jobs immediately if the ping fails (the lock is gone with the session). All jobs must be idempotent anyway, because two leaders can overlap briefly during a network partition.

## 11. Snapshot distribution (§8.2)
Publish writes `config_snapshots` row + blob → Redis ping → nodes long-poll/fetch, compile in background, `atomic.Pointer` swap. Fetch/compile failure → keep the last good snapshot, export `snapshot_version` and alert when > 60 s behind. Nodes can boot from the last blob in object storage if control plane is down.

## 12. Cutover: re-resolve vs pinned (§6.3, D10)
- Worker resolves `version = 0` (active) at **send time** unless `env.Pinned` or `env.SessionID != ""` → use `env.ResolvedVersion`.
- Every attempt records the integration version it used.
- Pinned/session versions keep accounts in `draining`; retire is refused while referenced (§6.5).

## Review checklist
- [ ] Every state change is a CAS with rows-affected checked.
- [ ] No DB transaction spans a network call.
- [ ] Every retry path has jitter, a cap, a budget and a TTL check.
- [ ] Every in-memory queue is bounded with a defined full behaviour + metric.
- [ ] Redis/DB outage behaviour is defined and tested (fail open vs closed).
- [ ] Chaos test with mockvendor covers the failure table rows you touched (`go-testing`).
