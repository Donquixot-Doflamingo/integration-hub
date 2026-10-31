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
- Auth: OIDC authorization code + PKCE (HUB-282, pending the Hub issuer); until then a dev provider (role picker) in mock builds or with `PUBLIC_AUTH_MODE=dev`. `getMe` drives which actions render (`RoleGate`, fails closed when `/me` fails).
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
`PUBLIC_ADMIN_API_BASE` (runtime, default `/admin/v1` on the console origin).
Auth (HUB-253): `PUBLIC_AUTH_MODE` (`dev` selects the role-picker provider; anything else OIDC), `HUB_SESSION_SECRET` (session cookie HMAC key, 32+ chars, required for OIDC), `HUB_OIDC_ISSUER`, `HUB_OIDC_CLIENT_ID`, `HUB_OIDC_REDIRECT_URI` (OIDC placeholder; wiring in HUB-282).
Build-time `HUB_API_MOCKING=true` (`npm run dev:mock` / `build:mock`) runs the console on MSW contract mocks with client-side rendering; normal builds contain no mock code.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | Client errors to console + optional reporting endpoint | no payloads |
| Metric | Admin API metrics cover usage (F-16) | |

## Security & PII
Session in an HMAC-signed httpOnly SameSite=Lax cookie set by the login action; tokens stay server side (never in page data or web storage); post-login `next` restricted to same-origin paths; CSP strict; no payload caching in the browser beyond the session; API key plaintext shown once.

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

## Framework version and advisories (HUB-279)
Decision: **stay on SvelteKit 2.70.3** for now ([ADR-0009](../tech/adr/0009-console-stays-on-sveltekit-2.md)); upgrade under HUB-281 by 2027-01-31 at the latest.

| Advisory (fixed only in Kit 3.0.0) | Needs | This console | Status |
| --- | --- | --- | --- |
| GHSA-pfhg-q3qv-9q6r, open redirect via `use:enhance` (Low) | a route accepting `//`-prefixed paths + enhanced form actions | No form actions yet; HUB-259+ will add `use:enhance` | Mitigated: `src/hooks.server.ts` answers 404 for every `//` path before routing (advisory workaround) |
| GHSA-27mw-82g8-phjj, XSS in prerendered pages via untrusted `cache-control` (Low) | prerendering + `setHeaders` with untrusted `cache-control` | Nothing is prerendered; no request-derived `setHeaders` | Not applicable; rule: never enable `prerender` on 2.x |

Other `npm audit` findings (minimatch under ESLint/typescript-eslint/openapi-typescript, deepmerge under the build, `cookie <0.7.0` under Kit) are dev/build-time only or not reachable (fixed cookie names). `npm audit --omit=dev`: 0 findings.
Why not upgrade now: Kit 3 needs TypeScript 6, but `openapi-typescript` 7.13.0 (our API type generator) only supports TypeScript 5, so the install needs an override; Kit 3 also moves config out of `svelte.config.js`, removes `$lib` and changes `$env`, and its 3.0.0 is one month old.

## Shipped so far
| Ticket | What shipped |
| --- | --- |
| HUB-250 | `frontend/` scaffold: SvelteKit 2 + Svelte 5 (runes enforced) + TypeScript strict, adapter-node (SSR), ESLint/Prettier, Vitest + Testing Library, Playwright; `.github/workflows/frontend.yml` |
| HUB-251 | `npm run gen:api` -> `src/lib/api/schema.d.ts`; `adminClient()` (openapi-fetch, bearer token, `X-Hub-Request-Id`, typed `HubApiError` from the envelope); lint guard against hand-written API types; CI freshness check |
| HUB-252 | MSW handlers for all 79 admin operations from contract examples (`contract.gen.ts`, generated); fixtures validated with Ajv 2020-12; `npm run dev:mock` with no backend |
| HUB-253 | Auth behind an `AuthProvider` interface: dev role-picker provider (mock / `PUBLIC_AUTH_MODE=dev`), OIDC placeholder (HUB-282); signed httpOnly session cookie; `/login`, `POST /logout`; redirect to login with `next`; `/me`-based `access` and `RoleGate` for mutation controls |
| HUB-255 | Shared states: `AsyncView` (keeps data while refetching), `ListView`, `ErrorPanel` (code, message, hint, details, retry helps, request id + copy), `EmptyState` (none vs filtered), `Skeleton`, `ErrorPage` for both error routes; a test enforces their use on every page |
| HUB-256 | Integrations list: search, owner tenant (a select of own tenants for tenant viewers), capability, paused, vendor and account filters plus page size in the URL; removable chips and Clear all; cursor paging with Previous/Next kept in the URL; Paused / No active version / draft marked in text |
| HUB-279 | SvelteKit 3 evaluated; stays on 2.70.3 (ADR-0009) with a `//` path guard in `hooks.server.ts`; upgrade filed as HUB-281 |
| HUB-254 | App shell: sidebar (8 sections), breadcrumbs, `/me` user menu, light/dark/system theme, mock-mode band, drawer below 1024 px; each section loads its list operation with loading/empty/error states |

Contract gaps found while building (see `frontend/CCR-NOTES.md`): CCR-001 (`X-Hub-Request-Id` not declared on admin responses, HUB-278), CCR-002 (no active-version filter on `listIntegrations`, HUB-283).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | Status in-progress; shipped HUB-250, 251, 252, 254; config and CCR-001 noted | frontend-engineer |
| 2026-10-31 | HUB-279: framework version decision and advisory assessment | frontend-engineer |
| 2026-10-31 | HUB-253: auth design, config and security notes | frontend-engineer |
| 2026-10-31 | HUB-255: shared state components | frontend-engineer |
| 2026-10-31 | HUB-256: integrations list; CCR-002 | frontend-engineer |
