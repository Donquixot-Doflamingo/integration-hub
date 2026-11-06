# ParkedOldestAgeHigh

**Severity:** page · **Rule group:** `hub-resilience` · **Labels:** `account`, `endpoint` (full ref, for example `clevertap.upload@1`)

## What fired
`max by (account, endpoint) (parked_oldest_age_s and on (instance) (scheduler_leader == 1)) > 600` for 1 minute: the oldest parked envelope for one `(account, endpoint)` has waited more than 10 minutes. Only the current scheduler leader's series count (a former leader keeps stale values until HUB-242).

Work parks only while the breaker for that `(account, endpoint)` is not closed. After it closes, the scheduler drains parked work at 20 % of the account rate, doubling every 30 s to 100 % (DESIGN §10.5).

## Impact
- **wait**, **async** and **async_callback** requests accepted with 202 are not delivered yet. Callers polling status see `queued` / `parked`; callbacks have not been sent.
- Each envelope still has its TTL: past `expires_at` it ends `EXPIRED` (status and callback say so) instead of being delivered.
- Sync callers are not parked; see [BreakerOpenTooLong](BreakerOpenTooLong.md) for what they see.

## First 5 minutes
1. **Vendor and account** dashboard (`hub-vendor`), set `account`: **Breaker state**, **Parked envelopes**, **Oldest parked envelope age**, **Drain releases**. The parked panels filter by account only (their `endpoint` label is the full ref).
2. Is the breaker closed? `max by (vendor, account, endpoint) (breaker_state{account="<account>"})`. Not 0: the vendor is still failing; follow [BreakerOpenTooLong](BreakerOpenTooLong.md) first.
3. Breaker closed: is the drain releasing? `sum by (account, endpoint) (rate(drain_released_total{account="<account>"}[5m]))` and `sum(scheduler_leader)` (must be exactly 1).
4. Size of the backlog vs the rate: `max by (account, endpoint) (parked_count{account="<account>"} and on (instance) (scheduler_leader == 1))`. At full rate the drain takes about `parked_count / account rps` seconds. The rate is the account `quota.rps`, else the vendor's `defaults.rate_limit.per_account_rps`, else 10/s: `getAccount` shows `quota`.
5. Scheduler logs: `drain.released` (count, `rate_factor`), `drain release failed`, `listing parked envelopes failed`, `scheduler leadership acquired`. `getBreaker` also returns `parked_count` and `parked_oldest_age_ms`.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Breaker still open | `breaker_state` 1 or 2 | Follow [BreakerOpenTooLong](BreakerOpenTooLong.md). Parked age grows by design until the vendor recovers. |
| Drain running but slow | `drain_released_total` rising, `rate_factor` 1, large `parked_count`, low quota | Expected: live traffic has priority on the account limiter. If the vendor has raised the quota, `updateAccount` with the new `quota.rps`. Do not raise it beyond what the vendor allows (it only turns into 429s). |
| No scheduler leader | `sum(scheduler_leader)` is 0 | Start or restart a `hub scheduler` process; it takes leadership and the drain resumes. Check `scheduler leadership attempt failed` logs (Postgres). |
| Drain cannot read or release | `listing parked envelopes failed` / `drain release failed` | Postgres problem on the scheduler: check connections and long transactions; escalate if it persists. |
| Breaker flapping | **Breaker transitions** shows repeated `closed` → `open`; releases start and stop | Vendor is only partly back. Let it settle, or `forceOpenBreaker` for a fixed `duration_ms` to stop hammering it, then `clearBreakerForce`. |
| Recovery not detected | Breaker open, vendor healthy, `health_probes_total` flat | No `defaults.health_probe` on the vendor: see the matching row in [BreakerOpenTooLong](BreakerOpenTooLong.md) (`forceCloseBreaker` starts the ramped drain). |

## Escalate when
- The backlog will not drain before envelopes hit their TTL (tell the tenant owners: requests will end `EXPIRED`).
- The breaker is closed, there is one leader, and `drain_released_total` stays flat for 5 minutes.
- The backlog is on a `moves_money_or_quota` or interactive integration.

## Related
- [BreakerOpenTooLong](BreakerOpenTooLong.md), [QueueOldestAgeHigh](QueueOldestAgeHigh.md) (released work becomes due work), [runbook index](README.md)
- DESIGN §10.4, §10.5, §10.7 (TTL), §10.9 (state machine)
- [F-12 Circuit breaker and drain](../features/F-12-circuit-breaker-drain.md), [OBSERVABILITY §9.3](../tech/OBSERVABILITY.md)
