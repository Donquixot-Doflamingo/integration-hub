# ADR-0027: Tenants are owned by ISFC; the Hub keeps a synced copy and its own settings

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-11 |
| Deciders | lead, product owner (decisions D5, D8 in `docs/tech/AUTH_PLAN.md` §19) |
| Tickets | HUB-466 (this ADR); implementation HUB-480 to HUB-485 |
| DESIGN refs | §5.1, §6.4, §18.5 |

Status of the behaviour described here: **planned (EP-15)**.

## Context
Tenants are created today in the Hub (`createTenant`, `tenants/<id>.yaml`). ISFC is the company's central tenant hub; a tenant is linked to the Hub by listing it in the Hub console client's `tenants` metadata. Asking operators to create a tenant twice, and grant access twice, is the problem. Constraints: the edge cannot call ISFC on the sync path (DESIGN §18.5), so it needs a local copy in its in-memory snapshot; the console list must keep working when ISFC is down; ISFC emits no tenant or client-link events today (gap G2, ISFC-CR-2); ISFC tenant keys are 1-64 characters but the Hub's tenant id is 3-64 (gap G3, `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`); ISFC API keys carry no tenant (gap G1, ISFC-CR-1).

## Options considered
1. **Console calls ISFC for every list and detail view.** Always fresh, but the console depends on ISFC being up, duplicates data, and does not help the edge.
2. **Push-only sync from events.** Fast, but ISFC has no events yet and lost or reordered events leave drift.
3. **Local read-only copy, reconciled by a pull that events only trigger.** Works today by polling; events (SQS, D8) later shorten the delay.

## Decision
Option 3.

1. **Ownership.** ISFC owns tenant id (UUID), key, name, status and which tenants are linked to the Hub. The Hub owns rate limit, `suspended`, callback endpoints, integrations, grants, display overrides and setup state. User-to-tenant roles stay in ISFC and are never stored in the Hub (the gateway resolves them per request).
2. **Copy.** `tenants/<id>.yaml` gains a read-only `isfc` block (`tenant_id`, `key`, `name`, `status`, `linked`, `synced_at`) written only by the reconciler, plus `setup: pending|complete`. The console reads the copy and shows "last synced"; a "Sync now" action runs the reconciler for one tenant.
3. **Reconciler.** Leader-only loop in the worker role. Triggers: an ISFC event consumer behind an interface (SQS, D8; a signed webhook is the alternative), a ticker (`HUB_ISFC_RECONCILE_INTERVAL`, default 5 min), and the admin operations `syncTenants` / `syncTenant`. An event only says "something changed for X"; the reconcile always pulls the full current state, so duplicate, lost and out-of-order events are harmless. Phase A polls only.
4. **Lifecycle.** First seen, linked and ACTIVE: create with `setup: pending`, `status: active` (calls are refused until setup is `complete`). Renamed: update `isfc.name` (the Hub `display_name` wins if set). INACTIVE or unlinked: `status: disabled`, 401 at the edge, nothing deleted. Re-activated or re-linked: back to `active`; a Hub-set `suspended` is kept. **The Hub never deletes a tenant**; integrations, grants and audit history still point at it. Every sync change writes a change-log entry with actor `isfc-sync`. The platform tenant (ADR-0026) is skipped by sync.
5. **Ids.** The link is always the ISFC UUID. The Hub id is derived once at creation: the lower-cased ISFC key, padded to 3 characters with `-t` if shorter, plus `-2`, `-3`, ... if taken. It never changes afterwards, even if the ISFC key does.
6. **Failure behaviour.** If ISFC is unreachable the Hub keeps the last good state, raises `hub_isfc_sync_failures_total`, and alerts after 3 failed runs in a row (`HubIsfcSyncStale`). A failed fetch never disables a tenant.
7. **`createTenant`** returns 409 `tenant_managed_by_isfc` when `HUB_TENANT_SOURCE=isfc`. Existing tenants are linked with `hubctl tenants link <hub-id> <isfc-uuid>` (HUB-498) so sync updates them instead of duplicating.
8. **Interim caller linking (D5).** Until ISFC-CR-1 (tenant on API keys) lands: one caller client per tenant, plus a Hub-side list of the ISFC caller client ids that belong to the tenant, removed after CR-1.
9. A `HUB_ISFC_SYNC_DRY_RUN=true` mode logs what the reconciler would change, for shadow mode in QA.

## Consequences
- Positive: one place to create and link tenants; the console and edge keep working when ISFC is down; no change to the hot path; rollback is flipping `HUB_TENANT_SOURCE` back to `local`.
- Negative / costs: tenant changes in ISFC appear in the Hub after up to one reconcile interval until events exist; new Postgres tables `hub.isfc_sync_runs` and `hub.isfc_caller_keys` (HUB-480); the Hub depends on ISFC-CR-5 (S2S read permissions for the reconciler); id derivation adds a mapping the operator must understand (shown in the tenant detail).
- Follow-ups: contract HUB-469 (Tenant `isfc`, `setup`, `syncTenants`, `syncTenant`, error `tenant_managed_by_isfc`); `mockisfc` (HUB-481); metrics in `docs/tech/OBSERVABILITY.md`.

## Deviation from DESIGN.md
None to a §2 decision; DESIGN assumes tenants are created in the Hub. Listed as PRD DV-8 together with ADR-0026.
