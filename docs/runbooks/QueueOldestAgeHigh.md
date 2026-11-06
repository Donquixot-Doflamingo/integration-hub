# QueueOldestAgeHigh

**Severity:** page · **Rule group:** `hub-delivery` · **Labels:** `pool` (`messaging` or `llm`)

## What fired
`max by (pool) (queue_oldest_age_s) > 120` for 1 minute: in one worker pool, an envelope that is due now (status `accepted` or `retry_scheduled`, `next_attempt_at` in the past) has not been claimed by a worker for more than 2 minutes. Envelopes waiting out a backoff, parked envelopes and envelopes of paused integrations (held 30 s at a time) do not count. The gauge comes from the scheduler leader every 10 s.

## Impact
- **async** and **async_callback** requests are delivered late; callbacks arrive late. Callers polling status see `accepted` or `retry_scheduled` for longer.
- **wait** requests whose immediate attempt did not finish in the wait window (202 `in_flight`) finish late.
- If the delay outlasts an envelope's TTL, it ends `EXPIRED`. Sync callers are not affected.

## First 5 minutes
1. **Platform** dashboard (`hub-platform`), row *Queue and delivery*: **Queue depth** (by `pool`, `status`) and **Oldest due envelope age**. Growing `accepted` with flat `in_flight` means workers are not claiming; `in_flight` high and steady means workers are busy.
2. Are there workers for this pool? Each worker serves one pool (`hub worker -pool messaging|llm`, env `HUB_WORKER_POOL`, default `messaging`). Query `sum by (pool, status) (queue_depth{pool="<pool>"})`. Check the **Process restarts** panel for crash loops.
3. Are workers waiting on the vendor? **Vendor and account** dashboard: **Vendor latency p99 by outcome** and **Deliberate waits p99** (`hub_wait_ms{kind="account_limiter"}` high means the account quota is the limit).
4. Worker logs: `claim failed` (ERROR, Postgres), `transition failed`. Scheduler logs: `queue age sample failed`, `queue depth sample failed`.
5. Is a drain flooding the pool? `sum by (account, endpoint) (rate(drain_released_total[5m]))`: released parked work becomes due work at once.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| No worker for the pool | `in_flight` 0 for the pool, `accepted` growing; no worker process with that `-pool` | Start `hub worker -pool <pool>` processes. |
| Workers saturated | `in_flight` flat at about 16 × worker processes (each worker runs at most 16 attempts and claims 32 per batch; fixed in code today) | Add worker processes for the pool. Check vendor latency first: a slow vendor fills every slot. |
| Account quota is the bottleneck | `hub_wait_ms{kind="account_limiter"}` high, vendor latency normal | Expected: workers throttle to the account rate instead of collecting 429s. More workers will not help. If the vendor raised the quota, `updateAccount` with the new `quota.rps`. |
| Drain after an outage | Age spike right after a breaker closed; `drain_released_total` rising | Usually clears as the ramp completes. Add workers if it does not fall within 10 minutes. |
| Postgres slow or contended | `claim failed` logs, slow claims, long transactions in `pg_stat_activity`, outbox bloat | Fix Postgres (kill runaway transactions, check autovacuum on `hub.outbox`); escalate. |
| Workers crash looping | **Process restarts** > 0 for workers | Read the crash logs; roll back the last release if it started with a deploy. |

## Escalate when
- Age keeps rising for 10 minutes after adding workers.
- Postgres is the cause, or workers crash after a release.
- `sum(scheduler_leader)` is 0: the gauge is cleared without a leader, so this alert cannot fire while the scheduler is missing.

## Related
- [ParkedOldestAgeHigh](ParkedOldestAgeHigh.md), [DLQSurge](DLQSurge.md), [runbook index](README.md)
- DESIGN §10.3 (backoff), §10.8 (bulkheads and rate limits), §11 (queue)
- [F-09 Durable delivery](../features/F-09-durable-delivery.md), [OBSERVABILITY §9.2](../tech/OBSERVABILITY.md)
