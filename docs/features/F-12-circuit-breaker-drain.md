# F-12 — Circuit breaker, parking & ramped drain

## Summary
A circuit breaker per `(account, endpoint)` shared across nodes via Redis, with integration overrides and pause, `on_circuit_open` policies for sync, synthetic health probes, parking of async work while open, and a ramped drain on recovery (DESIGN §5.3, §10.4, §10.5, D4, D5).

## Status
`shipped` — 2026-11-02 (M5, `docs/milestones/M5.md`). Admin force-open/close is EP-08.

## Owner
`backend-engineer`.

## Milestone / Epic
M5 · EP-06 · HUB-140–169 (shared with F-13).

## Problem
During a vendor outage every caller today discovers it separately, keeps hammering the vendor, loses async work, and then floods the vendor on recovery.

## Goals / Non-goals
**Goals**
- Breaker key `(account, endpoint)`; local cache, Redis sync off the request path; nodes agree within ~1 s.
- Closed → Open: > 50 % failures over 30 s with ≥ 20 calls (5xx, timeouts, connection errors; 429 at half weight) or 5 consecutive timeouts (override may tighten).
- Open 30 s, doubling per re-open, cap 10 min → Half-open: 3 probes.
- Synthetic health probe every 30 s while Open.
- `on_circuit_open`: `fail_fast` (default), `failover`, `queue_if_allowed`.
- Park + drain: 20 % of account rate limit for 30 s, doubling every 30 s; live traffic priority; re-open mid-drain keeps the rest parked.
- Integration pause (kill switch) → `INTEGRATION_PAUSED`.

**Non-goals**
- LLM pre-first-byte failover for streams (F-22).

## User stories
- [US-1201](../product/USER_STORIES.md) — Shared breaker per account and endpoint
- [US-1202](../product/USER_STORIES.md) — Behaviour per mode while open
- [US-1203](../product/USER_STORIES.md) — Failover and queue_if_allowed
- [US-1204](../product/USER_STORIES.md) — Recovery without live traffic
- [US-1205](../product/USER_STORIES.md) — Ramped drain
- [US-1206](../product/USER_STORIES.md) — Pause an integration

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | 503 envelope with `circuit: "open"`, `retry_after_ms`; 202 `queued/circuit_open`; header `X-Hub-Served-By`; `Prefer: respond-async` | `contracts/openapi/hub-public.v1.yaml#invokeIntegration` (`#/components/responses/ServiceUnavailable`, `#/components/responses/Accepted`, `#/components/headers/X-Hub-Served-By`, `#/components/parameters/Prefer`) |
| Admin API | `GET /admin/v1/breakers`, `GET .../{account_id}/{endpoint_ref}`, `POST .../force-open`, `POST .../force-close`, `POST .../clear-force` | `contracts/openapi/hub-admin.v1.yaml#listBreakers`, `#getBreaker`, `#forceOpenBreaker`, `#forceCloseBreaker`, `#clearBreakerForce` |
| Admin API | `POST /admin/v1/integrations/{integration_id}/pause`, `/resume` | `#pauseIntegration`, `#resumeIntegration` |

## Behaviour & rules
| Mode | Circuit open behaviour |
| --- | --- |
| `sync` + `fail_fast` | 503 `UPSTREAM_UNAVAILABLE`, `retry_after_ms` = remaining open window, nothing sent or queued |
| `sync` + `failover` | Next failover target from Transform; only idempotent endpoints or keyed requests; `X-Hub-Served-By: failover:1`; exhausted → `fail_fast` |
| `sync` + `queue_if_allowed` | 202 only if `Prefer: respond-async` or a callback subscription exists; else `fail_fast`. Never for OTP, payments, chat |
| `wait` | 202 at once, `status: queued, reason: circuit_open`; parked |
| `async`, `async_callback` | 202; parked; drained on recovery |

