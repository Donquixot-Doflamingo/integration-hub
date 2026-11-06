# F-20 — Hub MCP server (`hubmcp`)

## Summary
`backend/cmd/hubmcp`: a Go MCP server (stdio + streamable HTTP) that lets Claude and other MCP clients discover, dry-run and invoke Hub integrations as a tenant, plus optional read-only admin tools for on-call (PRD DV-4).

## Status
`in-progress` — 2026-11-07 (HUB-300 onward on `feat/HUB-300-hubmcp`).

## Owner
`backend-engineer` (uses `backend/.claude/skills/mcp-builder`); `contract-steward` for the tool contract.

## Milestone / Epic
After M3 (needs caller API + discovery) · EP-11 · HUB-300–329 (shared with F-21).

## Problem
AI assistants hallucinate vendor APIs and suggest calling vendors directly. Developers need their assistant to see the real, tenant-scoped catalogue and to try calls safely.

## Goals / Non-goals
**Goals**
- Config: `HUB_BASE_URL`, `HUB_API_KEY` (tenant key), optional `HUB_ADMIN_TOKEN`.
- Tenant tools (caller API only):

| Tool | Calls | Notes |
| --- | --- | --- |
| `hub_list_integrations` | `hub-public.v1.yaml#listIntegrations` | id, capability, operations, mode, paused, description |
| `hub_describe_integration` | `#getIntegration` + `#getCapability` | request/response schemas per operation |
| `hub_dry_run` | `#dryRunIntegration` | needs key scope `dry_run`; shows the upstream request (secrets `***`) by design; validation errors returned as structured content |
| `hub_invoke` | `#invokeIntegration` / `#invokeIntegrationOperation` (+ `#getIntegration` for the flag) | `confirm: true` and `idempotency_key` required when `moves_money_or_quota` is true; `outcome` is `completed`, `queued`, `error` or `refused` |
| `hub_get_request` | `#getRequestStatus` | poll a 202 |

- Admin tools (only registered when `HUB_ADMIN_TOKEN` is set; GET only):

| Tool | Calls |
| --- | --- |
| `hub_admin_search_requests` | `hub-admin.v1.yaml#searchRequests` |
| `hub_admin_request_timeline` | `#getRequestTimeline` + `#listRequestAttempts` |
| `hub_admin_breaker_states` | `#listBreakers` |

**Non-goals**
- Admin mutations of any kind; vendor/account/version exposure in tenant tools.

## User stories
- [US-2001](../product/USER_STORIES.md) — Discover integrations from Claude
- [US-2002](../product/USER_STORIES.md) — Dry-run from the editor
- [US-2003](../product/USER_STORIES.md) — Guarded invoke
- [US-2004](../product/USER_STORIES.md) — Check a request
- [US-2005](../product/USER_STORIES.md) — Read-only admin tools for on-call
- [US-2006](../product/USER_STORIES.md) — Transports

## UX / API surface
Tool input/output JSON Schemas live in `contracts/mcp/hubmcp-tools.json` (owned by `contract-steward`; `maps_to` names the operationIds above). Example client config:
```json
{ "mcpServers": { "integration-hub": {
    "command": "hubmcp", "args": ["--transport", "stdio"],
    "env": { "HUB_BASE_URL": "http://localhost:8080", "HUB_API_KEY": "<tenant key>" } } } }
```

## Behaviour & rules
1. `hubmcp` uses only endpoints in `contracts/`; it has no private API.
2. Tenant tools pass through Hub responses; they never add vendor, account or version, and strip `X-Hub-Integration-Version`.
3. `hub_invoke`: if the integration has `moves_money_or_quota: true` (checked via `getIntegration`, cached ≤ 60 s) and the input lacks `confirm: true` or `idempotency_key`, the tool returns `outcome: refused` **without calling the Hub**.
4. `hub_invoke` sets an `Idempotency-Key` (caller-supplied or generated UUIDv7) and returns it, so a retried tool call is safe.
5. 202 responses are returned as `outcome: queued` with `accepted`, 200 as `outcome: completed` with `result`, errors as tool results with `isError: true` and the canonical error object (contract `hubmcp-tools.json` output schema).
6. Tool annotations: list/describe/get/dry-run `readOnlyHint: true`; `hub_invoke` `destructiveHint: true`, `idempotentHint: false`, `openWorldHint: true`.
7. Secrets: `HUB_API_KEY` and `HUB_ADMIN_TOKEN` never echoed in tool output or logs (redacting logger).
8. Streamable HTTP mode binds to localhost by default; non-local binding requires explicit flag and its own auth (open question).

## Data model impact
None.

## Config impact
Env vars above; `--transport stdio|http`, `--addr`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | `tool_called` | tool name, is_error, duration_ms, integration id, request_id, outcome (always stderr) |
| Log | `hubmcp started` / `hubmcp stopped` / `admin tools disabled: …` | lifecycle; see OBSERVABILITY §4.6 |
| Header | `User-Agent: hubclient-go/<v> hubmcp/<version>` | lets the Hub attribute MCP traffic |

## Security & PII
Runs with the developer's tenant key: same permissions as their service. Guarded invoke prevents accidental side effects. Admin tools read-only.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Hub unreachable | Tool error with base URL hint | Claude reports it |
| 401 | Tool error suggesting key check | — |

## Testing
- **Unit**: tool handlers against an `httptest` server serving contract fixtures.
- **Integration**: hubmcp → hub (dev) → mockvendor.
- **Contract**: tool schemas validated; responses checked for absence of vendor/account/version fields.
- **Eval**: mcp-builder evaluation set (discover → dry-run → invoke flows).

## Rollout & migration
Distributed as a binary in releases; documented in the consumer skill (F-21).

## Open questions
- Who hosts a shared streamable-HTTP deployment, and how are per-user keys passed (PRD Q11)?
- ~~Source of the money/quota flag (PRD Q9)~~ Resolved by the contract: integration field `moves_money_or_quota`, exposed by discovery.

## Tickets
HUB-300 – HUB-329 (EP-11, filter `feature: F-20`).

## Implementation notes
- **SDK:** official `github.com/modelcontextprotocol/go-sdk` v1.8.0, low-level `Server.AddTool`, so the advertised `inputSchema`/`outputSchema` are the contract's JSON verbatim. Arguments are validated against the contract input schema (santhosh-tekuri/jsonschema, formats asserted) before a handler runs; a violation is a tool result with `isError: true` and `VALIDATION_FAILED`, so the model can fix the call.
- **Contract copy:** `go:embed` cannot reach `contracts/`, so `backend/cmd/hubmcp/hubmcp-tools.json` is a byte-for-byte copy (`go generate ./cmd/hubmcp` refreshes it). `TestEmbeddedContractIsTheFrozenOne` fails on any drift and `TestToolListEqualsFrozenContract` compares the advertised tools with `contracts/mcp/hubmcp-tools.json` itself.
- **Isolation:** caller tools use `pkg/hubclient`; admin tools use a GET-only client. `internal/archtest` forbids `cmd/hubmcp` from importing `internal/`.
- **Config:** `HUB_BASE_URL`, `HUB_API_KEY` (env only, no flag), optional `HUB_ADMIN_TOKEN` / `HUB_ADMIN_BASE_URL`; flags `-transport`, `-log-level`. Secrets print as `***` (`%v`, `%#v`, slog) and are redacted by value from logs and tool results.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-07 | In progress: scaffold, implementation notes, observability (HUB-300) | backend-engineer |
