# ADR-0026: ISFC is the identity provider; admin authorization is gateway identity plus one OPA policy per operation

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-11 |
| Deciders | lead, product owner (decisions D1, D3, D4, D6, D7, D9 in `docs/tech/AUTH_PLAN.md` §19) |
| Tickets | HUB-465 (this ADR); implementation HUB-472 to HUB-479, HUB-491 to HUB-497 (EP-15) |
| DESIGN refs | §16.1, §16.5; ADR-0005, ADR-0022 (partly superseded by this ADR) |

Status of the behaviour described here: **planned (EP-15)**. Nothing below is built until its ticket is `done`.

## Context
Today the admin API verifies an ES256 bearer JWT and reads roles from the claim `hub_roles` and tenants from `hub_tenants`. The console has only a dev role-picker; the real OIDC provider is a stub that returns 503 (HUB-282), and the session cookie is signed but not encrypted. The company already runs ISFC: Hydra and auth-service for login (Email OTP, Microsoft SSO), an api-gateway that checks JWT and session and sends `x-isfc-*` identity headers, user-service for per-tenant roles, and an OPA bundle pipeline (`authz-policies/<service>/`, one Rego file per route).

Constraints: the Hub must keep today's behaviour until it is switched off on purpose (every change sits behind a mode flag); the `/admin` plane is not on the sync hot path, so a network hop to OPA is acceptable there (it is not on `/v1`, see ADR-0028); platform-wide resources (vendors, accounts, transformers, snapshots, breakers, change log) belong to no tenant, while a `tenancy: required` ISFC panel only has per-tenant roles (gap G8); ISFC's sidecar trusts identity headers and checks only the token issuer (gap G5), and its `entry.rego` lets `ISFC_ADMIN` pass every rule (gap G7).

## Options considered
1. **Keep the Hub's own JWT/role claims; add an OIDC login only.** Smallest change, but the Hub would keep a second role system next to ISFC and nobody could grant access in one place.
2. **ISFC identity through the gateway, in-process role check only.** Roles come from `x-isfc-sub-attrs`. Simple and fast, but loses the per-operation policy that ISFC teams expect and review in Rego.
3. **ISFC identity through the gateway, plus one OPA policy per admin operation, plus the existing in-process role check as a second layer.** Matches how other ISFC services are protected, keeps `x-required-role` as defence in depth.
4. **Import ISFC's `go-commons/sidecar-middleware`.** Rejected: the module path `isfc/go-commons/...` only resolves through a `replace` outside this repo (breaks CI), it uses ISFC error codes, and it does not verify signatures (G10).

For platform-wide resources: (A) one **platform tenant** in ISFC, (B) two console clients, (C) rely on `ISFC_ADMIN`.

## Decision
Option 3, with these rules:

