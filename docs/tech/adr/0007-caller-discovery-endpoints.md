# ADR-0007: Additive caller discovery endpoints

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead, contract-steward |
| DESIGN refs | §4.1, D12 |

## Context
DESIGN §4.1 gives callers no way to list the integrations they may call or fetch the canonical schema they must send. The MCP server (ADR-0006) and the consumer skill need both to build tool input schemas and guide users.

## Options considered
1. **Use `/admin/v1`** — exposes control-plane data (vendors, accounts, versions) and needs OIDC; violates D12 for callers.
2. **Ship schemas inside the skill/MCP server** — drifts from the registry.
3. **Additive `/v1` discovery endpoints, tenant-scoped, vendor-free.**

## Decision
Option 3. Additive (minor bump of the public contract):
- `GET /v1/integrations` — integrations the caller's tenant owns or is granted: `id`, `capability` (e.g. `events.track@v1`), `operations`, `delivery.mode`, `interactive`, `requires_idempotency_key`, `description`.
- `GET /v1/integrations/{id}` — the above plus the request/response JSON Schemas (or references to `/v1/capabilities/{capability}`).
- `GET /v1/capabilities/{capability}` — canonical request/response schema, operations, interactive flag.

They never return vendor, account, transformer, integration version or snapshot version. Integrations the tenant cannot access return 404 (no existence oracle). Served from the snapshot (no DB read).

## Consequences
- Leak scanner in contract tests covers these responses (TESTING_STRATEGY §4.1).
- `x-pii` annotations are internal; the steward decides whether they are stripped from returned schemas (proposed: stripped).
- Small extra surface on the gateway; it is read-only and cheap.

## Deviation from DESIGN.md
Additive endpoints beyond §4.1. No change to existing endpoints.
