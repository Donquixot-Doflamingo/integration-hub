# ADR-0020: `hubmcp` admin write tools act as the signed-in user, stdio only, no delete path

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review); requirement from the user (HUB-314) |
| Tickets | HUB-314, HUB-315 |
| DESIGN refs | §16.1 (RBAC, change log), D7, D12; amends ADR-0006 and ADR-0018; PRD DV-4, FR-48, NFR-13; contracts/mcp/hubmcp-tools.json 1.1.0 |

## Context
ADR-0006 made the `hubmcp` admin tools **read-only**, and ADR-0018 gave hubmcp a GET-only admin client. The user then asked for more: a team's Claude must be able to create what the team needs on the control plane (tenant, API keys, vendor and endpoints, transformer versions, integrations, drafts, publish, weights, rollback, callback endpoints). It must use the user's own authentication and authorization, and it must **never delete** anything. Deleting stays a manual console action for people whose role allows it. The steward froze this as `hubmcp-tools` 1.1.0 (HUB-314): 34 new admin tools (16 writes), `forbidden_operations`, `secret_patterns`, `requires_confirm`, `sensitive_output` and `requires.transport: ["stdio"]` on write tools.

The open questions for the implementation:
1. Whose identity writes? A service token would hide the actor and bypass per-user RBAC.
2. On which transports? Over Streamable HTTP one process-level token would serve every connected client.
3. How do we make "no delete" a property of the code, not just a convention of the tool list?
4. Where is authorization decided: in hubmcp (pre-filter by role) or in the Hub?

## Options considered
1. **Keep read-only and send users to the console.** Safest, but does not meet the requirement.
2. **Service token with write scope in hubmcp.** One token for everyone, so the change log shows a robot. It needs its own RBAC in hubmcp, and over HTTP it hands every client write access.
3. **The user's own OIDC token (`HUB_ADMIN_TOKEN`), write tools on stdio only, the Hub's RBAC decides, and an allowlisted method-guarded client.** The user runs hubmcp locally and is the actor in the change log. The same role rules apply as in the console. hubmcp cannot send anything the contract does not map.

## Decision
Option 3.
- **Identity.** Write tools use `HUB_ADMIN_TOKEN`, the signed-in user's own token (from the IdP, locally `hubctl login -dev`). Every mutation is recorded in the Hub's change log under that subject. `reason` is required and forwarded with ` (via hubmcp)` appended, so auditors can tell MCP changes from console changes.
- **Transport.** Admin read tools keep the ADR-0018 rule: they need the token and are refused on a remote-reachable listener. Write tools (`requires.transport: ["stdio"]`) are **registered only on stdio**, never on Streamable HTTP, loopback included. Over HTTP, write operations are also absent from the admin client's allowlist, so they cannot be reached by any path.
- **Authorization.** hubmcp does not pre-filter by role. The Hub's RBAC (`x-required-role`) is the authority, and a 403 comes back as an `isError` result with the Hub's `FORBIDDEN` object.
- **No delete, by construction.**
  - The admin client knows only the routes the contract maps (`adminRoutes`, proved equal to the OpenAPI document).
  - Its allowlist is built from the `maps_to` of the tools this server registers.
  - Every HTTP client hubmcp uses (admin and `pkg/hubclient`) is wrapped by a `RoundTripper` that refuses any method other than GET, POST, PUT and PATCH.
  - `loadContract` refuses a contract that maps a DELETE or a `forbidden_operations` entry, or that has a write tool without required `reason` or not stdio-only.
  - The non-test source names no DELETE method (AST test).
- **Local guards, before any Hub call.**
  - `SECRET_IN_INPUT` for any argument (key or value, any depth, every tool including `hub_invoke`) that matches `secret_patterns`.
  - `CONFIRMATION_REQUIRED` for `requires_confirm` tools without `confirm: true`.
- **Sensitive output.** `sensitive_output` fields (minted API key, signing secret) are returned in that one result with a "shown once" note. From then on they join the redaction set by value, so they never appear in logs or in any later result.

## Consequences
- Positive:
  - A team can onboard end to end from its assistant with its own permissions and a full audit trail (e2e `TestMCPEndToEnd`).
  - A viewer gets FORBIDDEN, exactly as in the console.
  - No MCP path can delete, purge, replay, unseal, retire, pause, force a breaker, revoke or rotate, or accept a credential.
- Negative / costs:
  - The user's admin token sits in the MCP client's environment for as long as hubmcp runs. Its lifetime is the IdP's (the dev stub: 8 h).
  - `confirm` is a speed bump, not proof of consent: a model can set it without asking. The real controls are the user's RBAC, the client's approval prompt for non-read-only tools, the change log, and `expected_active_version` / `if_match` against concurrent changes.
  - A shared, multi-user HTTP deployment gets no write tools. To get them, each user runs their own stdio hubmcp.
- Follow-ups:
  - Story `US-20xx` for the admin write tools (docs-keeper, per HUB-314/315).
  - Consumer skill: the admin tools are documented in `references/mcp-setup.md`.
  - Metrics for hubmcp remain HUB-311.

## Amends
- **ADR-0006**, Decision: "Optional **read-only** admin tools when `HUB_ADMIN_TOKEN` is set" becomes "optional admin tools when `HUB_ADMIN_TOKEN` (the user's own token) is set; read tools on both transports, write tools on stdio only, never a delete".
- **ADR-0018**, Decision 3: "a small GET-only admin client" becomes "an allowlisted admin client built from the registered tools' `maps_to`, with a method guard that refuses DELETE". Decision 1 (HTTP auth, `HUB_ADMIN_TOKEN` refused on a remote-reachable listener) is unchanged.

## Deviation from DESIGN.md
None from DESIGN.md: §16.1 RBAC and the change log stay the enforcement point, and hubmcp remains a pure API client. It changes PRD scope: the non-goal "admin tools are read-only", DV-4, FR-48 and NFR-13 are updated to "no admin deletes; writes as the signed-in user, stdio only".
