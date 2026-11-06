# ADR-0018: `hubmcp` implementation choices (HTTP auth, idempotency keys, layout)

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| DESIGN refs | §4.5, §16.1, D12; ADR-0006; TECH_SPEC §18; SECURITY §2.5; contracts/mcp/hubmcp-tools.json 1.0.0 |

## Context
EP-11 (HUB-300–307) builds `hubmcp` against the frozen tool contract. Three places where the docs leave room or disagree had to be settled:

1. **Streamable HTTP auth.** HUB-301 asks for "bearer auth for shared deployments". TECH_SPEC §18 and SECURITY §2.5 say the tenant key comes from the MCP client's `Authorization` header per session, never stored or logged. The contract's config table lists `HUB_API_KEY` as required (written with stdio in mind), and PRD Q11 (who hosts a shared deployment) is open. A shared `HUB_ADMIN_TOKEN` in a multi-user HTTP deployment would hand every client the operator's admin read access.
2. **Idempotency keys.** TECH_SPEC §18 and F-20 rule 4 say `hub_invoke` generates an `Idempotency-Key` when absent and returns it. The frozen contract has no output field for it, and its rule 5 says auto-retry happens "only when an `idempotency_key` is present".
3. **Package layout.** TECH_SPEC §18 sketches `cmd/hubmcp/internal/{tools,client}` with a client "typed from the public/admin contracts"; HUB-300 says calls go through `pkg/hubclient`.

## Options considered
1. HTTP auth: (a) a static shared bearer token for the endpoint plus the process `HUB_API_KEY` for every client; (b) **the client's bearer is its tenant key**, verified with the Hub and used for that client's calls (TECH_SPEC/SECURITY); (c) full OAuth resource-server mode.
2. Idempotency: (a) generate a key and return it in an extra output field (contract change); (b) generate a key and not return it; (c) **send only the caller's key**, as the contract says.
3. Layout: (a) `internal/{tools,client}` packages; (b) **one `main` package in `cmd/hubmcp`** using `pkg/hubclient` for the caller API and a small GET-only admin client.

## Decision
1. **(b).** Over `-transport http`, every request needs `Authorization: Bearer <tenant API key>`. hubmcp checks the key with `GET /v1/integrations?limit=1` (401/403 from the Hub → 401 to the client; Hub unreachable → 500, fail closed), remembers only the SHA-256 fingerprint of accepted keys for 60 s, and runs that client's tools with a `hubclient` built from its key. `HUB_API_KEY` is not used over HTTP. The listener binds `127.0.0.1:8090` by default; a non-loopback address needs `-allow-remote`. `HUB_ADMIN_TOKEN` is accepted over HTTP only on a loopback listener without `-allow-remote` (a local, single-user setup); otherwise start-up fails. Cross-origin browser requests are refused (`http.CrossOriginProtection`) and the SDK's DNS-rebinding protection stays on. The SDK binds MCP sessions to the key fingerprint, so a session id cannot be reused with another key.
2. **(c).** `hub_invoke` forwards `idempotency_key` when given and never invents one. Integrations with `moves_money_or_quota` cannot be invoked without one (HUB-304 guard), and the tool description tells the model to always pass one. Auto-retry (once, retryable errors only) happens only with a caller key.
3. **(b).** The caller side is `pkg/hubclient` (one SDK, one set of contract tests); the admin side needs four GETs and has a client that cannot send anything else.

## Consequences
- A shared HTTP deployment needs no shared secret and each user acts with their own tenant permissions (answers the hubmcp part of PRD Q11). The key check adds one Hub call per key per minute.
- The contract's config table says `HUB_API_KEY` is required; over HTTP it is ignored. CCR suggested to the steward (additive wording only): "required for stdio; over streamable HTTP each client authenticates with its own tenant key".
- A model that omits `idempotency_key` on a non-money integration gets no retry safety from hubmcp. If the lead wants generated keys (TECH_SPEC wording), that needs an additive contract change (an optional `idempotency_key` output field) first.
- TECH_SPEC §18 is updated to match (flags `-transport`/`-addr`/`-allow-remote`, layout, idempotency).

## Deviation from DESIGN.md
None from DESIGN.md. Deviations from TECH_SPEC §18 (idempotency key generation, package layout) and an interpretation of the contract config table for the HTTP transport, as above.
