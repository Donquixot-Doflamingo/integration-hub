# F-20 — Hub MCP server (`hubmcp`)

## Summary
`backend/cmd/hubmcp`: a Go MCP server (stdio + streamable HTTP) that lets Claude and other MCP clients discover, dry-run and invoke Hub integrations as a tenant, plus optional admin tools with the signed-in user's own token: read tools for on-call and, on stdio only, write tools that let a team's assistant create what the team needs on the control plane, never delete (PRD DV-4, ADR-0020).

## Status
`in-progress` — 2026-11-07: HUB-300–307 built and in review on `feat/HUB-300-hubmcp` (shipped when merged); follow-ups HUB-311–313. HUB-315 (admin write tools, contract 1.1.0 from HUB-314) in review on `feat/HUB-314-mcp-write-tools`.

## Owner
`backend-engineer` (uses `backend/.claude/skills/mcp-builder`); `contract-steward` for the tool contract.

## Milestone / Epic
After M3 (needs caller API + discovery) · EP-11 · HUB-300–329 (shared with F-21).

## Problem
AI assistants hallucinate vendor APIs and suggest calling vendors directly. Developers need their assistant to see the real, tenant-scoped catalogue and to try calls safely.

## Goals / Non-goals
**Goals**
- Config: `HUB_BASE_URL`, `HUB_API_KEY` (tenant key), optional `HUB_ADMIN_TOKEN` (the signed-in user's own OIDC token, never a shared one).
- Tenant tools (caller API only):

| Tool | Calls | Notes |
| --- | --- | --- |
| `hub_list_integrations` | `hub-public.v1.yaml#listIntegrations` | id, capability, operations, mode, paused, description |
| `hub_describe_integration` | `#getIntegration` + `#getCapability` | request/response schemas per operation |
| `hub_dry_run` | `#dryRunIntegration` | needs key scope `dry_run`; shows the upstream request (secrets `***`) by design; validation errors returned as structured content |
| `hub_invoke` | `#invokeIntegration` / `#invokeIntegrationOperation` (+ `#getIntegration` for the flag) | `confirm: true` and `idempotency_key` required when `moves_money_or_quota` is true; `outcome` is `completed`, `queued`, `error` or `refused` |
| `hub_get_request` | `#getRequestStatus` | poll a 202 |

- Admin tools (only registered when `HUB_ADMIN_TOKEN` is set). The full tables with operation, method, minimum role and `requires_confirm` are in `contracts/mcp/README.md`.
  - 21 read and check tools on both transports, among them `hub_admin_search_requests`, `hub_admin_request_timeline` and `hub_admin_breaker_states`. The rest are get/list for tenants, vendors, endpoints, accounts, capabilities, transformers, integrations, versions, diff and registrar endpoints, plus the three non-mutating checks (`run_transformer_goldens`, `dry_run_transformer`, `validate_integration_version`).
  - 16 write tools, **stdio only** (HUB-315): create/update tenant, create API key, create/update vendor, create vendor endpoint, update account, create transformer version, create integration, create/update draft, publish, set weights, rollback, create/update registrar endpoint.

**Non-goals**
- Deleting anything through MCP: no HTTP DELETE, purge, replay, unseal, retire, credential operations, breaker force, pause, or key revoke/rotate (`forbidden_operations`). These stay manual in the console.
- Credentials or signing secrets as tool inputs; admin write tools over Streamable HTTP; vendor/account/version exposure in tenant tools.

## User stories
- [US-2001](../product/USER_STORIES.md) — Discover integrations from Claude
- [US-2002](../product/USER_STORIES.md) — Dry-run from the editor
- [US-2003](../product/USER_STORIES.md) — Guarded invoke
- [US-2004](../product/USER_STORIES.md) — Check a request
- [US-2005](../product/USER_STORIES.md) — Read-only admin tools for on-call
- US-20xx — Admin write tools as the signed-in user (story to be added by docs-keeper, HUB-314/315)
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
4. `hub_invoke` forwards the caller's `idempotency_key` as `Idempotency-Key` and never generates one: the frozen contract has no output field to return a generated key (ADR-0018). The tool description tells the model to always pass one; money/quota integrations refuse to run without it.
5. 202 responses are returned as `outcome: queued` with `accepted`, 200 as `outcome: completed` with `result`, errors as tool results with `isError: true` and the canonical error object (contract `hubmcp-tools.json` output schema).
6. Tool annotations: list/describe/get/dry-run `readOnlyHint: true`; `hub_invoke` `destructiveHint: true`, `idempotentHint: false`, `openWorldHint: true`.
7. Secrets: `HUB_API_KEY` and `HUB_ADMIN_TOKEN` never echoed in tool output or logs (redacting logger).
8. Streamable HTTP mode (`/mcp`) binds to `127.0.0.1:8090` by default; a non-local address needs `-allow-remote`. Every request carries `Authorization: Bearer <tenant API key>`: hubmcp verifies it with the Hub and uses it for that client's tools, so each user acts with their own tenant permissions (ADR-0018). `HUB_ADMIN_TOKEN` is refused on a remote-reachable listener.
9. **Admin write tools (HUB-315, ADR-0020).**
   - **Identity and transport.** They act as the signed-in user (`HUB_ADMIN_TOKEN`) and are registered only on stdio, never on Streamable HTTP (loopback included).
   - **Authorization.** The Hub's RBAC decides, and a 403 is an `isError` result with `FORBIDDEN`.
   - **Change log.** `reason` is required and forwarded with ` (via hubmcp)` appended. `change_id` comes back in every write result.
   - **Concurrency.** `if_match` is sent as `If-Match`, and get tools return `etag` from the `ETag` header.
10. **Local refusals (no Hub call, `isError`, `outcome: refused`).**
    - `CONFIRMATION_REQUIRED`: a `requires_confirm` tool (live traffic or secret minting) called without `confirm: true`.
    - `SECRET_IN_INPUT`: any argument of any tool, `hub_invoke` included, that matches the contract's `secret_patterns`.
11. **Sensitive output.** A minted API key or signing secret is returned in that one result with a "shown once" note. It is then redacted by value from every later result and log line.
12. **No delete path.**
    - The admin client can only send the routes the registered tools map.
    - Every hubmcp HTTP client refuses DELETE.
    - A contract copy that maps a DELETE or forbidden operation refuses to load.

## Data model impact
None.

## Config impact
Env vars above (`HUB_API_KEY` is used over stdio only); flags `-transport stdio|http`, `-addr` (default `127.0.0.1:8090`), `-allow-remote`, `-log-level`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | `tool_called` | tool name, is_error, duration_ms, integration id, request_id, outcome (always stderr); write tools add `mutating` and `change_id` (HUB-315). Never arguments, `reason` or minted secrets |
| Log | `hubmcp started` / `hubmcp listening` / `hubmcp stopped` / `admin tools disabled: …` | lifecycle; see OBSERVABILITY §4.6 |
| Metrics, span | OBSERVABILITY §14 | planned, HUB-311 |
| Header | `User-Agent: hubclient-go/<v> hubmcp/<version>` | lets the Hub attribute MCP traffic |

## Security & PII
Runs with the developer's tenant key: same permissions as their service. Guarded invoke prevents accidental side effects. Admin tools run with the user's own token and the Hub's RBAC. Write tools are stdio only, confirm-guarded for live traffic and secret minting, take no secret inputs, return minted secrets once, and can never delete (SECURITY §2.5, ADR-0020).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Hub unreachable | Tool error with base URL hint | Claude reports it |
| 401 | Tool error suggesting key check | — |
| 403 on an admin tool (role too low) | `isError` result with the Hub's `FORBIDDEN` object | Claude tells the user they lack the role |
| Write tool without `confirm: true` where required | `CONFIRMATION_REQUIRED`, no Hub call | Claude shows the change and asks |
| Argument contains a Hub key or signing secret | `SECRET_IN_INPUT`, no Hub call | Claude removes it |
| Concurrent change (stale `expected_active_version` / `if_match`) | Hub `CONFLICT` / `PRECONDITION_FAILED` as `isError` | Claude re-reads and retries with the user |

## Testing
See TESTING_STRATEGY §13 for the full list.
- **Unit**: tool handlers against a contract-validated fake Hub serving contract examples; every output validated against the tool's `outputSchema`; a success and an error scenario per tool.
- **Integration (e2e)**: hubmcp → hub gateway/worker/admin (built binaries, test database) → mockvendor. Since HUB-315 it also onboards a team with an admin `hubctl login -dev` token: tenant, API key, vendor and endpoint, transformer version, integration, draft, validate, publish (refused without confirm), invoke with the minted key, weights, rollback, each checked in the change log. A viewer token gets FORBIDDEN.
- **Admin write safety (HUB-315)**: `TestToolRegistrationByTransport`, `TestNoToolMapsToDeleteOrForbidden`, `TestAdminClientCannotDelete`, `TestSourceHasNoDeletePath`, `TestLoadContractRefusesUnsafeTools`, `TestWriteToolConfirmGuard`, `TestNoSecretInputProperties`, `TestSecretInInputIsRefused`, `TestSensitiveOutputIsReturnedOnceAndNeverLogged`, `TestReasonIfMatchAndETag`; `TestEveryToolSuccessAndError` covers all 42 tools (success, Hub error, viewer FORBIDDEN).
- **Contract**: advertised tools equal `contracts/mcp/hubmcp-tools.json`; tenant outputs scanned for vendor/account/version.
- **Protocol**: in-memory, stdio pipes, spawned binary, streamable HTTP (auth, origin).
- **Eval**: mcp-builder evaluation set (discover → dry-run → invoke flows), manual. MCP Inspector: manual (HUB-313).

## Rollout & migration
Distributed as a binary in releases; documented in the consumer skill (F-21).

## Open questions
- Who hosts a shared streamable-HTTP deployment (PRD Q11)? ~~How are per-user keys passed?~~ Each client's bearer is its tenant key (ADR-0018).
- ~~Source of the money/quota flag (PRD Q9)~~ Resolved by the contract: integration field `moves_money_or_quota`, exposed by discovery.

## Tickets
HUB-300 – HUB-329 (EP-11, filter `feature: F-20`).

## Implementation notes
- **SDK:** official `github.com/modelcontextprotocol/go-sdk` v1.8.0, low-level `Server.AddTool`, so the advertised `inputSchema`/`outputSchema` are the contract's JSON verbatim. Arguments are validated against the contract input schema (santhosh-tekuri/jsonschema, formats asserted) before a handler runs; a violation is a tool result with `isError: true` and `VALIDATION_FAILED`, so the model can fix the call.
- **Contract copy:** `go:embed` cannot reach `contracts/`, so `backend/cmd/hubmcp/hubmcp-tools.json` is a byte-for-byte copy (`go generate ./cmd/hubmcp` refreshes it). `TestEmbeddedContractIsTheFrozenOne` fails on any drift and `TestToolListEqualsFrozenContract` compares the advertised tools with `contracts/mcp/hubmcp-tools.json` itself.
- **Isolation:** caller tools use `pkg/hubclient`; admin tools use an allowlisted client (operations = the registered tools' `maps_to`; every HTTP client wrapped by a method guard that refuses DELETE). `internal/archtest` forbids `cmd/hubmcp` from importing `internal/`.
- **Config:** `HUB_BASE_URL`, `HUB_API_KEY` (env only, no flag; stdio), optional `HUB_ADMIN_TOKEN` / `HUB_ADMIN_BASE_URL`; flags `-transport`, `-addr`, `-allow-remote`, `-log-level`.
- **Outputs:** built field by field from `pkg/hubclient` types, never by passing Hub JSON through, so a field outside the contract's output schema (vendor, account, version, debug headers) cannot reach a tenant tool. Hub errors become `isError` results carrying the canonical error object (`code`, `message`, `retryable`, `retry_after_ms`, `reason`, `circuit`, `upstream`, `details`, `request_id`); `UNAUTHORIZED` adds a hint to check `HUB_API_KEY`, an unreachable Hub a hint to check `HUB_BASE_URL`.
- **hub_invoke:** pre-check `getIntegration` (cached ≤ 60 s per tenant key and integration); a paused integration returns `INTEGRATION_PAUSED` (`http_status: null`) without invoking; the `default` operation uses `invokeIntegration`, others `invokeIntegrationOperation`. One automatic retry only for a retryable error, only with an `idempotency_key`, and only when `retry_after_ms` ≤ 2 s (a longer wait goes back to the model). `X-Hub-Integration-Version` / `X-Hub-Served-By` never reach the output (hubclient ignores them; the output is built field by field).
- **Confirm guard (rule 3):** checked after the paused check: no `confirm: true` → `CONFIRMATION_REQUIRED` (checked first), then no `idempotency_key` → `IDEMPOTENCY_KEY_REQUIRED`; `outcome: refused`, `http_status`/`request_id` null, `isError: true`, `error.details[0].pointer` names the missing argument, and the text content starts with a summary of the held-back call (integration, operation, key, body) for the model to show the user. If the flag cannot be read the call fails without invoking (fail closed).
- **hub_dry_run:** besides the Hub's redaction, hubmcp forces `***` on every header listed in `redacted` and adds a warning if it had to.
- **Contract-driven admin tools (HUB-315, `admintools.go`):** apart from the three hand-written ones below, each admin tool's handler is derived from its input schema and its operation's route. Path parameters come from the input field of the same name. `if_match` becomes `If-Match`, and `confirm` is consumed by the guard. Every other field goes to the query (GET) or unchanged to the JSON body, with `reason` plus ` (via hubmcp)`. The response object is passed through, plus `etag` for get tools. `create_transformer_version` drops the yaml and fixtures the model just sent. `TestAdminRoutesMatchTheContract` proves the route table equals the OpenAPI document and the mapped operations.
- **Admin tools:** allowlisted client (`Authorization: Bearer $HUB_ADMIN_TOKEN`, base `HUB_ADMIN_BASE_URL` or `HUB_BASE_URL/admin/v1`). `hub_admin_search_requests` defaults `from` to one hour before `to` (or now) and drops any body field from rows; `hub_admin_request_timeline` combines timeline and attempts and drops event payloads unless `include_payloads` is true (masking/sealing stays the Hub's job); admin errors are passed through verbatim as `isError` results.
- **HTTP transport:** SDK `StreamableHTTPHandler` behind `http.CrossOriginProtection` and the SDK's `auth.RequireBearerToken`; the verifier asks the Hub whether the key is valid (fail closed) and puts that key's `hubclient` in the request's token info, so tools run with the client's own key. Secrets print as `***` (`%v`, `%#v`, slog) and are redacted by value from logs and tool results.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-07 | In progress: scaffold, implementation notes, observability (HUB-300) | backend-engineer |
| 2026-11-07 | Streamable HTTP transport and per-client key auth (HUB-301, ADR-0018) | backend-engineer |
| 2026-11-07 | Discovery tools (HUB-302) | backend-engineer |
| 2026-11-07 | Invoke, request status and dry-run tools; idempotency rule 4 aligned with the contract (HUB-303, ADR-0018) | backend-engineer |
| 2026-11-07 | Money/quota confirm guard (HUB-304) | backend-engineer |
| 2026-11-07 | Admin read-only tools (HUB-305) | backend-engineer |
| 2026-11-07 | Test suite (HUB-306); follow-ups HUB-311 (metrics/traces), HUB-312 (admin DLQ/versions CCR), HUB-313 (Inspector) | backend-engineer |
| 2026-11-07 | E2E against a running Hub (HUB-307); US-2003/US-2006 aligned with ADR-0018 | backend-engineer |
| 2026-11-07 | Client setup for Claude Code, Claude Desktop, Codex CLI and Cursor, tenant vs admin profile: `clients/claude-skill/integration-hub/references/mcp-setup.md` (HUB-318) | docs agent |
| 2026-11-07 | Admin write tools as the signed-in user, stdio only, no delete path (HUB-315, contract 1.1.0, ADR-0020) | backend-engineer |
