# F-25 — Tenants from ISFC

> Planned (EP-15). Nothing in this document is built until its ticket is `done`. Plan: [`docs/tech/AUTH_PLAN.md`](../tech/AUTH_PLAN.md) §2.1, §6. ADRs: [0027](../tech/adr/0027-tenants-owned-by-isfc-hub-keeps-a-synced-copy.md), [0028](../tech/adr/0028-caller-auth-with-isfc-backend-clients-checked-in-memory.md).

## Summary
ISFC owns tenants. The Hub keeps a read-only synced copy plus its own settings; a tenant linked to the Hub in ISFC appears in the Hub as "setup pending", and the tenant admin finishes the Hub-side setup in the console.

## Status
`in-progress` (docs and plan only) — 2026-11-11.

## Owner
`backend-engineer` (reconciler, caller-key sync), `frontend-engineer` (console), `contract-steward`.

## Milestone / Epic
post-M8 · `EP-15` · HUB-460–HUB-499 (this feature: HUB-460, HUB-464, HUB-466, HUB-480 to HUB-485, HUB-498).

## Problem
Tenants are created in the Hub and again in ISFC, and access is granted twice. Platform admin persona; tenant admin persona.

## Goals / Non-goals
**Goals**
- Create, update and disable Hub tenants from ISFC state; never delete.
- Keep console and edge working when ISFC is down (local copy, no hot-path call).
- Immutable derived Hub tenant ids; link by ISFC UUID; link existing tenants with `hubctl tenants link`.
- Sync caller keys (hashed) for F-24's in-memory caller check.

**Non-goals**
- Storing user-to-tenant roles (ISFC resolves them per request).
- A console that calls ISFC on every page view.
- Moving integrations, grants, rate limits or callbacks to ISFC (Hub-only settings).

## User stories
- [US-2501](../product/USER_STORIES.md#us-2501--a-tenant-appears-in-the-hub-after-it-is-linked-in-isfc) — A tenant appears in the Hub after it is linked in ISFC
- [US-2502](../product/USER_STORIES.md#us-2502--a-tenant-is-disabled-when-it-is-deactivated-in-isfc) — A tenant is disabled when it is deactivated in ISFC
- [US-2503](../product/USER_STORIES.md#us-2503--a-caller-team-onboards-with-an-isfc-backend-client) — A caller team onboards with an ISFC backend client
- [US-2403](../product/USER_STORIES.md#us-2403--tenant-admin-finishes-setup-of-their-tenant) — Tenant admin finishes setup (F-24)

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | `Tenant.isfc{tenant_id,key,name,status,linked,synced_at}`, `Tenant.setup` (read-only) | planned, HUB-469 |
| Admin API | `syncTenants`, `syncTenant` ("Sync now"); `createTenant` answers 409 `tenant_managed_by_isfc` | planned, HUB-469 |
| Console | tenant list and detail with ISFC fields read-only, "setup pending" badge, last synced, "Sync now", "Complete setup" | — |
| CLI | `hubctl tenants link <hub-id> <isfc-uuid>` plus an unmatched report | — (HUB-498) |

## Behaviour & rules
1. `HUB_TENANT_SOURCE=local` (default) = today's behaviour. `isfc` starts the reconciler (worker role, leader only) and blocks `createTenant`.
2. Lifecycle: new linked ACTIVE tenant: `setup: pending`, `active`; setup finished: `complete`; INACTIVE or unlinked: `disabled` (401 at the edge); re-activated: `active` again, a Hub `suspended` is kept; the Hub never deletes.
3. Hub id: lower-cased ISFC key, padded to 3 characters with `-t`, then `-2`, `-3` on collision; immutable. Pattern `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`. The link is the UUID.
4. Triggers: ISFC events (SQS behind an interface, D8), ticker (`HUB_ISFC_RECONCILE_INTERVAL`, 5 min), "Sync now". Events only trigger; the reconcile pulls full state.
5. ISFC unreachable: keep the last good state, count the failure, alert after 3 in a row; never disable on a failed fetch.
6. The platform tenant (F-24) is skipped. Changes are logged with actor `isfc-sync`; caller-key changes update `hub.isfc_caller_keys` and notify over Redis without publishing a snapshot.
7. `HUB_ISFC_SYNC_DRY_RUN=true` logs what would change.
8. Interim (D5, until ISFC-CR-1): one caller client per tenant, with a Hub-side list of ISFC caller client ids.

## Data model impact
Tenant YAML: `isfc` block and `setup`. Postgres (goose migration, HUB-480): `hub.isfc_sync_runs(id, trigger, started_at, finished_at, result, changes jsonb)`, `hub.isfc_caller_keys(isfc_key_id, key_hash, caller_client_id, tenant_ids uuid[], permissions text[], status, updated_at)`.

## Config impact
`tenants/<id>.yaml` gains `isfc` (written only by the reconciler) and `setup: pending|complete`; old files load unchanged. Env: `HUB_TENANT_SOURCE`, `HUB_ISFC_RECONCILE_INTERVAL`, `HUB_ISFC_SYNC_DRY_RUN`, `HUB_ISFC_*` (TECH_SPEC §8.1).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_isfc_sync_runs_total`, `hub_isfc_sync_failures_total`, `hub_isfc_sync_lag_seconds` | `trigger=event\|tick\|manual` |
| Metric | `hub_isfc_caller_keys` (gauge) | — |
| Log | `isfc.sync` | run id, created/updated/disabled counts |
| Alert | `HubIsfcSyncStale` | `hub_isfc_sync_lag_seconds` > 900 (warn) |

Also in `docs/tech/OBSERVABILITY.md` §15.

## Security & PII
The reconciler uses an S2S key with `TENANT:READ`, `CLIENT:READ` (ISFC-CR-5). ISFC API keys are plaintext in ISFC (gap G4): the Hub hashes on read and never stores or logs plaintext. No vendor identity in any field.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| ISFC unreachable | last good state kept; alert after 3 failed runs | nothing changes |
| Lost, duplicate or reordered event | harmless; the next reconcile pulls full state | nothing |
| Key revoked in ISFC | 401 after the next reconcile (≤ interval without events) | 401 |
| Tenant pending, disabled or INACTIVE | refused | 401 |

## Testing
- **Unit**: id derivation with collisions; diff for every lifecycle row; `mockisfc` only, never a real ISFC.
- **Integration** (real Postgres and Redis): reconcile lifecycle; revoke in `mockisfc` gives 401 at the edge after one reconcile; migration up and down.
- **Frontend**: MSW handlers, vitest, axe.

## Rollout & migration
Shadow mode (`HUB_ISFC_SYNC_DRY_RUN=true`), link existing tenants, turn sync on. Rollback: `HUB_TENANT_SOURCE=local`; ISFC fields are additive and nothing is deleted. AUTH_PLAN §15.

## Open questions
- Which ISFC permission reads `GET /api/v1/client/configuration/integration-hub` (HUB-463/464, ISFC-CR-5).
- ISFC events (CR-2) timeline; until then 5-minute polling.

## Tickets
`HUB-460`, `HUB-464`, `HUB-466`, `HUB-480`–`HUB-485`, `HUB-498` (see `docs/tickets/TICKETS.md`, filter by `feature: F-25`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-11-11 | Created from AUTH_PLAN (planned) | docs |
