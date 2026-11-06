# Access: console, hubctl, admin API and hubmcp

## Contents
- [Roles](#roles)
- [Console](#console)
- [hubctl](#hubctl)
- [Admin API with curl](#admin-api-with-curl)
- [hubmcp admin tools (your AI assistant)](#hubmcp-admin-tools-your-ai-assistant)
- [What each way can do](#what-each-way-can-do)

## Roles

The admin API uses OIDC tokens with RBAC roles `admin | editor | viewer | tenant_viewer` (DESIGN §16.1). Each operation's minimum role is its `x-required-role` in `contracts/openapi/hub-admin.v1.yaml`:

| Role | Can do (summary) |
| --- | --- |
| `viewer` | Read everything (tenant-scoped reads also work for `tenant_viewer` on their own tenants) |
| `editor` | Transformers (create version, dry-run, goldens), integrations (create, draft, validate, publish, weights, rollback, pause/resume), account connection test, DLQ replay |
| `admin` | Everything `editor` can, plus tenants, API keys, vendors and endpoints, accounts and credentials, callback endpoints, unseal, DLQ purge, breaker force |

The Hub decides: a call above your role answers 403 `FORBIDDEN`, whichever way you use.

## Console

Sign in at your environment's console URL (OIDC). The left navigation has **Integrations, Health, Requests, DLQ, Transformers, Tenants, Snapshots, Change log**. Action buttons only appear for roles that may use them; destructive actions open a confirmation dialog that asks for a reason.

## hubctl

Build it from the repo and run it from `backend/` (its `-config-dir config` and `-contracts-dir ../contracts` defaults are relative to `backend/`):

```bash
cd backend && make build            # writes bin/hubctl (and hub, mockvendor, hubmcp, hubload)
export HUB_ADMIN_URL=https://<admin host>    # no /admin/v1 suffix; default http://localhost:8081
export HUB_ADMIN_TOKEN=<your OIDC access token>
# local dev only (admin started with HUB_ADMIN_DEV_OIDC=true):
bin/hubctl login -dev -role admin -email you@example.com
bin/hubctl whoami                   # subject, roles, tenant scope, expiry
```

Commands that exist today (run `hubctl` alone for the usage text):

| Command | Does |
| --- | --- |
| `login -dev`, `whoami` | Dev token from the OIDC stub; show the current identity |
| `export -out DIR [-force]` | Write the live registry (latest snapshot, no secrets) as YAML files |
| `plan [-config-dir DIR]` | Diff the files against live, per object and path. Exit 3 when there are changes |
| `apply -reason TEXT [-config-dir DIR] [-yes]` | Publish the diff through the admin API. Without `-yes` it only prints the actions |
| `dry-run INTEGRATION BODY.json` | Show the vendor request the active version would send (highest-weight target), credentials redacted, nothing sent |
| `transform test [-v] [TRANSFORMER...]` | Run golden fixtures (all, or `T1`, `T1@7`) |
| `replay -reason TEXT -integration/-tenant/-account/-vendor/-error-code/-from/-to [-dry-run] [-yes]` | Replay dead letters by filter |
| `creds init-key`, `creds set`, `creds list` | Local dev credential file only (no change log, no connection test) |

**What `hubctl apply` applies** (anything else is refused before any change is made): account `config` and `quota`; tenant settings (`display_name`, `description`, `contact`, `rate_limit`, `disabled`); integration metadata (`description`, `grants`, `moves_money_or_quota`) and `paused`; and a **new integration version file** that `integration.yaml` also makes active (it drafts and publishes it). It refuses new tenants, API keys, vendors, accounts, transformers, callback endpoints and integrations, and edits of published versions.

The config-as-code loop:

```bash
bin/hubctl export -out /tmp/hub-live        # start from live
# edit files under /tmp/hub-live
bin/hubctl plan -config-dir /tmp/hub-live   # review the per-path diff
bin/hubctl apply -config-dir /tmp/hub-live -reason "why" -yes
```

## Admin API with curl

For anything the console, hubctl and MCP do not cover yet:

```bash
curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/me"
```

Every mutation needs `"reason"` (3 to 1000 characters) and returns a `change_id` visible under **Change log**. When a body holds a secret (account credentials), write it to a file readable only by you (`chmod 600`), send it with `-d @file.json`, and delete the file afterwards; never put secrets on the command line.

## hubmcp admin tools (your AI assistant)

`hubmcp` registers `hub_admin_*` tools when `HUB_ADMIN_TOKEN` is set. Setup per client (Claude Code, Claude Desktop, Codex CLI, Cursor): `clients/claude-skill/integration-hub/references/mcp-setup.md`.

- `HUB_ADMIN_TOKEN` is **your own** OIDC access token (dev: the token `hubctl login -dev` saved), never a shared or service token.
- `HUB_ADMIN_BASE_URL` must include `/admin/v1`, e.g. `http://localhost:8081/admin/v1`. If unset it defaults to `HUB_BASE_URL` + `/admin/v1`, which is wrong when the gateway and admin listen on different hosts or ports (they do in local dev: 8080 and 8081).
- On stdio `hubmcp` also needs `HUB_BASE_URL` and a tenant `HUB_API_KEY`, even for admin work.
- **Write tools exist only on stdio.** With `-transport http` only the read tools are registered, and a remote-reachable listener refuses `HUB_ADMIN_TOKEN` altogether.
- Every write tool needs `reason`; hubmcp appends ` (via hubmcp)` so the change log shows the change came through MCP. Tools marked `live_traffic` or `mints_secret` refuse with `CONFIRMATION_REQUIRED` until `confirm: true`.
- Updates take `if_match` (the `etag` from the matching `hub_admin_get_*` tool); publish, weights and rollback take `expected_active_version`. A concurrent change fails instead of being overwritten.
- Errors come back as tool results with the canonical error (`FORBIDDEN` = your role is too low; `UNAUTHORIZED` = sign in again).

## What each way can do

"—" means not available that way today: use another column. **Bold** marks things that are console-only on purpose (destructive) or human-only (secrets).

| Task | Console | hubctl | MCP tool | Admin API (`operationId`) |
| --- | --- | --- | --- | --- |
| Create tenant | Tenants → **Create tenant** | — | `hub_admin_create_tenant` | `createTenant` |
| Edit tenant (rate limit, contact) | Tenant → **Edit tenant** | `apply` (tenants file) | `hub_admin_update_tenant` | `updateTenant` |
| Disable / enable tenant | **Tenant → Disable tenant** | `apply` (`disabled`) | — (never) | `updateTenant` |
| Create API key | Tenant → API keys → **Create API key** | — | `hub_admin_create_api_key` | `createApiKey` |
| Rotate / revoke API key | **Tenant → API keys → Rotate / Revoke** | — | — (never) | `rotateApiKey`, `revokeApiKey` |
| Create vendor, add endpoint, update vendor | — | — | `hub_admin_create_vendor`, `hub_admin_create_vendor_endpoint`, `hub_admin_update_vendor` | `createVendor`, `createVendorEndpoint`, `updateVendor` |
| Create account with credentials | — | — (dev files only) | — (never: secrets) | **`createAccount`, by a human** |
| Change account config / quota | — | `apply` (accounts file) | `hub_admin_update_account` | `updateAccount` |
| Test account connection | — | — | — | `testAccountConnection` |
| Rotate account credentials | — | `creds set` (dev only) | — (never) | **runbook `docs/runbooks/credential-rotation.md`** |
| Drain / retire account | — | — | — (never) | **`transitionAccountLifecycle`** |
| Register callback endpoint | Tenant → Callback endpoints → **Register endpoint** | — | `hub_admin_create_registrar_endpoint` | `createRegistrarEndpoint` |
| Change callback URL | Callback endpoints → **Edit** | — | `hub_admin_update_registrar_endpoint` | `updateRegistrarEndpoint` |
| Rotate secret / disable / delete callback endpoint | **Callback endpoints → Rotate secret / Disable / Delete** | — | — (never) | `rotateRegistrarSecret`, `updateRegistrarEndpoint`, `deleteRegistrarEndpoint` |
| Create transformer version | — (view, dry-run and goldens only) | `transform test` locally | `hub_admin_create_transformer_version` | `createTransformerVersion` |
| Run goldens / dry-run a transformer | Transformers → version → **Run goldens** / **Dry-run** | `transform test` | `hub_admin_run_transformer_goldens`, `hub_admin_dry_run_transformer` | `runTransformerGoldens`, `dryRunTransformer` |
| Create integration | — | — | `hub_admin_create_integration` | `createIntegration` |
| Edit integration grants / description / money flag | — | `apply` (integration.yaml) | — | `updateIntegration` |
| Draft, validate, publish a version | Integration → **Edit a new draft** → Create draft → **Publish vN** | `apply` (new version file) | `hub_admin_create_integration_draft`, `hub_admin_update_integration_draft`, `hub_admin_validate_integration_version`, `hub_admin_publish_integration_version` | `createIntegrationDraft`, `updateIntegrationDraft`, `validateIntegrationVersion`, `publishIntegrationVersion` |
| Change weights (canary) | Integration → **Set weights** | `apply` (new version file) | `hub_admin_set_integration_weights` | `updateIntegrationWeights` |
| Roll back | Integration → version history → **Roll back to vN** | `apply` (new version file with the old content) | `hub_admin_rollback_integration` | `rollbackIntegration` |
| Compare versions | Integration → **Compare selected versions** | `plan` | `hub_admin_diff_integration_versions` | `diffIntegrationVersions` |
| Dry-run an integration | — | `dry-run INTEGRATION BODY.json` | `hub_dry_run` (tenant key with scope `dry_run`) | caller API `POST /v1/integrations/{id}/dry-run` |
| Pause / resume (kill switch) | **Integration → Pause / Resume integration** | `apply` (`paused`) | — (never) | `pauseIntegration`, `resumeIntegration` |
| Replay / purge dead letters | **DLQ** | `replay` | — (never) | `replayDeadLetters`, `purgeDeadLetters` |
| Read request timeline | Requests → request | — | `hub_admin_search_requests`, `hub_admin_request_timeline` | `searchRequests`, `getRequestTimeline` |
| Unseal sealed payloads | **Requests → request (admin)** | — | — (never) | `unsealRequest` |
| Breaker force open / close | **Health** | — | — (never; `hub_admin_breaker_states` reads) | `forceOpenBreaker`, `forceCloseBreaker`, `clearBreakerForce` |

**Local development only.** The Hub can also load the registry from files (`backend/config/`, `HUB_CONFIG_DIR`) and, started with `-config-watch`, publishes every change to that folder. That is how the dev config and `examples/` add vendors, accounts, transformers and integrations that `hubctl apply` refuses; local secrets go in with `hubctl creds set`. Never use it for a shared environment.
