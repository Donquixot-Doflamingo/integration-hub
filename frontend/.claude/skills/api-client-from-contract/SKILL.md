---
name: api-client-from-contract
description: Contract-first API access for the admin console. Regenerate typed client from contracts/openapi/hub-admin.v1.yaml (npm run gen:api to src/lib/api/schema.d.ts), call /admin/v1 only through the openapi-fetch wrapper in src/lib/api/client.ts (auth header, canonical error envelope, request-id surfacing), build MSW handlers from spec examples, and file a contract change request instead of inventing fields. Use whenever frontend code calls the admin API, adds or edits anything in src/lib/api, writes MSW mocks, sees a TypeScript error about a missing API field, or a screen needs data the contract does not provide.
---

# Typed admin API client from the contract

The admin console talks only to `/admin/v1`. The source of truth is
`contracts/openapi/hub-admin.v1.yaml` (OpenAPI 3.1, owned by the `contract-steward`). The frontend
**generates** its types from it and never hand-writes request/response shapes.

## Golden rules
1. Never hand-write API types, never cast to `any` to get past a missing field.
2. Never edit `contracts/` or `src/lib/api/schema.d.ts` by hand. `schema.d.ts` is generated and committed.
3. All HTTP calls go through `src/lib/api/client.ts`. No bare `fetch('/admin/v1/...')` in components or loads.
4. Nobody codes against an endpoint that is not in the contract (AGENTS.md §4).
5. Errors are read by `error.code` (canonical code), never by matching `message` text.

## Regenerate after every contract change
```bash
cd frontend
npm run gen:api      # openapi-typescript ../contracts/openapi/hub-admin.v1.yaml -o src/lib/api/schema.d.ts
npm run check        # type errors now point to every call site affected by the change
```
`package.json` script (keep it this way so CI can diff it):
```json
"gen:api": "openapi-typescript ../contracts/openapi/hub-admin.v1.yaml -o src/lib/api/schema.d.ts"
```
CI should run `gen:api` and fail if `git diff --exit-code src/lib/api/schema.d.ts` is non-empty
(the committed client is stale). Commit the regenerated file in the same commit as the code that adapts
to it, after the `contract(...)` commit (order: contract -> backend -> frontend).

## The wrapper: `src/lib/api/client.ts`
Responsibilities: base URL, bearer token, `traceparent` passthrough if present, normalising the error
envelope, and surfacing `X-Hub-Request-Id` so every error shown to an operator carries a request id.

```ts
import createClient, { type Middleware } from 'openapi-fetch';
import type { paths, components } from './schema';
import { PUBLIC_HUB_ADMIN_BASE } from '$env/static/public';

export type ApiError = {
  status: number;
  code: string;            // canonical code, e.g. FORBIDDEN, VALIDATION_FAILED
  message: string;
  retryable: boolean;
  requestId: string | null;
  details?: unknown;       // e.g. JSON pointers for VALIDATION_FAILED
};
export type ApiResult<T> =
  | { ok: true; data: T; status: number; requestId: string | null }
  | { ok: false; error: ApiError; status: number };

export function adminClient(opts: { fetch?: typeof fetch; token?: string }) {
  const client = createClient<paths>({ baseUrl: PUBLIC_HUB_ADMIN_BASE, fetch: opts.fetch });
  const auth: Middleware = {
    onRequest({ request }) {
      if (opts.token) request.headers.set('Authorization', `Bearer ${opts.token}`);
      return request;
    }
  };
  client.use(auth);

  async function call<T>(
    fn: (c: typeof client) => Promise<{ data?: T; error?: unknown; response: Response }>
  ): Promise<ApiResult<T>> {
    let r;
    try { r = await fn(client); }
    catch { return { ok: false, status: 0, error: networkError() }; }
    const requestId = r.response.headers.get('X-Hub-Request-Id');
    if (r.response.ok && r.data !== undefined)
      return { ok: true, data: r.data, status: r.response.status, requestId };
    return { ok: false, status: r.response.status, error: toApiError(r.error, r.response, requestId) };
  }
  return { raw: client, call };
}
```
- `toApiError` reads the envelope `{ error: { code, message, retryable, request_id, ... } }`; when the
  body is not an envelope (proxy 502, HTML), map to `code: 'UNEXPECTED_RESPONSE'` and keep the status.
  Prefer `error.request_id` from the body, fall back to the header.
