# frontend/AGENTS.md

Rules for work inside `frontend/`. The root `AGENTS.md` still applies.

## Scope
The SvelteKit 2 + Svelte 5 (runes) + TypeScript **admin console** for the Hub control plane (feature F-19, epic EP-10). It talks **only** to `/admin/v1`.

## Before you write code
1. Find or create the `HUB-###` ticket (`docs/tickets/TICKETS.md`) and branch `<type>/HUB-###-slug`.
2. Regenerate the typed client from the frozen contract: `npm run gen:api` (generates `src/lib/api/schema.d.ts` from `../contracts/openapi/hub-admin.v1.yaml`).
3. If the screen needs a field or endpoint that the contract lacks, **stop** and file a contract change request (`docs/process/CONTRACT_CHANGES.md`) for the `contract-steward`. Never invent fields. Never edit `contracts/`.
4. Load the matching skill from `frontend/.claude/skills/` (see its README): `svelte5-sveltekit`, `api-client-from-contract`, `admin-console-ux`, `frontend-testing`, `frontend-design`, `webapp-testing`.

## Rules
- API types are generated, never hand-written. All calls go through `src/lib/api/client.ts`.
- Develop against MSW mocks built from contract examples, so the UI never waits for the backend.
- Irreversible actions (publish, rollback, pause, retire, purge, force breaker, unseal) need a confirm dialog with a mandatory reason. Show the resulting change-log id.
- Never render secrets. An API key is shown once at creation.
- Accessible by default: keyboard, focus, labels, contrast. State is conveyed by text, not colour alone.

## Done means
`npm run check && npm run lint && npm test` green, Playwright e2e green for touched flows, docs and changelog updated, PR opened from a branch.
