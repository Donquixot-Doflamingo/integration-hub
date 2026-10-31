# ADR-0009: Admin console stays on SvelteKit 2.x until the SvelteKit 3 toolchain settles

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | frontend-engineer (lead to review) |
| Tickets | HUB-279 (evaluation), HUB-281 (upgrade) |
| DESIGN refs | §16.1; ADR-0005 |

## Context
`npm audit` on 2026-10-31 reports two advisories against `@sveltejs/kit` 2.x (we pin 2.70.3). Both are rated Low, both are fixed only in 3.0.0 (no 2.x patch):

| Advisory | Precondition | Applies to this console? |
| --- | --- | --- |
| [GHSA-pfhg-q3qv-9q6r](https://github.com/advisories/GHSA-pfhg-q3qv-9q6r) open redirect via enhanced form submission (CWE-601, CVSS 4.0 2.1) | A route that accepts a path beginning with `//` (e.g. a catch-all) **and** `use:enhance` on a form action there | Not today: no form actions, no `use:enhance`, no catch-all page routes. It will matter from HUB-259 (publish/rollback with `use:enhance`) |
| [GHSA-27mw-82g8-phjj](https://github.com/advisories/GHSA-27mw-82g8-phjj) XSS via untrusted `cache-control` in a prerendered page (CWE-79) | The app prerenders **and** passes an untrusted `cache-control` value to `setHeaders` | No: nothing is prerendered (`prerender` is never enabled; SSR on adapter-node per ADR-0005), and the console never calls `setHeaders` with request-derived values |

The rest of the report (19 findings, 4 low / 15 moderate) is dev tooling only: the `minimatch` ReDoS chain under ESLint 10, typescript-eslint, eslint-plugin-svelte and `@redocly/openapi-core` (openapi-typescript), `deepmerge` under the Vite/Rollup build, and `cookie <0.7.0` under SvelteKit (we only set cookies with fixed names, paths and no domain). They run on developer machines and CI against our own files and globs, never on user input at runtime. `npm audit --omit=dev` reports **0** findings. Note that `@sveltejs/kit` is a devDependency but its runtime is bundled into the adapter-node output, which is why the two Kit advisories are assessed above rather than dismissed as dev-only.

We tried the upgrade (`@sveltejs/kit` 3.1.1, `@sveltejs/adapter-node` 6.0.1) on a copy of `frontend/`:
- Kit 3 requires TypeScript `^6`. `openapi-typescript` 7.13.0 (latest, 2026-02-11; generates our `schema.d.ts`) declares a peer of `typescript ^5.x`, so `npm install` fails with ERESOLVE. It only installs with an `overrides` entry that forces an unsupported TypeScript onto the generator of our API types.
- Kit 3 is a broad breaking release: `svelte.config.js` is rejected (config moves into the Vite plugin, and `eslint-plugin-svelte`'s `svelteConfig` plus our runes-mode test read that file); `$lib` is removed in favour of `#lib` subpath imports (which need explicit `.ts` extensions under its tsconfig) or a deprecated `alias`; `tsconfig.json` must extend `$app/tsconfig`; `$env/*` modules, the `Pathname` type and `resolve()` route typing change. With those adjusted, `svelte-check` still reported errors in 6 files.
- Kit 3.0.0 went GA on 2026-10-01; 3.1.1 was published 2026-10-29. Two of our five pinned peers have not shipped releases that declare Kit 3 / TS 6 support.

## Options considered
1. **Upgrade to SvelteKit 3 now** - fixes both advisories at the source. Cons: needs an npm override on the API-type generator, a migration touching every import of `$lib` and every config file, and a framework major one month old. Not "clean with all checks green" today.
2. **Stay on 2.70.3 and mitigate** - apply the advisory's documented workaround for the only advisory that can become reachable, keep prerendering off, and schedule the upgrade with explicit triggers.
3. **Stay on 2.x and avoid `use:enhance` entirely** - also safe, but loses progressive enhancement for every mutation dialog (HUB-259+), which the admin-console-ux skill relies on for "keep the dialog open with the reason intact on error".

## Decision
Option 2.
- `src/hooks.server.ts` runs `rejectDoubleSlashPaths` (`src/lib/server/security.ts`) first: any request path starting with `//` gets a 404 before routing. This is the advisory's first workaround and removes the precondition of GHSA-pfhg-q3qv-9q6r for every route, including future catch-alls. Unit tested.
- Rules while on 2.x: never enable `prerender`; never pass a request-derived value to `setHeaders({ 'cache-control' })`; keep the double-slash guard first in the `handle` sequence.
- `npm audit --omit=dev` must stay at zero high/critical findings; dev-tool findings are re-checked whenever this ADR is reviewed.

**Until when.** Upgrade (HUB-281) at the first of:
- `openapi-typescript` releases a version that declares TypeScript 6 support (or we replace it), and Kit 3 has had two months of patch releases (from 2026-12-01); or
- a SvelteKit 2.x advisory rated medium or higher that affects SSR, form actions or the Node adapter; or
- **2027-01-31** at the latest, when this ADR must be revisited even if nothing above happened.

## Consequences
- Positive: no override of a peer contract for the generator of our API types; screens HUB-253 to HUB-258 ship on a stable toolchain; the reachable advisory is closed by a guard that also protects later routes.
- Negative / costs: the 3.x migration (config move, `#lib`, `$env`, TS 6) is deferred and grows with every new file that imports `$lib`. Skills and `frontend/AGENTS.md` keep saying SvelteKit 2 until HUB-281.
- Follow-ups: HUB-281 (upgrade to SvelteKit 3), the decision summary in `docs/features/F-19-admin-console.md`.

## Deviation from DESIGN.md
None.
