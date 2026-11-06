# F-08 — Audit trail

## Summary
A lossless, per-request audit: every pipeline stage emits an event with the exact bytes it saw (subject to the integration's payload policy), plus attempts and one final record. Writes are buffered and never block the hot path (DESIGN §13).

## Status
`in-progress` — 2026-10-31. Sync events, writer and full/masked/metadata_only shipped in M3 (`docs/milestones/M3.md`); sealed (HUB-208), spill over 64 KB (HUB-098) and async events (HUB-117) remain.

## Owner
`backend-engineer`.

## Milestone / Epic
M3 · EP-04 · HUB-080–109. Sealed policy and unseal are Release 2 (F-22).

## Problem
Nobody can answer "what exactly did we send to the bureau for this loan, and what did it return?" across teams today. On-call cannot explain failures; compliance cannot prove what happened.

## Goals / Non-goals
**Goals**
- Events of DESIGN §13.1: `request.received`, `request.validated`, `integration.resolved`, `request.transformed`, `vendor.sent`, `vendor.responded`, `response.transformed` / `response.transform_failed`, `request.completed|failed|expired`, plus `request.persisted`, `request.parked`, `request.accepted`, `policy.applied`, `callback.delivered`.
- Policies `full`, `masked` (default, via `x-pii`), `metadata_only` (DESIGN §13.2).
- Always-kept fields: sha256 + byte size of every payload, versions, account, host, attempts, breaker state, waits (DESIGN §13.3).
- Buffered non-blocking writer with bounded channel and drop counter.
- Read model for timelines (consumed by F-16 admin API and F-19 console).

**Non-goals**
- `sealed` + unseal (F-22); cold storage tiering to object storage (M8 hardening or Release 2); subject deletion job (F-18, HUB-233, ADR-0012: it purges event payloads to `omitted` and keeps sha256, bytes and meta).

## User stories
- [US-0801](../product/USER_STORIES.md) — Complete timeline per request
- [US-0802](../product/USER_STORIES.md) — Secrets never stored
- [US-0803](../product/USER_STORIES.md) — Masking via `x-pii`
- [US-0804](../product/USER_STORIES.md) — Audit never blocks the hot path

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | `GET /admin/v1/requests` (search), `GET .../{request_id}`, `GET .../{request_id}/timeline`, `GET .../{request_id}/attempts` | `contracts/openapi/hub-admin.v1.yaml#searchRequests`, `#getRequest`, `#getRequestTimeline`, `#listRequestAttempts` |
| Event schema | Audit event types (timeline view) | `contracts/openapi/hub-admin.v1.yaml#/components/schemas/TimelineEventType`, `#/components/schemas/TimelineEvent`. Stored audit-event payload schemas are not in `contracts/` (internal); to be added via CCR if another consumer needs them |

## Behaviour & rules
1. `request.received` stores raw body + headers with the tenant key replaced by its hash.
2. `integration.resolved` records integration id + version, vendor, account, transformers, policy, snapshot version, breaker state.
3. `request.transformed` stores the exact vendor request with secrets redacted.
4. `vendor.responded` stores the raw body verbatim up to 64 KB; the remainder goes to object storage with a `payload_ref`.
5. `masked`: fields tagged `x-pii` are masked/hashed at every stage; sha256 and size are computed on the **original** bytes so losslessness stays verifiable.
6. `metadata_only`: no bodies at any stage (default for LLM, DESIGN §15).
7. Sync hot path: events are non-blocking channel sends; full buffer → drop + `audit_events_dropped_total` + warn alert. Terminal records for async paths are written synchronously within the outbox transaction (DESIGN §13.4).
8. Every attempt writes `request_attempts` with `integration_version` and `account_id` (DESIGN §6.3).
9. Retention: hot 30 days, partitions by day (sealed 90 days in Release 2); metadata 1 year.

## Implementation notes (M3)
- `internal/audit`: `Trail` per request (seq, sha256 + bytes of the bytes seen, policy applied at write), `Writer` (bounded channel, `MaxRows`/`FlushEvery` batches, drain on stop, partitions yesterday..+7 days at start and every 6 h), `PGStore` (COPY events and attempts, upsert records).
- `masked`: canonical stages masked by x-pii pointer (`*` = each array item); vendor stages masked by the tagged values found in the canonical body. Identifiers, IPs and device ids: HMAC-SHA256 with a per-tenant key derived from `HUB_AUDIT_MASK_KEY`; email: HMAC of the local part, domain kept; phone, government id, financial: last 4; name, address: `***`; free_text: `{masked, sha256, bytes}`.
- Credential values are added to the trail when the cache returns them and replaced by `***` in every later payload; `request.transformed` is recorded before the auth scheme injects headers.
- Grep test: `TestE2EErrorClasses` → `checkStoredAudit` (HUB-096).

## Data model impact
`requests`, `request_events(request_id, seq, type, at, payload|payload_ref, sha256, bytes)`, `request_attempts` — partitioned by day (DESIGN §18.2).

## Config impact
Integration version `audit: { payloads, retention }`; capability schema `x-pii` tags.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `audit_events_dropped_total` | warn on any |
| Metric | `audit_buffer_fill_ratio`, `audit_write_failures_total` | buffer level; failed batch writes (their rows also count as `write_error` drops) |
| Metric | `hub_overhead_ms{stage="audit"}` | budget 0.1 ms with metrics |
| Alert | any audit events dropped | warn (DESIGN §16.3) |

## Security & PII
Grep test proves no secret in stored events (DESIGN §19 M3). Admin read access by RBAC; `tenant_viewer` limited to own tenant. Masking applies before persistence, never at read time.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Postgres slow / down (sync path) | Buffer fills then drops; alert | Nothing (latency unaffected) |
| Partition missing | Writer creates ahead-of-time partitions daily | — |

## Testing
- **Unit**: masking by `x-pii` path; sha256 on original bytes; truncation at 64 KB.
- **Integration**: full timeline for sync request; drop counter under a blocked writer.
- **Security**: grep test across all stored events after the e2e suite.

## Rollout & migration
Partition maintenance job ships with the first audit migration.

## Open questions
- Retention for `full` and `metadata_only` (DESIGN gives 30 d masked, 90 d sealed only) — PRD Q6.
- Is dropping audit events acceptable for regulated capabilities, or must they block/fail closed?

## Tickets
HUB-080 – HUB-109 (EP-04, filter `feature: F-08`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
