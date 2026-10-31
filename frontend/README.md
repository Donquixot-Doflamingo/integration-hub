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
| `npm run gen:api` | Regenerates `src/lib/api/schema.d.ts` (openapi-typescript) and `src/lib/api/mocks/contract.gen.ts` (contract examples) from `../contracts/openapi/hub-admin.v1.yaml`. Commit both; CI fails if either is stale |
| `npm run dev:mock` | Dev server on MSW mocks in the browser, no backend needed (client-side rendering in this mode) |
| `npm run build:mock` | Production build with mocks on (used by `test:e2e`) |
| `npm test` | Vitest unit and component tests (jsdom, Testing Library) |
| `npm run test:e2e` | Playwright e2e in `e2e/` against `build:mock` + preview (needs `npx playwright install chromium` once) |

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

## Auth (HUB-253)
Every page under `src/routes/(app)/` needs a session; without one you land on `/login?next=<page>`.
- Provider per deployment (`src/lib/server/auth/`): **dev** (role picker: admin, editor, viewer, tenant viewer
  with tenants) in mock builds or with `PUBLIC_AUTH_MODE=dev`; otherwise **OIDC**, a placeholder that reports
  missing `HUB_OIDC_ISSUER` / `HUB_OIDC_CLIENT_ID` / `HUB_OIDC_REDIRECT_URI` and refuses sign-in until HUB-282.
- Session: HMAC-signed httpOnly SameSite=Lax cookie, 8 h. Key `HUB_SESSION_SECRET` (32+ chars); dev mode
  without it uses a random per-process key. No token or session data in web storage. Sign out: `POST /logout`.
- Gating: `/me` (awaited in `(app)/+layout.ts`) gives `data.access`; wrap mutation controls in `RoleGate`
  (hidden for viewer and tenant viewer, and for everyone when `/me` failed). In mock mode MSW's `/me`
  answers as the identity picked at login.

Runes mode is enforced in `svelte.config.js` for every project component; Svelte 4 syntax fails to compile.
CI: [`.github/workflows/frontend.yml`](../.github/workflows/frontend.yml), runs on `frontend/**` changes.
