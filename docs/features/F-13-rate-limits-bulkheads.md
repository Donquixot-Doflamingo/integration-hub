# F-13 — Rate limits & bulkheads

## Summary
Two budgets and one isolation layer: tenant fairness at the edge (token bucket per tenant + integration), vendor quota per account in the upstream client (shared Redis token bucket, live traffic first), per-account bulkheads (HTTP pool + 64 in-flight), separate LLM worker pool, and the retry budget (DESIGN §10.3, §10.8, §3.2).

## Status
`shipped` — 2026-11-02 (M5, `docs/milestones/M5.md`).

## Owner
`backend-engineer`.

## Milestone / Epic
M5 · EP-06 · HUB-140–169.

## Problem
One noisy tenant can exhaust a shared vendor account; one slow vendor can exhaust connections and goroutines for everyone; retries during an incident amplify it.

## Goals / Non-goals
**Goals**
- Tenant limiter: local `x/time/rate` bucket per `(tenant, integration)` at the edge → 429 `UPSTREAM_RATE_LIMITED`.
- Account limiter: Redis token bucket shared across nodes; workers wait for tokens instead of collecting vendor 429s; live (sync/wait) traffic has priority over drain and retries.
- Bulkhead: per-account HTTP pool and concurrency limit (default 64); LLM endpoints in a separate pool.
- Retry budget: retries ≤ 20 % of the account's live traffic.
- Daily quotas from account config (`quota.daily`).

**Non-goals**
- LLM token budgets per tenant (F-22, DESIGN §15).

## User stories
- [US-1301](../product/USER_STORIES.md) — Tenant fairness
- [US-1302](../product/USER_STORIES.md) — Account quota without 429s
- [US-1303](../product/USER_STORIES.md) — Bulkheads
- [US-1304](../product/USER_STORIES.md) — Retry budget

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | 429 envelope `UPSTREAM_RATE_LIMITED` with `retry_after_ms` and `reason` (`vendor_rate_limit`, `account_quota`, `tenant_rate_limit`, `budget`) | `contracts/openapi/hub-public.v1.yaml#/components/schemas/ErrorEnvelope` |
| Admin API | Tenant `rate_limit`, account `quota` fields | `contracts/openapi/hub-admin.v1.yaml#updateTenant`, `#updateAccount` (`RateLimit`, `AccountQuota`) |

## Behaviour & rules
1. Edge order: auth → grant → **tenant rate limit** → body limit (DESIGN §18.4).
2. Tenant limits are local per node (budget split by node count, or Redis; see open questions); the account limiter is always Redis-shared.
3. Sync request when the account bucket is empty: short bounded wait (≤ remaining deadline), else 429 `UPSTREAM_RATE_LIMITED` reason `account_quota`.
4. Priority: live > drain > retries on the account limiter (DESIGN §10.5).
5. Bulkhead full on sync → 503 `UPSTREAM_UNAVAILABLE` (proposed) without calling the vendor; worker → requeue.
6. Retry budget exhausted → retry deferred (not dropped), metric incremented.
7. The breaker counts vendor 429 at half weight (F-12).
8. Combined breaker + limiter + bulkhead budget 0.1 ms p99 (DESIGN §18.6).
9. **Worker slots per target (HUB-412, ADR-0019).** The async worker claims continuously, `min(HUB_WORKER_CLAIM_BATCH, free slots)` as soon as a slot frees, and no `(account, endpoint)` may hold more than `HUB_WORKER_TARGET_CAP` of its `HUB_WORKER_CONCURRENCY` slots (default a quarter, at most the 64-connection bulkhead). A claim takes no more of a target's backlog than that target's free slots (none at its cap); an envelope that still finds its target full is put back unattempted (no attempt counted, no breaker outcome, no retry-budget token). A slow or hanging vendor therefore fills only its own share of the worker, the same way the connection bulkhead contains it on the sync path. Full picture: `docs/tech/ISOLATION.md`.

## Data model impact
`api_keys.rate_limit` / tenant limits; account `quota` in config. Redis buckets.

## Config impact
Vendor `defaults.rate_limit.per_account_rps`; account `quota: { rps, daily }`; tenant `rate_limit`; bulkhead `max_in_flight` per vendor endpoint (proposed field). Worker process settings (TECH_SPEC §8.1): `HUB_WORKER_CONCURRENCY` (256 messaging, 64 llm), `HUB_WORKER_CLAIM_BATCH` (32), `HUB_WORKER_TARGET_CAP` (concurrency / 4, at most 64).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `ratelimit_rejections_total{layer="tenant"|"account"}` | |
| Metric | `retry_budget_exhausted_total` | |
| Metric | `bulkhead_in_flight{account}`, `bulkhead_rejections_total` | |
| Metric | `ratelimit_wait_ms` | recorded in `vendor.sent` audit too |
| Metric | `worker_inflight{pool}`, `worker_target_inflight{account, endpoint}` | worker slots in use, per target (HUB-412) |
| Metric | `worker_target_cap_deferred_total{account, endpoint}` | envelopes put back over their target's cap (HUB-412) |
| Log | `worker.target_at_cap` (DEBUG) | one per put-back; the counter carries the rate |

## Security & PII
Limits protect vendor accounts from abuse by compromised tenant keys.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Redis down | Account limiter falls back to local per-node share (proposed) | Possibly vendor 429s |
| Slow vendor saturates bulkhead | Only that account affected | 503 for that account |
| Slow or hanging vendor with an async backlog | Holds at most its target cap of worker slots; the rest of its backlog waits in the outbox, unclaimed until it has a free slot | That vendor's async requests take longer; other vendors' latency unchanged (`TestNoisyNeighbour`) |

## Testing
- **Integration**: 4 workers, 50 rps quota, 1,000 envelopes → ≤ 50 rps, zero 429s at mockvendor.
- **Chaos**: `TestNoisyNeighbour` (`backend/internal/edge/isolation_integration_test.go`, HUB-412): account A slow (1.3 s answers), hanging until its timeout, refusing connections, or answering 503 while callers keep sending it 20/s; account B's async p99 stays within 300 ms of its baseline (bound justified in the test), B's sync p99 within 100 ms, B and A lose nothing. Before/after numbers: `docs/tech/ISOLATION.md`.
- **Integration**: `TestWorkerTargetCap`, `TestWorkerClaimsContinuously`, `TestWorkerShutdownFinishesInFlight` (`backend/internal/delivery`), `TestClaimWithinTargetRoom`, `TestDeferTarget` (`backend/internal/queue`).
- **Unit**: priority ordering; retry budget math.

## Rollout & migration
Start with generous tenant limits; tighten per observed traffic.

## Open questions
- Tenant limiter local or Redis-shared? DESIGN says "token bucket per tenant + integration" without scope.
- Error code when the bulkhead is full is not specified in DESIGN.
- Daily quota reset timezone.

## Tickets
HUB-140 – HUB-169 (EP-06, filter `feature: F-13`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-07 | Worker slots per target (rule 9), worker settings, metrics, noisy-neighbour tests (HUB-412) | backend-engineer |
