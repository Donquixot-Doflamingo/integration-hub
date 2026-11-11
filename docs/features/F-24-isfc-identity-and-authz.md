# F-24 — ISFC identity and authorization

> Planned (EP-15). Nothing in this document is built until its ticket is `done`. Plan: [`docs/tech/AUTH_PLAN.md`](../tech/AUTH_PLAN.md). ADRs: [0026](../tech/adr/0026-isfc-identity-and-per-operation-admin-policies.md), [0028](../tech/adr/0028-caller-auth-with-isfc-backend-clients-checked-in-memory.md).

## Summary
ISFC becomes the identity provider: operators sign in to the console with Email OTP or Microsoft, every admin call is authorized by one OPA policy per operation behind the ISFC gateway, and callers and automation can authenticate as ISFC backend clients. Today's JWT admin mode, dev issuer and `hk_live_` keys stay the default until switched off on purpose.

## Status
`in-progress` (docs and plan only) — 2026-11-11.

## Owner
`backend-engineer` (admin plane, edge, Rego), `frontend-engineer` (console login), `contract-steward` (contracts), lead (ISFC change requests).

## Milestone / Epic
post-M8 · `EP-15` · HUB-460–HUB-499 (this feature: HUB-460 to HUB-479, HUB-486 to HUB-498).

## Problem
The console has only a dev role-picker (HUB-282 stub); the admin API trusts a Hub-specific ES256 token; callers use Hub-specific keys. Platform admins and tenant admins must be managed in a second identity system, and the Hub cannot use the company's SSO.

## Goals / Non-goals
**Goals**
- Console login through ISFC (Email OTP, Microsoft); the dev role-picker only in mock builds and tests.
- Admin API: JWT verified by the Hub, gateway headers cross-checked, OPA policy per operation, in-process role check kept; fail closed.
- Platform tenant model (D1) and a new `tenant_admin` role (D4).
- Machine principals for CI and tools (D7: no unseal, no purge); callers as ISFC backend clients checked in memory (D2).

**Non-goals**
- Changing the caller contract (DESIGN §0): no vendor, account or version exposure.
- Google SSO, phone OTP, device binding; fixing ISFC's own security gaps (raised as ISFC-CR-1..6).
- Removing `hk_live_` keys on day one (cut-off rule in ADR-0028).

