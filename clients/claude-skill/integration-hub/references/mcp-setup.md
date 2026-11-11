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

## 1. Get the binary

A release binary from the platform team, or build it from the Hub repo (Go 1.26):

```bash
cd backend && make build              # writes bin/hubmcp (and hub, hubctl, mockvendor, hubload)
# or: go install github.com/Donquixot-Doflamingo/integration-hub/backend/cmd/hubmcp@latest
```

Use the **absolute path** of the binary in configs (`/Users/you/bin/hubmcp`): desktop apps do not
see your shell's `PATH`. `hubmcp -transport stdio` is the default; logs go to stderr.

## 2. Two profiles: tenant key, admin token

| Variable | Tenant profile | Admin profile | Meaning |
| --- | --- | --- | --- |
| `HUB_BASE_URL` | required | required | Gateway base URL, e.g. `http://localhost:8080`. Use a non-production environment for experiments |
| `HUB_API_KEY` | one of | one of, on stdio | Your tenant key (scope `invoke`; add `dry_run` for `hub_dry_run`). Legacy: prefer the ISFC credential below. Secret |
| `HUB_ISFC_CLIENT_ID` + `HUB_ISFC_API_KEY` | one of | one of, on stdio | Your ISFC backend client and the key `integration-hub` issued to it (instead of `HUB_API_KEY`; both set is a startup error). Secret |
| `HUB_TENANT_ID` | only for a multi-tenant ISFC key | optional | ISFC tenant UUID, sent as `X-Tenant-Id` |
| `HUB_ADMIN_TOKEN` | **unset** | required | **Your own** OIDC access token from your identity provider (local dev: the token `hubctl login -dev` saves), never a shared or service token. Unset → no `hub_admin_*` tool exists. Secret |
| `HUB_ADMIN_BASE_URL` | — | required | Control-plane URL **including `/admin/v1`**, e.g. `http://localhost:8081/admin/v1`. The admin API is not served below `HUB_BASE_URL`: hubmcp guesses only for a loopback gateway (same host, port 8081) and otherwise refuses to start without it. With ISFC sign-in it is the gateway route to the Hub admin API, e.g. `https://gateway.example.com/hub/admin/v1`, and `HUB_TENANT_ID` picks the tenant you act in |

Rules for the admin profile:
- **stdio only.** Admin write tools are registered only on stdio, run by you on your machine. Never
  put an admin token in a shared or hosted server: a remote-reachable `-transport http` listener
  refuses `HUB_ADMIN_TOKEN` altogether.
- Add it as a **separate server entry** (`hubmcp-admin`) so you turn admin tools on only when you
  need them, and keep the tenant entry for everyday work.
- Your token expires; when tools answer `UNAUTHORIZED`, get a new one and restart the server.
- The Hub's RBAC decides: a write above your role comes back `FORBIDDEN`. A `viewer` token can
  read only; creating tenants, API keys and vendors needs `admin`; transformers, integrations,
  drafts, publish, weights and rollback need `editor`. Every write needs a `reason` and lands in
  the change log as you, marked `(via hubmcp)`.
- Local dev token (admin started with dev OIDC):
  ```bash
  hubctl login -dev -role editor -email you@example.com   # or -role admin / viewer
  # Saved to ~/.config/hubctl/token (Linux) or ~/Library/Application Support/hubctl/token (macOS);
  # HUBCTL_TOKEN_FILE overrides. Dev tokens last 8 hours.
  export HUB_ADMIN_TOKEN="$(cat ~/.config/hubctl/token)"
  ```
- ISFC sign-in (staging and production): `hubctl login -isfc` signs you in with your ISFC account in
  the browser and saves the token the same way; export it as `HUB_ADMIN_TOKEN` and point
  `HUB_ADMIN_BASE_URL` at the gateway route (see the table above).
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

```bash
claude mcp add --scope user --env HUB_BASE_URL="$HUB_BASE_URL" --env HUB_API_KEY="$HUB_API_KEY" \
  --transport stdio hubmcp -- /abs/path/hubmcp
claude mcp list                       # hubmcp should show as connected
```

**Project `.mcp.json`** (committable: it holds variable names only, expanded from each developer's
environment; Claude Code asks for approval the first time):

```json
{
  "mcpServers": {
    "hubmcp": {
      "type": "stdio",
      "command": "hubmcp",
      "args": [],
      "env": { "HUB_BASE_URL": "${HUB_BASE_URL}", "HUB_API_KEY": "${HUB_API_KEY}" }
    }
  }
}
```

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
credential**: a tenant key as `Authorization: Bearer <key>`, or its ISFC backend client as the
headers `x-isfc-client-id` and `x-isfc-api-key` (plus `X-Tenant-Id` for a multi-tenant key); not both. Only tenant tools and admin read tools exist there,
never admin write tools.

