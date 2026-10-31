# backend/AGENTS.md

Rules for work inside `backend/`. The root `AGENTS.md` still applies.

## Scope
Go module `github.com/Donquixot-Doflamingo/integration-hub/backend`. Binaries: `cmd/hub` (gateway | worker | scheduler | admin), `cmd/hubctl`, `cmd/mockvendor`, `cmd/hubmcp`. Layout follows `docs/DESIGN.md` §18.1. Architecture and package dependency rules: `docs/tech/TECH_SPEC.md`.

## Before you write code
1. Find or create the `HUB-###` ticket (`docs/tickets/TICKETS.md`) and branch `<type>/HUB-###-slug`.
2. Check the operation exists in `contracts/openapi/*.yaml`. If it doesn't, or its shape is wrong, **stop** and file a contract change request (`docs/process/CONTRACT_CHANGES.md`) for the `contract-steward`. Never change `contracts/` yourself, and never ship a shape that is not in the contract.
3. Load the matching skill from `backend/.claude/skills/` (see its README): `go-idioms`, `go-architecture`, `go-system-design`, `go-testing`, `go-memory-performance`, `go-logging`, `go-debugging`, `go-observability`, `postgres-patterns`, `hub-transformers`, `api-contract-backend`, `mcp-builder`.

## Rules
- Hot path (DESIGN §18.5): snapshot pointer only, no DB or registry reads, no decryption, pooled buffers, non-blocking audit. Each middleware records its own duration and stays under 100 µs p99.
- `context.Context` is the first parameter of anything that does I/O. No goroutine is started without a way to stop it. Tests run `goleak`.
- Errors map to canonical codes (DESIGN §4.4) only at the edge. Internal packages return wrapped errors.
- Never log request or response bodies or secrets. The audit trail is the place for payloads, subject to policy.
- Tests: table-driven, `-race`, goldens for transformers, contract tests (kin-openapi) for every handler, integration tests behind `//go:build integration`.
- Migrations are append-only (goose). Never edit an applied migration.
- New metrics, logs and spans go in the feature doc and `docs/tech/OBSERVABILITY.md`.

## Done means
`make lint test itest` green, contract tests green, docs and changelog updated, PR opened from a branch.
