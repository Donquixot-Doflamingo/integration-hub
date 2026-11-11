# frontend/

SvelteKit 2 + Svelte 5 (runes) + TypeScript admin console for the Hub control plane (F-19, EP-10).
Rules: [`AGENTS.md`](AGENTS.md). Skills: [`.claude/skills/README.md`](.claude/skills/README.md). API contract: [`../contracts/openapi/hub-admin.v1.yaml`](../contracts/openapi/hub-admin.v1.yaml).

## Setup
Node 22 (`.nvmrc`; `engine-strict` is on). Dependencies are pinned to exact versions.

```bash
cd frontend
npm ci
```

## Scripts
| Script | What it does |
| --- | --- |
| `npm run dev` | Vite dev server (SSR) on http://localhost:5173 |
| `npm run build` / `npm run preview` | Production build with `@sveltejs/adapter-node`, then serve it |
| `npm run check` | `svelte-kit sync` + `svelte-check` (TypeScript strict, warnings fail) |
| `npm run lint` | Prettier check + ESLint (flat config) + `scripts/check-api-types.mjs` (no hand-written API types, no raw `fetch` to `/admin/v1` outside `src/lib/api/`) |
| `npm run format` | Prettier write |
| `npm run gen:api` | Regenerates `src/lib/api/schema.d.ts` (openapi-typescript), `src/lib/api/mocks/contract.gen.ts` (contract examples) and `src/lib/api/roles.gen.ts` (`x-required-role` per operation) from `../contracts/openapi/hub-admin.v1.yaml`. Commit all three; CI fails if any is stale |
| `npm run dev:mock` | Dev server on MSW mocks in the browser, no backend needed (client-side rendering in this mode) |
| `npm run build:mock` | Production build with mocks on (used by `test:e2e`) |
| `npm test` | Vitest unit and component tests (jsdom, Testing Library) |
| `npm run test:e2e` | Playwright e2e in `e2e/` against `build:mock` + preview (needs `npx playwright install chromium` once) |
| `npm run test:e2e:real` | Playwright e2e in `e2e-real/` against a running stack (`scripts/dev-up.sh`; set `HUB_E2E_BASE_URL` to its console URL, default `http://localhost:5173`). Each run creates its own vendors, accounts, transformers and integrations with a unique suffix. `HUB_E2E_SHOTS=<dir>` also saves desktop and 390 px screenshots of every authoring page |

## API access
All `/admin/v1` calls go through `adminClient()` in `src/lib/api/client.ts` (openapi-fetch + generated `paths`):
bearer token, `X-Hub-Request-Id` capture, and failures as a typed `HubApiError` (`code`, `retryable`, `requestId`,
`details`, `meta`) built from the canonical error envelope. Type aliases live in `src/lib/api/types.ts`.
Contract gaps go to [`CCR-NOTES.md`](CCR-NOTES.md).

## Mocks (MSW)
`src/lib/api/mocks/` answers every one of the 79 admin operations with the contract's own examples.
- **Fixtures are generated, not copied by hand**: `scripts/extract-examples.mjs` (part of `gen:api`) reads the YAML and
  writes `contract.gen.ts` (success example + response headers such as ETag + the error envelope of each declared
  4xx/5xx). It is typed as `ContractMocks`, so `npm run check` fails when an example stops matching the generated
  types or an operation has no mock.
- `contract.test.ts` validates every fixture against the YAML schemas with Ajv (JSON Schema 2020-12 + formats) and
  calls every handler through MSW.
- Handlers: `handlers.ts` (`createHandlers`, typed `mockOperation` / `mockError` / `mockPending` / `mockNetworkError`
  overrides for tests), `server.ts` (Vitest, `onUnhandledFrame: 'error'`), `browser.ts` (`dev:mock`, 300 ms latency).
- Mock mode is the build-time flag `HUB_API_MOCKING=true` (`__API_MOCKING__`). Normal builds contain no MSW code and
  no `mockServiceWorker.js` (the `msw/vite` plugin serves/emits it only in mock mode).