1. **ISFC owns identity.** Console users sign in through ISFC (Email OTP and Microsoft SSO, nothing else). The Hub has no user tables. The dev role-picker works only in mock builds and tests (`PUBLIC_AUTH_MODE`).
2. **Platform tenant (D1).** The console client `integration-hub-console` is `tenancy: required`. One ISFC tenant (for example key `INTEGRATION_HUB_PLATFORM`, id in `HUB_ISFC_PLATFORM_TENANT_ID`) stands for platform scope. A request in that tenant with `HUB_ADMIN` / `HUB_EDITOR` / `HUB_VIEWER` becomes today's unscoped `Principal`. A request in a linked tenant with `HUB_TENANT_VIEWER` or `HUB_TENANT_ADMIN` becomes a `Principal` scoped to that tenant's Hub id. `Principal.Scoped()` and `SeesTenant` stay as they are.
3. **New role `tenant_admin` (D4)**: a tenant viewer plus finishing setup and managing callback endpoints of its own tenant.
4. **Admin request path (`HUB_ADMIN_AUTH_MODE=isfc`).** The admin listener accepts requests that came through the gateway (network policy) and carry a valid Hydra JWT. The Hub verifies signature (RS256 and ES256 allow-list; no `none`, no HS*), `iss`, `exp`, `nbf`; checks `sub` equals `x-isfc-sub-id` and `client_id` equals `x-isfc-client-id` equals the console client id (mismatch: 401); builds the `Principal` from the headers (malformed attributes give no roles); asks OPA; then runs the in-process `x-required-role` check; tenant data scoping is unchanged (404 across tenants).
5. **OPA per operation.** `POST {HUB_OPA_URL}/v1/data/integrationhub/decision` with the sidecar input shape. Deny is 403 `forbidden`; error or timeout (`HUB_OPA_TIMEOUT`, default 200 ms) is 503 `authz_unavailable`: fail closed. The Hub writes its own small OPA client and header parser (about 150 lines) and keeps the input byte-compatible with ISFC's, proved by shared Rego tests.
6. **Rego lives in ISFC (D3)**, in `authz-policies/integration-hub/`, because the bundle pipeline and auth-service's rebuild trigger only work there. The Hub repo keeps the **generator** (`scripts/gen-authz.mjs`, reads every operation in `hub-admin.v1.yaml`, `--check` fails CI on missing or disagreeing files), the generated `rbac_matrix_test.rego` and a pinned copy for CI. Each operation has its own file; the permission catalogue is per resource (`HUB_VENDOR:READ`, ...) and the roles bundle them to give exactly today's matrix (`x-required-role` stays the source of truth, `TestRBACMatrix` must still pass).
7. **`ISFC_ADMIN` is not a Hub admin (D6).** Our `entry.rego` removes the shortcut.
8. **Machines (D7).** Automation (CI, hubctl, hubmcp over HTTP) calls the admin API directly with `x-isfc-client-id` + `x-isfc-api-key`; OPA checks the key; the Hub builds `Principal{Subject: "client:<id>", Machine: true}`. Machine principals are refused for `unsealRequest` and `purgeDlq` whatever their permissions.
9. **Console login (D9).** The backend-for-frontend model stays: tokens never reach page JavaScript. The server runs PKCE (S256), `state` and `nonce` in a short-lived encrypted cookie, exchanges the code with the browser's device id and user agent, and stores tokens in an AES-GCM encrypted session. Microsoft SSO is built now; it is **enabled in production only after ISFC-CR-4** (id_token verification, gap G6).
10. **Mode flags.** `jwt` mode (today's verifier and dev issuer) is the default and stays byte-for-byte unchanged; CI runs the RBAC matrix in both modes.

## Consequences
- Positive: one place (ISFC) to grant and revoke console access; per-operation policies reviewable by the ISFC team; fail-closed with a second in-process layer; no change to the data-scoping code.
- Negative / costs: the admin plane gains an OPA dependency (`HubAdminAuthzUnavailable` alert); 74 generated policy files must be kept in step with the contract (the generator `--check` guards this); one extra platform tenant row in ISFC; RS256 support must be added to the verifier (gap G9, HUB-472); the Hub must also trust a network policy (HUB-497) because the gateway headers are only trusted together with a verified JWT.
- Follow-ups: contract change HUB-469 (security schemes, `x-required-permission`, role `tenant_admin`); docs HUB-468; ISFC change requests CR-4 and CR-5 (`docs/tech/AUTH_PLAN.md` §13); runbook `docs/runbooks/isfc-auth.md` (HUB-497).

## Superseded parts
- [ADR-0005](0005-sveltekit-admin-console.md): the "OIDC Authorization Code + PKCE handled server-side with a dev stub" auth part. The SvelteKit console decision stays.
- [ADR-0022](0022-console-same-origin-admin-proxy.md): the dev-issuer sign-in as the way to obtain a session. The same-origin proxy stays; only the provider that fills `session.accessToken` changes, and the proxy adds `X-Tenant-Id`.

## Deviation from DESIGN.md
DESIGN §16.1 says admin auth is OIDC + RBAC; the identity provider is now ISFC and RBAC has a second, per-operation policy layer. Behaviour of the roles is unchanged. Listed as PRD DV-8.