## User stories
- [US-2401](../product/USER_STORIES.md#us-2401--operator-signs-in-with-email-otp-or-microsoft) — Operator signs in with Email OTP or Microsoft
- [US-2402](../product/USER_STORIES.md#us-2402--platform-admin-acts-across-all-tenants) — Platform admin acts across all tenants
- [US-2403](../product/USER_STORIES.md#us-2403--tenant-admin-finishes-setup-of-their-tenant) — Tenant admin finishes setup of their tenant
- [US-2404](../product/USER_STORIES.md#us-2404--access-is-denied-without-a-role) — Access is denied without a role
- [US-2405](../product/USER_STORIES.md#us-2405--automation-calls-the-admin-api-with-an-isfc-key) — Automation calls the admin API with an ISFC key
- Caller side: [US-2503](../product/USER_STORIES.md#us-2503--a-caller-team-onboards-with-an-isfc-backend-client)

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | security schemes `isfcGateway`, `isfcApiKey`; `x-required-permission` per operation; role `tenant_admin` | planned, HUB-469 (`hub-admin.v1.yaml`) |
| Admin API | error codes `authz_unavailable` (503), `tenant_managed_by_isfc` (409, F-25) | planned, HUB-469 |
| Caller API | security scheme `isfcS2S` (`x-isfc-client-id` + `x-isfc-api-key`), optional `X-Tenant-Id` | planned, HUB-470 (`hub-public.v1.yaml`) |
| Console | `/login` (Email OTP, Microsoft), `/auth/callback`, tenant switcher | — |
| CLI / MCP | `hubctl login -isfc`, `HUBCTL_ISFC_CLIENT_ID/SECRET`, `HUB_TENANT_ID` for hubmcp | planned, HUB-471 (`hubmcp-tools.json`) |

## Behaviour & rules
1. `HUB_ADMIN_AUTH_MODE=jwt` (default) is today's verifier and dev issuer, unchanged; `isfc` enables the rules below.
2. In `isfc` mode: verify the Hydra JWT (RS256 and ES256 allow-list; `iss`, `exp`, `nbf`), require `sub` = `x-isfc-sub-id` and `client_id` = `x-isfc-client-id` = console client id (else 401), build the principal from `x-isfc-tenant-id` and `x-isfc-sub-attrs` (malformed: no roles), call OPA, then check `x-required-role`.
3. Platform tenant + `HUB_ADMIN`/`HUB_EDITOR`/`HUB_VIEWER` = unscoped principal; linked tenant + `HUB_TENANT_VIEWER`/`HUB_TENANT_ADMIN` = scoped to that tenant; `ISFC_ADMIN` grants nothing.
4. OPA deny = 403 `forbidden`; error or timeout (`HUB_OPA_TIMEOUT`, 200 ms) = 503 `authz_unavailable`. `HUB_OPA_DISABLED=true` is refused unless `HUB_ENV=test`.
5. Machine principal: `x-isfc-client-id` + `x-isfc-api-key` direct to the admin listener; subject `client:<id>`; refused for `unsealRequest` and `purgeDlq`.
6. Edge, `HUB_EDGE_ISFC_AUTH=on`: ISFC credentials accepted next to `hk_live_` keys; in-memory hashed lookup; `HUB:INVOKE`/`HUB:DRY_RUN`/`HUB:REQUESTS_READ` map to existing scopes; both credentials at once = 400. No OPA on `/v1`.
7. Console: PKCE S256, `state`, `nonce` in an encrypted short-lived cookie; AES-GCM session; single-flight refresh; `PUBLIC_AUTH_MODE=dev` refused outside mock or test builds. Microsoft is enabled in production only after ISFC-CR-4 (D9).

## Data model impact
`hub.isfc_caller_keys` (hashed keys, F-25 migration, HUB-480). No user tables.

## Config impact
Env: `HUB_ADMIN_AUTH_MODE`, `HUB_EDGE_ISFC_AUTH`, `HUB_OPA_URL`, `HUB_OPA_TIMEOUT`, `HUB_ISFC_*`; frontend `PUBLIC_AUTH_MODE`, `HUB_SESSION_SECRET`. See TECH_SPEC §8.1.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_admin_authz_duration_ms` | `decision=allow\|deny\|error` |
| Metric | `hub_admin_authn_failures_total` | `reason=no_token\|bad_signature\|header_mismatch\|no_roles` |
| Metric | `hub_edge_auth_total` | `method=isfc\|hub_key`, `result` |
| Metric | `hub_stage_duration_ms{stage="auth"}` | unchanged, ISFC path included |
| Log | `admin.authz` | subject, client, tenant, operation, decision, reason (no token, no attribute dump) |
| Trace | span `admin.authz.opa` | decision |
| Alert | `HubAdminAuthzUnavailable` | OPA errors > 1 % for 5 min (warn) |

Also in `docs/tech/OBSERVABILITY.md` §15.

## Security & PII
Fail closed everywhere; the Hub never trusts `x-isfc-*` without a verified JWT; admin listener reachable only from the gateway and allow-listed automation (HUB-497); API keys hashed on read, never logged; `x-isfc-api-key` redacted by value; no vendor identity in any new field. Gaps G4, G5, G6, G15 in `docs/tech/SECURITY.md`.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| OPA down or slow | admin fails closed | 503 `authz_unavailable`; `/v1` unaffected |
| ISFC down | existing sessions work until expiry; no new logins; sync keeps the last state; `/v1` unaffected | login page error |
| Forged `x-isfc-*` | rejected | 401 |
| Revoked caller key | refused after the next reconcile | 401 |

## Testing
- **Unit** (table-driven, `-race`): header parser, principal mapping, RS256 verifier, OPA client, edge ISFC auth. All against `mockisfc` and a fake OPA; **never a real ISFC or Entra**.
- **Rego**: generated RBAC matrix (`opa test`); generator `--check` in CI.
- **Integration** (real Postgres, Redis, OPA container, `mockisfc`): `TestRBACMatrix` in both modes; revoked key gives 401 after one reconcile.
- **Frontend**: vitest for provider, session crypto, callback, tenant header; MSW mock e2e unchanged.
- **Perf**: HUB-490 auth stage with 10k keys.

## Rollout & migration
Phase 0 spikes (HUB-461 to HUB-464, need a live ISFC stack), shadow mode in QA, link existing tenants, callers move team by team, cut-off after at least 6 months. Rollback = flip the flag. See AUTH_PLAN §15.

## Open questions
- OPA sidecar latency numbers (HUB-463, pending a live ISFC stack); embedded OPA only if HUB-490 proves it fits.
- Entra `tenant_id` / `client_id` for QA (D9).
- ISFC-CR-1..CR-6 answers.

## Tickets
`HUB-460`–`HUB-479`, `HUB-486`–`HUB-498` (see `docs/tickets/TICKETS.md`, filter by `feature: F-24`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-11-11 | Created from AUTH_PLAN (planned) | docs |
