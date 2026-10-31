# ADR-0004: Contract-first with OpenAPI 3.1 and a contract-steward agent

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead |
| DESIGN refs | §4, §8.1, §16.1; AGENTS §4 |

## Context
Backend, frontend, MCP server, Go SDK and the consumer skill are built in parallel, largely by separate agents. Without a single written interface they drift, and the caller contract risks leaking vendor/account/version (D12).

## Options considered
1. **Code-first** (generate OpenAPI from Go handlers) — the backend becomes the source of truth; frontend waits for backend; leaks are discovered late.
2. **Contract-first, anyone edits** — fast, but uncontrolled versioning and inconsistent error envelopes.
3. **Contract-first with a single steward** — one owner of `contracts/`, written change requests, semver per document.

## Decision
Option 3. `contracts/` holds OpenAPI 3.1 documents (`openapi/hub-public.v1.yaml`, `openapi/hub-admin.v1.yaml`) and JSON Schema 2020-12 for capabilities and events — the single source of truth. Only the `contract-steward` agent edits it, through the CCR process (`docs/process/CONTRACT_CHANGES.md`). Each change: own commit `contract(<area>): … (HUB-###)`, `info.version` semver bump, `contracts/CHANGELOG.md` entry, examples updated.

## Consequences
- Backend validates every response against the spec in tests (kin-openapi; fallback libopenapi-validator if 3.1 support is insufficient — TESTING_STRATEGY §4.1).
- Frontend generates types with `openapi-typescript` and mocks with MSW from the same examples; never hand-writes API types.
- Capability JSON Schemas are not duplicated in `backend/config/` (TECH_SPEC §8.2).
- A small coordination cost per change; mitigated by keeping CCRs short and additive.

## Deviation from DESIGN.md
None (DESIGN does not prescribe a process).
