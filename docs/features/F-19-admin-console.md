# F-19 — Admin console (SvelteKit frontend)

## Summary
A SvelteKit (Svelte 5 + TypeScript) control-plane UI in `frontend/`, pulled forward from Release 2 (PRD DV-2) and built in parallel with backend milestones against the frozen admin contract. It covers cutover, health, investigation and recovery workflows.

## Status
`in-progress` — 2026-10-31 (scaffold, client, mocks, shell, sign-in and role gating, shared states, integrations list, detail, history and diff shipped; publish, rollback, weights, pause, the health board, breaker force, request search and the request timeline shipped (HUB-259 to HUB-264); contract round 1 adopted (HUB-278, 283, 284); the DLQ list, entry, replay and purge (HUB-266, HUB-267) and the transformer viewer with goldens and dry-run (HUB-268, HUB-269) shipped; console round 5: tenants (HUB-270), API keys with show-once (HUB-271), callback endpoints (HUB-272), snapshots (HUB-273) and the change log (HUB-274); Playwright e2e runs in CI (HUB-280, in review until its first GitHub run);, snapshots, change log and e2e in CI follow).

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
| Transformer viewer | View YAML + fixtures; run goldens; dry-run a body | `listTransformers`, `getTransformer`, `listTransformerVersions`, `getTransformerVersion`, `dryRunTransformer`, `runTransformerGoldens` |
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
- Auth: OIDC authorization code + PKCE (HUB-282, pending the Hub issuer); until then a dev provider (role picker) in mock builds or with `PUBLIC_AUTH_MODE=dev`. `getMe` drives which actions render (`RoleGate op=` / `can(access, op)` against each operation's `x-required-role`, HUB-288; fails closed when `/me` fails). A role that cannot call an operation does not see its control at all.
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
Against a running Hub (HUB-286, [ADR-0022](../tech/adr/0022-console-same-origin-admin-proxy.md)): `HUB_ADMIN_UPSTREAM` (the console server proxies `/admin/v1/**` there with the session's token; unset = routed in front) and `HUB_DEV_OIDC_ISSUER` (dev sign-in mints the token at the admin's dev OIDC issuer). `make dev-up` sets both.
Build-time `HUB_API_MOCKING=true` (`npm run dev:mock` / `build:mock`) runs the console on MSW contract mocks with client-side rendering; normal builds contain no mock code.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | Client errors to console + optional reporting endpoint | no payloads |
| Metric | Admin API metrics cover usage (F-16) | |

## Security & PII
Session in an HMAC-signed httpOnly SameSite=Lax cookie set by the login action; tokens stay server side (never in page data or web storage); post-login `next` restricted to same-origin paths; CSP strict; no payload caching in the browser beyond the session; API key plaintext and new callback signing secrets shown once (HUB-271, HUB-272: only in `SecretOnce`, only for enhanced submissions, never page `form`, storage, URL or logs).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Admin API down | Banner, read-only cached view cleared | — |
| Token expired | Silent refresh, else re-login | — |
| ETag conflict | Prompt to reload and re-apply | — |

## Testing
- **Unit / component**: Vitest + Testing Library.
- **E2E**: Playwright against the contract mock server, then against the real backend in M7. Manual and exploratory runs against the real stack: `make dev-up` (HUB-286). In CI since HUB-280 (job `e2e` in `.github/workflows/frontend.yml`: cached Chromium headless shell pinned by the `@playwright/test` version, 8 min cap, HTML report and traces uploaded on failure).
- **Contract**: generated client type-checks (`npm run check`).
- **Accessibility**: axe checks in Playwright.

## Rollout & migration
Screens ship as their admin endpoints freeze; behind OIDC from the first deploy.

## Open questions
- `runTransformerGoldens` diffs: the contract does not say which side of a `DiffOp` is the golden. The console shows `before` as Expected and `after` as Actual (the publish diff's old/new convention); to be confirmed by the steward (description-only change).
- Hosting (same origin as admin API or separate)? For now: the console server proxies `/admin/v1` on its own origin (ADR-0022, HUB-286).
- Does the health board need live streaming (SSE) in v1?

## Tickets
HUB-250 – HUB-299.

## Framework version and advisories (HUB-279)
Decision: **stay on SvelteKit 2.70.3** for now ([ADR-0009](../tech/adr/0009-console-stays-on-sveltekit-2.md)); upgrade under HUB-281 by 2027-01-31 at the latest.

| Advisory (fixed only in Kit 3.0.0) | Needs | This console | Status |
| --- | --- | --- | --- |
| GHSA-pfhg-q3qv-9q6r, open redirect via `use:enhance` (Low) | a route accepting `//`-prefixed paths + enhanced form actions | Enhanced form actions since HUB-259 (publish, rollback, ...); no catch-all routes | Mitigated: `src/hooks.server.ts` answers 404 for every `//` path before routing (advisory workaround), still first in `handle` and unit-tested |
| GHSA-27mw-82g8-phjj, XSS in prerendered pages via untrusted `cache-control` (Low) | prerendering + `setHeaders` with untrusted `cache-control` | Nothing is prerendered; no request-derived `setHeaders` | Not applicable; rule: never enable `prerender` on 2.x |

Other `npm audit` findings (minimatch under ESLint/typescript-eslint/openapi-typescript, deepmerge under the build, `cookie <0.7.0` under Kit) are dev/build-time only or not reachable (fixed cookie names). `npm audit --omit=dev`: 0 findings.
Why not upgrade now: Kit 3 needs TypeScript 6, but `openapi-typescript` 7.13.0 (our API type generator) only supports TypeScript 5, so the install needs an override; Kit 3 also moves config out of `svelte.config.js`, removes `$lib` and changes `$env`, and its 3.0.0 is one month old.

## Mutations (HUB-259+)
- Every mutation is a SvelteKit **form action** in the route's `+page.server.ts` (`src/lib/server/actions.ts`: session check, reason check, `finish()` maps the API answer). The admin bearer token is added server side by `handleFetch`; the browser never holds it. Pages submit with `use:enhance` (ADR-0009 guard in place); nothing is prerendered.
- One `ConfirmDialog` for every dangerous action: title with the object, consequence, required reason (console minimum **10 characters**, contract minimum 3), optional typed confirmation, Cancel focused first, focus trapped, Escape closes, focus returns to the trigger. Success closes and shows a `ChangeNotice` with the change log id; failure keeps the reason and shows code, message and request id; `CONFLICT` / `PRECONDITION_FAILED` show "Someone else changed ..." with Reload.
- Concurrency: publish, rollback and weights send `expected_active_version` (409 on mismatch). Pause and resume take no precondition (pausing twice is a logged no-op). Tenant edits (HUB-270) send the ETag of the loaded object as `If-Match` (`client.ts` returns `etag` on every success), so a concurrent change answers 412 and the dialog offers Reload.
- DLQ bulk actions (HUB-267): the contract requires `reason` on dry runs, so the count step sends a fixed console text (`DRY_RUN_REASON`); dry runs return no `change_id`, so nothing reaches the change log. The DLQ bulk mocks echo `dry_run` (`change_id: null` on dry runs).
- Mock builds: the browser's MSW worker cannot answer server-side form actions, so `hooks.server.ts` answers admin calls from the same handlers (`src/lib/api/mocks/server-fetch.ts`, mock builds only; production bundles contain no mock code). The cookie `hub_mock_scenario` selects contract-derived states (`v8-active`, `paused`, `breaker-forced`) or contract error examples (`<operationId>:<status>`) for both the worker and the server; e2e uses it.

## Shipped so far
| Ticket | What shipped |
| --- | --- |
| HUB-250 | `frontend/` scaffold: SvelteKit 2 + Svelte 5 (runes enforced) + TypeScript strict, adapter-node (SSR), ESLint/Prettier, Vitest + Testing Library, Playwright; `.github/workflows/frontend.yml` |
| HUB-251 | `npm run gen:api` -> `src/lib/api/schema.d.ts`; `adminClient()` (openapi-fetch, bearer token, `X-Hub-Request-Id`, typed `HubApiError` from the envelope); lint guard against hand-written API types; CI freshness check |
| HUB-252 | MSW handlers for all 79 admin operations from contract examples (`contract.gen.ts`, generated); fixtures validated with Ajv 2020-12; `npm run dev:mock` with no backend |
| HUB-253 | Auth behind an `AuthProvider` interface: dev role-picker provider (mock / `PUBLIC_AUTH_MODE=dev`), OIDC placeholder (HUB-282); signed httpOnly session cookie; `/login`, `POST /logout`; redirect to login with `next`; `/me`-based `access` and `RoleGate` for mutation controls |
| HUB-255 | Shared states: `AsyncView` (keeps data while refetching), `ListView`, `ErrorPanel` (code, message, hint, details, retry helps, request id + copy), `EmptyState` (none vs filtered), `Skeleton`, `ErrorPage` for both error routes; a test enforces their use on every page |
| HUB-256 | Integrations list: search, owner tenant (a select of own tenants for tenant viewers), capability, paused, active version and newer-version filters (HUB-283), vendor and account filters plus page size in the URL; removable chips and Clear all; cursor paging with Previous/Next kept in the URL; Paused / No active version / draft marked in text |
| HUB-257 | Integration detail `/integrations/{id}`: status, owner, capability, versions, grants, money flag, paused reason; active spec (targets + weights, failover, delivery, breaker override, webhooks, audit, shadow); full version history newest first (all pages) with Active/Draft marked; version page `/integrations/{id}/versions/{n}`; read-only note for viewer and tenant viewer via `RoleGate` |
| HUB-258 | Version diff `/integrations/{id}/diff?from=&to=` on `diffIntegrationVersions`: side-by-side view from the unified YAML diff (old left, new right, `-`/`+` and text labels, long unchanged runs folded) and a path list from the structured ops (Added/Removed/Changed, before/after); summary line; version pickers, swap, view in the URL; default pair newest vs active; selectable from history rows |
| HUB-259 | Publish page `/integrations/{id}/publish`: YAML/JSON spec editor with line numbers; save creates (`createIntegrationDraft`) or replaces (`updateIntegrationDraft`) the draft and runs `validateIntegrationVersion`; checks shown in text + icon; diff against the active version; publish in `ConfirmDialog` with `expected_active_version`. "Roll back to vN" on published history rows (`rollbackIntegration`). 400 pointers mapped to the reason field or spec lines, 422 checks on the editor and in the dialog, 409 reload prompt; success shows new version + change log id |
| HUB-260 | "Set weights" dialog (two or more targets; live total must be 100; publish now or keep as draft; `updateIntegrationWeights` with `expected_active_version`); Pause / Resume in a separate kill-switch group with impact text (`pauseIntegration`, `resumeIntegration`); all with reason and change log id |
| HUB-261 | Health board `/health`: `listBreakers` + `listAccounts` (all pages) grouped by vendor, worst first; state (text + icon, retry/probe/forced/open since), failure rate, window calls, parked count and age, account lifecycle, updated; auto-refresh 5/10/30/60 s with pause and refresh now (`invalidate('hub:health')`, skipped while hidden); stale notice on a failed refresh; note for tenant viewers. Dead letters per account since HUB-284 |
| HUB-262 | Breaker force on the health board, admin only (`RoleGate min="admin"`): `forceOpenBreaker`, `forceCloseBreaker`, `clearBreakerForce` as form actions; dialog lists affected integrations from `getBreaker`, typed account id for open/close (re-checked server side), hold 15 min to 24 h or until cleared; auto-refresh holds while a dialog is open |
| HUB-263 | Request explorer `/requests` on `searchRequests`: tenant (own tenants for tenant viewers), integration, status (any of), error codes, vendor, account, idempotency key, UTC From/To with quick ranges, page size; all in the URL with chips and Clear all; `status`/`code` comma-separated per the contract; ranges over 31 days refused client side; cursor paging |
| HUB-264 | Request timeline `/requests/{id}`: `getRequest` + `getRequestTimeline` + `listRequestAttempts` in parallel; summary with timings and audit policy; events in `seq` order with gaps and offsets, size, SHA-256, attempt; payloads per policy (masked as returned, sealed locked placeholder, metadata-only note, truncated reference) plus a client-side guard that hides unmasked secret-looking fields; attempts with integration version and account (not for tenant viewers) |
| HUB-266 | DLQ `/dlq` on `listDeadLetters`: tenant, integration, vendor, account (not for tenant viewers), reason (code or `poison`), replayed, UTC From/To (reversed range refused client side), page size; all in the URL with chips; cursor paging. Entry `/dlq/{request_id}` on `getDeadLetter`: reason + message, replay state, timeline link, attempt history (`AttemptsTable`, shared with the timeline), canonical request and last vendor request/response via the shared `Payload` (as returned, `***` kept, secret-looking unmasked fields hidden); tenant viewers get no vendor exchange |
| HUB-267 | DLQ replay (editor) and purge (admin, set apart) by the list's filters, all pages: count step = the same operation with `dry_run: true` (form actions `countReplay` / `countPurge`), then `ConfirmDialog` with the exact count, the filters, warnings (already-replayed entries included, over 10,000 per call); purge needs the count typed, re-counted server side before the permanent call; success: result, change log id, request id, link to the replayed entries. Filters travel as `filter.*` hidden fields (the change reason is also `reason`) |
| HUB-268 | Transformers `/transformers` (kind, capability, vendor, search in the URL; chips; paging), `/transformers/{id}` (`getTransformer` + all `listTransformerVersions` pages; latest version in full; linked history with goldens state), `/transformers/{id}/versions/{n}` (`getTransformerVersion`). `transformer/VersionView`: facts, vendor doc link (http(s) only, new tab), stored goldens, `CodeView` source (text-only YAML tokens, line numbers), fixtures. `GoldensRunner` (editor, form action `runGoldens` shared by both routes from `transformers/actions.server.ts`) and `GoldensResult` (failing first, error, `DiffPaths` with Expected / Actual). Tenant viewers get a note |
| HUB-269 | Transformer dry-run on every version (editor; `dryRun` form action, `dryRunTransformer` with `ref`): canonical `input` + optional caller `headers` (request/inbound) or `vendor_response` (response), started from a fixture, optional account id; `$lib/utils/dry-run.ts` / `json-input.ts` check the form in the browser on submit (line and column, objects only, string headers) and again in the action; `DryRunResult`: compile errors with line, vendor request as method / URL / headers table / body, canonical response or error; secrets as returned (`***`) plus the `redactSecrets` guard; nothing logged |
| HUB-270 | Tenants `/tenants` (`listTenants`, search `q` + paging in the URL; admins: "Create tenant", `createTenant`) and `/tenants/{id}` (`getTenant`: contact, rate limit, disabled, integrations owned linked to `/integrations?tenant_id=`, active keys). Admins edit (`updateTenant` merge-patch with only the changed fields, `If-Match` from the ETag, 412 Reload prompt) and disable (tenant id typed) / enable. `tenants/tenant-form.ts` checks `TenantCreate` / `TenantUpdate` in the dialog and again in the actions. Tenant viewers: `listTenants` needs viewer, so their list is `getTenant` per own tenant; other tenants answer 404 without a call. Grants are per integration in hub-admin (`Integration.grants`), not tenant fields: the page links the integrations instead |
| HUB-271 | API keys on `/tenants/{id}` (`listApiKeys` per tenant, all pages, `key_state` filter in the URL; prefix, state, scopes, dates; never a secret). Admins: create (`createApiKey`, scopes narrowed optionally to one integration, UTC expiry), rotate (`rotateApiKey`, overlap 0 to 30 d, the reveal says until when the old key works) and revoke (`revokeApiKey`, prefix typed). **Show-once**: the secret goes from the action answer to `SecretOnce` (read-only field, Copy, warning, Escape ignored, Done after "I have stored it") and is dropped on Done; `ConfirmDialog oneTimeSecret` reloads with `invalidateAll` instead of `update()`, so the answer is never the page's `form`; `requireEnhanced` refuses no-JS posts (no HTML page with the key in history); the notice has only ids; nothing logged, stored or in a URL. Tested in Vitest and Playwright (DOM, inputs, storage, URL, history state, console, request URLs, reload, Back/Forward) |
| HUB-272 | Callback (registrar) endpoints on `/tenants/{id}` (`listRegistrarEndpoints` per tenant, all pages): URL, state, secret version and overlap, subscribers. Admins: register (`createRegistrarEndpoint`), edit URL / description (`updateRegistrarEndpoint`, changes only, no `If-Match`: the list has no per-item ETag), disable / enable, rotate the signing secret (`rotateRegistrarSecret`, overlap) and delete (`deleteRegistrarEndpoint`, id typed, reason as query). Signing secrets: the new one from register / rotate is shown **once** in `SecretOnce` (warning: it verifies `X-Hub-Signature`; the rotate reveal says until when both secrets sign), with the API-key guarantees (`oneTimeSecret`, `requireEnhanced`, dropped on Done, never stored, logged or in a URL); afterwards only the version is shown. Decided 2026-11-01 by the lead: the API returns the secret only in those answers and the tenant needs it to verify callbacks, so the ticket's "write-only in the UI" means "never displayed again". SSRF refusals (`VALIDATION_FAILED` pointer `/url`) land on the URL field (mock `registrar-url-private`, CCR-004) |
| HUB-273 | Snapshots `/snapshots` (`listSnapshots`, cursor paging in the URL): version, summary, author, change log link, checksum (short + title + copy), size, created. `getLatestSnapshot` (`wait=0`) marks the latest row ("Latest" in text) and fills a summary panel. "Show changes in N" disclosure per row reads `getSnapshot` once and lists `changed_objects` linked via `$lib/objects.ts`. No blob download (internal format) |
| HUB-274 | Change log `/change-log` (`listChangeLog`: actor, action, object type, object id, UTC range, page size in the URL; chips; paging; reversed range refused) and `/change-log/{change_id}` (`getChangeLogEntry`: what, reason, when, actor, snapshot; before / after as `DiffPaths` from `$lib/utils/json-diff.ts`, both objects in full). Objects link to their console page (integration, transformer, tenant, request). `ChangeNotice` links the entry itself |
| HUB-280 | CI job `e2e` in `.github/workflows/frontend.yml` beside `console`: `npm ci`, browser cache keyed by OS + pinned `@playwright/test` version, `playwright install --with-deps --only-shell chromium` on a miss (`install-deps` on a hit), `npm run test:e2e` (build:mock + preview), `timeout-minutes: 8`, report + traces uploaded `if: failure()`; HTML reporter under `CI`. Verified locally only (99 tests, about 60 s, 3 runs green); the axe helper waits for `aria-busy` to clear first |
| HUB-279 | SvelteKit 3 evaluated; stays on 2.70.3 (ADR-0009) with a `//` path guard in `hooks.server.ts`; upgrade filed as HUB-281 |
| HUB-288 | Destructive and mutating controls render only for roles that satisfy the operation's `x-required-role`: `scripts/extract-roles.mjs` (part of `gen:api`, CI freshness check) writes `src/lib/api/roles.gen.ts`; `can(access, op)` and `RoleGate op= / ops=` read it. Covered per role and screen in `routes/(app)/destructive-controls.test.ts` (DLQ, health, integration detail, tenants, tenant detail, transformer version). The 403 handling in dialogs stays |
| HUB-286 | One-command local stack (`scripts/dev-up.sh`, `make dev-up`): Postgres/Redis (compose or env), migrations, seed, mockvendor, hub admin (dev OIDC), gateway, worker, scheduler, console. Console: same-origin `/admin/v1` proxy with the session token (`admin-proxy.ts`), dev sign-in through the admin's dev issuer, SSR serialization of `x-hub-request-id` / `etag` |
| HUB-254 | App shell: sidebar (8 sections), breadcrumbs, `/me` user menu, light/dark/system theme, mock-mode band, drawer below 1024 px; each section loads its list operation with loading/empty/error states |

CCR-004 (2026-11-01, HUB-272): an example of the registrar SSRF refusal (pointer `/url`), example-only; requested.

Contract gaps found while building (see `frontend/CCR-NOTES.md`): CCR-001 (`X-Hub-Request-Id` not declared on admin responses, HUB-278), CCR-002 (no active-version filter on `listIntegrations`, HUB-283), CCR-003 (no dead-letter count per account for the health board, HUB-284). All three shipped in hub-admin 1.1.0 (contract round 1, `contracts/notices/2026-10-31-round-1.md`); the console adopts them in round 4:

| CCR | Console side |
| --- | --- |
| CCR-001 (HUB-278) | `client.ts` reads the declared `X-Hub-Request-Id` on every response; on errors the envelope's `request_id` wins, then the header; a missing or empty header is `null`. Form actions return the request id with the change log id (`ActionSuccessData.requestId`) and `ChangeNotice` shows it with a copy button. |
| CCR-002 (HUB-283) | Integrations list: "Active version" (`has_active_version`: Published / None) and "Newer version or draft" (`active_version_behind`: Waiting / None waiting) selects, in the URL with chips; the API filters before paging. A note explains that None plus the second filter matches nothing (contract: integrations without an active version match neither value). |
| CCR-003 (HUB-284) | Health board "Dead letters" column: the account's `dead_letter_count` on each of its endpoint rows, linked to `/dlq?account_id=`, with "oldest N ago" from `dead_letter_oldest_at`; 0 without a link; Unknown when the account or the figure is missing. The summary adds the total (each account once). The figure may lag by up to a minute (contract). |

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | Status in-progress; shipped HUB-250, 251, 252, 254; config and CCR-001 noted | frontend-engineer |
| 2026-10-31 | HUB-279: framework version decision and advisory assessment | frontend-engineer |
| 2026-10-31 | HUB-253: auth design, config and security notes | frontend-engineer |
| 2026-10-31 | HUB-255: shared state components | frontend-engineer |
| 2026-10-31 | HUB-256: integrations list; CCR-002 | frontend-engineer |
| 2026-10-31 | HUB-257: integration detail and version history | frontend-engineer |
| 2026-10-31 | HUB-258: version diff view | frontend-engineer |
| 2026-10-31 | HUB-259: publish and rollback; mutation pattern (form actions, ConfirmDialog, mock scenarios) | frontend-engineer |
| 2026-10-31 | HUB-260: weights and pause/resume | frontend-engineer |
| 2026-10-31 | HUB-261: health board; CCR-003 | frontend-engineer |
| 2026-10-31 | HUB-262: breaker force controls | frontend-engineer |
| 2026-10-31 | HUB-263: request explorer search | frontend-engineer |
| 2026-10-31 | HUB-264: request timeline | frontend-engineer |
| 2026-10-31 | Contract round 1 adoption: CCR-001 / HUB-278 (request id header) | frontend-engineer |
| 2026-10-31 | CCR-002 / HUB-283: active-version filters on the integrations list | frontend-engineer |
| 2026-10-31 | CCR-003 / HUB-284: dead letters column on the health board | frontend-engineer |
| 2026-10-31 | HUB-266: DLQ list and entry | frontend-engineer |
| 2026-10-31 | HUB-267: DLQ replay and purge by filter | frontend-engineer |
| 2026-10-31 | HUB-268: transformer viewer and goldens | frontend-engineer |
| 2026-10-31 | HUB-269: transformer dry-run | frontend-engineer |
| 2026-10-31 | HUB-270: tenant pages; If-Match on tenant edits | frontend-engineer |
| 2026-11-01 | HUB-271: API keys with show-once | frontend-engineer |
| 2026-11-01 | HUB-272: callback endpoints; CCR-004; open question on signing secrets | frontend-engineer |
| 2026-11-01 | HUB-272: signing secret shown once (lead decision closes the open question) | frontend-engineer |
| 2026-11-01 | HUB-274: change log list and entry | frontend-engineer |
| 2026-11-01 | HUB-273: snapshots view | frontend-engineer |
| 2026-11-01 | HUB-280: e2e in CI | frontend-engineer |
| 2026-11-07 | HUB-286: console against a running Hub (proxy, dev issuer sign-in, dev stack scripts) | frontend-engineer |
| 2026-11-07 | HUB-288: controls gated by the contract's x-required-role | frontend-engineer |
