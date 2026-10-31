# ADR-0001: Monorepo layout

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead |
| DESIGN refs | §18.1, §17 |

## Context
DESIGN §18.1 describes a single Go module rooted at the repo. We also need an admin console (ADR-0005), an MCP server (ADR-0006), a consumer Claude skill, frozen API contracts shared by all of them (ADR-0004), and deploy assets. Agents work in parallel per folder (AGENTS §2, §11) and need clear ownership boundaries.

## Options considered
1. **Single Go module at the root** (DESIGN §18.1 literally), frontend elsewhere — simplest Go paths, but mixes Node and Go tooling at the root and blurs ownership.
2. **Polyrepo** (backend, frontend, contracts) — clean ownership but contract changes need cross-repo coordination, which defeats contract-first commits (contract → backend → frontend in one PR train).
3. **Monorepo with top-level folders per concern**, Go module under `backend/`.

## Decision
Option 3:
- `backend/` = Go module `github.com/Donquixot-Doflamingo/integration-hub/backend`, internal layout exactly DESIGN §18.1 (`cmd/hub`, `cmd/hubctl`, `cmd/mockvendor`, **plus `cmd/hubmcp`**; `internal/*`; `pkg/hubclient`; `config/`; `migrations/`).
- `deploy/` at repo root (docker-compose, k8s later, alert rules, dashboards, load scripts) — moved out of the module because it is shared infra.
- `frontend/` = SvelteKit admin console; `contracts/` = OpenAPI 3.1 + JSON Schema; `clients/claude-skill/` = consumer skill; `docs/`.
- Binary-private code goes under `cmd/<bin>/internal/` rather than new top-level `internal/` packages.
- **Accepted addition (lead, 2026-10-31, HUB-020)**: `internal/core`, a leaf package of shared value types and canonical error codes, to keep the dependency graph acyclic (TECH_SPEC §4.1). Also added: `internal/testkit` for test-only helpers (in-process mockvendor); production code never imports it, enforced by `internal/archtest`.

## Consequences
- One PR can carry a contract commit, backend commits and frontend commits in order (ADR-0004, ADR-0008).
- Go import paths include `/backend`; `pkg/hubclient` consumers import `github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`. If the SDK is published widely later, it may move to its own module.
- Go `embed` cannot reach `contracts/` from `backend/`; build and container steps copy contracts in (TECH_SPEC §8.2).
- CI uses path filters per folder.

## Deviation from DESIGN.md
§18.1 places `deploy/` and `docs/` inside the module tree and has no `cmd/hubmcp`; both changes are structural only. `internal/core` and `internal/testkit` are additions.
