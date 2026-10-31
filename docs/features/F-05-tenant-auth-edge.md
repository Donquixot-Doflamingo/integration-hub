# F-05 — Tenant auth, grants & edge validation

## Summary
The first gateway stages: authenticate the tenant key, check owner/grant, apply body limits, validate the canonical body against the pre-compiled capability schema, and mint the request id. Also the additive caller discovery endpoints (PRD DV-3) so callers, `hubmcp` and the Claude skill can find what to call (DESIGN §4.2, §5.1).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`; `contract-steward` for the discovery endpoints.

## Milestone / Epic
M3 · EP-04 · HUB-080–109 (shared with F-06, F-07, F-08).

## Problem
Bad requests must fail immediately and cheaply, never after queueing (DESIGN §5.1). Callers also need a vendor-free way to learn which integrations they may call and what body to send.

## Goals / Non-goals
**Goals**
- Bearer tenant key → hash lookup in the snapshot (no DB). Keys shown once, stored hashed, rotatable with overlap (DESIGN §16.5).
- Owner/grantee check (`integration_grants`); API key scopes `invoke`, `dry_run`, `requests_read`, each optionally narrowed to one integration as `<scope>:<integration_id>` (`hub-admin.v1.yaml#/components/schemas/ApiKeyScope`). `dry_run` is not granted by default. A missing scope gives 403 `FORBIDDEN`, reason `missing_scope`.
- Body size limits: 1 MB default, 4 MB for LLM capabilities.
- Schema validation with JSON-pointer details; `VALIDATION_FAILED` never deferred to a queue.
- Discovery: `GET /v1/integrations`, `GET /v1/integrations/{id}`, `GET /v1/capabilities/{capability}`.

**Non-goals**
- mTLS tenant identity (optional per DESIGN §4.2; Release 2). Tenant rate limiting is F-13 (same stage, separate feature).

## User stories
- [US-0501](../product/USER_STORIES.md) — API keys shown once
- [US-0502](../product/USER_STORIES.md) — Owner and grantee enforcement
- [US-0503](../product/USER_STORIES.md) — Discover what I can call
- [US-0504](../product/USER_STORIES.md) — Validation errors at the edge

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | `GET /v1/integrations` | `contracts/openapi/hub-public.v1.yaml#listIntegrations` |
| Caller API | `GET /v1/integrations/{id}` | `#getIntegration` |
| Caller API | `GET /v1/capabilities/{capability}` | `#getCapability` |
| Admin API | Tenants and API keys (create/rotate/revoke) | `contracts/openapi/hub-admin.v1.yaml#createTenant`, `#updateTenant`, `#createApiKey`, `#rotateApiKey`, `#revokeApiKey` |

Discovery item shape (no vendor/account/version):
```json
{ "integration_id": "int_evt_brandA", "capability": "events.track@v1", "operations": ["default"],
  "delivery_mode": "wait", "interactive": false, "paused": false,
  "moves_money_or_quota": false, "idempotency_key_required": false,
  "description": "Brand A product analytics events" }
```

## Behaviour & rules
1. Stage order (DESIGN §18.4): recover → request id + trace → auth → grant → tenant rate limit → body limit → schema validate.
2. Unknown/revoked key → 401 `UNAUTHORIZED`. Known key, not owner/grantee → 403 `FORBIDDEN`. Unknown integration → 404 `INTEGRATION_NOT_FOUND`.
3. Discovery returns only integrations the tenant owns or is granted; an integration outside that set is 404, not 403, to avoid enumeration.
4. Discovery responses are built from the snapshot; fields are an allowlist (`integration_id`, `capability`, `operations`, `delivery_mode`, `interactive`, `paused`, `moves_money_or_quota`, `idempotency_key_required`, `description`; `hub-public.v1.yaml#/components/schemas/IntegrationSummary`). Contract tests assert no `vendor`, `account`, `version`, `target` keys appear (DESIGN §0).
5. `GET /v1/capabilities/{capability}` returns request/response JSON Schemas per operation, with `x-pii` tags preserved and `interactive` flag, but no vendor schema.
6. Validation happens before any outbox write in every mode. Errors list `pointer` + `reason`.
7. Each stage publishes its own duration (`hub_overhead_ms{stage}`) and stays < 100 µs p99 except validation (0.8 ms budget; DESIGN §18.6).

## Data model impact
`tenants`, `api_keys(key_hash, scopes[], rate_limit)`, `integration_grants` (DESIGN §18.2). Optional `integrations.description` column (new, for discovery).

## Config impact
Integration YAML gains optional `description` (used only by discovery). Tenant YAML: keys by hash, scopes.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_overhead_ms{stage="auth"|"grant"|"body_limit"|"validate"}` | |
| Metric | `hub_requests_total{code="UNAUTHORIZED"|"FORBIDDEN"|"VALIDATION_FAILED"}` | |
| Log | `auth_failed` | key hash prefix only, never the key |
| Trace | span `edge` with attributes tenant, integration | |

## Security & PII
Keys hashed (constant-time compare via hash map lookup); key never logged; tenant key replaced by its hash in `request.received` (DESIGN §13.1). Discovery leaks no vendor identity.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Snapshot not loaded | `/readyz` 503, no traffic | LB retry |
| Body too large | Rejected before parse | 413 `PAYLOAD_TOO_LARGE` |
| Schema invalid | 400 with pointers | `VALIDATION_FAILED` |

## Testing
- **Unit**: auth/grant matrix tables; validation error pointers.
- **Integration**: key rotation overlap.
- **Contract**: discovery responses validated against `hub-public.v1.yaml`; negative test asserting no vendor fields.

## Rollout & migration
Discovery endpoints are additive (minor contract bump).

## Open questions
- ~~HTTP status for oversized bodies (413 vs 400)~~ Resolved by the contract: 413 `PAYLOAD_TOO_LARGE`.
- ~~Do paused integrations appear in discovery?~~ Resolved by the contract: yes, owned or granted integrations are listed with `paused: true` (PRD Q10).
- ~~Should discovery expose per-integration "idempotency required"?~~ Resolved by the contract: `moves_money_or_quota` and `idempotency_key_required` on every discovery item (integration-level, not derived from the capability).

## Tickets
HUB-080 – HUB-109 (EP-04, filter `feature: F-05`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
