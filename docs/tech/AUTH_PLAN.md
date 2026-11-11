# Auth plan: ISFC identity, tenancy and authorization for the Integration Hub

| | |
| --- | --- |
| Status | **Proposed** (plan only; nothing in this document is built yet) |
| Date | 2026-11-11 |
| Ticket | HUB-460 (this plan); work tickets HUB-461 to HUB-498, epic EP-15 (§17) |
| Features | F-24 ISFC identity and authorization, F-25 Tenants from ISFC (to be created, §16) |
| ADRs to write | ADR-0026, ADR-0027, ADR-0028 (§16) |
| Sources read | ISFC code in `Isfc/isfc-local/isfc` (api-gateway, auth-service, go-commons/sidecar-middleware, authz-policies, omniauth, local-dev) and the `isfc-integration` skill; this repo's `AGENTS.md`, `CLAUDE.md`, `docs/DESIGN.md`, `docs/tech/SECURITY.md`, `docs/tech/TECH_SPEC.md`, ADR-0005/0018/0020/0022, the admin and public contracts, `backend/internal/control/adminapi`, `backend/internal/edge`, `frontend/src/lib/server/auth` |

This plan replaces the console's dev role-picker with a real ISFC login and makes ISFC the source of truth for **who** someone is, **which tenants** exist and **who can do what in which tenant**. The Hub keeps the data that only it understands: per-tenant integration settings, grants, rate limits, callbacks and the integrations themselves.

The order is: **verify first (Phase 0), then docs and contracts, then code**. Every step sits behind a mode flag, so today's behaviour (dev issuer, `hk_live_` keys, mock e2e) keeps working until we switch it off on purpose.

---

## 1. Goals and non-goals

**Goals**
1. **Console login through ISFC.** Two methods for now: **Email OTP** and **Microsoft SSO**. The dev role-picker can no longer be used outside mock builds and tests.
2. **Every admin API call goes through the ISFC api-gateway.** Each admin operation has **its own Rego policy**, checked by OPA, and the Hub fails closed.
3. **Tenants are created and linked in ISFC.** ISFC is the central tenant hub. When a tenant is linked to the Hub's client there, the Hub creates the matching tenant automatically, in a "setup pending" state. The tenant admin then completes the Hub-side setup in the console. Nobody creates a tenant twice or grants access twice.
4. **Callers are ISFC `backend_services` clients.** A calling team creates its own backend client in ISFC and links it to the Hub's backend client for the tenants it acts for. Its service uses the ISFC auth lib, which fetches the API key and sends it. The Hub authorizes the call without breaking the hot-path budget.
5. **Internal and backend-to-backend clients** (CI, automation, hubctl, hubmcp) have a supported, audited way to call the admin API.

**Non-goals (for this plan)**
- Changing the caller contract. Callers still send integration ID + operation + body (DESIGN §0). Vendor, account and version stay hidden.
- Google SSO, phone OTP, Truecaller, device binding.
- Fixing ISFC's own security gaps. These are listed in §12 and raised as ISFC change requests in §13; the Hub must not depend on them being fixed.
- Removing `hk_live_` tenant keys on day one (§15 covers the migration).

---

## 2. Answers to the questions raised

### 2.1 Where do tenants live, and how does the Hub stay in sync?

**ISFC owns the tenant. The Hub keeps a local copy plus its own settings.**