## App shell (HUB-254)
`src/routes/(app)/+layout.svelte`: sidebar navigation (`src/lib/nav.ts`), breadcrumbs, user menu from `/me`, theme
switch (system/light/dark, remembered in localStorage, applied before paint by the script in `src/app.html`), and a
band that names the admin API (striped in mock mode). Below 1024 px the sidebar becomes a drawer. Each section page
(`src/routes/(app)/<section>/`) loads its list operation in `+page.ts` and renders it with `ListView`
(loading, empty, error with request id and Retry, populated).
Visual direction: cool workspace, slate-navy rail, one teal accent; Atkinson Hyperlegible Next/Mono (self-hosted via
Fontsource) because ids like `req_01J9Z...` must not confuse 0/O or 1/l. Tokens live in `src/app.css`.

## Shared states (HUB-255)
Every data view uses these, so loading, empty and error look and behave the same everywhere (a unit test fails
when a page under `src/routes/(app)/` renders its own `{#await}` instead):
| Component | Use it for |
| --- | --- |
| `AsyncView` | One `api.call(...)` from `load`: skeleton while loading, `ErrorPanel` on failure, `children(data)` on success; keeps the previous data (marked busy) while a new call runs |
| `ListView` | A list operation: `AsyncView` + table skeleton with the real columns + `EmptyState` + accessible table (sticky first column); `footer` snippet for paging |
| `ErrorPanel` | Any failed call: canonical `code`, HTTP status, message, operator hint, validation details (JSON pointers), whether retrying helps (with `retry_after_ms`), request id with copy, Retry |
| `EmptyState` | "Nothing exists yet" vs `filtered` ("filters hide everything", with a Clear all filters link) |
| `Skeleton` | Loading placeholder for detail sections |
| `ErrorPage` | Body of both `+error.svelte` pages (unknown routes, errors thrown from loads) |

## Integration screens (HUB-256 to HUB-258)
- `/integrations`: filters (including the active-version filters of hub-admin 1.1.0) and page size in the URL (`filters.ts`), chips, cursor paging (`$lib/utils/paging.ts`, `Pager`).
- `/integrations/{id}`: header, active spec (`$lib/components/integration/SpecView.svelte`), full version history
  (`collectPages` follows `next_cursor`), From/To selection for a diff; `/integrations/{id}/versions/{n}` for one version.
- `/integrations/{id}/diff?from=&to=&view=split|paths`: `$lib/components/diff/` (`DiffSplit` from the unified YAML diff,
  `DiffPaths` from the structured ops), helpers in `$lib/utils/diff.ts`. Reuse these for the publish preview (HUB-259).

## DLQ and transformer screens (HUB-266 to HUB-269)
- `/dlq`: filters in the URL (`dlq/filters.ts`), cursor paging, `/dlq/{request_id}` entry (reason, attempts, payloads).
  Replay (editor) and purge (admin) by filter: a dry-run count first, then `ConfirmDialog` with that exact count;
  purge needs the count typed and is re-counted on the server (`dlq/+page.server.ts`, `dlq/bulk.ts`).
- `/transformers`, `/transformers/{id}`, `/transformers/{id}/versions/{n}`: `$lib/components/transformer/`
  (`VersionView`, `GoldensRunner`, `GoldensResult`, `DryRun`, `DryRunResult`), `CodeView` (text-only YAML highlighting,
  `$lib/utils/yaml-highlight.ts`); form actions shared from `transformers/actions.server.ts`.

## Tenant screens (HUB-270 to HUB-272)
- `/tenants` (search and paging in the URL; tenant viewers get `getTenant` per own tenant because `listTenants`
  needs viewer) and `/tenants/{id}`. Create and edit go through `ConfirmDialog` with `TenantFields`; the checks
  live in `tenants/tenant-form.ts` and run in the browser and in the form actions. Edits send the loaded ETag
  (`ApiResult.etag`) as `If-Match`.
