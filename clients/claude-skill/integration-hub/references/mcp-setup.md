<<<<<<< HEAD
# Adding the `hubmcp` MCP server to Claude Code (and other MCP clients)

`hubmcp` exposes the Hub caller API as MCP tools, so Claude can discover, dry-run and invoke
integrations for you. It runs locally over **stdio** and authenticates with your tenant key.
With your own admin sign-in (`HUB_ADMIN_TOKEN`), it can also read control-plane state and create
or change what your team needs: tenants, API keys, vendors, transformers, integrations and
callback endpoints. It can never delete anything.
(It also supports streamable HTTP if your platform team hosts it; ask them for the URL. A hosted
HTTP server never offers the admin write tools.)
=======
# Adding the `hubmcp` MCP server (Claude Code, Claude Desktop, Codex CLI, Cursor)

`hubmcp` exposes the Hub to an AI assistant as MCP tools. With your **tenant key** it can discover,
dry-run and invoke integrations like any caller. With **your own admin token** added (platform
engineers only), it can also read operational state and create or change control-plane objects as
you; it can never delete anything. The tool contract is `contracts/mcp/hubmcp-tools.json` in the
Hub repo (its `README.md` explains every rule).

Snippets verified against each client's docs on 2026-11-07; links are next to each section.

## Contents
- [1. Get the binary](#1-get-the-binary)
- [2. Two profiles: tenant key, admin token](#2-two-profiles-tenant-key-admin-token)
- [3. Claude Code](#3-claude-code)
- [4. Claude Desktop](#4-claude-desktop)
- [5. Codex CLI](#5-codex-cli)
- [6. Cursor](#6-cursor)
- [7. A hosted hubmcp over Streamable HTTP](#7-a-hosted-hubmcp-over-streamable-http)
- [8. Tools](#8-tools)
- [Troubleshooting](#troubleshooting)
>>>>>>> origin/main

## 1. Get the binary

A release binary from the platform team, or build it from the Hub repo (Go 1.26):

```bash
cd backend && make build              # writes bin/hubmcp (and hub, hubctl, mockvendor, hubload)
# or: go install github.com/Donquixot-Doflamingo/integration-hub/backend/cmd/hubmcp@latest
```

<<<<<<< HEAD
## 2. Configure environment
| Variable | Required | Meaning |
| --- | --- | --- |
| `HUB_BASE_URL` | yes | Hub base URL for the environment (use non-production for experiments) |
| `HUB_API_KEY` | yes | Your tenant key. Keep it in a secret manager / keychain, export it in your shell profile from there |
| `HUB_ADMIN_TOKEN` | no | **Your own** admin sign-in token, never a shared or service token. Enables the `hub_admin_*` tools: read tools on any transport, write tools on stdio only. The Hub's role rules apply to you exactly as in the console. See section 2a |
| `HUB_ADMIN_BASE_URL` | no | Control-plane URL. Defaults to `HUB_BASE_URL` + `/admin/v1` |

## 2a. Your admin token (only if you want the `hub_admin_*` tools)
- **Your organisation's identity provider.** Get an access token for the Hub admin API there, as your platform team documents it. Export it as `HUB_ADMIN_TOKEN` from your secret manager or keychain, never from a file you commit.
- **Locally, against a dev Hub** (admin started with dev OIDC):
  ```bash
  hubctl login -dev -role editor -email you@example.com   # or -role admin / viewer
  # The token is saved to the hubctl config dir (Linux ~/.config/hubctl/token,
  # macOS ~/Library/Application Support/hubctl/token; HUBCTL_TOKEN_FILE overrides).
  export HUB_ADMIN_TOKEN="$(cat ~/.config/hubctl/token)"
  ```
- **What you can do follows your role.** A `viewer` token can read, but every write tool answers `FORBIDDEN`. Creating tenants, API keys and vendors needs `admin`. Transformers, integrations, drafts, publish, weights and rollback need `editor`.
- **Every change is audited.** It goes to the Hub's change log under your identity, with the `reason` you give, plus " (via hubmcp)".
- **Tokens expire.** The dev stub's tokens last 8 hours. When tools answer `UNAUTHORIZED`, sign in again and restart the MCP server.
=======
Use the **absolute path** of the binary in configs (`/Users/you/bin/hubmcp`): desktop apps do not
see your shell's `PATH`. `hubmcp -transport stdio` is the default; logs go to stderr.

## 2. Two profiles: tenant key, admin token

| Variable | Tenant profile | Admin profile | Meaning |
| --- | --- | --- | --- |
| `HUB_BASE_URL` | required | required | Gateway base URL, e.g. `http://localhost:8080`. Use a non-production environment for experiments |
| `HUB_API_KEY` | required | required on stdio | Your tenant key (scope `invoke`; add `dry_run` for `hub_dry_run`). Secret |
| `HUB_ADMIN_TOKEN` | **unset** | required | **Your own** OIDC access token from your identity provider (local dev: the token `hubctl login -dev` saves), never a shared or service token. Unset → no `hub_admin_*` tool exists. Secret |
| `HUB_ADMIN_BASE_URL` | — | set it | Control-plane URL **including `/admin/v1`**, e.g. `http://localhost:8081/admin/v1`. Defaults to `HUB_BASE_URL` + `/admin/v1`, which is wrong when the gateway and admin are on different hosts or ports (local dev: 8080 vs 8081) |

Rules for the admin profile:
- **stdio only.** Admin write tools are registered only on stdio, run by you on your machine. Never
  put an admin token in a shared or hosted server: a remote-reachable `-transport http` listener
  refuses `HUB_ADMIN_TOKEN` altogether.
- Add it as a **separate server entry** (`hubmcp-admin`) so you turn admin tools on only when you
  need them, and keep the tenant entry for everyday work.
- Your token expires; when tools answer `UNAUTHORIZED`, get a new one and restart the server.
- The Hub's RBAC decides: a write above your role comes back `FORBIDDEN`. Every write needs a
  `reason` and lands in the change log as you, marked `(via hubmcp)`.
- **No deletes, ever.** Deleting, purging or replaying the DLQ, unseal, pause/resume, disabling
  tenants or callback endpoints, key revoke/rotate, secret rotation, credential changes, account
  drain/retire and breaker force stay manual in the console for roles that have them. Account
  creation (it needs credential values) is done by a person in the console or with hubctl / the
  admin API. `hubmcp` refuses any argument that looks like a Hub key or signing secret.

Keep secrets out of committed files: every client below can read them from your environment, a
keychain or an env file that stays out of Git.

## 3. Claude Code

Docs: [code.claude.com/docs/en/mcp](https://code.claude.com/docs/en/mcp).

**Command line** (stores the expanded values in `~/.claude.json`; local scope is the default, `user`
makes it available in every project). Put `--transport stdio` between `--env` and the name:
>>>>>>> origin/main

```bash
claude mcp add --scope user --env HUB_BASE_URL="$HUB_BASE_URL" --env HUB_API_KEY="$HUB_API_KEY" \
  --transport stdio hubmcp -- /abs/path/hubmcp
claude mcp list                       # hubmcp should show as connected
```
<<<<<<< HEAD
With admin tools, add `--env HUB_ADMIN_TOKEN="$HUB_ADMIN_TOKEN"`. A user-scope server with your
own token is the intended setup: write tools exist only over stdio, for the person who runs it.
Note: this stores the expanded values in your local Claude Code config. Do not use this form in
shared screenshots or recordings, and never use `--scope project` with a literal key.
=======
>>>>>>> origin/main

**Project `.mcp.json`** (committable: it holds variable names only, expanded from each developer's
environment; Claude Code asks for approval the first time):

```json
{
  "mcpServers": {
    "hubmcp": {
      "type": "stdio",
      "command": "hubmcp",
      "args": [],
<<<<<<< HEAD
      "env": {
        "HUB_BASE_URL": "${HUB_BASE_URL}",
        "HUB_API_KEY": "${HUB_API_KEY}",
        "HUB_ADMIN_TOKEN": "${HUB_ADMIN_TOKEN:-}"
      }
=======
      "env": { "HUB_BASE_URL": "${HUB_BASE_URL}", "HUB_API_KEY": "${HUB_API_KEY}" }
>>>>>>> origin/main
    }
  }
}
```
<<<<<<< HEAD
Each developer exports their own `HUB_BASE_URL` / `HUB_API_KEY` (and, optionally, their own
`HUB_ADMIN_TOKEN`). Claude Code asks for approval the first time it sees a project-scoped server.
When `HUB_ADMIN_TOKEN` is empty, no `hub_admin_*` tool is registered.

## 3c. Other MCP clients (the same stdio server)
`hubmcp` is a standard stdio MCP server. The command and environment are the same everywhere; only
the config file differs. The snippets below were **not verified** against these clients' current
docs at the time of writing (2026-11-07). Check each client's MCP documentation before you rely on
the exact keys, and especially on whether `env` values are expanded from your shell.

Codex CLI, `~/.codex/config.toml` (*unverified syntax*):
```toml
[mcp_servers.hub]
command = "hubmcp"
args = []
# Values here are literal: do not commit this file with real keys. Recent Codex versions may
# offer a way to pass variables through from your shell instead; check the Codex docs.
env = { HUB_BASE_URL = "http://localhost:8080", HUB_API_KEY = "<your tenant key>", HUB_ADMIN_TOKEN = "<your own admin token>" }
```

Cursor, `.cursor/mcp.json` in the project or `~/.cursor/mcp.json` (*unverified syntax*, including
the `${env:NAME}` interpolation):
```json
{
  "mcpServers": {
    "hub": {
      "command": "hubmcp",
      "args": [],
      "env": {
        "HUB_BASE_URL": "${env:HUB_BASE_URL}",
        "HUB_API_KEY": "${env:HUB_API_KEY}",
        "HUB_ADMIN_TOKEN": "${env:HUB_ADMIN_TOKEN}"
      }
    }
  }
}
```

Claude Desktop, `claude_desktop_config.json` (macOS: `~/Library/Application Support/Claude/`;
Windows: `%APPDATA%\Claude\`) (*unverified syntax*). Desktop apps do not read your shell profile,
so use the absolute path to `hubmcp` and literal values, and keep the file private:
```json
{
  "mcpServers": {
    "hub": {
      "command": "/absolute/path/to/hubmcp",
      "args": [],
      "env": { "HUB_BASE_URL": "http://localhost:8080", "HUB_API_KEY": "<your tenant key>", "HUB_ADMIN_TOKEN": "<your own admin token>" }
    }
  }
}
```
=======
>>>>>>> origin/main

**Admin profile:** user or local scope only, never `--scope project`:

```bash
claude mcp add --scope user \
  --env HUB_BASE_URL="$HUB_BASE_URL" --env HUB_API_KEY="$HUB_API_KEY" \
  --env HUB_ADMIN_TOKEN="$HUB_ADMIN_TOKEN" --env HUB_ADMIN_BASE_URL="$HUB_ADMIN_URL/admin/v1" \
  --transport stdio hubmcp-admin -- /abs/path/hubmcp
```

This writes the token into `~/.claude.json`; remove and re-add the entry when the token changes
(`claude mcp remove hubmcp-admin`).

## 4. Claude Desktop

Docs: [modelcontextprotocol.io: connect local servers](https://modelcontextprotocol.io/docs/develop/connect-local-servers).
Open Settings → Developer → **Edit Config**. The file is:
- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

Values in `env` are taken literally, so do not paste the key there. Point `command` at a small
wrapper that reads secrets from your keychain and starts `hubmcp`:

```sh
#!/bin/sh
# ~/bin/hubmcp-tenant (chmod 700). macOS keychain shown; on Linux use `secret-tool lookup ...`.
export HUB_BASE_URL="https://hub.example.internal"
export HUB_API_KEY="$(security find-generic-password -s hub-api-key -w)"
exec /abs/path/hubmcp
```

```json
{
  "mcpServers": {
    "hubmcp": { "command": "/Users/you/bin/hubmcp-tenant", "args": [] }
  }
}
```

For the admin profile add a second wrapper (`hubmcp-admin`) that also exports `HUB_ADMIN_TOKEN`
and `HUB_ADMIN_BASE_URL`, and a second entry. Quit and restart Claude Desktop after every change;
server logs are in `~/Library/Logs/Claude/mcp-server-hubmcp.log` (Windows `%APPDATA%\Claude\logs`).

## 5. Codex CLI

Docs: [Codex: MCP](https://developers.openai.com/codex/mcp). Config lives in
`~/.codex/config.toml` (or `.codex/config.toml` in a trusted project). Forward the variables from
your environment by name with `env_vars`, so no secret is written to the file:

```toml
[mcp_servers.hubmcp]
command = "/abs/path/hubmcp"
env_vars = ["HUB_BASE_URL", "HUB_API_KEY"]

[mcp_servers.hubmcp-admin]          # optional; your own token, stdio only
command = "/abs/path/hubmcp"
env_vars = ["HUB_BASE_URL", "HUB_API_KEY", "HUB_ADMIN_TOKEN", "HUB_ADMIN_BASE_URL"]
```

Or from the command line (this writes the values themselves into the config):

```bash
codex mcp add hubmcp --env HUB_BASE_URL="$HUB_BASE_URL" --env HUB_API_KEY="$HUB_API_KEY" -- /abs/path/hubmcp
```

Optional keys: `startup_timeout_sec` (default 10), `tool_timeout_sec` (default 60),
`enabled_tools` / `disabled_tools` (for example, list only the five tenant tools).

## 6. Cursor

Docs: [cursor.com/docs/context/mcp](https://cursor.com/docs/context/mcp). Project
`.cursor/mcp.json` or global `~/.cursor/mcp.json`; `${env:NAME}` reads your environment:

```json
{
  "mcpServers": {
    "hubmcp": {
      "type": "stdio",
      "command": "/abs/path/hubmcp",
      "args": [],
      "env": { "HUB_BASE_URL": "${env:HUB_BASE_URL}", "HUB_API_KEY": "${env:HUB_API_KEY}" }
    }
  }
}
```

Alternatively `"envFile": ".env.hub"` (stdio servers only; keep that file out of Git). Put an
admin entry only in the **global** `~/.cursor/mcp.json`, never in a committed project file.

## 7. A hosted hubmcp over Streamable HTTP

If the platform team hosts `hubmcp -transport http` (default listen `127.0.0.1:8090`; a non-loopback
address needs `-allow-remote`), each client connects to `<url>/mcp` and authenticates with **its own
tenant key** as `Authorization: Bearer <key>`. Only tenant tools and admin read tools exist there,
never admin write tools.

| Client | Config |
| --- | --- |
| Claude Code `.mcp.json` | `{"type": "http", "url": "https://hubmcp.example.internal/mcp", "headers": {"Authorization": "Bearer ${HUB_API_KEY}"}}` |
| Codex `config.toml` | `url = "https://hubmcp.example.internal/mcp"` and `bearer_token_env_var = "HUB_API_KEY"` |
| Cursor `mcp.json` | `{"url": "https://hubmcp.example.internal/mcp", "headers": {"Authorization": "Bearer ${env:HUB_API_KEY}"}}` |

## 8. Tools

Tenant tools (always registered):

| Tool | Does | Notes |
| --- | --- | --- |
<<<<<<< HEAD
| `hub_list_integrations` | Lists integrations your tenant may invoke | Wraps `GET /v1/integrations` |
| `hub_describe_integration` | One integration + its capability request/response schemas | Wraps `GET /v1/integrations/{id}` and `GET /v1/capabilities/{capability}` |
| `hub_dry_run` | Validates a canonical body and shows the exact upstream request the Hub would send (secrets `***`) | Sends nothing to vendors. Needs `HUB_API_KEY` scope `dry_run`. Call first; never copy vendor details from it into code |
| `hub_invoke` | Invokes (optionally an operation) with an idempotency key | **Requires `confirm: true` and `idempotency_key` for integrations with `moves_money_or_quota: true`**; get the user's explicit go-ahead before setting it |
| `hub_get_request` | Status and final result of a request id | Use after a 202 / `Queued` |
| `hub_admin_*` read tools (optional) | Search requests, timelines, breakers; get/list tenants, vendors, endpoints, accounts, capabilities, transformers, integrations, versions and diffs; validate a draft, run goldens, dry-run a transformer | Only with `HUB_ADMIN_TOKEN`. Metadata only, never unsealed payloads |
| `hub_admin_*` write tools (optional, stdio only) | Create or update tenants, API keys, vendors, endpoints, account config and quota, transformer versions, integrations and drafts, publish, set weights, roll back, create or update callback endpoints | Only with your own `HUB_ADMIN_TOKEN` over stdio. `reason` is required. Live-traffic changes and key or secret minting need `confirm: true` after you approve. **Nothing can be deleted through MCP**: deleting, purging, replaying, unsealing, retiring, pausing, forcing breakers, revoking or rotating keys and setting credentials stay manual in the console |
=======
| `hub_list_integrations` | Integrations your tenant may invoke | `GET /v1/integrations` |
| `hub_describe_integration` | One integration + its capability schemas | `GET /v1/integrations/{id}` + `GET /v1/capabilities/{capability}` |
| `hub_dry_run` | Validate a body and show the upstream request (secrets `***`); `operation` and `session` for multi-step | Sends nothing. Needs key scope `dry_run`. Never copy vendor details into code |
| `hub_invoke` | Invoke (optionally `operation`, `session`, `idempotency_key`, `prefer_async`, `deadline_ms`) | **Refuses money/quota integrations without `idempotency_key` and `confirm: true`**; set `confirm` only after the user agrees. A 202 is `outcome: "queued"` |
| `hub_get_request` | Status and final result of a request id | Use after `queued` |
>>>>>>> origin/main

Admin tools (only with `HUB_ADMIN_TOKEN`):
- Read (any transport): `hub_admin_search_requests`, `hub_admin_request_timeline`, `hub_admin_breaker_states`, `hub_admin_list_tenants`, `hub_admin_get_tenant`, `hub_admin_list_vendors`, `hub_admin_get_vendor`, `hub_admin_list_vendor_endpoints`, `hub_admin_get_vendor_endpoint`, `hub_admin_list_accounts`, `hub_admin_get_account`, `hub_admin_list_capabilities`, `hub_admin_get_transformer`, `hub_admin_run_transformer_goldens`, `hub_admin_dry_run_transformer`, `hub_admin_list_integrations`, `hub_admin_get_integration`, `hub_admin_get_integration_version`, `hub_admin_diff_integration_versions`, `hub_admin_validate_integration_version`, `hub_admin_get_registrar_endpoint`.
- Write (stdio only, `reason` required; `confirm: true` where marked): `hub_admin_create_tenant`, `hub_admin_update_tenant` (confirm), `hub_admin_create_api_key` (confirm; key shown once), `hub_admin_create_vendor`, `hub_admin_update_vendor` (confirm), `hub_admin_create_vendor_endpoint`, `hub_admin_update_account` (confirm), `hub_admin_create_transformer_version`, `hub_admin_create_integration`, `hub_admin_create_integration_draft`, `hub_admin_update_integration_draft`, `hub_admin_publish_integration_version` (confirm), `hub_admin_set_integration_weights` (confirm), `hub_admin_rollback_integration` (confirm), `hub_admin_create_registrar_endpoint` (confirm; secret shown once), `hub_admin_update_registrar_endpoint` (confirm).

How operators use the admin tools, task by task: the `hub-operator` skill in the Hub repo
(`.claude/skills/hub-operator/`).

Typical caller flow: `hub_list_integrations` → `hub_describe_integration` → build body →
`hub_dry_run` → show the user → `hub_invoke` (with `confirm: true` only after the user agrees, where
required) → `hub_get_request` if queued. Two-step: `hub_invoke` with `operation: "send_otp"`, keep
the returned session, then `hub_invoke` with `operation: "verify_otp"` and `session`.

Onboarding flow with admin tools (each write after showing the user what it does):
`hub_admin_create_tenant` -> `hub_admin_create_api_key` (confirm; the key is shown **once**:
store it in your secret manager, it is never shown again) -> `hub_admin_create_vendor` +
`hub_admin_create_vendor_endpoint` -> `hub_admin_create_transformer_version` (goldens must pass)
-> `hub_admin_create_integration` -> `hub_admin_create_integration_draft` ->
`hub_admin_validate_integration_version` -> `hub_admin_publish_integration_version` (confirm, after
showing the validation result and the diff). Vendor credentials are set by a person in the
console or with `hubctl`, never through Claude.

Local refusals (nothing was sent to the Hub):
- `CONFIRMATION_REQUIRED`: show the user the change and ask before retrying with `confirm: true`.
- `SECRET_IN_INPUT`: an argument contains something that looks like a Hub API key or signing
  secret. Remove it; refer to keys by `key_id` or `prefix`.

## Troubleshooting
<<<<<<< HEAD
- Server not connected: run `hubmcp` directly in a terminal with the env vars set to see its error.
- `UNAUTHORIZED`: wrong environment/key pair, or the key was rotated.
- Empty integration list: your tenant has no integrations or grants in this environment.
- No `hub_admin_*` tools: `HUB_ADMIN_TOKEN` is not set in the server's environment. No write tools: the
  server runs over streamable HTTP (writes are stdio only).
- `FORBIDDEN` from an admin tool: your role does not allow it. Ask an admin; MCP does not work around RBAC.
- `UNAUTHORIZED` from an admin tool: your admin token expired. Sign in again (`hubctl login -dev`
  locally) and restart the MCP server.
=======
- Not connected: run the same command in a terminal with the same variables to see the error
  (`HUB_BASE_URL is required`, `HUB_API_KEY is required (environment only)`).
- `UNAUTHORIZED`: wrong environment/key pair, a rotated key, or an expired admin token.
- No `hub_admin_*` tools: `HUB_ADMIN_TOKEN` is not reaching the process, or (write tools) the server
  runs over HTTP.
- Admin tools fail with connection errors: `HUB_ADMIN_BASE_URL` missing or without `/admin/v1`.
- Empty integration list: your tenant owns or is granted nothing in this environment.
>>>>>>> origin/main
