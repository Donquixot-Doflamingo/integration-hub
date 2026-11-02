# CLAUDE.md — Integration Hub

@AGENTS.md

## Language rule
Always respond in **English**, even when the user writes in Hindi or Hinglish.

## Where things are
- Design (behaviour source of truth): `docs/DESIGN.md`
- Product: `docs/product/PRD.md`, `docs/product/USER_STORIES.md`, `docs/product/PERSONAS.md`
- Features: `docs/features/README.md` (index of `F-XX` docs)
- Tech: `docs/tech/TECH_SPEC.md`, `docs/tech/OBSERVABILITY.md`, `docs/tech/TESTING_STRATEGY.md`, `docs/tech/SECURITY.md`, `docs/tech/adr/`
- Contracts (frozen API): `contracts/` (OpenAPI 3.1, JSON Schema, MCP tool contract)
- Backlog: `docs/tickets/TICKETS.md`
- Process: `docs/process/GIT_WORKFLOW.md`, `docs/process/CONTRACT_CHANGES.md`, `docs/process/RELEASE.md`
- Changelog (one entry per commit): `CHANGELOG.md`

## Current phase
- **Phase 1 (merged):** repo foundation. Docs, contracts, skills, agents, tickets.
- **Phase 2 (in progress):** M0–M7 done (`docs/milestones/M0.md` … `M7.md`). Code, milestone by milestone, from DESIGN.md §19 (M0 → M8), with the admin console (EP-10) and MCP server (EP-11) in parallel against the frozen contracts.

## Working rules (DESIGN §0)
- **Callers never see vendor names, account names or versions.** Caller contract = integration ID + canonical body → canonical response. If a change would leak vendor identity into the caller contract, stop and tell the user.
- **Hot path discipline** (DESIGN §18.5): no DB or registry reads on the sync path, only the in-memory snapshot. Every middleware publishes its own duration metric.
- **Never call real vendors in tests.** Use `backend/cmd/mockvendor`. Real vendor payloads in the design are illustrative: verify against the vendor's current API docs and cite the doc URL in the transformer file.
- **Every transformer ships with golden fixtures.** Publishing a transformer without passing goldens must be impossible.
- **Never log secrets.** Credentials only enter through `creds.*` in header/query/auth targets. The logger redacts by value.
- Table-driven tests. `go test -race` in CI. Integration tests use real Postgres and Redis (docker compose or testcontainers).
- **Contract first**: no endpoint, field or event shape is coded before it is in `contracts/`. Only `contract-steward` edits `contracts/`.
- **Never commit to `main`.** Work on `<type>/HUB-###-slug` branches and open a PR. Every commit adds its own `CHANGELOG.md` entry.
- At the end of each milestone: all tests green, a summary of what was built, what was deferred and the **Deviations**, then commit and continue unless told to stop.
- If the design is ambiguous or wrong in practice, pick the simplest option consistent with the decisions below, implement it, record an ADR, and list it under Deviations.

## Decisions (DESIGN §2, short form)
| # | Decision | Default |
| --- | --- | --- |
| D1 | Core entity | Integration |
| D2 | ID location | Path: `POST /v1/integrations/{id}/invoke` |
| D3 | Default delivery mode | `wait`, 3 s window |
| D4 | Sync + circuit open | `fail_fast` (503 + `retry_after_ms`) |
| D5 | Breaker scope | `(account, endpoint)`, shared across nodes; per-integration pause and overrides |
| D6 | Bureau / Aadhaar / PAN audit | `sealed`, 90 days, unseal is audited |
| D7 | Transformer authors | Platform team + PRs gated by golden tests |
| D8 | Language + expressions | Go + `expr-lang/expr` |
| D9 | Phase-1 queue | Postgres outbox (`SKIP LOCKED`) behind `Queue` |
| D10 | Pending at cutover | Re-resolve at send time; `pinned` opt-in |
| D11 | Breaking canonical schema change | New integration ID; old one kept ≥ 6 months |
| D12 | Callers selecting versions | Never possible |

## Kickoff confirmations (defaults taken, 2026-10-31)
- Language: **Go** (backend), **SvelteKit / Svelte 5 + TypeScript** (admin console).
- Repo: new monorepo `integration-hub`. Go module `github.com/Donquixot-Doflamingo/integration-hub/backend`.
- Local Docker for Postgres + Redis: **yes**, via Colima (HUB-012). Use Node from `/opt/homebrew/opt/node@22/bin`.

## Commands (Phase 2, once scaffolded)
- Backend: `cd backend && make build test itest lint bench run`
- Frontend: `cd frontend && npm run dev | check | test | gen:api`
- Infra: `docker compose -f deploy/docker-compose.yml up -d`