- API keys (`tenants/[tenant_id]/ApiKeys.svelte`, `keys.ts`): a new secret only ever goes from the action answer to
  `$lib/components/SecretOnce.svelte` and is dropped on Done. Any action that answers with a secret must use
  `ConfirmDialog oneTimeSecret` and `requireEnhanced` (`$lib/server/actions.ts`), and must not log it.
- Callback endpoints (`CallbackEndpoints.svelte`, `endpoints.ts`): a new signing secret (register, rotate) is shown once
  in `SecretOnce` with the same rules as API keys; only its version afterwards. Mock scenario `registrar-url-private`
  answers the SSRF refusal.

## Change log and snapshots (HUB-273, HUB-274)
- `/change-log`: filters in the URL (`change-log/filters.ts`), `/change-log/{change_id}` with before / after as a
  path diff (`$lib/utils/json-diff.ts` -> `DiffPaths`). `ChangeNotice` links every change id to its entry.
- `/snapshots`: latest marked from `getLatestSnapshot`; `SnapshotChanges` reads `getSnapshot` when a row is expanded.
  Object links for both screens: `$lib/objects.ts`.

## Auth (HUB-253, HUB-491 to HUB-495)
Every page under `src/routes/(app)/` needs a session; without one you land on `/login?next=<page>`
(`/login?expired=1` when the session ended). The model is [AUTH_PLAN §9](../docs/tech/AUTH_PLAN.md): the console is a
backend-for-frontend, **tokens never reach page JavaScript** ([ADR-0005](../docs/tech/adr/0005-sveltekit-admin-console.md), ADR-0022).
- `PUBLIC_AUTH_MODE` picks the provider (`src/lib/server/auth/`): **`isfc`** is the real login; **`dev`** (role picker)
  works only in mock builds (`HUB_API_MOCKING=true`, always `dev`) and when `NODE_ENV=test`. Anything else is refused:
  `dev` in a deployment stops the server from starting, an unset or unknown mode starts but nobody can sign in.
- ISFC login: `/login` lists **Email OTP** and **Microsoft** from `GET {ISFC_AUTH}/oauth2/method/integration-hub-console`
  (nothing else). `POST /login?/start` makes PKCE (S256), `state` and `nonce` and keeps them in the AES-GCM `hub_login`
  cookie (10 min, path `/auth`); Email OTP then redirects to Hydra. Microsoft needs Hydra's CSRF cookie in the browser,
  so `lib/auth/microsoft.ts` opens the flow with the server's challenge and goes to Entra. `GET /auth/callback` checks
  `state`, exchanges the code at `POST {ISFC_AUTH}/oauth2/token` with `x-device-id` and the browser's User-Agent, and
  sets the session. Errors `AS_FB_21`, `AS_VE_34`, `GW_007`, `GW_015` have their own messages (`lib/auth/isfc-errors.ts`).
- Session: AES-256-GCM encrypted httpOnly SameSite=Lax cookie (8 h, chunked over 4 KB), key `HUB_SESSION_SECRET` (32+
  chars); the access token is not readable from the cookie value. The dev picker without a secret uses a random
  per-process key. A session made by the other provider is not accepted. No token in web storage.
- Refresh (server side): a minute before expiry, or once after the gateway refuses the token (`GW_002/003/004`),
  single-flight per refresh token; a 409 from ISFC is retried once after 300 ms. A refused refresh token ends the session
  (`/login?expired=1`); an ISFC outage does not. Sign out: `POST /logout` calls `POST {ISFC_AUTH}/api/v1/logout`, then
  clears the cookie whatever ISFC answers.
