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
| `npm run gen:api` | Regenerates `src/lib/api/schema.d.ts` from `../contracts/openapi/hub-admin.v1.yaml` (openapi-typescript). Commit the result; CI fails if it is stale |
| `npm test` | Vitest unit and component tests (jsdom, Testing Library) |
| `npm run test:e2e` | Playwright e2e in `e2e/` (builds and previews the app; needs `npx playwright install chromium` once) |

## API access
All `/admin/v1` calls go through `adminClient()` in `src/lib/api/client.ts` (openapi-fetch + generated `paths`):
bearer token, `X-Hub-Request-Id` capture, and failures as a typed `HubApiError` (`code`, `retryable`, `requestId`,
`details`, `meta`) built from the canonical error envelope. Type aliases live in `src/lib/api/types.ts`.
Contract gaps go to [`CCR-NOTES.md`](CCR-NOTES.md).

Runes mode is enforced in `svelte.config.js` for every project component; Svelte 4 syntax fails to compile.
CI: [`.github/workflows/frontend.yml`](../.github/workflows/frontend.yml), runs on `frontend/**` changes.
