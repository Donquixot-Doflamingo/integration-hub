# F-10 — Idempotency

## Summary
`Idempotency-Key` handling backed by Redis: the same key within 24 h returns the stored result of a completed request; concurrent duplicates make one vendor call. The key is mandatory for integrations that move money or vendor quota (DESIGN §4.2, §5.1, §6.3).

## Status
`in-progress` — 2026-10-31. Store done (HUB-118, `backend/internal/idempotency`); edge wiring (400 `IDEMPOTENCY_KEY_REQUIRED` already enforced by HUB-088, replay/409 responses, Redis-down policy) is the lead's M4 edge work.

## Owner
`backend-engineer`.

## Milestone / Epic
M4 · EP-05 · HUB-110–139.

## Problem
Callers retry on timeouts. Without idempotency, a retried bureau pull is billed twice and a retried payment moves money twice.

## Goals / Non-goals
**Goals**
- Redis lookup after resolve, before transform (DESIGN §5.1 step 4), only when a key is sent (skipped otherwise; DESIGN §18.5.6).
- Store final status + response (the integration's output, or the vendor's in pass-through, with its status and `X-Hub-Pass-Through`) for 24 h; replay returns it with `X-Hub-Idempotent-Replay: true` (`IDEMPOTENT_REPLAY` semantics, 200/202).
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
2. States in Redis: `in_progress(request_id)` → `completed(status, headers, body, request_id)`. In-progress lease 5 min (a crashed gateway does not pin the key); completed TTL 24 h from the first claim.
3. Duplicate while `in_progress`: sync → 409 `IDEMPOTENCY_CONFLICT` (contract); async → return the original 202 with the original `request_id`.
4. Duplicate after completion → stored result + `X-Hub-Idempotent-Replay: true`, no vendor call, audit `policy.applied` noting replay.
5. Failed (non-success terminal) results are not stored as completed; the next request with the key runs normally.
6. Key + body hash stored; reuse with a different body is rejected with 409 `IDEMPOTENCY_CONFLICT` (US-1004).
7. Mandatory where the integration has `moves_money_or_quota: true`: missing key → 400 `IDEMPOTENCY_KEY_REQUIRED`.
8. Vendor-side idempotency keys are forwarded where the vendor supports them (DESIGN §11.3), derived from the Hub key.
9. Redis budget: 1.0 ms p99 (DESIGN §18.6).

## Implementation notes (HUB-118)
- Package `backend/internal/idempotency` (layer L3: `core`, `telemetry`; ADR-0001 addition). The package doc (`doc.go`) is the contract the edge codes against.
- API: `New(redis.Scripter, *telemetry.Metrics, Options) *Store`; `Begin(ctx, Key{Tenant, Integration, Key}, bodyHash, requestID) (Outcome, error)` → `Fresh` | `InProgress{RequestID}` | `Completed{RequestID, Response{Status, Header, Body}}` | `Conflict{RequestID}`; `Complete(ctx, Key, requestID, Response) error`; `Release(ctx, Key, requestID) error`; `BodyHash(operation, body)`; `Warm(ctx)` preloads the scripts.
- Each operation is one single-key Lua script (atomic, cluster-safe, one round trip). `Complete` and `Release` are guarded by the request id that won `Begin` (`ErrNotOwner` otherwise). `Begin` again by the holder returns `Fresh` (safe to retry).
- **Replayable**: only 200 and 202 (`ErrNotReplayable` for anything else). The edge calls `Release` for every other outcome (error envelopes, panics, shutdown), per rule 5 and DESIGN §6.3. A worker that fails a request whose 202 was stored should `Release` it by request id.
- Stored headers: allowlist `Content-Type`, `Location`, `X-Hub-Served-By`, `X-Hub-Session`; `X-Hub-Request-Id` is `Outcome.RequestID`; everything else (credentials, cookies, debug `X-Hub-Integration-Version`) is dropped. Status + headers + body ≤ 1 MiB (`ErrTooLarge`, nothing written).
- Redis down: `*UnavailableError` (`errors.Is(err, ErrUnavailable)`, unwraps to the client error). Each round trip is capped at 50 ms (`Options.Timeout`, TECH_SPEC §12.2); the client needs `ContextTimeoutEnabled` and `DialerRetries: 1` (TECH_SPEC §8.1).
- Latency (compose Redis on Colima, Apple M4, `BenchmarkBegin`, 3 × 20,000 ops): replay with a 4 KB body p50 0.21 ms, p99 0.26–0.30 ms; fresh claim p50 0.19 ms, p99 0.23–0.25 ms (budget 1.0 ms).

