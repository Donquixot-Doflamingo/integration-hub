# F-19 — Admin console (SvelteKit frontend)

## Summary
A SvelteKit (Svelte 5 + TypeScript) control-plane UI in `frontend/`, pulled forward from Release 2 (PRD DV-2) and built in parallel with backend milestones against the frozen admin contract. It covers cutover, health, investigation and recovery workflows.

## Status
`in-progress` — 2026-10-31 (scaffold, generated client, contract mocks and app shell shipped; screens follow HUB-253+).

## Owner
`frontend-engineer`.

## Milestone / Epic
Parallel to M1–M8 · EP-10 · HUB-250–299.

## Problem
On-call and integration admins cannot run incidents or cutovers efficiently from a CLI alone; compliance needs a readable timeline.

## Goals / Non-goals
**Goals** — screens:

| Screen | Main actions | Admin operationIds (`contracts/openapi/hub-admin.v1.yaml`) |
| --- | --- | --- |
| Integrations list / detail | Version history, side-by-side diff, publish, rollback, weights, pause | `listIntegrations`, `getIntegration`, `listIntegrationVersions`, `getIntegrationVersion`, `diffIntegrationVersions`, `publishIntegrationVersion`, `rollbackIntegration`, `updateIntegrationWeights`, `pauseIntegration`, `resumeIntegration` |
| Vendor & account health board | Breaker states, time in state, parked count/age, error rate; force breaker | `listBreakers`, `getBreaker`, `forceOpenBreaker`, `forceCloseBreaker`, `clearBreakerForce`, `listAccounts` |
| Request explorer | Search; timeline of events and attempts; payload views per policy | `searchRequests`, `getRequest`, `getRequestTimeline`, `listRequestAttempts` |
| DLQ console | Filter; replay / purge by filter with reason | `listDeadLetters`, `getDeadLetter`, `replayDeadLetters`, `purgeDeadLetters` |
| Transformer viewer | View YAML + fixtures; run goldens; dry-run a body | `listTransformers`, `getTransformerVersion`, `dryRunTransformer`, `runTransformerGoldens` |
| Tenants / API keys / registrar | Create tenant, create/revoke key (shown once), registrar endpoints | `listTenants`, `createTenant`, `listApiKeys`, `createApiKey`, `revokeApiKey`, `listRegistrarEndpoints`, `createRegistrarEndpoint` |
| Change log | Filter by actor/object/time; before/after diff | `listChangeLog`, `getChangeLogEntry` |

**Non-goals**
- Transformer editing (Release 2), tenant self-service, unseal UI (F-22), any caller-API usage.

## User stories
- [US-1901](../product/USER_STORIES.md) — Request explorer with timeline
- [US-1902](../product/USER_STORIES.md) — Integration detail and cutover controls
- [US-1903](../product/USER_STORIES.md) — Vendor & account health board
- [US-1904](../product/USER_STORIES.md) — DLQ console
- [US-1905](../product/USER_STORIES.md) — Transformer viewer with dry-run and goldens
- [US-1906](../product/USER_STORIES.md) — Tenants, keys, registrar, change log

## UX / API surface
- Typed client generated from `contracts/openapi/hub-admin.v1.yaml` (`npm run gen:api`); no hand-written API types (AGENTS.md §4.6).
- Auth: OIDC authorization code + PKCE; role from token claims (`getMe`) drives which actions render.
- Mutations always show a confirmation dialog with required **reason** and a preview (diff or match count).

## Behaviour & rules
1. The console is a pure client of the admin API; it never talks to Postgres/Redis.
2. Every mutating action requires a reason and shows the resulting `change_log` id.
3. Health board refreshes ≤ 5 s (polling; SSE optional later).
4. Payloads render per audit policy (`masked` shows masked values; `metadata_only` shows sha256/size only).
5. `tenant_viewer` sees only their tenant's integrations and requests and no vendor/account names (F-16).
6. Destructive actions (purge, force breaker) need typed confirmation.
7. Accessibility: keyboard navigable, WCAG 2.1 AA contrast.
8. Development against a mock server generated from the contract until backend endpoints exist.

## Data model impact
None.

## Config impact
`PUBLIC_ADMIN_API_BASE` (runtime, default `/admin/v1` on the console origin), OIDC client id/issuer (HUB-253).
Build-time `HUB_API_MOCKING=true` (`npm run dev:mock` / `build:mock`) runs the console on MSW contract mocks with client-side rendering; normal builds contain no mock code.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | Client errors to console + optional reporting endpoint | no payloads |
| Metric | Admin API metrics cover usage (F-16) | |

## Security & PII
Tokens in memory (no localStorage); CSP strict; no payload caching in the browser beyond the session; API key plaintext shown once.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Admin API down | Banner, read-only cached view cleared | — |
| Token expired | Silent refresh, else re-login | — |
| ETag conflict | Prompt to reload and re-apply | — |

## Testing
- **Unit / component**: Vitest + Testing Library.
- **E2E**: Playwright against the contract mock server, then against the real backend in M7.
- **Contract**: generated client type-checks (`npm run check`).
- **Accessibility**: axe checks in Playwright.

## Rollout & migration
Screens ship as their admin endpoints freeze; behind OIDC from the first deploy.

## Open questions
- Hosting (same origin as admin API or separate)?
- Does the health board need live streaming (SSE) in v1?

## Tickets
HUB-250 – HUB-299.

## Shipped so far
| Ticket | What shipped |
| --- | --- |
| HUB-250 | `frontend/` scaffold: SvelteKit 2 + Svelte 5 (runes enforced) + TypeScript strict, adapter-node (SSR), ESLint/Prettier, Vitest + Testing Library, Playwright; `.github/workflows/frontend.yml` |
| HUB-251 | `npm run gen:api` -> `src/lib/api/schema.d.ts`; `adminClient()` (openapi-fetch, bearer token, `X-Hub-Request-Id`, typed `HubApiError` from the envelope); lint guard against hand-written API types; CI freshness check |
| HUB-252 | MSW handlers for all 79 admin operations from contract examples (`contract.gen.ts`, generated); fixtures validated with Ajv 2020-12; `npm run dev:mock` with no backend |
| HUB-254 | App shell: sidebar (8 sections), breadcrumbs, `/me` user menu, light/dark/system theme, mock-mode band, drawer below 1024 px; each section loads its list operation with loading/empty/error states |

Contract gaps found while building: CCR-001 (`X-Hub-Request-Id` not declared on admin responses), see `frontend/CCR-NOTES.md`.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | Status in-progress; shipped HUB-250, 251, 252, 254; config and CCR-001 noted | frontend-engineer |
