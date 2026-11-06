# Adding the `hubmcp` MCP server to Claude Code (and other MCP clients)

`hubmcp` exposes the Hub caller API as MCP tools, so Claude can discover, dry-run and invoke
integrations for you. It runs locally over **stdio** and authenticates with your tenant key.
With your own admin sign-in (`HUB_ADMIN_TOKEN`), it can also read control-plane state and create
or change what your team needs: tenants, API keys, vendors, transformers, integrations and
callback endpoints. It can never delete anything.
(It also supports streamable HTTP if your platform team hosts it; ask them for the URL. A hosted
HTTP server never offers the admin write tools.)

## 1. Get the binary
Either a release binary from the platform team, or build from the Hub repo (needs Go):
```bash
go install github.com/Donquixot-Doflamingo/integration-hub/backend/cmd/hubmcp@latest
which hubmcp
```
If the module path or tag differs in your org, follow the Hub repo README.

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

## 3a. Add with `claude mcp add` (stdio, user scope)
```bash
claude mcp add hubmcp --scope user \
  --env HUB_BASE_URL="$HUB_BASE_URL" \
  --env HUB_API_KEY="$HUB_API_KEY" \
  -- hubmcp
claude mcp list            # hubmcp should show as connected
```
With admin tools, add `--env HUB_ADMIN_TOKEN="$HUB_ADMIN_TOKEN"`. A user-scope server with your
own token is the intended setup: write tools exist only over stdio, for the person who runs it.
Note: this stores the expanded values in your local Claude Code config. Do not use this form in
shared screenshots or recordings, and never use `--scope project` with a literal key.

## 3b. Project `.mcp.json` (committable, no secrets)
`.mcp.json` supports environment-variable expansion, so the file holds only variable names:
```json
{
  "mcpServers": {
    "hubmcp": {
      "type": "stdio",
      "command": "hubmcp",
      "args": [],
      "env": {
        "HUB_BASE_URL": "${HUB_BASE_URL}",
        "HUB_API_KEY": "${HUB_API_KEY}",
        "HUB_ADMIN_TOKEN": "${HUB_ADMIN_TOKEN:-}"
      }
    }
  }
}
```
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

## 4. Tools
| Tool | Does | Notes |
| --- | --- | --- |
| `hub_list_integrations` | Lists integrations your tenant may invoke | Wraps `GET /v1/integrations` |
| `hub_describe_integration` | One integration + its capability request/response schemas | Wraps `GET /v1/integrations/{id}` and `GET /v1/capabilities/{capability}` |
| `hub_dry_run` | Validates a canonical body and shows the exact upstream request the Hub would send (secrets `***`) | Sends nothing to vendors. Needs `HUB_API_KEY` scope `dry_run`. Call first; never copy vendor details from it into code |
| `hub_invoke` | Invokes (optionally an operation) with an idempotency key | **Requires `confirm: true` and `idempotency_key` for integrations with `moves_money_or_quota: true`**; get the user's explicit go-ahead before setting it |
| `hub_get_request` | Status and final result of a request id | Use after a 202 / `Queued` |
| `hub_admin_*` read tools (optional) | Search requests, timelines, breakers; get/list tenants, vendors, endpoints, accounts, capabilities, transformers, integrations, versions and diffs; validate a draft, run goldens, dry-run a transformer | Only with `HUB_ADMIN_TOKEN`. Metadata only, never unsealed payloads |
| `hub_admin_*` write tools (optional, stdio only) | Create or update tenants, API keys, vendors, endpoints, account config and quota, transformer versions, integrations and drafts, publish, set weights, roll back, create or update callback endpoints | Only with your own `HUB_ADMIN_TOKEN` over stdio. `reason` is required. Live-traffic changes and key or secret minting need `confirm: true` after you approve. **Nothing can be deleted through MCP**: deleting, purging, replaying, unsealing, retiring, pausing, forcing breakers, revoking or rotating keys and setting credentials stay manual in the console |

Typical flow: `hub_list_integrations` -> `hub_describe_integration` -> build body -> `hub_dry_run`
-> show the user -> `hub_invoke` (with `confirm: true` only after the user agrees, where required)
-> `hub_get_request` if queued.

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
- Server not connected: run `hubmcp` directly in a terminal with the env vars set to see its error.
- `UNAUTHORIZED`: wrong environment/key pair, or the key was rotated.
- Empty integration list: your tenant has no integrations or grants in this environment.
- No `hub_admin_*` tools: `HUB_ADMIN_TOKEN` is not set in the server's environment. No write tools: the
  server runs over streamable HTTP (writes are stdio only).
- `FORBIDDEN` from an admin tool: your role does not allow it. Ask an admin; MCP does not work around RBAC.
- `UNAUTHORIZED` from an admin tool: your admin token expired. Sign in again (`hubctl login -dev`
  locally) and restart the MCP server.
