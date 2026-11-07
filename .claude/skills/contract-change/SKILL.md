---
name: contract-change
description: Contract change flow for the Integration Hub - writing a contract change request (CCR) per docs/process/CONTRACT_CHANGES.md, and for the contract-steward, editing OpenAPI 3.1 / JSON Schema in contracts/ safely, semver bump of info.version, contracts/CHANGELOG.md, examples, no vendor/account/version leakage in the public caller spec, validation, notifying backend and frontend, and the commit order contract to backend to frontend. Use whenever an endpoint, field, query param, error code, webhook event or MCP tool shape must be added or changed (operation and endpoint request schemas are registry data, not contracts), when code needs something the contract lacks, or when reviewing a CCR.
---

# Contract changes

`contracts/` is the only interface between backend, frontend, `hubmcp` and SDKs (AGENTS.md §4).
Only the **contract-steward** edits it. Everyone else files a **change request (CCR)** and waits.

Files (check the folder; names may grow): `contracts/openapi/hub-admin.v1.yaml` (admin API, used
by the console), the caller API OpenAPI file next to it, webhook-event JSON Schemas, the MCP tool
contract, and `contracts/CHANGELOG.md`. Request schemas of integration operations and vendor
endpoints are **not** contracts: they are registry data, edited in the console and versioned with
the integration or endpoint (ADR-0023). A new integration or a schema change needs no CCR.

## Part 1: requesting a change (backend, frontend, MCP agents)
1. Stop coding against the missing shape. Do not edit `contracts/`, do not invent fields, do not
   mock a field the spec lacks.
2. Write the CCR as described in `docs/process/CONTRACT_CHANGES.md` (that file's template wins).
   Minimum content:
```markdown
## CCR: add `expires_at` to request status (requested by frontend-engineer, HUB-262)
- API: caller | admin | event | mcp      -> admin
- Operation / schema: getRequest (`GET /admin/v1/requests/{request_id}`), schema `RequestDetail`
- Change: add optional `expires_at` (string, date-time, nullable) to `RequestDetail`
- Why: DLQ console must show TTL (DESIGN §10.7); currently not exposed
- Compatibility: additive (minor)
- Example: { "request_id": "req_01J9ABCD", "status": "parked", "expires_at": "2026-10-29T23:30:00Z" }
- Consumers affected: frontend (DLQ, request explorer), hubmcp (no)
```
3. Open/link a `contract` ticket in the EP's range (`tickets` skill); your own ticket depends on it
   and is `blocked` until the contract commit lands. Keep building what the contract already has.

## Part 2: steward review
Reject or reshape a CCR if:
- It leaks **vendor names, account names, versions or vendor-specific fields** into the caller API
  (D12, AGENTS.md §3.1). Vendor detail may appear only in the admin API.
- It is a breaking caller change: use a **new integration ID** instead (D11).
- It is a breaking admin change without the user's approval.
- It duplicates an existing field or invents a new error envelope (one envelope only, DESIGN §4.3).

## Part 3: editing OpenAPI 3.1 safely
- Prefer additive: new optional fields, new endpoints, new enum values only where clients tolerate
  unknown values (document that).
- Breaking (major): removing/renaming fields, making optional -> required in requests, changing
  types, narrowing enums in responses, changing status codes or paths.
- JSON Schema 2020-12 dialect: use `type: [string, "null"]` for nullable (not `nullable: true`),
  `examples` arrays, `$ref` to `components/schemas`.
- Every new operation: unique `operationId` (camelCase verbNoun), tags, auth, all responses including
  the canonical error envelope (`$ref` the shared `Error` schema) and `X-Hub-Request-Id` header.
- Every schema change comes with **examples** (MSW mocks and contract tests are derived from them).
- Mark PII with `x-pii` where relevant (drives audit masking).
- Admin mutations accept a required `reason` and return a `change_log_id`.

## Part 4: version, changelog, validate
- Bump `info.version`: additive -> minor (`1.3.0 -> 1.4.0`), fix to docs/examples only -> patch,
  breaking -> major (admin only, approved).
- `contracts/CHANGELOG.md` entry (newest first): version, date, operationIds/schemas changed,
  additive/breaking, CCR + ticket id.
- Validate before committing:
  - YAML parses; every `$ref` resolves; `operationId`s unique; examples validate against schemas.
  - Run the repo's lint target if present (e.g. `make contracts-lint` or an OpenAPI linter such as
    Redocly/Spectral via `npx`); check `contracts/AGENTS.md` for the agreed command.
  - Frontend smoke: `cd frontend && npm run gen:api && npm run check` compiles.
- Search the caller spec for leakage: `grep -niE 'vendor|account|clevertap|openai|version' <caller spec>`
  and justify every hit (e.g. `info.version` is fine).

## Part 5: commit order and notification
1. **Contract commit first, alone**: `contract(admin): add expires_at to request detail (HUB-261)`
   + root `CHANGELOG.md` entry (and `contracts/CHANGELOG.md`).
2. Notify `backend-engineer` and `frontend-engineer` (and MCP/SDK owners) in writing: what changed,
   affected operationIds, version, what each must do.
3. **Backend** implements; contract tests validate every response against the spec.
4. **Frontend** runs `npm run gen:api`, adapts, updates MSW fixtures from the new examples.
5. If callers see it: update `clients/claude-skill/` and MCP tool docs (AGENTS.md §8.9).

## Anti-patterns
- Backend shipping a shape the spec does not have, "we will update the contract later".
- Contract edits bundled into a feature commit.
- Forgetting the version bump or examples.
- Adding `vendor` or `account` to a caller response "for debugging". Use the admin API or audit.
