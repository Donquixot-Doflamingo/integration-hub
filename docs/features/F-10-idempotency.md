# F-10 — Idempotency

## Summary
`Idempotency-Key` handling backed by Redis: the same key within 24 h returns the stored result of a completed request; concurrent duplicates make one vendor call. The key is mandatory for capabilities that move money or vendor quota (DESIGN §4.2, §5.1, §6.3).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M4 · EP-05 · HUB-110–139.

## Problem
Callers retry on timeouts. Without idempotency, a retried bureau pull is billed twice and a retried payment moves money twice.

## Goals / Non-goals
**Goals**
- Redis lookup after resolve, before transform (DESIGN §5.1 step 4), only when a key is sent (skipped otherwise; DESIGN §18.5.6).
- Store final status + canonical response for 24 h; replay returns it with `X-Hub-Idempotent-Replay: true` (`IDEMPOTENT_REPLAY` semantics, 200/202).
- In-progress marker to collapse concurrent duplicates.
- Mandatory key for integrations with `moves_money_or_quota: true`.
- Weighted routing uses the key as the hash input, so a key always lands on the same target (DESIGN §5.1 step 3).

**Non-goals**
- Short-circuiting *failed* requests: a retry with the same key after a failure is a new attempt and resolves fresh (DESIGN §6.3).

## User stories
- [US-1001](../product/USER_STORIES.md) — Same key, same result
- [US-1002](../product/USER_STORIES.md) — Mandatory where money or quota moves
- [US-1003](../product/USER_STORIES.md) — Retries of failed requests resolve fresh
- [US-1004](../product/USER_STORIES.md) — Key reuse with a different body

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | `Idempotency-Key` header on `invoke` | `contracts/openapi/hub-public.v1.yaml#/components/parameters/IdempotencyKey` |
| Caller API | Response header `X-Hub-Idempotent-Replay` | `contracts/openapi/hub-public.v1.yaml#/components/headers/X-Hub-Idempotent-Replay` on `#invokeIntegration` / `#invokeIntegrationOperation` responses |
| Caller API | 400 `IDEMPOTENCY_KEY_REQUIRED`, 409 `IDEMPOTENCY_CONFLICT` | `contracts/openapi/hub-public.v1.yaml#/components/responses/BadRequest`, `#/components/responses/IdempotencyConflict` |
| Integration flag | `moves_money_or_quota`, `idempotency_key_required` | `contracts/openapi/hub-admin.v1.yaml#/components/schemas/Integration`; discovery `hub-public.v1.yaml#/components/schemas/IntegrationSummary` |

## Behaviour & rules
1. Key scope: tenant + integration (contract `IdempotencyKey`); the operation is part of the stored body hash. Keys from different tenants never collide.
2. States in Redis: `in_progress(request_id)` → `completed(status, body, request_id)`. Completed TTL 24 h.
3. Duplicate while `in_progress`: sync → 409 `IDEMPOTENCY_CONFLICT` (contract); async → return the original 202 with the original `request_id`.
4. Duplicate after completion → stored result + `X-Hub-Idempotent-Replay: true`, no vendor call, audit `policy.applied` noting replay.
5. Failed (non-success terminal) results are not stored as completed; the next request with the key runs normally.
6. Key + body hash stored; reuse with a different body is rejected with 409 `IDEMPOTENCY_CONFLICT` (US-1004).
7. Mandatory where the integration has `moves_money_or_quota: true`: missing key → 400 `IDEMPOTENCY_KEY_REQUIRED`.
8. Vendor-side idempotency keys are forwarded where the vendor supports them (DESIGN §11.3), derived from the Hub key.
9. Redis budget: 1.0 ms p99 (DESIGN §18.6).

## Data model impact
Redis keys only; `requests.idempotency_key` column for search (proposed).

## Config impact
Integration flag `moves_money_or_quota` (admin `createIntegration` / `updateIntegration`); it replaces the proposed capability flag `x-side-effects` (PRD Q9, resolved by the contract).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `idempotency_replays_total{integration}` | |
| Metric | `hub_overhead_ms{stage="idempotency"}` | |
| Metric | `idempotency_redis_errors_total` | |

## Security & PII
Keys may embed business ids; they are stored hashed in Redis and shown in audit as provided (they are caller-chosen identifiers, not secrets). Skill guidance: never put PII in keys.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Redis unavailable, key present, money/quota capability | Fail closed | 500 `INTERNAL_ERROR` (retryable; contract catalogue) |
| Redis unavailable, other capability | Proceed without dedupe, count metric | Normal |
| Two nodes race on same key | `SET NX` decides; loser treated as duplicate | One vendor call |

## Testing
- **Integration**: duplicate submissions with the same key → one vendor call (DESIGN §19 M4 accept); concurrent duplicates.
- **Unit**: key scoping, body-hash mismatch.
- **Contract**: replay header present and response schema identical.

## Rollout & migration
Mandatory-key enforcement turned on per integration as `moves_money_or_quota` is set.

## Open questions
- ~~Behaviour for duplicates while the first is in progress on the sync path~~ Resolved by the contract: immediate 409 `IDEMPOTENCY_CONFLICT`.
- Redis-down policy (fail open vs closed) is not in DESIGN.
- ~~Key/body mismatch handling is not in DESIGN.~~ Resolved by the contract: 409 `IDEMPOTENCY_CONFLICT`.

## Tickets
HUB-110 – HUB-139 (EP-05, filter `feature: F-10`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
