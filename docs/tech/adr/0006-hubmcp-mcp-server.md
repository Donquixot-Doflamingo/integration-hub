# ADR-0006: `hubmcp` MCP server in Go as an API client

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead |
| DESIGN refs | §4, §16.1, D12 |

## Context
Teams want LLM agents (Claude, IDE assistants) to discover integrations, dry-run and invoke them, and check request status, without writing an HTTP client. This must not create a second path into the Hub that bypasses auth, grants, rate limits or the audit trail, and must not leak vendor/account/version.

## Options considered
1. **MCP endpoint inside the gateway** — no extra binary, but adds a protocol and session state to the hot-path process.
2. **TypeScript MCP server** — mature SDK, but a second backend language and a separate client of the contract.
3. **Go `hubmcp` binary using `github.com/modelcontextprotocol/go-sdk`**, acting purely as a client of the public and admin HTTP APIs.

## Decision
Option 3. `backend/cmd/hubmcp`, transports **stdio** and **streamable HTTP**. Tenant tools: `hub_list_integrations`, `hub_describe_integration`, `hub_dry_run`, `hub_invoke`, `hub_get_request`, built on the discovery endpoints (ADR-0007) and `/v1`. Optional **read-only** admin tools when `HUB_ADMIN_TOKEN` is set. It never touches Postgres or Redis. Tool definitions are part of `contracts/` (steward-owned).

## Consequences
- Every MCP call goes through the gateway: same auth, grants, limits, audit.
- `hub_invoke` is side-effecting: marked destructive, idempotency key always sent (SECURITY §2.5).
- Separate image and observability (OBSERVABILITY §14); tests in TESTING_STRATEGY §13.
- The go-sdk version must be pinned and tracked (MCP spec evolves).

## Deviation from DESIGN.md
New binary not in §18.1 (see ADR-0001). No behaviour change.