1. Breaker state read from local cache on the hot path (DESIGN §18.5.6); budget with limiter/bulkhead 0.1 ms.
2. Integration override can only tighten thresholds, not loosen the shared breaker (interpretation; see open questions).
3. Pause: sync/wait → 503 `INTEGRATION_PAUSED`, nothing parked; async envelopes are nacked for later by the worker; other integrations on the account unaffected.
4. Parked envelopes keep arrival order per `(account, endpoint)`; scheduler (leader-elected) runs `Queue.Release` at the ramp rate.
5. Drain starts ≤ 60 s after breaker close (success metric).
6. Health probe uses the vendor's `health_probe` definition (dry where possible, DESIGN §8.3).
7. **What the breaker does not cover, and what does (HUB-412).** The breaker parks a vendor that fails (5xx, connection errors, timeouts), but only after its threshold, and never a vendor that is slow but still answers 2xx. Until it opens, and for slow vendors always, the worker's per-`(account, endpoint)` in-flight cap (F-13 rule 9, ADR-0019) keeps that vendor's attempts to its share of the worker's slots. Envelopes over the cap are put back without counting as attempts or breaker outcomes, so the cap never trips or skews a breaker. Probes in half-open hold at most cap slots too. Full picture with tests: `docs/tech/ISOLATION.md`.
8. The scheduler's drain releases nothing before its first registry snapshot (it would not know the account rate; HUB-406).

## Data model impact
`outbox.status = parked` with `(status, account_id, endpoint_id)` index. Breaker state in Redis only.

## Config impact
Vendor `defaults.breaker`, `health_probe`; integration `breaker.override`, `delivery.on_circuit_open`, `failover`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `breaker_state`, `breaker_transitions_total` | per account/endpoint |
| Metric | `parked_count`, `parked_oldest_age_s`, `drain_rate` | |
| Metric | `worker_target_inflight`, `worker_target_cap_deferred_total` | per breaker key: how much of the worker a misbehaving vendor holds (HUB-412) |
| Alert | breaker open > 5 min | page |
| Alert | `parked_oldest_age_s` > 10 min | page |

## Security & PII
Force-open/close and pause are admin mutations in `change_log` with reason.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Redis down | Nodes use local breaker state only | Possibly slower agreement |
| Scheduler leader lost | Standby takes over; drain resumes | Slight delay |
| Re-open mid-drain | Remaining stay parked | 202s continue |
| Vendor slow but answering (never trips) | Contained by the worker's per-target cap; its backlog waits in the outbox | Its async requests slow down; other accounts unaffected (`TestNoisyNeighbour/a_slow_but_successful`) |
| Vendor hangs until timeout, before the breaker opens | At most cap worker slots wait on it; then it trips and parks | Other accounts unaffected (`TestNoisyNeighbour/b_hangs_until_timeout`) |

## Testing
- **Chaos** (mockvendor; DESIGN §19 M5 accept 1–5): open within window; 503/202/park per mode; recovery drain ≤ 60 s with 20 → 100 % ramp; re-open mid-drain; two integrations on one account trip together, pausing one leaves the other; failover header.
- **Chaos (isolation, HUB-412)**: `TestNoisyNeighbour`: another account's async and sync latency and delivery while one account is slow, hanging, refusing or answering 503; the misbehaving account's requests all succeed after recovery (retried or parked and drained).
- **Integration**: `TestDrainWaitsForSnapshot` (HUB-406).
- **Unit**: breaker state machine table; ramp schedule.

## Rollout & migration
Defaults on per vendor; overrides per integration via publish.

## Open questions
- Can an integration override loosen thresholds? DESIGN example only tightens.
- Is failover allowed in `wait`/`async` modes for the worker? DESIGN §10.2 mentions failover only for sync.

## Tickets
HUB-140 – HUB-169 (EP-06, filter `feature: F-12`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-07 | Rules 7–8: what the breaker does not cover (worker per-target cap), drain waits for a snapshot (HUB-412, HUB-406) | backend-engineer |