## Data model impact
Redis keys only (TECH_SPEC §6.4): `hub:idem:<sha256(tenant, integration, key)>` hash; `requests.idempotency_key` column for search (proposed).

## Config impact
Integration flag `moves_money_or_quota` (admin `createIntegration` / `updateIntegration`); it replaced the proposed schema flag `x-side-effects` (PRD Q9, resolved by the contract).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `idempotency_hits_total{result}` | `miss`, `in_progress`, `replay`, `conflict`, `error`; replaces the proposed `idempotency_replays_total{integration}` (`result="replay"`) and `idempotency_redis_errors_total` (`result="error"`); no `integration` label to keep the hot path to pre-resolved series (HUB-118) |
| Metric | `idempotency_writes_total{op, result}` | complete/release outcomes, incl. `not_owner` (lease outlived) and `too_large` (HUB-118) |
| Metric | `redis_op_duration_seconds{op}` | `idempotency_begin`, `idempotency_complete`, `idempotency_release` (HUB-118) |
| Metric | `hub_stage_duration_ms{stage="idempotency"}` | recorded by the edge around Begin/Complete (wiring) |

## Security & PII
Keys may embed business ids; they are stored hashed in Redis and shown in audit as provided (they are caller-chosen identifiers, not secrets). Skill guidance: never put PII in keys.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Redis unavailable, key present, money/quota integration | Fail closed | 500 `INTERNAL_ERROR` (retryable; contract catalogue) |
| Redis unavailable, other integration | Proceed without dedupe, count metric | Normal |
| Two nodes race on same key | `SET NX` decides; loser treated as duplicate | One vendor call |

## Testing
- **Integration**: duplicate submissions with the same key → one vendor call (DESIGN §19 M4 accept, HUB-122); concurrent duplicates.
- **Store integration (HUB-118, compose Redis, `-race`)**: fresh → completed → identical replay (200 and 202), same key different body or operation → conflict, scope isolation, 32 concurrent `Begin` × 20 rounds → exactly one `Fresh`, lease and 24 h TTL from first claim (`PEXPIRETIME`), expired lease frees the key, release, size limit, Redis unavailable → typed error within 250 ms, no raw key or credential in Redis; `BenchmarkBegin` p99.
- **Unit (HUB-118, fake `redis.Scripter`)**: reply parsing, key scoping and hashing, input validation, replayable statuses, header allowlist round trip, error mapping, per-op timeout, metrics.
- **Contract**: replay header present and response schema identical.

## Rollout & migration
Mandatory-key enforcement turned on per integration as `moves_money_or_quota` is set.

## Open questions
- ~~Behaviour for duplicates while the first is in progress on the sync path~~ Resolved by the contract: immediate 409 `IDEMPOTENCY_CONFLICT`.
- Redis-down policy (fail open vs closed) is not in DESIGN. Taken: fail closed (500 `INTERNAL_ERROR`) for `moves_money_or_quota`, pass-through otherwise (Failure modes above); the store returns a typed `ErrUnavailable` and the edge applies the policy.
- Releasing after an ambiguous failure (504 `UPSTREAM_TIMEOUT`, the vendor may have acted) lets a retry call the vendor again; DESIGN §6.3 accepts this and relies on vendor-side keys (rule 8).
- ~~Key/body mismatch handling is not in DESIGN.~~ Resolved by the contract: 409 `IDEMPOTENCY_CONFLICT`.

## Tickets
HUB-110 – HUB-139 (EP-05, filter `feature: F-10`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | Store implemented (HUB-118): implementation notes, lease, metrics, tests; status in-progress | backend-engineer |
| 2026-11-07 | Integration wording; replayed pass-through answers (ADR-0023, HUB-437) | docs-keeper |
