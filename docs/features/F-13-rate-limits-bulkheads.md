# F-13 — Rate limits & bulkheads

## Summary
Two budgets and one isolation layer: tenant fairness at the edge (token bucket per tenant + integration), vendor quota per account in the upstream client (shared Redis token bucket, live traffic first), per-account bulkheads (HTTP pool + 64 in-flight), separate LLM worker pool, and the retry budget (DESIGN §10.3, §10.8, §3.2).

## Status
`proposed` — 2026-10-31.

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

## Data model impact
`api_keys.rate_limit` / tenant limits; account `quota` in config. Redis buckets.

## Config impact
Vendor `defaults.rate_limit.per_account_rps`; account `quota: { rps, daily }`; tenant `rate_limit`; bulkhead `max_in_flight` per vendor endpoint (proposed field).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `ratelimit_rejections_total{layer="tenant"|"account"}` | |
| Metric | `retry_budget_exhausted_total` | |
| Metric | `bulkhead_in_flight{account}`, `bulkhead_rejections_total` | |
| Metric | `ratelimit_wait_ms` | recorded in `vendor.sent` audit too |

## Security & PII
Limits protect vendor accounts from abuse by compromised tenant keys.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Redis down | Account limiter falls back to local per-node share (proposed) | Possibly vendor 429s |
| Slow vendor saturates bulkhead | Only that account affected | 503 for that account |

## Testing
- **Integration**: 4 workers, 50 rps quota, 1,000 envelopes → ≤ 50 rps, zero 429s at mockvendor.
- **Chaos**: slow-drip account does not affect another account's p99.
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
