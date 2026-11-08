# F-16 — Admin API, RBAC & change log

## Summary
The control plane API `/admin/v1` over every registry and operational resource, protected by OIDC and RBAC (`admin | editor | viewer | tenant_viewer`), with every mutation recorded in `change_log`. It backs `hubctl` (F-17), the admin console (F-19) and `hubmcp` admin tools (F-20) (DESIGN §16.1).

## Status
`shipped` — 2026-11-02 (M7, `docs/milestones/M7.md`): all 79 operations of `hub-admin.v1.yaml`.

## Owner
`backend-engineer`; `contract-steward` owns `contracts/openapi/hub-admin.v1.yaml`.

## Milestone / Epic
M7 · EP-08 · HUB-200–229 (shared with F-17). Endpoints needed earlier (snapshots, transformer dry-run, DLQ, breakers) land with their features and are completed here.

## Problem
Without an API, every operation (publish, pause, replay, rotate) is SQL or a redeploy, with no record of who did what.

## Goals / Non-goals
**Goals**
- Resources: `vendors` (endpoints with request schemas), `accounts` (test-connection, rotate, lifecycle; none for auth `none` vendors), `transformers` (dry-run, run samples), `integrations` (versions, publish, rollback, diff, pause, weights), `tenants`, `api-keys`, `registrar-endpoints`, `snapshots`, `requests` (search, timeline, unseal), `dlq` (list, replay, purge), `breakers` (state, force-open/close).
- OIDC (stub in dev), RBAC per role.
- `change_log(actor, action, object, before, after, reason, at)` for every mutation; `reason` required.
- Pagination, filtering, ETags for optimistic concurrency.

**Non-goals**
- Tenant self-service (Release 2); unseal implementation (F-22; endpoint reserved).

## User stories
- [US-1601](../product/USER_STORIES.md) — Manage the registry by API
- [US-1602](../product/USER_STORIES.md) — RBAC
- [US-1603](../product/USER_STORIES.md) — Change log
- [US-1604](../product/USER_STORIES.md) — Breaker force and pause

## UX / API surface
| Area | Representative operationIds in `contracts/openapi/hub-admin.v1.yaml` (74 in 2.0.0; the `/capabilities*` operations were removed by ADR-0023) |
| --- | --- |
| Vendors / accounts | `listVendors`, `createVendor`, `createVendorEndpoint`, `listAccounts`, `createAccount`, `testAccountConnection`, `addNextCredential`, `promoteNextCredential`, `deleteOldCredential`, `transitionAccountLifecycle` |
| Transformers | `listTransformers`, `getTransformer`, `createTransformerVersion` (samples checked: `goldens_pass`, `endpoint_schema`), `dryRunTransformer`, `runTransformerGoldens` |
| Integrations | `listIntegrations` (admin view), `createIntegration` (`interactive`), `createIntegrationDraft` (operations with request schemas), `validateIntegrationVersion`, `publishIntegrationVersion`, `rollbackIntegration`, `diffIntegrationVersions`, `pauseIntegration`, `resumeIntegration`, `updateIntegrationWeights` |
| Tenants | `createTenant`, `createApiKey`, `rotateApiKey`, `revokeApiKey`, `createRegistrarEndpoint`, `rotateRegistrarSecret` |
| Ops | `getLatestSnapshot`, `searchRequests`, `getRequestTimeline`, `unsealRequest` (R2), `listDeadLetters`, `replayDeadLetters`, `purgeDeadLetters`, `listBreakers`, `forceOpenBreaker`, `forceCloseBreaker`, `clearBreakerForce` |
| Audit / identity | `listChangeLog`, `getChangeLogEntry`, `getMe` |

## Behaviour & rules
| Role | Read | Mutate registry | Publish / rollback / weights | Ops (pause, breaker force, DLQ) | Unseal |
| --- | --- | --- | --- | --- | --- |
| `admin` | all | yes | yes | yes | with separate permission (R2) |
| `editor` | all | yes | yes | pause, DLQ replay | no |
| `viewer` | all (masked payloads) | no | no | no | no |
| `tenant_viewer` | own tenant's integrations and requests | no | no | no | no |

1. Admin API is off the request path (DESIGN §17: 2 replicas).
2. Every mutation writes `change_log` in the same transaction as the snapshot it publishes (HUB-451); rejected without `reason`. A change that archives nothing (identical content) is logged without `snapshot_version`.
3. Destructive actions (DLQ purge, retire, breaker force) require an explicit confirmation field in the body.
4. Responses never include credential values or signing secrets.
5. `tenant_viewer` responses hide vendor/account names (proposed, keeps the caller invariant for tenant users).
6. Mutations through the API, `hubctl` and the console all go through the same publish pipeline (DESIGN §8.2).

## Data model impact
`change_log` table (DESIGN §18.2); role bindings (OIDC group → role mapping, config).

## Config impact
OIDC issuer, audience, group → role mapping.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `admin_requests_total{operation,status}`, `admin_latency_ms` | |
| Log | `admin_mutation` | actor, action, object id |
| Trace | span per admin operation | |

## Security & PII
OIDC tokens validated (issuer, audience, expiry); CSRF not applicable for bearer tokens; console uses PKCE (F-19). Payload views respect audit policy.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Admin plane down | Gateways keep last snapshot | Nothing |
| Concurrent edits | ETag mismatch → 412 | — |
| Concurrent edits inside one replica | Serialised by an in-process semaphore that holds no database connection (HUB-451); across replicas the archive transaction's advisory lock plus a base-version check re-runs the loser's edit on the newer tree | Nothing, or 409 after 5 lost races |
| Change-log write fails | The snapshot and its change-log row commit in one transaction, so neither exists; credentials written before the edit are rolled back | 500 |
| Newest archived snapshot cannot be loaded (corrupt or missing blob, validation tightened) | Reads and edits are refused (nothing can start from it); nodes keep serving their snapshot | 500 INTERNAL_ERROR naming the snapshot version and the cause, `meta.cause = snapshot_unloadable`, `meta.snapshot_version` (the contract has no 503 or dedicated code) |

## Testing
- **Contract**: every response validated against `hub-admin.v1.yaml`.
- **Unit**: RBAC matrix table test per operation.
- **Integration**: every mutation appears in `change_log` (DESIGN §19 M7 accept).

## Rollout & migration
Contract frozen first; endpoints implemented across M1–M7, completed here.

## Open questions
- Should `tenant_viewer` see vendor/account names? DESIGN doesn't say; proposed: no.
- Unseal permission model (separate role vs permission) — F-22.

## Tickets
HUB-200 – HUB-229 (EP-08, filter `feature: F-16`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-02 | Shipped in M7 (see docs/milestones/M7.md) | backend-engineer |
| 2026-11-07 | Admin API 2.0.0: no capability resources; endpoint request schemas, samples, operations (ADR-0023, HUB-437) | docs-keeper |
| 2026-11-08 | Publish deadlock, atomic snapshot + change log, unloadable-snapshot error, archive guards (HUB-451) | backend-engineer |
