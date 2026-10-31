# Adding the `hubmcp` MCP server to Claude Code

`hubmcp` exposes the Hub caller API as MCP tools, so Claude can discover, dry-run and invoke
integrations for you. It runs locally over **stdio** and authenticates with your tenant key.
(It also supports streamable HTTP if your platform team hosts it; ask them for the URL.)

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
| `HUB_ADMIN_TOKEN` | no | Enables read-only admin tools (`hub_admin_search_requests`, `hub_admin_request_timeline`, `hub_admin_breaker_states`) for platform/on-call users |

## 3a. Add with `claude mcp add` (stdio, user scope)
```bash
claude mcp add hubmcp --scope user \
  --env HUB_BASE_URL="$HUB_BASE_URL" \
  --env HUB_API_KEY="$HUB_API_KEY" \
  -- hubmcp
claude mcp list            # hubmcp should show as connected
```
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
        "HUB_API_KEY": "${HUB_API_KEY}"
      }
    }
  }
}
```
Each developer exports their own `HUB_BASE_URL` / `HUB_API_KEY`. Claude Code asks for approval the
first time it sees a project-scoped server.

## 4. Tools
| Tool | Does | Notes |
| --- | --- | --- |
| `hub_list_integrations` | Lists integrations your tenant may invoke | Wraps `GET /v1/integrations` |
| `hub_describe_integration` | One integration + its capability request/response schemas | Wraps `GET /v1/integrations/{id}` and `GET /v1/capabilities/{capability}` |
| `hub_dry_run` | Validates a canonical body and shows the exact upstream request the Hub would send (secrets `***`) | Sends nothing to vendors. Needs `HUB_API_KEY` scope `dry_run`. Call first; never copy vendor details from it into code |
| `hub_invoke` | Invokes (optionally an operation) with an idempotency key | **Requires `confirm: true` and `idempotency_key` for integrations with `moves_money_or_quota: true`**; get the user's explicit go-ahead before setting it |
| `hub_get_request` | Status and final result of a request id | Use after a 202 / `Queued` |
| `hub_admin_*` (optional) | Read-only admin views | Only with `HUB_ADMIN_TOKEN`; no mutations exist in MCP |

Typical flow: `hub_list_integrations` -> `hub_describe_integration` -> build body -> `hub_dry_run`
-> show the user -> `hub_invoke` (with `confirm: true` only after the user agrees, where required)
-> `hub_get_request` if queued.

## Troubleshooting
- Server not connected: run `hubmcp` directly in a terminal with the env vars set to see its error.
- `UNAUTHORIZED`: wrong environment/key pair, or the key was rotated.
- Empty integration list: your tenant has no integrations or grants in this environment.