- Tenants: the top bar switcher lists `GET {gateway}/auth/oauth2/panels` (platform tenant first); the choice is kept in
  the session and the admin proxy sends it as `X-Tenant-Id` and adopts the gateway's `X-Isfc-Tenant-Id` when none was
  sent. Switching reloads every load function and `/me`. Gateway errors (`GW_015`, ...) reach the pages as Hub errors.
- Settings (server, private): `HUB_ISFC_AUTH_URL` (required), `HUB_ISFC_CLIENT_ID` (default `integration-hub-console`),
  `HUB_ISFC_REDIRECT_URI` (default `<origin>/auth/callback`), `HUB_ISFC_GATEWAY_URL` (tenant switcher),
  `HUB_ISFC_PLATFORM_TENANT_ID`, `HUB_SESSION_SECRET`, `HUB_ADMIN_UPSTREAM` (the ISFC gateway route of the Hub admin).
  Public: `PUBLIC_AUTH_MODE`, optional `PUBLIC_ISFC_ADMIN_URL` (link to change ISFC-owned tenant data).
- Tests never call a real ISFC or Entra: `src/lib/testing/fake-isfc.ts` is the fake auth-service and gateway.
- Gating: `/me` (awaited in `(app)/+layout.ts`) gives `data.access`; wrap mutation controls in `RoleGate`
  (hidden for viewer and tenant viewer, and for everyone when `/me` failed). Roles follow contract 2.1.0:
  `admin` ⊃ `editor` ⊃ `viewer` ⊃ `tenant_viewer`, `admin` ⊃ `tenant_admin` ⊃ `tenant_viewer`. In mock mode MSW's `/me`
  answers as the identity picked at login.

## Against a running Hub (HUB-286, HUB-495)
`make dev-up` at the repo root starts the whole stack (see the root [`README.md`](../README.md#run-the-whole-stack-locally-manual-testing)). To point a console you run yourself at a Hub with ISFC:

```bash
PUBLIC_AUTH_MODE=isfc \
HUB_ISFC_AUTH_URL=https://auth.isfc.example \
HUB_ISFC_GATEWAY_URL=https://gateway.isfc.example \
HUB_SESSION_SECRET="$(openssl rand -base64 32)" \
HUB_ADMIN_UPSTREAM=https://gateway.isfc.example/hub \
npm run dev
```

The dev role picker against a local Hub (`PUBLIC_AUTH_MODE=dev` with `HUB_DEV_OIDC_ISSUER`) now needs `NODE_ENV=test`
(it is refused otherwise, HUB-495); `scripts/dev-up.sh` is owned outside the console and sets neither yet.

- `HUB_ADMIN_UPSTREAM`: the console server proxies `/admin/v1/**` there and adds the session's bearer token, so the
  token never reaches browser JavaScript ([ADR-0022](../docs/tech/adr/0022-console-same-origin-admin-proxy.md),
  `src/lib/server/admin-proxy.ts`). Only contract headers pass each way (plus `X-Tenant-Id` from the session); cross-origin
  mutations are refused. Unset, `/admin/v1` must be routed to the admin server by whatever is in front of the console.
- `HUB_DEV_OIDC_ISSUER` (dev picker only, mock builds and tests): asks the admin's dev OIDC issuer
  (`hub admin -admin-dev-oidc`) for a token with the picked role and tenants. Never set in production.
- Which controls render follows the contract: `RoleGate op="purgeDeadLetters"` / `can(access, op)` read the minimum
  role from `src/lib/api/roles.gen.ts`, generated from each operation's `x-required-role` (HUB-288). A role that cannot
  call an operation does not see its control at all.

Runes mode is enforced in `svelte.config.js` for every project component; Svelte 4 syntax fails to compile.
CI: [`.github/workflows/frontend.yml`](../.github/workflows/frontend.yml), runs on `frontend/**` changes: job `console`
(generated files, check, lint, unit tests, build) and job `e2e` (Playwright on `build:mock`, cached Chromium headless
shell, 8 min cap, report and traces uploaded on failure). Run the same locally with `CI=true npm run test:e2e`.
