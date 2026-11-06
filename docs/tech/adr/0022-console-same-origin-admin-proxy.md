# ADR-0022: Console proxies `/admin/v1` on its own origin and signs in through the dev issuer locally

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | frontend-engineer (for lead review) |
| DESIGN refs | §16.1; ADR-0005; F-19 "Open questions: hosting"; F-16 / M7 dev OIDC issuer; SECURITY §2 |

## Context
Until HUB-286 the console ran only on MSW contract mocks. Its client calls `PUBLIC_ADMIN_API_BASE` (default `/admin/v1` on the console origin) and assumed "something in front" routes that path to the Hub admin server and adds the bearer token. Nothing did: the session token lived in the httpOnly cookie on the console server, the browser had no token, and the dev provider minted none. F-19 left hosting (same origin or separate) open.

Running the console against a real Hub for manual testing (one command, `scripts/dev-up.sh`) needs: (1) a token for the chosen role, (2) a way for SSR loads, form actions and client navigations to reach `/admin/v1` with that token, without the token reaching browser JavaScript.

## Options considered
1. Expose the token to the browser and call the admin origin directly (CORS). Puts a bearer token in page JS and needs CORS on the admin API (not in the contract).
2. A separate reverse proxy (nginx, Caddy) in front of console and admin that injects the token. The proxy cannot read the console's HMAC-signed session cookie without sharing its secret and code; one more moving part on every laptop.
3. **The console server proxies `/admin/v1/**` to `HUB_ADMIN_UPSTREAM` and adds the session's token** (a SvelteKit `handle` step). Dev sign-in asks the admin's dev OIDC issuer (`hub admin -admin-dev-oidc`, `HUB_DEV_OIDC_ISSUER`) for a token with the picked role.

## Decision
**Option 3.**
- `src/lib/server/admin-proxy.ts`: active only when `HUB_ADMIN_UPSTREAM` is set (no-op otherwise, so a deployment that routes `/admin/v1` in front keeps working). It forwards method, path, query and body; only the request headers the contract uses (`accept`, `content-type`, `if-match`, `if-none-match`, `traceparent`) and only the contract response headers (`content-type`, `etag`, `x-hub-request-id`, `retry-after`, `cache-control`, `content-disposition`, `location`). `Authorization` is always the session's token, never a header from the browser; cookies never go upstream. Mutations must be same-origin (`Origin` or `Sec-Fetch-Site`) or a server sub-request (form actions). Upstream down -> 502 with a hint.
- `src/lib/server/auth/dev.ts`: with `HUB_DEV_OIDC_ISSUER` set (and not in a mock build) the dev provider reads the issuer's discovery document and posts `{sub, email, name, roles, tenants, ttl_s}` to its `token_endpoint`. The token goes into the httpOnly session only. The issuer is a dev-only backend feature, not part of `/admin/v1`.
- `hooks.server.ts` serializes `x-hub-request-id` and `etag` for universal loads (`filterSerializedResponseHeaders`); without it SSR fails on real responses.

## Consequences
- The console works against any Hub with two env vars; the same proxy is a valid production model (console and admin on one origin, token server-side), which answers the F-19 hosting question for now.
- Every admin call from the browser takes one extra hop through the console server. Acceptable for an operator UI.
- The dev issuer must never be enabled in production: on the admin it is opt-in (`HUB_ADMIN_DEV_OIDC=true`, off by default, refused together with a real `HUB_OIDC_ISSUER`; M7), and the console only uses it with `PUBLIC_AUTH_MODE=dev` in a non-mock build.
- Real OIDC (HUB-282) plugs into the same proxy: only the provider that fills `session.accessToken` changes.