- Server-side code (`+page.server.ts`, actions) passes the token from `event.locals`; browser code
  must not hold the token (see `svelte5-sveltekit`).
- Use the `fetch` given to `load`/actions so SSR dedupes and cookies flow.
- Use generated types for UI models: `type Integration = components['schemas']['Integration']`.
  Put such aliases in `src/lib/api/types.ts`; do not redeclare fields.

## Handling errors in the UI
| `code` | UI behaviour |
| --- | --- |
| `UNAUTHORIZED` | redirect to login (session expired) |
| `FORBIDDEN` | inline "You need the editor role" style message; keep the page |
| `VALIDATION_FAILED` | map `details` pointers to form fields; keep user input |
| `*_NOT_FOUND` | `error(404)` from load -> `+error.svelte` |
| conflict / precondition codes from the admin spec | show "changed by someone else, reload diff" |
| anything else | generic error panel with **request id** + copy button |

Always show `requestId` in error panels and toasts (operators paste it into the request explorer).
Use only codes the spec lists; if the admin spec defines extra codes, map them here in `errors.ts`.

## When the contract lacks something
The screen needs a field, filter, sort or endpoint that is not in `hub-admin.v1.yaml`:
1. **Stop.** Do not invent the field, do not compute it from unrelated data, do not mock it in MSW only.
2. File a contract change request per `docs/process/CONTRACT_CHANGES.md` for the `contract-steward`:
   what screen, which endpoint/schema, proposed field (name, type, nullability, example), additive or
   breaking, and the ticket `HUB-###` (see the repo-wide `contract-change` skill).
3. Open or link a `todo`/`blocked` ticket for the UI part and keep building the parts that do exist.
4. After the steward merges the contract commit: `npm run gen:api`, adapt, update MSW handlers.

## MSW mocks from the spec
The UI is developed and tested against MSW, never against a hand-made fake shape.
- Location: `src/lib/api/mocks/handlers.ts` (+ `fixtures/*.json`), `server.ts` (`msw/node`, Vitest),
  `browser.ts` (`msw/browser`, `npm run dev:mock`).
- Fixture bodies come from the `examples` in `hub-admin.v1.yaml`. Preferred approach: a small script
  (`scripts/extract-examples.ts`) that reads the YAML and writes one JSON per `operationId` into
  `fixtures/`, run as part of `gen:api`. Then handlers only wire paths to fixtures.
- Type handler bodies with the generated types so a contract change breaks the mock at compile time:
```ts
import { http, HttpResponse } from 'msw';
import type { components } from '../schema';
import list from './fixtures/listIntegrations.json';
type Page = components['schemas']['IntegrationPage'];   // use the real schema name from schema.d.ts
export const handlers = [
  http.get('*/admin/v1/integrations', () => HttpResponse.json(list satisfies Page)),
];
```
- Add error-case handlers that return the canonical envelope (with `request_id`) for 403, 409, 422.
- Do not add mock fields that the spec does not have; `satisfies` will catch most of it.

## Anti-patterns
- `interface Integration { id: string; ... }` written by hand.
- `res.json() as any`; optional-chaining around a field "the backend will add later".
- Catching errors and showing `err.message` without the request id.
- Calling the caller API (`/v1/...`) from the console; it uses `/admin/v1` only.
- Logging tokens or API-key responses to the console.

## Checklist
- [ ] `npm run gen:api` run; `schema.d.ts` diff committed with the adapting code.
- [ ] Every call uses `adminClient(...).call(...)`; errors surface `code` + request id.
- [ ] MSW handlers/fixtures updated from spec examples, including one error case.
- [ ] No field used that is missing from the contract; CCR filed if one was needed.