| Data | Owner | Stored in the Hub? | Why |
| --- | --- | --- | --- |
| Tenant id (UUID), key, name, status | ISFC | Yes, as a **read-only copy** (`isfc_tenant_id`, `isfc_key`, `isfc_name`, `isfc_status`, `synced_at`) | The edge cannot call ISFC on the sync path (DESIGN §18.5), so it needs the copy in its in-memory snapshot. The console list stays fast and still works when ISFC is down. |
| Which tenants the Hub is linked to | ISFC (`tenants` in the Hub console client's metadata) | Yes (copy) | Linking happens in ISFC, as requested. |
| Which users may enter which tenant, with which roles | ISFC (user-service tenant rows) | **No** | The gateway resolves it on every request and sends `x-isfc-tenant-id` and `x-isfc-sub-attrs`. |
| Which caller clients may call the Hub for which tenant | ISFC (API keys the Hub backend client issues; tenant binding needs ISFC-CR-1, §13) | Yes, as **hashed** keys in an in-memory cache | The edge must check the key in memory (§8). |
| Hub-only settings: rate limit, `suspended`, callback endpoints, integrations owned, grants, display overrides, setup state | **Hub** | Yes, in the tenant entity (`tenants/<id>.yaml` in the config tree) | ISFC knows nothing about integrations. |

**Should the console call ISFC's API to list tenants or show their details?**
- **No for the list and the detail page.** Read the local copy, show "last synced" next to it, and add a **"Sync now"** action that runs the reconciler for one tenant.
- Calling ISFC on every page view makes the console depend on ISFC being up, duplicates what we already hold, and still doesn't help the edge, which needs the copy anyway.

**Sync mechanism.** We use events to trigger a reconcile, and the reconcile is what actually decides:

```
ISFC change (tenant created/updated, Hub client's tenants list changed, key issued/revoked)
   └─> event "client X / tenant Y changed"  (SQS or signed webhook: ISFC-CR-2)
         └─> Hub worker: reconcile(Hub clients)   <── also every 5 min, and on "Sync now"
               1. GET Hub console client metadata → linked tenant UUIDs (ordered)
               2. GET /api/v1/tenant/:id for each   → key, name, status
               3. GET /api/v1/client/configuration/<Hub backend client> → caller keys (hash on read)
               4. diff with the Hub copy → create / update / disable, publish a snapshot only if config changed
```

- The event only says "something changed for X". The reconciler always pulls the full current state, so lost, duplicate or out-of-order events are harmless.
- The periodic reconcile is the safety net. It also means **we can ship before ISFC has events**: Phase A polls only.
- ISFC today emits **no** tenant or client-link events. Its only change hook is the GitHub `repository_dispatch` that rebuilds OPA bundles (`auth-service/internal/domain/policy/publisher.go`, `BundleNotifier.ClientChanged`). ISFC-CR-2 asks for a real event next to that hook.

**Tenant lifecycle in the Hub**

| ISFC state | Hub tenant `setup` | Hub tenant `status` | What callers and users see |
| --- | --- | --- | --- |
| Linked to the Hub client and ACTIVE, first seen | `pending` (created by sync) | `active` | Users with a Hub role in that tenant can open it in the console and finish setup. Calls are refused until setup is `complete` (no grants or keys exist yet anyway). |
| Setup finished in the console | `complete` | `active` | Normal. |
| Renamed in ISFC | unchanged | unchanged | `isfc_name` updated; the Hub's own `display_name` wins if one is set. |
| INACTIVE in ISFC, or unlinked from the Hub client | unchanged | `disabled` (by sync) | 401 at the edge. Console shows "Disabled in ISFC". Nothing is deleted. |
| Re-activated or re-linked | unchanged | back to `active` | Normal again. A `suspended` the Hub set itself is kept. |
| Deleted in ISFC (not possible today; only `PATCH status`) | — | `disabled` | Same as INACTIVE. |

- The Hub **never deletes** a tenant on its own. Integrations, grants and audit history still point at it.
- Every sync change writes a change-log entry with actor `isfc-sync` and the ISFC event or reconcile id.

**Hub tenant id vs ISFC tenant id**
- The Hub tenant id (`orders-service`) is used in `owner_tenant`, `grants`, file paths, logs and metrics. It must match `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$` (`backend/internal/registry/load.go:106`).
- An ISFC key only has to match `^[A-Za-z0-9_-]{1,64}$`, so `HL` fits ISFC but is **too short** for the Hub (gap G3, §12).
- The rule:
  - **The link is always the UUID** (`isfc_tenant_id`).
  - The Hub id is derived once, at creation: the lower-cased ISFC key, padded to 3 characters with `-t` if it's shorter, plus `-2`, `-3`, … if that id is already taken.
  - The Hub id then never changes, even if the ISFC key does.

### 2.2 Tenancy of the Hub console client

As agreed, the console client in ISFC is **`tenancy: required`**, and its `tenants` list is the set of tenants linked to the Integration Hub. The gateway then:
- resolves the tenant for each request from `X-Tenant-Id` (or falls back to the user's top tenant);
- refuses users who have no access to that tenant (403 `GW_015`);
- sends `x-isfc-tenant-id` and the roles **for that tenant**.

**Gap G8: platform-wide resources.** Vendors, endpoints, accounts, transformers, snapshots, breakers and the change log belong to no tenant, but a `tenancy: required` panel only has per-tenant roles. Options:

| Option | How | Verdict |
| --- | --- | --- |
| **A. Platform tenant (recommended)** | Create one ISFC tenant, e.g. key `INTEGRATION_HUB_PLATFORM`, and put it first in the console client's `tenants`. Platform-team users get `HUB_ADMIN`, `HUB_EDITOR` or `HUB_VIEWER` in that tenant. The Hub reads `HUB_ISFC_PLATFORM_TENANT_ID` and treats a request in that tenant as platform scope (unscoped `Principal`). Sync skips this tenant. | One client, one login, and it uses ISFC as designed. The cost is one extra row in ISFC's tenant list. |
| B. Two console clients | `integration-hub-platform` (`tenancy: none`) and `integration-hub-console` (`tenancy: required`) | Two logins and a panel switcher just to do your job. |
| C. Rely on `ISFC_ADMIN` | ISFC platform admins act as Hub admins | Too broad, and no viewer or editor split. |

How the Hub builds the `Principal` (Option A):

| `x-isfc-tenant-id` | ISFC roles (for that tenant) | Hub role | Scope |
| --- | --- | --- | --- |
| = platform tenant | `HUB_ADMIN` / `HUB_EDITOR` / `HUB_VIEWER` | `admin` / `editor` / `viewer` | all tenants (today's unscoped principal) |
| = a linked tenant | `HUB_TENANT_VIEWER` | `tenant_viewer` | that tenant only (`Principal.Tenants = [hub id of that tenant]`) |
| = a linked tenant | `HUB_TENANT_ADMIN` (new, decision D4) | `tenant_admin` | that tenant: finish setup, callback endpoints, view caller links |

- This keeps today's `Principal.Scoped()` and `SeesTenant` code paths unchanged (`backend/internal/control/adminapi/auth.go`).
- The tenant dropdown in the console is ISFC's `GET /oauth2/panels` list for the current panel.

### 2.3 Callers: "OPA for callers too, but as backend clients"

There's a conflict between two earlier statements:
- "no Rego on the client API" (first message);
- "OPA for callers too" (this message);

and a hard rule: DESIGN §18.5 and §18.6 allow **0.1 ms** in total for auth, grant check and tenant limit on the sync path, with **no network hop**. An OPA sidecar is an HTTP call to `localhost:8181` per request, typically 0.3–1 ms, plus a new failure mode (fail closed means invoke goes down whenever OPA does).

**Recommendation (D2):** callers authenticate with **ISFC S2S credentials** (`x-isfc-client-id` + `x-isfc-api-key`), and the Hub checks exactly what the ISFC Rego would check, **in memory, in Go**:
- the key exists, is ACTIVE, and was issued by the Hub backend client to this caller client;
- the key's permissions include the Hub permission for the call;
- the key covers the tenant.

The data comes from the same source OPA uses: `GET /api/v1/client/configuration/<Hub backend client>`. The reconciler fetches it and hashes the keys immediately; gateway nodes load the hashes into memory (§8).

- Callers get the ISFC model they want (backend client, auth lib, link in ISFC), and ISFC stays the place where access is granted and revoked.
- If we still want "a Rego file for the caller plane", Phase 0 benchmarks **embedded** OPA (`github.com/open-policy-agent/opa/v1/rego`, prepared query, in-process). We adopt it only if p99 stays under the budget with 10k keys (HUB-490).

### 2.4 The clients we create in ISFC

| ISFC client | `application_type` | `client_type` | Purpose |
| --- | --- | --- | --- |
| `integration-hub-console` | `web` | `INTERNAL` | Console login (Email OTP + Microsoft). `tenancy: required`, `tenants` = platform tenant + linked tenants, `client_domain` = console origin, `allowed_domains` = company domain, `create_user_if_not_exists: false`. Roles `HUB_ADMIN`, `HUB_EDITOR`, `HUB_VIEWER`, `HUB_TENANT_ADMIN`, `HUB_TENANT_VIEWER`, plus the admin permissions in §7.4. |
| `integration-hub` | `backend_services` | `INTERNAL` | The Hub service itself. Owns the OPA bundle (`enable_rego_bundle_push: true`, package `integrationhub`). **Issues API keys** to caller clients (caller plane) and to automation clients (admin plane). Holds the caller permissions `HUB:INVOKE`, `HUB:DRY_RUN`, `HUB:REQUESTS_READ`. Its own key on auth-service (`TENANT:READ`, `CLIENT:READ`) lets the reconciler read ISFC (ISFC-CR-5). |
| `<caller>-backend` (one per calling team) | `backend_services` | `INTERNAL` | Created by the calling team. Linked by getting a key from `integration-hub` with a role such as `HUB_CALLER` (`HUB:INVOKE` …), bound to the tenants it acts for (ISFC-CR-1). |
| `hubctl` (optional, HUB-496) | `web` (public, PKCE, loopback redirect) | `INTERNAL` | Humans running hubctl or hubmcp locally as themselves. |
| `<automation>-backend` (for example `integration-hub-ci`) | `backend_services` | `INTERNAL` | CI `hubctl apply`, scripts. Gets a key from `integration-hub` with admin-plane permissions only (for example `HUB_INTEGRATION:WRITE`). |

---

## 3. What exists today

### 3.1 ISFC (read from code, `Isfc/isfc-local/isfc`)

- **api-gateway.** Fixed pipeline: strip client `x-isfc-*` → route → CORS → rate limit → JWT check (JWKS, RS/ES/PS algorithms) → session check (auth-service gRPC) → RBAC attributes (user-service) → identity headers → proxy. On a `tenant: required` route it resolves the tenant and sets `x-isfc-tenant-id`. See `api-gateway/README.md`.
- **Headers sent upstream:** `x-isfc-client-id`, `x-isfc-group-id`, `x-isfc-sub-id`, `x-isfc-sub-type`, `x-isfc-sub-attrs` (base64 JSON: roles, permissions, profile attributes), `x-isfc-sub-email-id`, `x-isfc-tenant-id`.
- **OPA contract (`go-commons/sidecar-middleware`).** `POST {OPA}/v1/data/<client id without '-'>/decision` with `input.sub`, `input.http{method,path}`, `input.client{clientId,apiKey}`, `input.tenant.id`. It returns `decision.allow`. Deny is 403 `AM_FB_01`; OPA down is 500 `AM_IE_01`.
- **Policies (`authz-policies/<service>/`).** `entry.rego` with helpers plus one file per route (`permit[method][path]` + `details`), and `opa test` tests. The bundle also carries `GET /api/v1/client/configuration/<client>` as `data.json` (`allowed_api_keys`, `allowed_client_ids`). The template is `authz-policies/templates/panel-backend`.
- **S2S.** The target client issues a key to the caller client with roles and permissions. The caller fetches it (`GET|POST /api/v1/client/api-key`, its own client credentials), caches it, and refreshes once on 401/403. Calls go **directly** to the target, not through the gateway. Permissions are listed in `authz-policies/S2S_PERMISSIONS.md`.
- **Login.** `EMAIL_OTP` and `MICROSOFT_SSO` are login methods attached to a client, with `allowed_domains`.
  - Email OTP: a standard Hydra authorization-code request with PKCE. Hydra sends the user to the shared ISFC login page, which handles the OTP and redirects to `redirect_uri?code=`.
  - Microsoft: the panel starts a Hydra login flow (to get its flow token), then redirects to Entra. auth-service's callback finishes the flow.
  - The code is exchanged at auth-service `POST /oauth2/token`, which also creates the session the gateway checks.
  - Local OTP is always `123456`.
- **Tenants.** `GET|POST /api/v1/tenant`, `GET|PATCH /api/v1/tenant/:id`. A tenant is `{id uuid, key, name, status ACTIVE|INACTIVE}`. A panel lists its tenants in client metadata (`tenancy`, `tenants`).

### 3.2 The Hub today (this repo)

| Area | Today | Files |
| --- | --- | --- |
| Admin API auth | Bearer JWT, **ES256 only**. Roles from claim `hub_roles`, tenants from `hub_tenants`. A dev issuer is built into `hub admin`. | `backend/internal/control/adminapi/auth.go`, `server.go:117-157`, `backend/cmd/hub/internal/roles/admin.go` |
| RBAC | `admin ⊃ editor ⊃ viewer ⊃ tenant_viewer`. Each of the 74 operations has `x-required-role` in the contract. `TestRBACMatrix` covers every route × role. | `adminapi/server_test.go:82`, `contracts/openapi/hub-admin.v1.yaml` |
| Console login | Dev role-picker only. `oidc.ts` is a stub that always returns 503 (HUB-282). The session cookie is HMAC-signed, **not encrypted**. A same-origin proxy (ADR-0022) attaches the token server-side. | `frontend/src/lib/server/auth/*`, `frontend/src/hooks.server.ts`, `frontend/src/lib/server/admin-proxy.ts` |
| Caller auth | `hk_live_` keys, SHA-256 hashed, looked up in the in-memory snapshot. Scopes `invoke`, `dry_run`, `requests_read` (the last is not enforced, HUB-415). | `backend/internal/edge/auth.go` |
| Tenants | `tenants/<id>.yaml` in the config tree: keys, rate limit, `status`, `disabled`. Created by `createTenant` (admin). | `backend/internal/registry/types.go:397`, `adminapi/tenants.go` |
| hubctl / hubmcp | `HUB_ADMIN_TOKEN` = the user's own bearer token. `hubctl login -dev` uses the dev issuer. | `backend/cmd/hubctl/admin.go`, `backend/cmd/hubmcp` |
| Users | No user tables. Identity exists only in the token. The change log records `actor` and `actor_roles`. | `backend/migrations/00007_change_log.sql` |

---

## 4. Target architecture

```
                 ┌──────────────────────── ISFC ────────────────────────┐
 Browser ──login─┼─> Hydra / ISFC login page (Email OTP, Microsoft)     │
    │            │   auth-service (tokens, sessions, clients, tenants,  │
    │            │                 API keys, client configuration)      │
    │            │   user-service (per-tenant roles)                    │
    ▼            │                                                      │
 Console (SvelteKit server: encrypted session, keeps tokens)            │
    │  /admin/v1/** + Authorization + X-Tenant-Id                        │
    ▼            │                                                      │
 ISFC api-gateway ── JWT + session + tenant + roles ──> x-isfc-* ───────┼──┐
                 └──────────────────────────────────────────────────────┘  │
                                                                           ▼
                         Hub admin (:8081) ── verify JWT (JWKS) ── Principal ── OPA sidecar (Rego per operation)
                                │                                                    ▲ bundle: integrationhub
                                │                                                    │ (policies + client configuration)
 Automation (CI, hubctl) ── x-isfc-client-id + x-isfc-api-key (direct, not via gateway) ─┘

 Caller service (ISFC auth lib) ── x-isfc-client-id + x-isfc-api-key [+ X-Tenant-Id] ──> Hub gateway (:8080) /v1/...
                                                                     in-memory key check, no OPA hop
 Legacy caller ── Authorization: Bearer hk_live_… ──────────────────> Hub gateway (unchanged until §15 cut-off)

 Hub worker: reconciler ── S2S key ──> auth-service (tenants, client metadata, client configuration)
          └─ writes tenant copies + hashed caller keys ─> Postgres ─> Redis notify ─> gateway nodes reload
```

---

## 5. Non-breaking strategy

| Switch | Values | Default | Effect |
| --- | --- | --- | --- |
| `HUB_ADMIN_AUTH_MODE` (backend) | `jwt` \| `isfc` | `jwt` | `jwt` is today's verifier and dev issuer, unchanged. `isfc` turns on §7. |
| `HUB_EDGE_ISFC_AUTH` (backend) | `off` \| `on` | `off` | `on` accepts ISFC S2S credentials **in addition to** `hk_live_` keys. |
| `HUB_TENANT_SOURCE` (backend) | `local` \| `isfc` | `local` | `isfc` starts the reconciler and makes `createTenant` return 409 `tenant_managed_by_isfc`. |
| `PUBLIC_AUTH_MODE` (frontend) | `dev` \| `isfc` | `dev` in mock builds; **must be set** otherwise | `isfc` is §9. `dev` is refused unless `__API_MOCKING__` is set or `NODE_ENV=test`. This is how the dev role-picker goes away from real deployments without breaking 29 Playwright specs. |

- Every existing test keeps running in the default mode.
- New tests run the new modes.
- CI runs both admin auth modes for the RBAC matrix.

---

## 6. Tenancy and sync design (F-25)

### 6.1 Data model

**Tenant YAML** (`tenants/<id>.yaml`) gains a contract-visible `isfc` block, plus `setup`:

```yaml
tenant: hl
display_name: Home Loans          # Hub override, optional
isfc:                             # written only by the reconciler
  tenant_id: 6f1c…-…              # the link (UUID)
  key: HL
  name: Home Loans
  status: ACTIVE
  linked: true                    # present in the console client's tenants list
  synced_at: 2026-11-11T10:00:00Z
setup: pending                    # pending | complete
status: active                    # active | suspended (Hub) ; disabled set by sync when ISFC says so
rate_limit: { rps: 100, burst: 200 }
api_keys: []                      # hk_live_ keys, legacy (§15)
```

**New Postgres tables** (migration in HUB-480):
- `hub.isfc_sync_runs(id, trigger, started_at, finished_at, result, changes jsonb)`;
- `hub.isfc_caller_keys(isfc_key_id, key_hash, caller_client_id, tenant_ids uuid[], permissions text[], status, updated_at)`.

  Keys are hashed with `registry.HashKey` the moment they're read. **The plaintext is never stored or logged.**

### 6.2 Reconciler

- Runs in the `worker` role, on the leader only (the same lease pattern as other singletons).
- Triggers:
  - an ISFC event (ISFC-CR-2), consumed by an SQS consumer behind an interface, or a signed webhook at `POST /internal/isfc/events` on the admin listener (decision D8);
  - a ticker (`HUB_ISFC_RECONCILE_INTERVAL`, default 5 min);
  - the admin operation `syncTenants` (new contract op, "Sync now").
- The pull is idempotent. A config change goes through the same control-layer code as `updateTenant`, then a snapshot is published. Caller-key changes skip the snapshot: they update `hub.isfc_caller_keys` and publish a Redis notification, so key revocation doesn't flood the change log.
- If ISFC is unreachable, the Hub keeps the last good state, raises `hub_isfc_sync_failures_total`, and alerts after 3 failed runs in a row. It never disables tenants because a fetch failed.

### 6.3 Console

- The tenant list shows the ISFC name, key and status, a "setup pending" badge, and "last synced".
- ISFC fields are read-only, with a link to ISFC for changing them.
- `createTenant` is hidden in `isfc` mode.
- A "Complete setup" flow covers rate limit, callback endpoints, grants and display name.

---

## 7. Admin plane: authentication and authorization (F-24)

### 7.1 Request path in `HUB_ADMIN_AUTH_MODE=isfc`

1. **Where requests come from.** The admin listener accepts only requests that came through the gateway (network policy; HUB-497) **and** carry a valid Hydra JWT. Automation requests, which arrive directly, must carry `x-isfc-api-key` instead (§7.5).
2. **Verify the JWT ourselves.** Check the signature against Hydra JWKS, plus `iss`, `exp`, `nbf` and optionally `aud`. ISFC's sidecar checks only the issuer (`TODO(isfc-security) 2`), so we don't copy that. This needs RS256 support in `RemoteKeys` and `JWTVerifier` (Hydra signs RS256 by default; the Hub parses only P-256 today).
3. **Check the token matches the headers.** `sub` equals `x-isfc-sub-id` and `client_id` equals `x-isfc-client-id`, which must equal the console client id. On a mismatch: 401.
4. **Build the `Principal`** from `x-isfc-sub-id`, `x-isfc-sub-email-id`, `x-isfc-tenant-id` and `x-isfc-sub-attrs` (roles, permissions), per §2.2. Malformed attributes give no roles (fail closed).
5. **Ask OPA.** `POST {HUB_OPA_URL}/v1/data/integrationhub/decision` with the sidecar input shape. Deny is 403 `forbidden` (Hub error shape). An error or timeout (`HUB_OPA_TIMEOUT`, default 200 ms) is 503 `authz_unavailable`, so it fails closed. The call publishes `hub_admin_authz_duration_ms{decision}`.
6. **Keep the in-process role check** (`x-required-role`) as a second layer. This matches the ISFC guidance to re-check in the handler.
7. **Tenant data scoping** stays where it is today (`SeesTenant`, 404 across tenants), now fed by the `x-isfc-tenant-id` → Hub tenant id mapping from the synced copy.

We write our **own** OPA client (about 150 lines) and header parser rather than importing `go-commons/sidecar-middleware`:
- its module path `isfc/go-commons/...` only resolves through a `replace` pointing outside this repo, which breaks CI;
- it uses ISFC's error codes, not ours;
- it doesn't verify signatures.

The input shape stays byte-compatible, and the same Rego tests prove it (HUB-479).

### 7.2 `/me`

`Me{subject, email, name, roles, tenants, permissions, token_expires_at}` stays the same shape:
- `roles` are the mapped Hub roles;
- `tenants` is the current tenant's Hub id (empty in platform scope);
- `permissions` are the ISFC permissions.

Nothing breaks for the console or hubmcp.

### 7.3 Rego layout

- Policies live in ISFC's `authz-policies/integration-hub/` (decision D3). Their bundle pipeline and auth-service's rebuild trigger only work there.
- The Hub repo keeps the **generator and tests**, plus a pinned copy for CI:

```
authz-policies/integration-hub/
├── metadata.yaml            local/qa/stage/prod client ids of `integration-hub`, bundle_path_prefix: integration-hub
├── entry.rego               copy of the panel template entry + hub helpers (platform_scope, tenant_scope, hub_role_rank)
├── policies/admin/v1/       one file per operation, e.g. vendors/createVendor.rego
│   ├── me/getMe.rego
│   ├── vendors/{listVendors,getVendor,createVendor,updateVendor,createEndpoint,...}.rego
│   ├── ... (74 files, one per operationId in hub-admin.v1.yaml)
└── tests/                   rbac_matrix_test.rego (generated from the contract), entry_test.rego
```

- **The generator** (`scripts/gen-authz.mjs`, HUB-478) reads every operation in `contracts/openapi/hub-admin.v1.yaml` and writes the policy file with: the path regex (with gin-style shadowing exclusions, as the ISFC README describes), the required permission, and the scope (platform or tenant). After generation, files may be edited by hand. The generator's `--check` mode fails CI if a file is missing, or if its permission or scope disagrees with the contract.
- **Path is the path the Hub sees**, for example `/admin/v1/vendors/v_1`, after the gateway's prefix rewrite.

### 7.4 Permission catalogue (on `integration-hub-console`, and on `integration-hub` for machine keys)

The permissions are per resource, so finer control later doesn't need new policies. The roles bundle them to keep **exactly today's matrix**. The source of truth is each operation's `x-required-role`, and `TestRBACMatrix` must still pass.

| Resource group (operations) | Read | Write (editor today) | Admin (admin today) |
| --- | --- | --- | --- |
| Vendors, endpoints | `HUB_VENDOR:READ` | — | `HUB_VENDOR:ADMIN` |
| Accounts, credentials | `HUB_ACCOUNT:READ` | `HUB_ACCOUNT:TEST` | `HUB_ACCOUNT:ADMIN` |
| Transformers, goldens | `HUB_TRANSFORMER:READ` | `HUB_TRANSFORMER:WRITE` | — |
| Integrations, drafts, publish, rollback, weights, pause | `HUB_INTEGRATION:READ` | `HUB_INTEGRATION:WRITE` | — |
| Tenants, API keys, callback endpoints | `HUB_TENANT:READ` | `HUB_TENANT:SETUP` (tenant_admin, D4) | `HUB_TENANT:ADMIN` |
| Requests, audit unseal | `HUB_REQUEST:READ` | — | `HUB_REQUEST:UNSEAL` |
| DLQ | `HUB_DLQ:READ` | `HUB_DLQ:REPLAY` | `HUB_DLQ:PURGE` |
| Breakers | `HUB_BREAKER:READ` | — | `HUB_BREAKER:ADMIN` |
| Snapshots, change log | `HUB_PLATFORM:READ` | — | — |

**Roles:**
- `HUB_VIEWER` = all `:READ`.
- `HUB_EDITOR` = viewer + all write permissions.
- `HUB_ADMIN` = everything.
- `HUB_TENANT_VIEWER` = `HUB_TENANT:READ`, `HUB_INTEGRATION:READ`, `HUB_REQUEST:READ`, `HUB_DLQ:READ`, valid in tenant scope only.
- `HUB_TENANT_ADMIN` = tenant viewer + `HUB_TENANT:SETUP` (D4).

**`ISFC_ADMIN`:** ISFC's template `entry.rego` lets it pass every rule. Our `entry.rego` **removes that shortcut** unless decision D6 says otherwise.

### 7.5 Machine access to the admin API (internal and backend-to-backend)

- An automation client (`integration-hub-ci`) gets a key from `integration-hub` with a role holding only the permissions it needs.
- It calls the Hub admin **directly** with `x-isfc-client-id` and `x-isfc-api-key`. This is the ISFC S2S rule.
- OPA checks the key with `has_required_permissions_on_api_key` against the bundle's `allowed_api_keys`.
- The Hub builds `Principal{Subject: "client:<id>", Machine: true}` with the key's permissions mapped to the highest role they fully cover. The change-log actor is `client:<id>`.
- Machine principals are **refused** for `unsealRequest` and `purgeDlq`, whatever the permissions (human-only operations, D7).

---

## 8. Caller plane (`/v1`): ISFC backend clients, no OPA hop

### 8.1 Onboarding a calling team

1. In ISFC, the team creates `<team>-backend` (`backend_services`).
2. An Integration Hub admin, or the tenant admin once ISFC-CR-1 lands, issues a key from `integration-hub` to `<team>-backend`, with role `HUB_CALLER` (`HUB:INVOKE`, optionally `HUB:DRY_RUN` and `HUB:REQUESTS_READ`) and the **tenants** it may act for.
3. In the Hub console, the tenant admin grants that tenant the integrations it needs. Grants stay a Hub concept.
4. The team's service uses the ISFC auth lib (`sidecar.NewSidecarTransport` in Go, `isfc-auth-lib` in Java) with `ISFC_S2S_CLIENT_IDS=<hub host>=integration-hub`. The lib fetches, caches and refreshes the key.

### 8.2 What the edge does (`HUB_EDGE_ISFC_AUTH=on`)

| Step | Rule |
| --- | --- |
| Pick the credential | `x-isfc-api-key` present → ISFC path. `Authorization: Bearer hk_…` → legacy path. Both present → 400. |
| Look up the key | `HashKey(x-isfc-api-key)` in the in-memory map loaded from `hub.isfc_caller_keys`, plus a constant-time compare. Not found, not ACTIVE, or `caller_client_id ≠ x-isfc-client-id` → **401** (the ISFC lib refreshes its key once on 401/403). |
| Resolve the tenant | Key covers exactly one linked tenant → that tenant. More than one → `X-Tenant-Id` (ISFC UUID) is required and must be in the key's tenants, otherwise 400 or 403. The UUID maps to the Hub tenant id through the snapshot copy. |
| Tenant state | `setup: pending`, `disabled` or ISFC INACTIVE → 401. `suspended` → 403 `tenant_suspended` (as today). |
| Scope | `HUB:INVOKE` → `invoke`, `HUB:DRY_RUN` → `dry_run`, `HUB:REQUESTS_READ` → `requests_read`. Integration-level control stays in Hub **grants**, so ISFC doesn't need one permission per integration. |
| Everything after | Unchanged: grant check, tenant limiter, body limit, validate, resolve, idempotency (keyed by Hub tenant), and so on. |

- **Latency:** one SHA-256, one map lookup and a small slice scan. This fits the `auth` stage budget, proven by the benchmark in HUB-490.
- **Revocation** takes effect within the reconcile interval, or within seconds once ISFC events exist. That's longer than today's instant revocation of `hk_` keys, and it's documented in SECURITY.md.
- **Audit:** `request.received` records `caller_client_id`, `isfc_key_id` and `key_hash`, never the key. The logger redacts `x-isfc-api-key` by value.

### 8.3 Optional: Rego on the caller plane

Only if decision D2 says so: one `caller.rego` evaluated **in-process** with OPA's Go SDK (prepared query) on the same input, after the in-memory key check. It's adopted only if HUB-490 shows the p99 added cost stays inside the auth budget. Never a sidecar hop on `/v1`.

---

## 9. Console login (replaces the dev role-picker)

The backend-for-frontend model of ADR-0005 and ADR-0022 stays: **the access and refresh tokens never reach page JavaScript**.

| Step | What happens |
| --- | --- |
| Login page `GET /login` | The server fetches `GET {ISFC_AUTH}/oauth2/method/integration-hub-console` and renders **Email OTP** and **Microsoft** buttons (only those two; anything else is ignored). |
| Email OTP | `POST /login?/start` makes the server generate PKCE (S256), `state` and `nonce`, and store them in a short-lived encrypted httpOnly cookie `hub_login`. It responds with 303 to `{hydra}/oauth2/auth?...`. Hydra shows the ISFC login page (OTP), then redirects to `/auth/callback?code=&state=`. |
| Microsoft | Needs Hydra's CSRF cookie in the browser, so a small client script calls `GET {ISFC_AUTH}/oauth2/auth?…` with `credentials: include` to get the flow token, then redirects to Entra. The PKCE challenge and state come from the server, as for OTP. auth-service's callback finishes the flow and Hydra redirects to `/auth/callback`. This is the same as omniauth `sso()`; we can vendor that small piece or depend on `@isfc/omniauth` once it's published (ISFC-CR-6). |
| Callback `/auth/callback` | The server checks `state`, then calls `POST {ISFC_AUTH}/oauth2/token` (authorization_code, code_verifier, `session_info {platform: WEB, loginMethod}`) **with the browser's `x-device-id` and User-Agent**, because auth-service binds the session to the device. It stores tokens in an **encrypted** session (AES-GCM, key from `HUB_SESSION_SECRET`; closes HUB-282 AC) and removes `code` from the URL. |
| Refresh | Server-side, before expiry or on 401 `GW_004`. A single refresh runs per session (lock), and a 409 from auth-service is retried once after 300 ms, like the gateway's `token_handler`. |
| Tenant | The dropdown is `GET {gateway}/auth/oauth2/panels` → this panel's tenants (platform tenant first). The choice is stored in the session, and the proxy adds `X-Tenant-Id` to every `/admin/v1/**` call and reads back `X-Isfc-Tenant-Id`. Switching tenant clears client caches and reloads `/me`. |
| Admin calls | `HUB_ADMIN_UPSTREAM` points at the **ISFC gateway route** for the Hub admin. The proxy and `attachAdminToken` are unchanged apart from the tenant header. |
| Logout | `POST {ISFC_AUTH}/api/v1/logout {session_id}` with the access token, then delete the session cookie. |
| Errors | Each gets its own message: 403 `AS_FB_21` (no tenant in this panel), `AS_VE_34` (email domain not allowed), 401 `GW_007` (session ended), 403 `GW_015` (no access to that tenant). |

**Gateway route** (ISFC routing document):

```yaml
- name: integration-hub-admin
  hosts: [hub.internal.<domain>]
  prefix: /hub/admin
  upstream: http://hub-admin.<ns>.svc.cluster.local:8081
  strip_prefix: true
  rewrite_prefix: /admin
  auth: required
  tenant: required
  rate_limit: per-user
  timeout: 30s
```

---

## 10. hubctl, hubmcp, SDK and consumer skill

| Tool | Change |
| --- | --- |
| `hubctl login` | A new `-isfc` flow: PKCE with a loopback redirect (`http://127.0.0.1:<port>/callback`) on the `hubctl` public client, the token saved at 0600 as today. `-dev` stays for local `jwt` mode. `HUB_ADMIN_URL` points at the gateway route. |
| `hubctl` in CI | `HUBCTL_ISFC_CLIENT_ID` + `HUBCTL_ISFC_CLIENT_SECRET` → fetch the key from auth-service → call the admin **directly** with `x-isfc-client-id` and `x-isfc-api-key` (§7.5). |
| `hubmcp` | `HUB_ADMIN_TOKEN` stays the user's own token (ADR-0020). `HUB_ADMIN_BASE_URL` must be the gateway route, and a new `HUB_TENANT_ID` sends `X-Tenant-Id`. The HTTP transport also accepts ISFC S2S credentials. |
| `pkg/hubclient` (Go SDK) | An option `WithISFC(clientID, secret, authBaseURL)` that does what the ISFC auth lib does (fetch, cache, refresh once on 401/403). |
| `clients/claude-skill` | New onboarding steps (§8.1); `hk_live_` described as legacy. |

---

## 11. Observability

| Signal | Name | Labels / fields |
| --- | --- | --- |
| Metric | `hub_admin_authz_duration_ms` | `decision=allow\|deny\|error` |
| Metric | `hub_admin_authn_failures_total` | `reason=no_token\|bad_signature\|header_mismatch\|no_roles` |
| Metric | `hub_stage_duration_ms{stage="auth"}` | unchanged; ISFC path included |
| Metric | `hub_edge_auth_total` | `method=isfc\|hub_key`, `result` (to track legacy-key retirement) |
| Metric | `hub_isfc_sync_runs_total`, `hub_isfc_sync_failures_total`, `hub_isfc_sync_lag_seconds` | `trigger=event\|tick\|manual` |
| Metric | `hub_isfc_caller_keys` (gauge) | — |
| Log | `admin.authz` | subject, client, tenant, operation, decision, reason (no token, no attrs dump) |
| Log | `isfc.sync` | run id, created/updated/disabled counts |
| Trace | span `admin.authz.opa` | decision |
| Alert | `HubAdminAuthzUnavailable` | OPA errors > 1% for 5 min |
| Alert | `HubIsfcSyncStale` | `hub_isfc_sync_lag_seconds` > 900 |

Every new name is added to `docs/tech/OBSERVABILITY.md` and the feature docs (AGENTS.md §8.6).

---

## 12. Bugs and gaps found during the study

| # | Gap | Impact | Handling |
| --- | --- | --- | --- |
| G1 | **ISFC API keys have no tenant** (`auth-service/internal/domain/apikey/model.go`; `allowed_api_keys` only has key, client, status, roles, permissions). | "Link my client to the Hub for tenant X" can't be expressed in ISFC today. | ISFC-CR-1. **Interim:** one caller client per tenant, and the Hub tenant's setup lists the ISFC caller client ids that belong to it (console field, Hub-side). Remove it once CR-1 ships. |
| G2 | **ISFC emits no tenant or client-link events**; only a GitHub dispatch for bundle rebuilds. | No push sync. | ISFC-CR-2. Polling reconcile works without it. |
| G3 | **Tenant id formats differ.** ISFC key is 1–64 chars; Hub id is 3–64. | `HL` can't become a Hub id as-is. | Derived immutable Hub id; link by UUID (§2.1). |
| G4 | **ISFC API keys are plaintext** in the auth-service DB, in the configuration API and in the OPA bundle `data.json` (`TODO(isfc-security) 4`). | Anyone who can read the bundle in S3 or call the config API can impersonate callers. | The Hub hashes on read and never stores or logs plaintext. ISFC-CR-4 asks for `key_sha256` in the configuration response, so we never receive plaintext at all. |
| G5 | **ISFC sidecar trusts identity headers and checks only the token issuer** (`TODO(isfc-security) 2`, 7). | Anyone who reaches a backend directly can forge `x-isfc-*`. | The Hub verifies the JWT itself (§7.1); a network policy restricts the admin listener (HUB-497). |
| G6 | **Microsoft `id_token` is parsed without verification** in auth-service (`internal/idp/http.go:93`). | A weak link in the Microsoft login path, on ISFC's side. | ISFC-CR-4. Enable Microsoft in production only after it's fixed (D9). |
| G7 | **`ISFC_ADMIN` passes every rule** in the ISFC template `entry.rego`. | ISFC platform admins would silently be Hub admins. | Removed in our `entry.rego` (D6). |
| G8 | **Platform-wide resources versus `tenancy: required`.** | Vendors and accounts have no tenant. | Platform tenant (§2.2, D1). |
| G9 | **Hub JWT verifier is ES256 / P-256 only.** | Hydra tokens (RS256) would fail. | HUB-472. |
| G10 | **`go-commons` and `@isfc/omniauth` aren't published** (module path `isfc/go-commons/...`, npm package private). | We can't import them in CI. | Write our own small client (§7.1). ISFC-CR-6. |
| G11 | **Local port clash.** ISFC uses 8080, 8081, 5432 and 6379, as does the Hub. | The two stacks can't run side by side. | HUB-461: Hub ports configurable in `scripts/dev-up.sh` (gateway 8280, admin 8281, Postgres 5433, Redis 6380). |
| G12 | **Hub docs drift from code**: SECURITY §7 RBAC matrix, TECH_SPEC `hub_tenant` vs `hub_tenants`, `/auth/login` routes that don't exist, an `oidc-stub` compose service that doesn't exist, no CSP although SECURITY §2.2 claims one. | Misleading reviewers. | Fixed in HUB-468. CSP gets its own ticket (HUB-498). |
| G13 | **`requests_read` is not enforced** (HUB-415) and `GET /v1/requests/{id}` checks no scope. | ISFC `HUB:REQUESTS_READ` would be meaningless. | HUB-415 must land before HUB-487. |
| G14 | **Session cookie is signed, not encrypted** (HUB-282). | The access token can be read from the cookie value. | HUB-492. |
| G15 | **Caller key revocation is slower** than with `hk_` keys (reconcile interval). | Up to 5 min of access after revocation without events. | Documented. Events (CR-2) bring it to seconds. A "Sync now" action also exists. |
| G16 | **`hubmcp` default `HUB_ADMIN_BASE_URL`** (`HUB_BASE_URL + /admin/v1`) isn't where the admin listens. | Already confusing, and it gets worse behind a gateway. | Fixed in HUB-496. |

---

## 13. Change requests to the ISFC team

| Id | Request | Needed by |
| --- | --- | --- |
| ISFC-CR-1 | Optional `tenants: [uuid]` on client API keys (create, update, list, configuration response); empty means "no tenant restriction". Shown in the Admin UI key dialog. | Caller linking per tenant (G1) |
| ISFC-CR-2 | Change events on SNS/SQS (or signed webhooks): `tenant.created`, `tenant.updated`, `client.tenants_changed`, `client.api_key.changed`, `client.mapping.changed`. Each carries `event_id`, `occurred_at`, `client_id`, `tenant_id`. At-least-once delivery. Emitted next to `BundleNotifier.ClientChanged`. | Push sync (G2) |
| ISFC-CR-3 | Let a tenant admin issue keys from a given client for **their** tenant only (self-service linking). | Self-service onboarding (D4) |
| ISFC-CR-4 | Security: verify IdP id_tokens (TODO 2); hash API keys and expose `key_sha256` in the configuration response (TODO 4). | G4, G6 |
| ISFC-CR-5 | S2S permissions for `integration-hub` on auth-service: `TENANT:READ`, `CLIENT:READ`, and read access to `GET /api/v1/client/configuration/integration-hub`. Add them to `S2S_PERMISSIONS.md` and `seed-s2s.py`. | Reconciler |
| ISFC-CR-6 | Publish `@isfc/omniauth` and `go-commons/sidecar-middleware` to an internal registry. | Nice to have (G10) |

---

## 14. Contract changes (contract-steward, before any code)

| Ticket | Contract | Change | Semver |
| --- | --- | --- | --- |
| HUB-469 | `hub-admin.v1.yaml` | Add security scheme `isfcGateway` (Bearer JWT via the ISFC gateway + `X-Tenant-Id`) and `isfcApiKey` (machine). `x-required-permission` per operation next to `x-required-role`. Role enum + `tenant_admin` (if D4). `Tenant` gains read-only `isfc{tenant_id,key,name,status,linked,synced_at}` and `setup`. New ops `syncTenants` and `syncTenant`. New error codes `tenant_managed_by_isfc` (409) and `authz_unavailable` (503). | minor (additive). The new enum value is called out for the frontend `gen:api`. |
| HUB-470 | `hub-public.v1.yaml` | Add security scheme `isfcS2S` (`x-isfc-client-id` + `x-isfc-api-key`) as an alternative to `bearerAuth`, plus an optional `X-Tenant-Id` header. **No vendor, account or version leak** (identity only). | minor |
| HUB-471 | `contracts/mcp/hubmcp-tools.json` | Env `HUB_TENANT_ID`, the meaning of `HUB_ADMIN_BASE_URL`, and ISFC credentials for the HTTP transport. | minor |

---

## 15. Rollout and migration

1. **Phase 0 verification** (HUB-461 to HUB-464) on the local ISFC stack. Nothing ships.
2. **Shadow mode in QA:**
   - admin in `isfc` mode behind the gateway;
   - console in `isfc` mode for the platform team;
   - reconciler on with `HUB_TENANT_SOURCE=isfc` in **report-only** mode (`HUB_ISFC_SYNC_DRY_RUN=true`): it logs what it would change.
3. **Link existing tenants.** `hubctl tenants link <hub-id> <isfc-tenant-uuid>` (HUB-498) writes `isfc.tenant_id` for tenants that already exist in the Hub, so sync updates them instead of creating duplicates. A report lists Hub tenants with no ISFC match.
4. **Turn sync on.**
5. **Callers move** to ISFC credentials team by team (`hub_edge_auth_total{method}` shows progress). `hk_live_` keys keep working.
6. **Cut-off:** after every caller has moved (and at least 6 months, by analogy with D11), set `HUB_EDGE_LEGACY_KEYS=off`. Admin key-minting routes return 410 in ISFC mode.
7. **Rollback** at any step means flipping the flag back. No data migration is destructive: ISFC fields are additive and `hk_` keys are untouched.

---

## 16. Docs to write or update (AGENTS.md §8)

| Doc | Change |
| --- | --- |
| ADR-0026 | ISFC as the identity provider. Admin authz via gateway headers + signature check + OPA policy per operation. Platform tenant. Supersedes the auth parts of ADR-0005 and ADR-0022 (the proxy itself stays). |
| ADR-0027 | Tenants are owned by ISFC. The Hub keeps a copy + Hub-only settings. Event-triggered, pull-based reconciler. Derived immutable tenant ids. |
| ADR-0028 | Caller auth with ISFC backend clients, checked in memory; no OPA on the sync path (DESIGN §18.5); legacy `hk_` keys until cut-off. |
| `docs/features/F-24-isfc-identity-and-authz.md`, `F-25-tenants-from-isfc.md` | From `_TEMPLATE.md`; add to `docs/features/README.md` and its graph. |
| `docs/product/USER_STORIES.md` | US-2401… (operator signs in with Email OTP or Microsoft; platform admin acts across tenants; tenant admin finishes setup; access denied without a role) and US-2501… (tenant appears after linking in ISFC; tenant disabled when deactivated in ISFC; caller team onboards with an ISFC backend client). |
| `docs/product/PRD.md` | New FRs (SSO login, per-operation policy, ISFC tenant source, ISFC caller credentials), NFR (auth stage budget unchanged), DV entry for the platform tenant. |
| `docs/tech/SECURITY.md` | Trust boundaries (gateway → admin, worker → ISFC), fixed RBAC matrix, G4/G5/G6/G15. |
| `docs/tech/TECH_SPEC.md` | Env vars (§5 flags, `HUB_OPA_URL`, `HUB_ISFC_*`), compose, §17 console auth. |
| `docs/tech/OBSERVABILITY.md` | §11 |
| `docs/tech/TESTING_STRATEGY.md` | `mockisfc` (a fake auth-service: tenants, client metadata, configuration, key fetch, token, JWKS). **Tests never call a real ISFC.** |
| `CLAUDE.md` / `AGENTS.md` | Add "ISFC is the identity and tenant source; never call real ISFC in tests". |

---

## 17. Tickets: EP-15 "ISFC identity, tenancy and authorization" (HUB-460 to HUB-499)

Ticket format follows `docs/tickets/README.md`. These are **drafts**; they move into `TICKETS.md` (and the EP-15 row into the ID-range table) once this plan is approved. Stories use the feature prefix until real ids exist.

### Phase 0: Verify (no product code)

#### HUB-460 — Plan: ISFC identity, tenancy and authorization
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** in-progress · **Depends on:** — · **Feature:** F-24, F-25 · **Stories:** —
- **Branch:** `docs/HUB-460-isfc-auth-plan`

This document.

**Acceptance criteria**
- [ ] Decisions D1–D9 answered by the user and recorded here.
- [ ] Tickets copied into `TICKETS.md`, EP-15 added to the ID-range table, `recount.py` run.

#### HUB-461 — Run the ISFC local stack beside the Hub
- **Epic:** EP-15 · **Type:** chore · **Area:** infra
- **Status:** todo · **Depends on:** HUB-460 · **Feature:** F-24 · **Stories:** —
- **Branch:** `chore/HUB-461-isfc-local-stack`

Make the Hub's local ports configurable so both stacks run at once (G11), and document the start order.

**Acceptance criteria**
- [ ] `scripts/dev-up.sh` honours `HUB_GATEWAY_PORT`, `HUB_ADMIN_PORT`, `HUB_PG_PORT`, `HUB_REDIS_PORT`; `deploy/docker-compose.yml` reads them.
- [ ] `docs/runbooks/isfc-local.md`: ISFC `SETUP.md` steps + Hub steps, verified on one machine.
- [ ] Existing `dev-up.sh` defaults unchanged (e2e-real still green).

#### HUB-462 — Spike: console login and gateway headers end to end
- **Epic:** EP-15 · **Type:** test · **Area:** infra
- **Status:** todo · **Depends on:** HUB-461 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `test/HUB-462-spike-isfc-login`

Create the ISFC clients from §2.4 (console + `integration-hub`), roles, a platform tenant and two demo tenants, and a gateway route to a header-dump upstream. Log in with Email OTP.

**Acceptance criteria**
- [ ] Findings note (`docs/tech/spikes/isfc-login.md`) with real samples of every `x-isfc-*` header, the decoded `sub-attrs`, the token header (`alg`, `kid`) and claims, and the `/oauth2/token` response.
- [ ] Confirmed: OTP flow returns to our redirect URI; session from `/oauth2/token` passes the gateway session check; `X-Tenant-Id` switching changes roles; 403 `GW_015` for a tenant without a role.
- [ ] Seed script `scripts/isfc-seed-hub.py` (idempotent) that recreates all of it.

#### HUB-463 — Spike: OPA policy and S2S key flow
- **Epic:** EP-15 · **Type:** test · **Area:** infra
- **Status:** todo · **Depends on:** HUB-462 · **Feature:** F-24 · **Stories:** —
- **Branch:** `test/HUB-463-spike-opa-s2s`

Build a bundle for `integration-hub` with three hand-written policies (one platform read, one admin write, one tenant read). Issue a key from `integration-hub` to a demo caller and fetch it with the caller's credentials.

**Acceptance criteria**
- [ ] `opa eval` allow/deny for user and API-key inputs recorded in the findings note.
- [ ] Shape of `GET /api/v1/client/configuration/integration-hub` recorded; which permission it needs confirmed (ISFC-CR-5 filed).
- [ ] Measured: local OPA sidecar round trip p50/p99 for one decision.

#### HUB-464 — Spike: tenant sync feasibility and ISFC change requests
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-462 · **Feature:** F-25 · **Stories:** —
- **Branch:** `docs/HUB-464-spike-tenant-sync`

Read the tenant and client APIs with the `integration-hub` S2S key, and confirm what a reconciler can see.

**Acceptance criteria**
- [ ] Confirmed endpoints and permissions for: list tenants, get tenant, get client metadata (`tenants`), get client configuration.
- [ ] ISFC-CR-1, CR-2, CR-4, CR-5 written up and sent to the ISFC team; answers linked here.
- [ ] Microsoft SSO: Entra app details obtained, or explicitly deferred (D9).

### Phase 1: Docs and contracts

#### HUB-465 — ADR-0026: ISFC identity and per-operation OPA policies for the admin API
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-463 · **Feature:** F-24 · **Stories:** —
- **Branch:** `docs/HUB-465-adr-isfc-admin-authz`

**Acceptance criteria**
- [ ] ADR-0026 Accepted; ADR-0005 and ADR-0022 marked "partly superseded" with a link; PRD DV entry for the platform tenant.

#### HUB-466 — ADR-0027: tenants owned by ISFC, Hub keeps a synced copy
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-464 · **Feature:** F-25 · **Stories:** —
- **Branch:** `docs/HUB-466-adr-isfc-tenants`

**Acceptance criteria**
- [ ] ADR-0027 Accepted, covering source-of-truth table, lifecycle, id derivation, reconcile vs events, failure behaviour.

#### HUB-467 — ADR-0028: caller auth with ISFC backend clients, checked in memory
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-463 · **Feature:** F-24 · **Stories:** —
- **Branch:** `docs/HUB-467-adr-isfc-callers`

**Acceptance criteria**
- [ ] ADR-0028 Accepted, with the HUB-463 OPA latency numbers and the DESIGN §18.5 argument; legacy-key cut-off rule recorded.

#### HUB-468 — Feature docs, stories, PRD, SECURITY / TECH_SPEC / OBSERVABILITY updates
- **Epic:** EP-15 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-465, HUB-466, HUB-467 · **Feature:** F-24, F-25 · **Stories:** US-24xx, US-25xx
- **Branch:** `docs/HUB-468-isfc-feature-docs`

**Acceptance criteria**
- [ ] F-24 and F-25 created from the template and indexed; user stories with Given/When/Then; PRD FR/NFR added.
- [ ] SECURITY §7 matrix matches `TestRBACMatrix`; TECH_SPEC drift (G12) fixed; OBSERVABILITY lists §11.
- [ ] `TESTING_STRATEGY.md` describes `mockisfc`; CLAUDE.md and AGENTS.md rule "never call real ISFC in tests".

#### HUB-469 — contract(admin): ISFC security schemes, tenant ISFC fields, sync operations
- **Epic:** EP-15 · **Type:** contract · **Area:** contracts
- **Status:** todo · **Depends on:** HUB-465, HUB-466 · **Feature:** F-24, F-25 · **Stories:** —
- **Branch:** `contract/HUB-469-admin-isfc`

**Acceptance criteria**
- [ ] §14 admin changes in `hub-admin.v1.yaml`; `info.version` minor bump; `contracts/CHANGELOG.md` entry; lint green; backend and frontend notified.

#### HUB-470 — contract(public): ISFC S2S security scheme and X-Tenant-Id
- **Epic:** EP-15 · **Type:** contract · **Area:** contracts
- **Status:** todo · **Depends on:** HUB-467 · **Feature:** F-24 · **Stories:** —
- **Branch:** `contract/HUB-470-public-isfc`

**Acceptance criteria**
- [ ] `isfcS2S` scheme and optional `X-Tenant-Id`; no vendor, account or version field; minor bump; changelog.

#### HUB-471 — contract(mcp): ISFC settings for hubmcp
- **Epic:** EP-15 · **Type:** contract · **Area:** contracts
- **Status:** todo · **Depends on:** HUB-469 · **Feature:** F-24 · **Stories:** —
- **Branch:** `contract/HUB-471-mcp-isfc`

**Acceptance criteria**
- [ ] `HUB_TENANT_ID`, gateway `HUB_ADMIN_BASE_URL`, ISFC credentials for HTTP transport; changelog.

### Phase 2: Backend, admin plane

#### HUB-472 — RS256 JWKS support in the admin JWT verifier
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-462 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-472-rs256-jwks`

**Acceptance criteria**
- [ ] `RemoteKeys` parses RSA keys (≥ 2048 bits) as well as P-256; `JWTVerifier` accepts RS256 and ES256 (explicit allow-list, no `none`, no HS*).
- [ ] Table tests: RS256 valid, wrong kid, rotated key, short RSA key refused; existing ES256 tests unchanged.

#### HUB-473 — ISFC gateway identity → Principal (`HUB_ADMIN_AUTH_MODE=isfc`)
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-469, HUB-472 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-473-isfc-principal`

**Acceptance criteria**
- [ ] Header parser + token/header cross-check (§7.1 steps 2–4); platform vs tenant scope per §2.2; malformed `sub-attrs` → no roles.
- [ ] Mode flag; `jwt` mode byte-for-byte unchanged (existing tests green).
- [ ] Metric `hub_admin_authn_failures_total{reason}`.

#### HUB-474 — OPA decision client for the admin API
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-473 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-474-opa-client`

**Acceptance criteria**
- [ ] Sidecar-compatible input; deny → 403, error or timeout → 503 `authz_unavailable`; `hub_admin_authz_duration_ms{decision}`; span `admin.authz.opa`.
- [ ] `HUB_OPA_DISABLED=true` refused unless `HUB_ENV=test`.
- [ ] Unit tests with a fake OPA; integration test with a real OPA container (testcontainers).

#### HUB-475 — Wire ISFC mode into the admin server, `/me`, change log
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-474 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-475-admin-isfc-wiring`

**Acceptance criteria**
- [ ] `authorize` = Principal → OPA → role check; `/me` per §7.2; change-log actor = email (or `client:<id>`).
- [ ] `TestRBACMatrix` runs in both modes (ISFC mode with signed RS256 test tokens + generated headers + real policies).

#### HUB-476 — Machine principals on the admin API (ISFC API keys)
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-475 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-476-admin-machine-keys`

**Acceptance criteria**
- [ ] `x-isfc-client-id` + `x-isfc-api-key` accepted directly; OPA decides; principal `client:<id>`; unseal and purge refused for machines.
- [ ] Logger redacts `x-isfc-api-key` by value (test).

### Phase 3: Rego

#### HUB-477 — `entry.rego` and permission catalogue for `integration-hub`
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-463, HUB-469 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-477-authz-entry`

**Acceptance criteria**
- [ ] `entry.rego` from the ISFC panel template + `platform_scope`, `tenant_scope`; `ISFC_ADMIN` shortcut removed (D6).
- [ ] Permission and role catalogue (§7.4) seeded by `scripts/isfc-seed-hub.py`.

#### HUB-478 — Policy generator from the admin contract
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-477 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-478-authz-generator`

**Acceptance criteria**
- [ ] `scripts/gen-authz.mjs` writes one file per operation (path regex with shadowing exclusions, permission, scope); `--check` fails on missing or disagreeing files.
- [ ] CI job runs `--check` and `opa test`.

#### HUB-479 — 74 admin policies + Rego RBAC matrix tests
- **Epic:** EP-15 · **Type:** test · **Area:** backend
- **Status:** todo · **Depends on:** HUB-478 · **Feature:** F-24 · **Stories:** —
- **Branch:** `test/HUB-479-authz-policies`

**Acceptance criteria**
- [ ] One policy per `operationId`; generated `rbac_matrix_test.rego` mirrors `TestRBACMatrix` (every op × role × scope); cross-tenant and machine cases.
- [ ] Policies copied to ISFC `authz-policies/integration-hub/`; `scripts/check.sh integration-hub` green there.

### Phase 4: Tenant sync

#### HUB-480 — Tenant ISFC fields, setup state, sync tables
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-469 · **Feature:** F-25 · **Stories:** US-25xx
- **Branch:** `feat/HUB-480-tenant-isfc-fields`

**Acceptance criteria**
- [ ] `registry.Tenant` gains `isfc` and `setup`; loader validates; old YAML loads unchanged.
- [ ] Goose migration for `hub.isfc_sync_runs` and `hub.isfc_caller_keys`; integration test on real Postgres.
- [ ] Edge refuses `setup: pending` tenants (401) only when `HUB_TENANT_SOURCE=isfc`.

#### HUB-481 — ISFC client and `mockisfc`
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-464 · **Feature:** F-25 · **Stories:** —
- **Branch:** `feat/HUB-481-isfc-client`

**Acceptance criteria**
- [ ] `internal/isfc` client: fetch own key from auth-service (client credentials), refresh once on 401/403; tenants, client metadata, client configuration; timeouts; no secret in logs.
- [ ] `backend/cmd/mockisfc` (or a test server package) serving the same endpoints + JWKS + token mint; used by all ISFC tests.

#### HUB-482 — Tenant reconciler
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-480, HUB-481 · **Feature:** F-25 · **Stories:** US-25xx
- **Branch:** `feat/HUB-482-tenant-reconciler`

**Acceptance criteria**
- [ ] Leader-only loop in `worker`; create `pending`, update names, disable on INACTIVE or unlink, re-enable; never deletes; id derivation per §2.1 with collision test.
- [ ] Dry-run mode; change-log actor `isfc-sync`; ISFC down keeps last state; metrics and alert per §11.
- [ ] Table tests for every lifecycle row in §2.1.

#### HUB-483 — Caller-key sync and gateway reload
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-482 · **Feature:** F-24 · **Stories:** US-25xx
- **Branch:** `feat/HUB-483-caller-key-sync`

**Acceptance criteria**
- [ ] Reconciler hashes keys on read into `hub.isfc_caller_keys` (with tenants once CR-1 exists, else the interim Hub-side mapping); Redis notify; gateway nodes rebuild an in-memory map without touching the snapshot.
- [ ] Test: revoked in mockisfc → 401 at the edge after one reconcile.

#### HUB-484 — Sync triggers: ISFC events and "Sync now"
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-482 · **Feature:** F-25 · **Stories:** US-25xx
- **Branch:** `feat/HUB-484-sync-triggers`

**Acceptance criteria**
- [ ] `syncTenants` / `syncTenant` admin ops; event consumer behind an interface (SQS or webhook per D8) that only triggers a reconcile; duplicate and out-of-order events harmless (test).

#### HUB-485 — Console: ISFC-managed tenants and setup flow
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-469 · **Feature:** F-25 · **Stories:** US-25xx
- **Branch:** `feat/HUB-485-console-isfc-tenants`

**Acceptance criteria**
- [ ] Tenant list and detail show ISFC fields read-only, setup badge, last synced, "Sync now"; create hidden in ISFC mode; "Complete setup" flow.
- [ ] MSW handlers and unit tests; axe clean.

### Phase 5: Caller plane

#### HUB-486 — Edge: accept ISFC S2S credentials
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-470, HUB-483 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-486-edge-isfc-auth`

**Acceptance criteria**
- [ ] §8.2 rules behind `HUB_EDGE_ISFC_AUTH`; `hub_edge_auth_total{method,result}`; `hk_` path unchanged (existing edge tests green).
- [ ] Table tests: wrong client id, revoked, multi-tenant key without `X-Tenant-Id`, tenant not in key, pending tenant, suspended tenant.

#### HUB-487 — Scopes from ISFC permissions
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-486, HUB-415 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-487-isfc-scopes`

**Acceptance criteria**
- [ ] `HUB:INVOKE`, `HUB:DRY_RUN` and `HUB:REQUESTS_READ` map to the existing scopes; `requests_read` enforced for both credential types.

#### HUB-488 — Audit and logs for ISFC callers
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-486 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-488-isfc-caller-audit`

**Acceptance criteria**
- [ ] `request.received` carries `caller_client_id` and `isfc_key_id`; redaction test for `x-isfc-api-key`.

#### HUB-489 — SDK, hubmcp HTTP transport and consumer skill
- **Epic:** EP-15 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-486, HUB-471 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-489-sdk-mcp-isfc`

**Acceptance criteria**
- [ ] `pkg/hubclient` `WithISFC`; hubmcp HTTP accepts ISFC credentials; `clients/claude-skill` onboarding per §8.1.

#### HUB-490 — Benchmark: auth stage with ISFC keys (and embedded OPA, if D2 asks)
- **Epic:** EP-15 · **Type:** perf · **Area:** backend
- **Status:** todo · **Depends on:** HUB-486 · **Feature:** F-24 · **Stories:** —
- **Branch:** `perf/HUB-490-isfc-auth-bench`

**Acceptance criteria**
- [ ] `make bench`: auth stage p99 with 10k ISFC keys within the DESIGN §18.6 budget; result in `docs/tech/LATENCY.md`.
- [ ] If D2 = embedded OPA: same numbers with one prepared query; adopt only if within budget.

### Phase 6: Console login and tools

#### HUB-491 — Console ISFC provider: Email OTP, callback, code exchange
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-462 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-491-console-isfc-otp`

Replaces the HUB-282 stub (HUB-282 closed as superseded).

**Acceptance criteria**
- [ ] Login page lists Email OTP and Microsoft from `/oauth2/method/<client>`; PKCE S256, `state`, `nonce` in an encrypted short-lived cookie; callback validates `state`; server-side code exchange with `x-device-id` and User-Agent.
- [ ] Unit tests with a fake auth-service; error messages for `AS_FB_21`, `AS_VE_34`.

#### HUB-492 — Encrypted session, refresh, logout
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-491 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-492-console-session`

**Acceptance criteria**
- [ ] AES-GCM session (token unreadable from the cookie value: test); single-flight refresh with 409 retry; expiry → `/login?expired=1`; logout calls ISFC and clears the cookie.

#### HUB-493 — Microsoft SSO button
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-491, HUB-464 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-493-console-microsoft`

**Acceptance criteria**
- [ ] Browser starts the Hydra flow with server-issued PKCE and state; Entra redirect; lands on `/auth/callback`; tested against a fake provider in unit tests; manual check against Entra in QA (D9).

#### HUB-494 — Tenant switcher and `X-Tenant-Id` through the proxy
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-492, HUB-475 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-494-console-tenant-switcher`

**Acceptance criteria**
- [ ] Dropdown from `/oauth2/panels` (platform tenant first); choice kept in the session; proxy sends `X-Tenant-Id`, syncs `X-Isfc-Tenant-Id`; switching clears caches and reloads `/me`; 403 `GW_015` message.

#### HUB-495 — Dev login restricted to mock builds; real local stack on ISFC
- **Epic:** EP-15 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-494 · **Feature:** F-24 · **Stories:** —
- **Branch:** `feat/HUB-495-dev-login-mock-only`

**Acceptance criteria**
- [ ] `PUBLIC_AUTH_MODE=dev` refused outside mock or test builds; mock e2e (29 specs) still green.
- [ ] `dev-up.sh --isfc` runs the console in ISFC mode; `e2e-real` signs in with Email OTP `123456` against local ISFC.

#### HUB-496 — hubctl ISFC login and machine mode; hubmcp base URL
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-476, HUB-471 · **Feature:** F-24 · **Stories:** US-24xx
- **Branch:** `feat/HUB-496-hubctl-isfc`

**Acceptance criteria**
- [ ] `hubctl login -isfc` (PKCE loopback); `HUBCTL_ISFC_CLIENT_ID/SECRET` machine mode; `HUB_TENANT_ID`; hubmcp default admin URL fixed (G16); tests against mockisfc.

### Phase 7: Rollout

#### HUB-497 — Deployment: OPA sidecar, gateway routes, network policy
- **Epic:** EP-15 · **Type:** chore · **Area:** infra
- **Status:** todo · **Depends on:** HUB-479, HUB-475 · **Feature:** F-24 · **Stories:** —
- **Branch:** `chore/HUB-497-isfc-deploy`

**Acceptance criteria**
- [ ] Helm values (ISFC `deploy/charts/isfc-service`, `opa.enabled`, `opa.platform: integration-hub`); gateway route from §9; network policy: admin listener reachable only from the gateway and allow-listed automation; runbook `docs/runbooks/isfc-auth.md` (OPA down, sync stale, key revocation).

#### HUB-498 — Link existing tenants, console CSP, acceptance
- **Epic:** EP-15 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-482, HUB-495, HUB-497 · **Feature:** F-24, F-25 · **Stories:** —
- **Branch:** `feat/HUB-498-isfc-cutover`

Split into three tickets before starting (link tool / CSP / acceptance).

**Acceptance criteria**
- [ ] `hubctl tenants link <hub-id> <isfc-uuid>` + unmatched report.
- [ ] Console CSP (G12).
- [ ] End-to-end acceptance on QA: Email OTP login, platform vs tenant scope, every policy denies without its permission, tenant auto-created on link, disabled on deactivate, caller with ISFC key invokes, revoked key refused. Summary with Deviations (AGENTS.md §10).

**Order and parallelism:**
- 461 → 462 → (463 ∥ 464) → ADRs (465–467) → docs (468) and contracts (469–471).
- Then three streams run side by side against the frozen contracts:
  - backend admin 472–476 → Rego 477–479;
  - sync 480–485;
  - console 491–495.
- The caller plane (486–490) follows sync.
- Rollout (497–498) comes last.

---

## 18. Test and verification plan

| Level | What |
| --- | --- |
| Phase 0 manual | HUB-462 to HUB-464 findings notes, on the local ISFC stack (OTP `123456`). |
| Unit (Go, table-driven, `-race`) | Header parser, principal mapping, JWT RS256, OPA client, reconciler diff, id derivation, edge ISFC auth. |
| Rego | `opa test` with the generated RBAC matrix; generator `--check` in CI. |
| Integration (real Postgres + Redis + OPA container + `mockisfc`) | Admin RBAC matrix in ISFC mode; reconcile lifecycle; caller key revoke → 401. |
| Frontend | vitest for provider, session crypto, callback, proxy tenant header; MSW mock e2e unchanged; `e2e-real` against local ISFC. |
| Perf | HUB-490 auth stage benchmark; hot-path budget unchanged. |
| Security | Redaction tests (`x-isfc-api-key`, tokens, cookies); forged `x-isfc-*` without a valid JWT → 401; cross-tenant → 404. |

**Rule:** tests never call real ISFC or Entra. `mockisfc` and a fake OPA or OPA container only, matching the "never call real vendors in tests" rule.

---

## 19. Decisions needed from the user

| # | Question | Recommendation |
| --- | --- | --- |
| D1 | How the console handles platform-wide resources under `tenancy: required` | **Platform tenant** (§2.2 option A) |
| D2 | OPA on the caller plane | **In-memory check of the same ISFC key data, no OPA hop**; embedded OPA only if HUB-490 proves it fits |
| D3 | Where the Rego lives | **ISFC `authz-policies/integration-hub/`**, generator and tests in the Hub CI |
| D4 | Add a `tenant_admin` role now (tenant users finish setup and manage callbacks themselves) | **Yes**, it matches "user comes and sets up the tenant" |
| D5 | Interim caller linking until ISFC-CR-1 (tenant on API keys) | **One caller client per tenant + Hub-side list**, removed after CR-1 |
| D6 | Should `ISFC_ADMIN` count as Hub admin? | **No** |
| D7 | Machines allowed to unseal audit or purge the DLQ? | **No** |
| D8 | Event transport from ISFC | **SQS** (ISFC already uses it; LocalStack locally), behind an interface |
| D9 | Microsoft SSO timing | Build now; **enable in production after ISFC-CR-4** (id_token verification); need Entra `tenant_id` / `client_id` for QA |
