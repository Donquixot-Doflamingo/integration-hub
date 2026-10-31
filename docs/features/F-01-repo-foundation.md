# F-01 — Repo foundation, dev environment & CI

## Summary
The monorepo skeleton, conventions, local dev stack and CI that every other feature builds on: Phase-1 docs and contracts, then the M0 Go scaffold (`hub` roles, health endpoints, redacting logger, telemetry skeleton).

## Status
`in-progress` — 2026-10-31. Phase 1 merged; M0 scaffold complete on `phase-2/m0-scaffold` ([summary](../milestones/M0.md)). Remaining: HUB-013 (branch protection), HUB-014/015 (contract lint), HUB-033.

## Owner
Lead (Phase 1 docs) · `backend-engineer` (M0 scaffold) · `frontend-engineer` (frontend scaffold).

## Milestone / Epic
Phase 1 + M0 (DESIGN §19) · EP-00 / EP-01 · HUB-001–039 (HUB-020–039 shared with F-02).

## Problem
Backend, frontend, MCP server and docs agents work in parallel. Without fixed layout, IDs, contracts and CI, they drift and nobody can tell what is agreed.

## Goals / Non-goals
**Goals**
- Monorepo layout (PRD DV-1): `backend/` (Go module `github.com/Donquixot-Doflamingo/integration-hub/backend`, DESIGN §18.1 layout inside it), `frontend/` (SvelteKit), `contracts/`, `clients/claude-skill/`, `deploy/`, `docs/`.
- Phase-1 docs: PRD, personas, stories, feature docs, tech spec, ADRs, tickets, process docs, frozen contracts.
- M0: `Makefile` targets `build test itest lint bench run`; `golangci-lint`; CI workflow; `deploy/docker-compose.yml` (Postgres 16, Redis 7); `hub gateway|worker|scheduler|admin` entrypoints; env + flag config; `/healthz`, `/readyz`; redacting `slog`; OTel/metrics skeleton (DESIGN §19 M0).

**Non-goals**
- Kubernetes manifests (later), production deploy pipeline, any request handling.

## User stories
- [US-0101](../product/USER_STORIES.md) — One-command local environment
- [US-0102](../product/USER_STORIES.md) — CI enforces the rules
- [US-0103](../product/USER_STORIES.md) — Docs and conventions before code
- [US-0104](../product/USER_STORIES.md) — Redacting logger from day one

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Ops endpoints | `GET /healthz`, `GET /readyz` on every role | `contracts/openapi/hub-public.v1.yaml#getHealthz`, `#getReadyz` |
| CLI | `hub gateway|worker|scheduler|admin`, `make build test itest lint bench run` | — |
| Frontend | `npm run dev|check|test|gen:api` | — |

## Behaviour & rules
1. One binary, four role entrypoints sharing transform/upstream code (DESIGN §17).
2. `/healthz` = process alive; `/readyz` = dependencies reachable (Postgres, Redis; snapshot loaded from M1 on). Readiness names the failing dependency.
3. Config precedence: flags > env > defaults. No secrets in flags.
4. The redacting `slog` handler is installed before any other logger use; it replaces registered secret values anywhere in a record with `***` (DESIGN §9).
5. CI blocks merge on: lint, `go test -race ./...`, `make itest` against real Postgres/Redis, `npm run check && npm test`, OpenAPI lint, contract version bump check (AGENTS.md §4).
6. Every commit carries a `CHANGELOG.md` entry (AGENTS.md §7); CI checks presence.

## Data model impact
None (migrations framework `goose` wired up, empty initial migration).

## Config impact
Process config only: `HUB_ROLE`, `HUB_HTTP_ADDR`, `HUB_PG_DSN`, `HUB_REDIS_ADDR`, `HUB_LOG_LEVEL`, `HUB_OTEL_ENDPOINT` (names proposed; final list in `docs/tech/TECH_SPEC.md`).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_build_info{version,commit,role}` | gauge = 1 |
| Metric | Go runtime + process metrics | default collectors |
| Log | `startup`, `shutdown`, `readiness_changed` | role, dependency |
| Trace | OTel provider initialised, no spans yet | exporter configurable |

## Security & PII
Redactor exists from M0; secret scanning (e.g. gitleaks) in CI; `.env` files git-ignored.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Postgres down at boot | Process starts, `/readyz` 503 | Load balancer keeps node out |
| Redis down | `/readyz` 503 naming redis | Same |
| Bad config | Exit non-zero with path-qualified error | — |

## Testing
- **Unit**: config precedence table tests; redactor tests (message, attrs, nested groups, JSON strings).
- **Integration**: docker-compose up → `/readyz` 200; stop Redis → 503.
- **Chaos / Contract / Golden**: N/A in M0 (contract lint only).

## Rollout & migration
Phase 1 merges as one PR set from `phase-1/*` branches; M0 starts after merge.

## Open questions
- Docker is not yet installed on the dev machine (HUB-012); testcontainers vs compose for `itest` in CI.
- ~~Should `/healthz` and `/readyz` be in the caller OpenAPI or a separate `ops.yaml`?~~ Resolved by the contract: both are in `hub-public.v1.yaml` (tag `health`); `/readyz` fails with 503 `NOT_READY`.

## Tickets
HUB-001 – HUB-039 (EP-00 docs/foundation; EP-01 M0 scaffold; HUB-020–039 shared with F-02).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