| Client | Config |
| --- | --- |
| Claude Code `.mcp.json` | `{"type": "http", "url": "https://hubmcp.example.internal/mcp", "headers": {"Authorization": "Bearer ${HUB_API_KEY}"}}` |
| Codex `config.toml` | `url = "https://hubmcp.example.internal/mcp"` and `bearer_token_env_var = "HUB_API_KEY"` |
| Cursor `mcp.json` | `{"url": "https://hubmcp.example.internal/mcp", "headers": {"Authorization": "Bearer ${env:HUB_API_KEY}"}}` |
| Claude Code `.mcp.json`, ISFC | `{"type": "http", "url": "https://hubmcp.example.internal/mcp", "headers": {"x-isfc-client-id": "${ISFC_CLIENT_ID}", "x-isfc-api-key": "${HUB_ISFC_API_KEY}"}}` |

A server started on a loopback address with `HUB_ISFC_CLIENT_ID` and `HUB_ISFC_API_KEY` uses that
credential for requests that bring none; a remote-reachable server refuses to start with it.

## 8. Tools

Tenant tools (always registered):

| Tool | Does | Notes |
| --- | --- | --- |
| `hub_list_integrations` | Integrations your tenant may invoke | `GET /v1/integrations` |
| `hub_describe_integration` | One integration: delivery policy and, per operation, its `request_schema` (body, and caller headers if any; `required` fields must be sent) | `GET /v1/integrations/{id}`. There is no response schema |
| `hub_dry_run` | Validate a body and show the upstream request (secrets `***`); `operation`, `headers` and `session` as for `hub_invoke` | Sends nothing. Needs key scope `dry_run`. Never copy vendor details into code |
| `hub_invoke` | Invoke (optionally `operation`, `headers`, `session`, `idempotency_key`, `prefer_async`, `deadline_ms`) | **Refuses money/quota integrations without `idempotency_key` and `confirm: true`**; set `confirm` only after the user agrees. A 202 is `outcome: "queued"`. `headers`: only names declared in the operation's `request_schema.headers` (the Hub's own headers are refused; use the dedicated inputs). `pass_through: true`: `result` is the third party's own body, unchanged (also on `outcome: "error"`, where `error` is null and `http_status` is the third party's status) |
| `hub_get_request` | Status and final result of a request id | Use after `queued` |

Admin tools (only with `HUB_ADMIN_TOKEN`):
- Read (any transport): `hub_admin_search_requests`, `hub_admin_request_timeline`, `hub_admin_breaker_states`, `hub_admin_list_tenants`, `hub_admin_get_tenant`, `hub_admin_list_vendors`, `hub_admin_get_vendor`, `hub_admin_list_vendor_endpoints`, `hub_admin_get_vendor_endpoint`, `hub_admin_list_accounts`, `hub_admin_get_account`, `hub_admin_get_transformer`, `hub_admin_run_transformer_goldens`, `hub_admin_dry_run_transformer`, `hub_admin_list_integrations`, `hub_admin_get_integration`, `hub_admin_get_integration_version`, `hub_admin_diff_integration_versions`, `hub_admin_validate_integration_version`, `hub_admin_get_registrar_endpoint`.
- Write (stdio only, `reason` required; `confirm: true` where marked): `hub_admin_create_tenant`, `hub_admin_update_tenant` (confirm), `hub_admin_create_api_key` (confirm; key shown once), `hub_admin_create_vendor`, `hub_admin_update_vendor` (confirm), `hub_admin_create_vendor_endpoint` (with its `request_schema`), `hub_admin_update_account` (confirm), `hub_admin_create_transformer_version`, `hub_admin_create_integration`, `hub_admin_create_integration_draft`, `hub_admin_update_integration_draft`, `hub_admin_publish_integration_version` (confirm), `hub_admin_set_integration_weights` (confirm), `hub_admin_rollback_integration` (confirm), `hub_admin_create_registrar_endpoint` (confirm; secret shown once), `hub_admin_update_registrar_endpoint` (confirm).

How operators use the admin tools, task by task: the `hub-operator` skill in the Hub repo
(`.claude/skills/hub-operator/`).

Typical caller flow: `hub_list_integrations` → `hub_describe_integration` → build the body from the
operation's `request_schema` (all `required` fields, declared headers in `headers`) →
`hub_dry_run` → show the user → `hub_invoke` (with `confirm: true` only after the user agrees, where
required) → `hub_get_request` if queued. Two-step: `hub_invoke` with `operation: "send_otp"`, keep
the returned session, then `hub_invoke` with `operation: "verify_otp"` and `session`.

## Troubleshooting
- Not connected: run the same command in a terminal with the same variables to see the error
  (`HUB_BASE_URL is required`, `HUB_API_KEY is required (environment only)`).
- `UNAUTHORIZED`: wrong environment/key pair, a rotated key, or an expired admin token.
- No `hub_admin_*` tools: `HUB_ADMIN_TOKEN` is not reaching the process, or (write tools) the server
  runs over HTTP.
- Admin tools fail with connection errors: `HUB_ADMIN_BASE_URL` missing or without `/admin/v1`.
- Empty integration list: your tenant owns or is granted nothing in this environment.
- `FORBIDDEN` from an admin tool: your role does not allow it. Ask an admin; MCP does not work
  around RBAC.
- Refused locally (nothing was sent to the Hub):
  - `CONFIRMATION_REQUIRED`: show the user the change and ask before retrying with `confirm: true`.
  - `SECRET_IN_INPUT`: an argument looks like a Hub API key or signing secret. Remove it; refer to
    keys by `key_id` or `prefix`.
