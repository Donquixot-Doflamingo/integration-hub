# Integration Hub — Ticket Backlog

Single source of truth for planned work until an external tracker exists. Format and rules: `AGENTS.md` §5, definition of done: `AGENTS.md` §9, how to work this file: [`README.md`](README.md). Behaviour is defined by `docs/DESIGN.md`; section references below (`§x.y`) point there.

Total tickets: **215** · done: **157** · todo: **51** · other (in-progress/in-review/blocked): **7**

## Epics

| Epic | Name | Milestone | Features | ID range | Tickets | Done |
| --- | --- | --- | --- | --- | --- | --- |
| EP-00 | Phase-1 foundation (docs, skills, agents, contracts, repo + GitHub setup) | Phase 1 | F-01 | HUB-001–HUB-019 | 16 | 13 |
| EP-01 | M0 Scaffold | M0 | F-01, F-02 | HUB-020–HUB-039 | 14 | 14 |
| EP-02 | M1 Config registry | M1 | F-03 | HUB-040–HUB-059 | 10 | 10 |
| EP-03 | M2 Transformer engine | M2 | F-04 | HUB-060–HUB-079 | 12 | 12 |
| EP-04 | M3 Gateway sync path + audit | M3 | F-05, F-06, F-07, F-08 | HUB-080–HUB-109 | 19 | 18 |
| EP-05 | M4 Outbox, worker, modes, idempotency, callbacks | M4 | F-09, F-10, F-11 | HUB-110–HUB-139 | 15 | 14 |
| EP-06 | M5 Resilience | M5 | F-12, F-13 | HUB-140–HUB-169 | 16 | 15 |
| EP-07 | M6 Versioning & cutover | M6 | F-14, F-15 | HUB-170–HUB-199 | 16 | 16 |
| EP-08 | M7 Control plane & hubctl | M7 | F-16, F-17 | HUB-200–HUB-229 | 15 | 15 |
| EP-09 | M8 Observability & hardening | M8 | F-18 | HUB-230–HUB-249 | 17 | 1 |
| EP-10 | Admin console (SvelteKit) | parallel to M0–M7 | F-19 | HUB-250–HUB-299 | 36 | 29 |
| EP-11 | MCP server + consumer skill + SDK | parallel to M3–M7 | F-20, F-21 | HUB-300–HUB-329 | 11 | 0 |
| EP-12 | Release 2 (DESIGN §19 Phase 2) | Phase 2 | F-22 | HUB-330–HUB-399 | 18 | 0 |

## EP-00 — Phase-1 foundation (docs, skills, agents, contracts, repo + GitHub setup)

### HUB-001 — Save DESIGN.md and create AGENTS.md / CLAUDE.md
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** — · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-001-design-and-agents`

Save the kickoff design unchanged as `docs/DESIGN.md` (DESIGN §0 first actions) and write the operating manual `AGENTS.md` plus a short `CLAUDE.md` that imports it, carrying the language rule, working rules and the §2 decisions table.

**Acceptance criteria**
- [x] `docs/DESIGN.md` is byte-identical to the kickoff file
- [x] `AGENTS.md` covers repo map, product rules, contract-first workflow, tickets, git, changelog, DoD
- [x] `CLAUDE.md` is under 150 lines and imports `AGENTS.md`

### HUB-002 — Write PRD, personas and user stories
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-002-prd-personas-stories`

Create `docs/product/PRD.md`, `PERSONAS.md` and `USER_STORIES.md` from DESIGN §1, §3, §14 (AGENTS §8 step 1). Stories use `US-FFxx` ids grouped by feature with Given/When/Then criteria.

**Acceptance criteria**
- [x] PRD states goals, non-goals and the §1 success metrics
- [x] Every feature F-01…F-22 has at least one user story
- [x] Stories have Given/When/Then acceptance criteria

### HUB-003 — Write feature docs F-01…F-22 and feature index
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** HUB-002 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-003-feature-docs`

Create one `docs/features/F-XX-<slug>.md` per feature from `_TEMPLATE.md`, plus `docs/features/README.md` as the index (AGENTS §8 step 2). Each doc lists stories, contracts touched, metrics/logs/alerts and tickets.

**Acceptance criteria**
- [x] `docs/features/_TEMPLATE.md` and `README.md` exist
- [x] One doc per feature F-01…F-22 with status `proposed`
- [x] Each doc links its epic in this backlog

### HUB-004 — Write tech spec, observability, testing, security docs and ADRs
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-004-tech-docs-adrs`

Create `docs/tech/TECH_SPEC.md`, `OBSERVABILITY.md`, `TESTING_STRATEGY.md`, `SECURITY.md` and the initial ADRs in `docs/tech/adr/` recording the §2 decisions (Go + expr, Postgres outbox, re-resolve default, etc.).

**Acceptance criteria**
- [x] Each doc exists and cites the DESIGN sections it elaborates
- [x] ADRs cover at least D8, D9, D10 and the monorepo/contract-first layout
- [x] OBSERVABILITY.md lists every §16.2 metric and §16.3 alert

### HUB-005 — Write process docs: git workflow, contract changes, release
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-005-process-docs`

Create `docs/process/GIT_WORKFLOW.md`, `CONTRACT_CHANGES.md` and `RELEASE.md` expanding AGENTS §4, §6, §7.

**Acceptance criteria**
- [x] Branch naming, commit format and merge-commit policy documented
- [x] Contract change request template and steward flow documented
- [x] Release steps turn `[Unreleased]` into a versioned section

### HUB-006 — Freeze v1 contracts: public, admin, mockvendor, MCP tools, capability + event schemas
- **Epic:** EP-00 · **Type:** contract · **Area:** contracts
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `contract/HUB-006-freeze-v1-contracts`

Author the OpenAPI 3.1 documents in `contracts/openapi/` (public caller API §4, admin API §16.1, mockvendor control API §19 M0), the MCP tool definitions, and JSON Schemas for capabilities (`events.track@v1`, …) and webhook events (§12). These are the only interface between backend, frontend, MCP and SDKs (AGENTS §4).

**Acceptance criteria**
- [x] Public, admin and mockvendor OpenAPI documents lint clean
- [x] MCP tool schemas and capability/event JSON Schemas (2020-12) exist
- [x] `contracts/CHANGELOG.md` records v1.0.0 of each document
- [x] No caller-facing schema exposes vendor, account or version

### HUB-007 — Add backend skills
- **Epic:** EP-00 · **Type:** chore · **Area:** backend
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-007-backend-skills`

Add skills under `backend/.claude/skills/` (Go idioms, architecture, system design, testing, profiling, structured logging, debugging, observability, Postgres/outbox patterns, MCP builder) per AGENTS §12.

**Acceptance criteria**
- [x] Each skill has a `SKILL.md` with a description
- [x] Third-party skills carry `LICENSE.txt` and are listed in `THIRD_PARTY_SKILLS.md`

### HUB-008 — Add frontend, process and consumer skills
- **Epic:** EP-00 · **Type:** chore · **Area:** frontend
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-008-frontend-process-consumer-skills`

Add frontend skills (`frontend/.claude/skills/`), repo-wide process skills (`.claude/skills/`: PRD, stories, feature docs, tickets, commits/changelog, ADRs) and the first cut of the consumer skill in `clients/claude-skill/`.

**Acceptance criteria**
- [x] Frontend skills cover Svelte 5/SvelteKit, design, typed client, testing, a11y
- [x] Process skills exist at repo root
- [x] `clients/claude-skill/integration-hub/SKILL.md` explains invoking the Hub without vendor knowledge

### HUB-009 — Define sub-agents
- **Epic:** EP-00 · **Type:** chore · **Area:** docs
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-009-sub-agents`

Create `.claude/agents/` definitions for `contract-steward`, `backend-engineer`, `frontend-engineer` and `docs-keeper` (AGENTS §11).

**Acceptance criteria**
- [x] Each agent file states scope, folders it may edit and skills it uses
- [x] Only `contract-steward` may edit `contracts/`

### HUB-010 — Write the ticket backlog
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** done · **Depends on:** HUB-003, HUB-006 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-010-ticket-backlog`

Create `docs/tickets/README.md` and `docs/tickets/TICKETS.md` covering every milestone in DESIGN §19 plus the admin console, MCP server and Release 2 placeholders.

**Acceptance criteria**
- [x] Every ticket follows AGENTS §5 format with testable acceptance criteria
- [x] Every §19 Accept line maps to at least one ticket
- [x] Parallel tracks and critical path are documented

### HUB-011 — Add CHANGELOG, PR template and CI placeholder
- **Epic:** EP-00 · **Type:** chore · **Area:** infra
- **Status:** done · **Depends on:** HUB-001 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-011-changelog-pr-template-ci`

Create root `CHANGELOG.md` with `[Unreleased]`, `.github/pull_request_template.md` with the AGENTS §9 DoD checklist, and a placeholder CI workflow that later tickets fill in.

**Acceptance criteria**
- [x] `CHANGELOG.md` follows the AGENTS §7 entry format
- [x] PR template contains the DoD checklist
- [x] `.github/workflows/ci.yml` exists and runs on pull requests

### HUB-012 — Install toolchain on the dev machine
- **Epic:** EP-00 · **Type:** chore · **Area:** infra
- **Status:** done · **Depends on:** — · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-012-install-toolchain`

Install and verify Go 1.25, Node 22 (with npm), Docker (with compose v2) and golangci-lint on the development machine so M0 and the frontend scaffold can start.

**Acceptance criteria**
- [x] `go version` reports ≥ 1.25 (1.27.3 installed; `go.mod` minimum is 1.26, see HUB-023)
- [x] `node --version` reports v22.x
- [x] `docker compose version` works and `docker run hello-world` succeeds
- [x] `golangci-lint --version` works
- [x] Versions recorded in `docs/tech/TECH_SPEC.md` (via docs-keeper)

### HUB-013 — Configure GitHub branch protection on main
- **Epic:** EP-00 · **Type:** chore · **Area:** infra
- **Status:** todo · **Depends on:** HUB-011 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-013-branch-protection`

Protect `main` so it changes only via PRs (AGENTS §6). Requires the repo owner to act in GitHub settings; the agent prepares the exact checklist and verifies afterwards.

**Acceptance criteria**
- [ ] Direct pushes to `main` are rejected (verified with a test push from a branch-less clone)
- [ ] PRs require passing CI status checks and one approval
- [ ] Merge commits allowed; squash and rebase merges disabled
- [ ] User confirms the settings were applied

### HUB-014 — CI: contract lint job (Redocly / Spectral)
- **Epic:** EP-00 · **Type:** chore · **Area:** infra
- **Status:** in-review · **Depends on:** HUB-006, HUB-011 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-014-ci-contract-lint`

Add a CI job that lints every OpenAPI document in `contracts/openapi/` and validates the JSON Schemas in `contracts/schemas/`, and fails if `info.version` changed without a `contracts/CHANGELOG.md` entry (AGENTS §4.3).

**Acceptance criteria**
- [ ] CI fails on an intentionally broken OpenAPI document
- [ ] CI fails when a contract file changes without a version bump + changelog entry
- [ ] Job runs only when `contracts/**` changes and finishes in < 2 min

Steward note (2026-10-31): `contract-lint` in `.github/workflows/contracts.yml` runs `make contracts-check` and `make contracts-version-check BASE=<PR base sha | push before>`. It also triggers on the root `Makefile` and the workflow file, since both define the check. Proven locally, not yet in CI: `make contracts-check` fails on broken copies (HUB-015 note); `contracts-version-check` fails on a content change without a bump, a bump without a changelog entry, a lower version, an event-schema change without a hub-public bump and a deleted document, and passes on formatting-only changes and on this branch; both targets together take about 1.5 s locally. All three criteria need a GitHub Actions run on the PR, so they stay open and the ticket stays `in-review` until that run is green.

### HUB-015 — make contracts-check: repeatable contract validation (Redocly lint, ajv for JSON Schemas, $ref + example + vendor-leak checks)
- **Epic:** EP-00 · **Type:** chore · **Area:** contracts
- **Status:** done · **Depends on:** HUB-012 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-015-contracts-check`

Replace the throwaway script used for the 1.0.0 freeze with a repeatable `make contracts-check` target (`contracts/README.md` "Validate locally"). It lints the three OpenAPI documents with Redocly, compiles every JSON Schema under `contracts/schemas/` with ajv (draft 2020-12), resolves every `$ref`, validates every example against its schema, and runs the caller vendor-leak check (D12) and the MCP `maps_to` operationId check. HUB-014's CI job calls this target.

**Acceptance criteria**
- [x] `make contracts-check` exits 0 on the frozen 1.0.0 contracts and non-zero on an intentionally broken `$ref`, example or schema
- [x] Redocly lint covers `hub-public.v1.yaml`, `hub-admin.v1.yaml` and `mockvendor.v1.yaml`; ajv compiles `contracts/schemas/**/*.json`
- [x] Vendor-leak check fails if a `hub-public` parameter, header or body field names a vendor, account or version (documented exceptions `dryRunIntegration` and `receiveVendorWebhook` allow-listed)
- [x] Every `maps_to` operationId in `contracts/mcp/hubmcp-tools.json` exists in its spec
- [x] Tool versions pinned; usage documented in `contracts/README.md` via a CCR to the contract-steward

Steward note (2026-10-31): exit 0 checked on the frozen 1.0.0 tree (`git archive main contracts`) and on the round-1 state; non-zero proven on copies with a broken internal and external `$ref`, invalid examples (wrong type, wrong type for a nullable field, bad ULID), a bad schema type, a misspelled schema keyword, a vendor-named hub-public parameter, a vendor account id in a caller example, an unknown MCP `maps_to`, an unquoted timestamp and a missing hub-admin `X-Hub-Request-Id`. Redocly changes from `recommended-strict` and why: contracts/README.md "Validate locally".

### HUB-016 — Resolve Phase-1 design open questions with the user and record ADRs
- **Epic:** EP-00 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** — · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `docs/HUB-016-open-questions-adrs`

Walk the user through the open questions that the contracts did not settle (PRD §14 Q1–Q8, Q11; OI-1 dry-run exception; TECH_SPEC §22; open questions in the feature docs) and record each decision as an ADR in `docs/tech/adr/` (AGENTS §8 step 3), updating PRD, feature docs and tickets to match.

**Acceptance criteria**
- [ ] Every open question in PRD §14 and §14.1 is marked resolved (with an ADR link) or explicitly deferred with an owner
- [ ] One ADR per decision that changes architecture, data model or a DESIGN §2 decision
- [ ] Decisions that change a shape are filed as CCRs (`docs/process/CONTRACT_CHANGES.md`), not edited into `contracts/`
- [ ] Feature docs' "Open questions" sections updated to match

## EP-01 — M0 Scaffold

### HUB-020 — Create backend Go module and package layout
- **Epic:** EP-01 · **Type:** chore · **Area:** backend
- **Status:** done · **Depends on:** HUB-012 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-020-go-module-layout`

Create `backend/go.mod` (`github.com/Donquixot-Doflamingo/integration-hub/backend`) and the empty package layout from DESIGN §18.1 (`cmd/hub`, `cmd/hubctl`, `cmd/mockvendor`, `cmd/hubmcp`, `internal/*`, `pkg/hubclient`, `config/`, `migrations/`).

**Acceptance criteria**
- [x] `go build ./...` succeeds in `backend/`
- [x] Every §18.1 package exists with a `doc.go` describing its responsibility
- [x] `go vet ./...` is clean

### HUB-021 — Makefile targets and golangci-lint config
- **Epic:** EP-01 · **Type:** chore · **Area:** infra
- **Status:** done · **Depends on:** HUB-020 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-021-makefile-lint`

Add `backend/Makefile` with `build test itest lint bench run` (DESIGN §19 M0) and a `.golangci.yml` with a strict but practical linter set.

**Acceptance criteria**
- [x] `make build` produces `hub`, `hubctl`, `mockvendor`, `hubmcp` binaries
- [x] `make test` runs `go test -race ./...`
- [x] `make itest` runs tests tagged `integration`
- [x] `make lint` passes on the scaffold

### HUB-022 — CI workflow for backend
- **Epic:** EP-01 · **Type:** chore · **Area:** infra
- **Status:** done · **Depends on:** HUB-021, HUB-011 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-022-ci-backend`

Fill the CI placeholder with backend jobs: lint, `make test` (race), `make itest` with Postgres 16 and Redis 7 service containers, and build all binaries.

**Acceptance criteria**
- [x] CI runs lint, unit (race), integration and build jobs on every PR
- [x] A failing unit test makes the PR check fail
- [x] Go module and build caches are used; full run < 10 min

### HUB-023 — docker-compose with Postgres 16 + Redis 7 and goose migrations
- **Epic:** EP-01 · **Type:** chore · **Area:** infra
- **Status:** done · **Depends on:** HUB-020 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `chore/HUB-023-compose-migrations`

Add `deploy/docker-compose.yml` (Postgres 16, Redis 7, healthchecks) and wire `goose` migrations in `backend/migrations/` with an empty baseline and `make migrate` (DESIGN §18.7).

**Acceptance criteria**
- [x] `docker compose up -d` brings both services to healthy
- [x] `make migrate` applies the baseline against compose Postgres
- [x] `make itest` uses the compose services

### HUB-024 — hub binary with role entrypoints and config loading
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-020 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `feat/HUB-024-hub-roles-config`

Implement `hub gateway | worker | scheduler | admin` (DESIGN §17) with config from env + flags (listen addr, DB/Redis URLs, snapshot source) and graceful shutdown.

**Acceptance criteria**
- [x] Each role starts and stops cleanly on SIGTERM within 10 s
- [x] Unknown role exits non-zero with usage text
- [x] Config precedence flag > env > default is table-tested

### HUB-025 — /healthz and /readyz endpoints
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-024, HUB-023 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `feat/HUB-025-healthz-readyz`

Every role serves `/healthz` (process alive) and `/readyz` (Postgres, Redis reachable and, for gateway/worker, a snapshot loaded) (DESIGN §19 M0).

**Acceptance criteria**
- [x] `/healthz` returns 200 as soon as the process is up
- [x] `/readyz` returns 503 with a reason when Postgres or Redis is down
- [x] Integration test covers both states

### HUB-026 — Redacting slog handler
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-020 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `feat/HUB-026-redacting-slog`

Add an `internal/telemetry` `slog.Handler` that holds current secret values and replaces any occurrence in messages and attributes with `***` (DESIGN §9, AGENTS §3.5).

**Acceptance criteria**
- [x] Registered secret values never appear in output, in message, attrs or nested groups (table test)
- [x] Secrets can be added/removed at runtime safely under `-race`
- [x] Handler overhead benchmarked and recorded

### HUB-027 — OpenTelemetry and metrics skeleton
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-024 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `feat/HUB-027-otel-metrics-skeleton`

Wire the OTel SDK (tracer provider, OTLP exporter optional), a Prometheus `/metrics` endpoint, and a middleware helper that publishes per-stage duration metrics (DESIGN §16.2, §18.5 rule 5).

**Acceptance criteria**
- [x] `/metrics` exposes Go runtime metrics and a sample `hub_overhead_ms` histogram
- [x] Incoming `traceparent` is continued in a span
- [x] Tracing can be disabled by config with no exporter running

### HUB-028 — mockvendor server, control API and request recording
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-020, HUB-006 · **Feature:** F-02 · **Stories:** US-02xx
- **Branch:** `feat/HUB-028-mockvendor-core`

Implement `cmd/mockvendor` per the mockvendor contract: arbitrary routes, a control API to set per-route behaviour and reset, and recording of received requests for test assertions (DESIGN §19 M0).

**Acceptance criteria**
- [x] Control API matches the frozen mockvendor OpenAPI document
- [x] Recorded requests (method, path, headers, body) can be listed and cleared
- [x] A Go test helper starts mockvendor in-process on a random port

### HUB-029 — mockvendor fault behaviours
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-028 · **Feature:** F-02 · **Stories:** US-02xx
- **Branch:** `feat/HUB-029-mockvendor-behaviours`

Add every M0 behaviour: fixed/random latency, status codes, error ratio, 429 with `Retry-After`, hard down (connection refused), slow drip, malformed body.

**Acceptance criteria**
- [x] Each behaviour has its own table-driven test proving the observed effect
- [x] Error ratio converges within ±5 % over 1 000 calls
- [x] Hard down yields connection refused, not an HTTP error

### HUB-030 — mockvendor inbound webhook sender
- **Epic:** EP-01 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-028 · **Feature:** F-02 · **Stories:** US-02xx
- **Branch:** `feat/HUB-030-mockvendor-webhooks`

Let mockvendor send signed or unsigned webhooks to a configured Hub URL on demand, for later inbound webhook tests (DESIGN §12.2, §19 M0).

**Acceptance criteria**
- [x] Control API call triggers a POST with the given body and headers
- [x] HMAC signing option produces a verifiable signature
- [x] Test covers delivery to an httptest receiver

### HUB-031 — Contract test harness with kin-openapi
- **Epic:** EP-01 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-006, HUB-025 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `test/HUB-031-contract-test-harness`

Add a test helper that loads the frozen OpenAPI documents with kin-openapi and validates every request/response recorded in handler tests (AGENTS §4.6).

**Acceptance criteria**
- [x] Helper validates status, headers and body against the operation in the contract
- [x] A deliberately wrong response fails the test with the JSON pointer
- [x] `/healthz` and `/readyz` are validated (or explicitly excluded with a reason)

### HUB-032 — M0 acceptance and milestone summary
- **Epic:** EP-01 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-022, HUB-025, HUB-029, HUB-030, HUB-031 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `test/HUB-032-m0-acceptance`

Prove the M0 Accept line and post the milestone wrap-up (AGENTS §10).

**Acceptance criteria**
- [x] `docker compose up` + `hub gateway` serves `/healthz` (scripted smoke test in CI)
- [x] All mockvendor behaviours covered by tests
- [x] `make test` and `make itest` green
- [x] Milestone summary with Deviations posted; feature docs F-01, F-02 updated

### HUB-033 — CCR: getReadyz 503 example lacks the required request_id
- **Epic:** EP-01 · **Type:** contract · **Area:** contracts
- **Status:** done · **Depends on:** HUB-006 · **Feature:** F-01 · **Stories:** US-01xx
- **Branch:** `contract/HUB-033-readyz-example-request-id`

### CCR: getReadyz 503 example lacks the required request_id
- **Requested by**: backend-engineer (found while implementing HUB-025)
- **Why**: `CanonicalError` requires `request_id` (`contracts/schemas/events/canonical-error.json`), and the 503 response declares an `X-Hub-Request-Id` header, but the `getReadyz` 503 example has no `request_id`. The backend returns one (schema wins). The example would fail strict example validation (HUB-015) and the MSW fixture the frontend builds from it would not be valid.
- **Document**: hub-public.v1.yaml
- **Operation / schema**: `getReadyz`, `GET /readyz`, response 503 example
- **Before**:
  ```yaml
  error: { code: NOT_READY, message: Node is not ready, retryable: true, retry_after_ms: 1000, circuit: null, reason: null }
  ```
- **After**:
  ```yaml
  error:
    code: NOT_READY
    message: Node is not ready
    retryable: true
    retry_after_ms: 1000
    details: [ { message: redis unreachable } ]
    request_id: req_01J9Z3M1A5B7C9D1E3F5G7H9JN
  ```
- **Breaking?**: no (example only; patch bump 1.0.0 → 1.0.1)
- **Consumers affected**: none at runtime; frontend MSW fixtures if generated from this example
- **Error codes**: NOT_READY (existing)
- **Rollout**: contract commit before the M0 PR merges, or in the next contract PR

**Acceptance criteria**
- [x] Example includes `request_id` and a `details` entry; contract validation passes
- [x] `hub-public` `info.version` 1.0.1 and `contracts/CHANGELOG.md` entry
- [ ] Contract test (HUB-031) validates the live 503 body against the operation

Steward note (2026-10-31): the frozen 1.0.0 example already carried `request_id`; the CCR's "before" snippet predates the freeze. 1.0.1 aligns the rest of the example with the backend (`details: [{message: redis unreachable}]`, no null `circuit`/`reason`/`upstream`) and keeps request id `...JQ`, because `...JN` is already used by the `receiveVendorWebhook` example. The contract-test criterion stays open for the backend (HUB-031).

## EP-02 — M1 Config registry

### HUB-040 — Config domain types
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-020 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-040-config-domain-types`

Define Go types in `internal/registry` for capability, vendor (+ endpoints `name@n`), account, transformer reference, integration with versions and active pointer, tenant, API key, registrar endpoint (DESIGN §3.1, §3.4, §8.1).

**Acceptance criteria**
- [x] Types round-trip YAML ↔ struct for the §3.4 and §8.3 examples
- [x] Integration versions are immutable values (no exported setters)

### HUB-041 — Loaders: capability (compiled JSON Schema), vendor, account
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-040 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-041-loaders-cap-vendor-account`

Load capability schemas (JSON Schema 2020-12 + `x-pii`, `interactive`, `operations`) and compile them with `santhosh-tekuri/jsonschema`; load vendor and account YAML (DESIGN §8.1, §8.3).

**Acceptance criteria**
- [x] Capability schemas from `contracts/schemas/capabilities/` compile once at load
- [x] `x-pii` paths and `interactive` flag are exposed on the compiled capability
- [x] Vendor endpoints parse timeouts, idempotent, batch_max and breaker defaults

### HUB-042 — Loaders: integration versions, tenant, API keys, registrar endpoints
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-040 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-042-loaders-integration-tenant`

Load integration YAML with all versions (targets, weights, failover, delivery, breaker, webhooks, audit, publish meta), tenants with hashed API keys and grants, and registrar endpoints (DESIGN §3.4, §8.1).

**Acceptance criteria**
- [x] Loader accepts the §3.4 example verbatim
- [x] API keys are only ever stored as hashes
- [x] Delivery defaults applied: mode `wait`, `wait_ms` 3000, `on_circuit_open` `fail_fast`, `pending` `re_resolve` (§2 D3/D4/D10)

### HUB-043 — Cross-reference validation with path-qualified errors
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-041, HUB-042 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-043-config-validation`

Validate the whole config set: referenced vendors/accounts/transformers/endpoints/registrar endpoints exist, weights sum to 100, interactive capabilities only use `sync` (§5.4), `wait|async|async_callback` declare a polling path or subscription (§5.2). Errors carry a file + YAML path.

**Acceptance criteria**
- [x] Table test with ≥ 15 invalid configs, each asserting the exact error path and message
- [x] All errors are reported in one pass, not just the first
- [x] Valid dev config produces zero errors

### HUB-044 — Snapshot build and atomic swap
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-043 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-044-snapshot-atomic-swap`

Build an immutable `Snapshot` with in-memory indexes (DESIGN §18.3) and expose `Registry.Snapshot()` backed by an atomic pointer swap.

**Acceptance criteria**
- [x] Concurrent readers during repeated swaps pass under `-race`
- [x] Readers holding an old snapshot keep a consistent view
- [x] Snapshot carries a monotonically increasing version and checksum

### HUB-045 — Dev file watcher
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-044 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-045-dev-file-watcher`

Watch `config/` in dev, debounce changes, rebuild and swap the snapshot; an invalid change keeps the last good snapshot and logs the errors (DESIGN §19 M1, §6.4).

**Acceptance criteria**
- [x] Editing a valid file swaps the snapshot within 2 s
- [x] An invalid edit is rejected and the previous snapshot keeps serving
- [x] Watcher is off unless explicitly enabled

### HUB-046 — Dev config set
- **Epic:** EP-02 · **Type:** chore · **Area:** backend
- **Status:** done · **Depends on:** HUB-043 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `chore/HUB-046-dev-config`

Add `config/` with `events.track@v1`, vendor `clevertap` pointing at mockvendor, accounts `ct-brandA-in`, `ct-brandB-us`, tenant `orders-service`, integrations `int_evt_brandA`, `int_evt_brandB`, `int_evt_brandB_min` (DESIGN §19 M1).

**Acceptance criteria**
- [x] Dev config loads and validates with zero errors
- [x] Account credentials reference the local encrypted file, never plaintext
- [x] Capability schema matches `contracts/schemas/capabilities/`

### HUB-047 — Postgres registry tables and sqlc queries
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-023, HUB-040 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-047-registry-tables`

Add migrations for registry tables of DESIGN §18.2 (tenants, api_keys, vendors, vendor_endpoints, accounts, account_credentials, capabilities, transformers, integrations, integration_versions, integration_grants, registrar_endpoints, config_snapshots) with sqlc queries.

**Acceptance criteria**
- [x] `make migrate` creates all tables; down migration reverses cleanly
- [x] `integration_versions` and `transformers` reject UPDATE (trigger or permissions) to enforce immutability
- [x] sqlc code generated and compiled in CI

### HUB-048 — Snapshot persistence and boot from last good
- **Epic:** EP-02 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-044, HUB-047 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `feat/HUB-048-snapshot-persistence`

Persist each snapshot (checksum, author, diff, blob in object storage; local directory in dev) to `config_snapshots`, and let nodes boot from the last stored snapshot when the control plane is down (DESIGN §8.2, §17).

**Acceptance criteria**
- [x] Snapshot blob checksum verified on load; corrupted blob is refused
- [x] Gateway boots with the control plane unavailable using the last blob
- [x] Snapshots older than 90 days are pruned by a job

### HUB-049 — M1 acceptance and milestone summary
- **Epic:** EP-02 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-045, HUB-046, HUB-048 · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `test/HUB-049-m1-acceptance`

Prove the M1 Accept line and post the wrap-up.

**Acceptance criteria**
- [x] Invalid configs rejected with precise errors (table tests green)
- [x] Snapshot swap under concurrent reads is race-free (`-race`)
- [x] Milestone summary with Deviations posted; F-03 doc updated

## EP-03 — M2 Transformer engine

### HUB-060 — Transformer DSL parser
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-040 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-060-dsl-parser`

Parse transformer YAML (request: method/path/headers/query/body; response; errors; batch; stream placeholder) into an AST of target paths → expression strings (DESIGN §7.1, §7.4).

**Acceptance criteria**
- [x] T1/R1 examples from §7.4 parse into the expected AST
- [x] Duplicate or conflicting target paths fail with a path-qualified error
- [x] Unknown top-level keys fail parsing

### HUB-061 — Typed expression env and compile-once
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-060, HUB-041 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-061-typed-env-compile`

Build an `expr-lang/expr` env typed from the capability schema with `req`, `hdr`, `account`, `creds`, `session`, `res`, `now()` (DESIGN §7.2) and compile every expression once at snapshot build.

**Acceptance criteria**
- [x] Reference to a missing field (`req.user.idd`) fails compile with transformer + path
- [x] `res` is unavailable in request mapping and fails compile if used
- [x] No expression is compiled on the request path (asserted by a counter in tests)

### HUB-062 — Stdlib: strings and time
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-061 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-062-stdlib-strings-time`

Implement `lower upper trim split join replace format truncate` and `now epochSeconds epochMillis parseTime formatTime addDuration` (DESIGN §7.3).

**Acceptance criteria**
- [x] Table tests for every function incl. edge cases (empty, unicode, invalid time)
- [x] `now()` is injectable for deterministic goldens

### HUB-063 — Stdlib: data, encoding/identity and domain functions
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-061 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-063-stdlib-data-domain`

Implement `?? merge pick omit flatten map filter len toJSON fromJSON`, `uuid sha256 hmacSHA256 base64 urlEncode`, `phoneE164 emailNormalize currencyMinor maskPAN maskAadhaar` (DESIGN §7.3).

**Acceptance criteria**
- [x] Table tests for every function
- [x] `maskPAN` / `maskAadhaar` never return the full identifier
- [x] `uuid` is deterministic under a seeded test source

### HUB-064 — Request mapping, credential target rule and dry-run
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-061 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-064-request-mapping`

Implement `BuildRequest` (method, path, headers, query, body) and a dry-run function returning the exact vendor request. `creds.*` is allowed only in header/query/auth targets unless `allow_creds_in_body: true` (DESIGN §7.2, §7.6).

**Acceptance criteria**
- [x] `creds.*` in a body target fails compile unless `allow_creds_in_body` is set
- [x] Dry-run output equals what `BuildRequest` would send, with secrets redacted
- [x] Account config values (`account.config.*`) resolve correctly

### HUB-065 — Response and error mapping
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-061 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-065-response-error-mapping`

Implement `MapResponse`: full/minimal response mapping and ordered error rules (`when` → `code`, `retryable`, `retry_after_ms`) with `header(res, ...)` (DESIGN §7.4 R1/R2).

**Acceptance criteria**
- [x] First matching error rule wins (table test with overlapping rules)
- [x] Response output is validated against the capability response schema
- [x] A throwing expression yields `TRANSFORM_FAILED` with the failing expression recorded

### HUB-066 — Transformer safety budgets
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-064, HUB-065 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-066-safety-budgets`

Enforce max expression depth, max output size and a per-transform time budget; no loops beyond `map`/`filter`, no network or disk (DESIGN §7.1).

**Acceptance criteria**
- [x] Over-deep expression rejected at compile
- [x] Output larger than the limit fails with `TRANSFORM_FAILED`
- [x] A pathological `map` exceeding the time budget aborts within budget + 1 ms

### HUB-067 — Golden fixture runner and `hubctl transform test`
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-064, HUB-065 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-067-golden-runner`

Run `fixtures/<name>.input.json` → `.expected_request.json` and `.vendor_response.json` → `.expected_response.json` for each transformer, as a library and as `hubctl transform test` (DESIGN §7.6).

**Acceptance criteria**
- [x] Mismatch prints a readable JSON diff with paths
- [x] Exit code non-zero on any failing golden
- [x] Runs in CI on every PR touching `config/transformers/`

### HUB-068 — Transformers T1, T2, R1, R2 with fixtures
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-067, HUB-062, HUB-063, HUB-046 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-068-t1-t2-r1-r2`

Author T1, T2, R1, R2 from DESIGN §7.4 with golden fixtures; verify CleverTap field names against the current upload API docs and cite the URL in each file (AGENTS §3.3).

**Acceptance criteria**
- [x] Each transformer has ≥ 2 golden cases incl. an error response
- [x] Doc URL comment present in each transformer file
- [x] `hubctl transform test` passes for all four

### HUB-069 — Compile transformers into the snapshot; reject failing goldens
- **Epic:** EP-03 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-044, HUB-067 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `feat/HUB-069-snapshot-transformers`

Snapshot build compiles all transformers and runs their goldens; a snapshot with a type error or failing golden is refused (AGENTS §3.4, DESIGN §6.4).

**Acceptance criteria**
- [x] Snapshot build fails if any golden fails, naming the transformer and fixture
- [x] Compiled transformers are shared across integrations referencing the same `T@n`

### HUB-070 — BenchmarkTransform4KB
- **Epic:** EP-03 · **Type:** perf · **Area:** backend
- **Status:** done · **Depends on:** HUB-068 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `perf/HUB-070-bench-transform-4kb`

Benchmark request + response transform on a 4 KB `events.track` payload and record p99 (DESIGN §7.6.5, §18.6).

**Acceptance criteria**
- [x] `BenchmarkTransform4KB` p99 ≤ 0.5 ms on the reference machine
- [x] Allocations per op recorded; result checked into `docs/tech/` via docs-keeper

### HUB-071 — M2 acceptance and milestone summary
- **Epic:** EP-03 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-066, HUB-069, HUB-070 · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `test/HUB-071-m2-acceptance`

Prove the M2 Accept line and post the wrap-up.

**Acceptance criteria**
- [x] Goldens pass
- [x] Test: reference to a missing field fails compile
- [x] Budget limits enforced (tests green)
- [x] `BenchmarkTransform4KB` ≤ 0.5 ms p99
- [x] Milestone summary posted; F-04 doc updated

## EP-04 — M3 Gateway sync path + audit

### HUB-080 — Edge HTTP server, request id and error envelope
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-024, HUB-027, HUB-031 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-080-edge-server-envelope`

Build the gateway router (`chi`) with recover, request id (`req_` + ULID), trace continuation and the canonical error envelope with every §4.4 code and HTTP status.

**Acceptance criteria**
- [x] Every response carries `X-Hub-Request-Id`
- [x] Panic in a handler returns the envelope with 500 and is logged without body secrets
- [x] Error code → HTTP status mapping table-tested against §4.4 and the public contract

### HUB-081 — Tenant API key auth and grant check
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-080, HUB-044 · **Feature:** F-05 · **Stories:** US-05xx
- **Branch:** `feat/HUB-081-tenant-auth-grants`

Authenticate `Authorization: Bearer` against the in-memory hashed key set and check the tenant owns or is granted the integration (DESIGN §5.1 step 1, §16.5). No DB access on this path.

**Acceptance criteria**
- [x] Bad key → 401 `UNAUTHORIZED`; non-owner/non-grantee → 403 `FORBIDDEN`; unknown id → 404 `INTEGRATION_NOT_FOUND`
- [x] Key comparison is constant-time
- [x] Middleware publishes its duration metric and stays < 100 µs p99

### HUB-082 — Body limit and capability schema validation
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-080, HUB-041 · **Feature:** F-05 · **Stories:** US-05xx
- **Branch:** `feat/HUB-082-body-limit-validation`

Enforce body limits (1 MB default, 4 MB LLM) and validate the body against the pre-compiled capability schema, returning 400 `VALIDATION_FAILED` with JSON pointers (DESIGN §5.1 step 2, §16.5).

**Acceptance criteria**
- [x] Oversized body rejected before full read
- [x] Each validation error lists its JSON pointer in `details`
- [x] Validation of a 4 KB body ≤ 0.8 ms p99 (benchmark)

### HUB-083 — Resolver: active version and weighted target
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-044 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-083-resolver-weights`

Implement `Resolver.Resolve` (DESIGN §18.3): version 0 → active, else exact version; weighted target chosen by hash of idempotency key or request id; failover chain and policies returned.

**Acceptance criteria**
- [x] Same route key always picks the same target (stickiness test)
- [x] Weights 90/10 distribute within ±2 % over 100 000 keys
- [x] Resolve ≤ 0.05 ms p99 (benchmark)

### HUB-084 — CredentialSource: local encrypted file, cache and redaction
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-044, HUB-026 · **Feature:** F-07 · **Stories:** US-07xx
- **Branch:** `feat/HUB-084-cred-source-local`

Implement `CredentialSource` backed by an AES-GCM encrypted local file with a dev key, a decrypted cache keyed `account + cred_version` (TTL 5 min, warmed on snapshot load) and registration of every secret with the redacting logger (DESIGN §9).

**Acceptance criteria**
- [x] No decryption happens on the request path (cache warmed at snapshot load; asserted in test)
- [x] Every loaded secret is registered for redaction
- [x] Tampered ciphertext fails closed with a clear error

### HUB-085 — Auth schemes: static headers, bearer, basic
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-084, HUB-064 · **Feature:** F-07 · **Stories:** US-07xx
- **Branch:** `feat/HUB-085-auth-schemes-basic`

Add `internal/creds` auth-scheme plugins for static headers (CleverTap), bearer (OpenAI/Anthropic) and basic (Twilio) (DESIGN §9).

**Acceptance criteria**
- [x] Each scheme injects the right header (table test)
- [x] Injected values never appear in logs (`TestInjectedValuesNeverLogged`: cache-registered values, base64 basic form included, redacted by the log handler)
- [ ] Injected values never appear in the `request.transformed` audit (depends on the audit writer: `Apply` runs after the snapshot and installs a new header map, but static-header creds come from the transformer itself, so the writer must redact by value)
- [x] Scheme chosen from the vendor definition, not the transformer

### HUB-086 — Upstream client: per-account pools, timeouts, deadline, egress allowlist
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-042 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-086-upstream-client`

Implement `Upstream.Do` with a per-account HTTP pool, `connect_timeout_ms` and `timeout_ms` per endpoint, `X-Request-Deadline` cap, TLS 1.2+ and egress restricted to hosts declared in the vendor definition (DESIGN §10.1, §16.5).

**Acceptance criteria**
- [x] Timeout and connect-timeout honoured within ±10 % (mockvendor latency tests)
- [x] `X-Request-Deadline` lower than endpoint timeout wins
- [x] Request to an undeclared host is refused before dialing

### HUB-087 — Outcome classification and sync retry rule
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-086, HUB-065 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-087-outcome-classification`

Map vendor outcomes to canonical codes (DESIGN §4.4, §10.2) via response/error transformers, with one immediate retry on timeout/reset only if the endpoint is idempotent or an idempotency key is present.

**Acceptance criteria**
- [x] Timeout → 504 `UPSTREAM_TIMEOUT`; 5xx → 503 `UPSTREAM_UNAVAILABLE`; 429 → 429 with `retry_after_ms`; 401/403 → 502 `UPSTREAM_AUTH_FAILED`; other 4xx → 422 `UPSTREAM_REJECTED` (table test)
- [x] Non-idempotent endpoint without key is never retried
- [x] Vendor message passed through for `UPSTREAM_REJECTED`

### HUB-088 — Invoke handlers (sync pipeline)
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-081, HUB-082, HUB-083, HUB-085, HUB-087 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-088-invoke-sync`

Wire `POST /v1/integrations/{id}/invoke` and `/invoke/{op}` through the §18.4 sync pipeline (resolve → transform → upstream → response map → respond) with per-stage duration metrics; debug-only `X-Hub-Integration-Version`.

**Acceptance criteria**
- [x] Happy path returns the canonical response from mockvendor
- [x] Unknown `op` for a multi-operation capability → 404 with envelope
- [x] No DB or registry read on the path (test with DB stopped after warm-up)
- [x] Responses validate against `contracts/openapi/hub-public.v1.yaml`

### HUB-089 — Dry-run endpoint
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-064 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `feat/HUB-089-dry-run-endpoint`

Implement `dryRunIntegration` (`POST /v1/integrations/{id}/dry-run`) returning the exact upstream request (method, URL, headers, body) the Hub would send, without sending it (DESIGN §4.1, §7.6.3). This is the documented, scope-gated exception to the no-vendor-leak rule (PRD DV-6): key scope `dry_run` required, audited with `dry_run: true`.

**Acceptance criteria**
- [x] Mockvendor records zero calls during dry-run; no queue entry or idempotency record is created
- [x] Credentials and secret values appear only as `***` in output
- [x] Key without scope `dry_run` → 403 `FORBIDDEN`, reason `missing_scope`
- [x] Response validates against the public contract

### HUB-090 — Discovery endpoints
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-081, HUB-041 · **Feature:** F-05 · **Stories:** US-05xx
- **Branch:** `feat/HUB-090-discovery-endpoints`

Implement `GET /v1/integrations` (integrations visible to the calling tenant), `GET /v1/integrations/{id}` and `GET /v1/capabilities/{capability}` (request/response schemas, operations, interactive flag).

**Acceptance criteria**
- [x] A tenant only sees integrations it owns or is granted
- [x] No vendor, account or version field in any discovery response (contract + grep test)
- [x] Responses validate against the public contract

### HUB-091 — Audit tables and buffered non-blocking writer
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-023 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `feat/HUB-091-audit-writer`

Add day-partitioned `requests`, `request_events`, `request_attempts` and an `AuditSink` with bounded channel, batch insert, drop counter and `audit_events_dropped_total` (DESIGN §13.4, §18.5 rule 3).

**Acceptance criteria**
- [x] `Emit` never blocks (test with a stalled DB)
- [x] Dropped events increment the counter
- [x] Partitions for the next 7 days are created automatically

### HUB-092 — Sync audit events with hashes
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-091, HUB-088 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `feat/HUB-092-audit-sync-events`

Emit every §13.1 event for the sync path, each with sha256 + byte size of the payload, tenant key replaced by its hash, and secrets redacted; bodies over 64 KB spill to object storage (DESIGN §13.1, §13.3).

**Acceptance criteria**
- [x] A sync request produces received → validated → resolved → transformed → sent → responded → response.transformed → completed
- [x] `integration.resolved` records version and snapshot version
- [x] Every event stores sha256 and bytes even when the body is not stored

### HUB-093 — Payload policies: full, masked, metadata_only
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-092, HUB-041 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `feat/HUB-093-payload-policies`

Apply the integration's audit policy: `full`, `masked` (default; `x-pii` fields masked/hashed), `metadata_only` (no bodies). `sealed` lands in HUB-208 (DESIGN §13.2).

**Acceptance criteria**
- [x] `masked` masks every `x-pii` path in all stored stages (table test)
- [x] `metadata_only` stores no bodies but keeps hashes and sizes
- [x] Policy recorded on the `requests` row

### HUB-094 — pkg/hubclient: Result / Queued / Error
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-006 · **Feature:** F-21 · **Stories:** US-21xx
- **Branch:** `feat/HUB-094-hubclient-core` (built on `phase-2/m3-hubclient`)

Ship the Go SDK core generated/written against the public contract: `Invoke` returns `Result`, `Queued` (for 202, never success) or a typed `Error` with `Retryable` and `RetryAfter` (DESIGN §4.5).

**Acceptance criteria**
- [x] A 202 can only be observed as `Queued` (compile-time distinct type)
- [x] Error envelope decoded into typed codes
- [x] Tests run against an httptest server validated by the contract harness

### HUB-095 — E2E: every error class maps to the right canonical code
- **Epic:** EP-04 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-029 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `test/HUB-095-e2e-error-classes`

End-to-end tests caller → hub → mockvendor for every vendor outcome class (DESIGN §19 M3 Accept).

**Acceptance criteria**
- [x] One test case per row of §10.2 sync column plus validation/auth/not-found errors
- [x] Each asserts HTTP status, `code`, `retryable` and `retry_after_ms`
- [x] Runs in `make itest`

### HUB-096 — Audit completeness and secret grep test
- **Epic:** EP-04 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-092, HUB-093, HUB-095 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `test/HUB-096-audit-secret-grep`

Verify the audit timeline is complete for every e2e case and that no secret value appears anywhere in stored events or logs (DESIGN §19 M3 Accept).

**Acceptance criteria**
- [x] Timeline for each e2e case contains all expected event types in order
- [x] Grep of all `request_events` payloads and captured logs finds no secret value
- [x] Test fails if a new secret is added to dev creds and leaks

### HUB-097 — Overhead benchmark, public contract tests and M3 summary
- **Epic:** EP-04 · **Type:** perf · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-089, HUB-090, HUB-092, HUB-094, HUB-096 · **Feature:** F-06 · **Stories:** US-06xx
- **Branch:** `perf/HUB-097-m3-overhead-bench`

Measure Hub-added latency with mockvendor at 0 ms, run contract tests for all M3 public endpoints, and post the M3 wrap-up.

**Acceptance criteria**
- [x] Overhead p99 ≤ 5 ms with mockvendor at 0 ms (DESIGN §19 M3 Accept)
- [x] Per-stage durations reported against §18.6 budget
- [x] All M3 endpoints pass contract validation
- [x] Milestone summary posted; F-05…F-08 docs updated

### HUB-098 — Spill audit payloads over 64 KB to object storage
- **Epic:** EP-04 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-091, HUB-093 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `feat/HUB-098-audit-payload-spill`

Payloads over 64 KB are stored truncated in `request_events` today (`payload_state = truncated`). Write the full policy-applied payload to object storage off the request path and record `payload_ref` (DESIGN §13.1). Found while building HUB-093.

**Acceptance criteria**
- [ ] A payload over 64 KB is stored in full in object storage; `payload_ref` points at it
- [ ] The spill never blocks the request path (done by the audit writer)
- [ ] The spilled object holds the policy-applied bytes, never the clear payload under `masked`

## EP-05 — M4 Outbox, worker, modes, idempotency, callbacks

### HUB-110 — Postgres outbox Queue implementation
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-023 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-110-outbox-queue`

Add the `outbox` and `dead_letters` tables with §18.2 indexes and implement `Queue` (DESIGN §11.2): `Enqueue` durable before return, `Claim` with `SKIP LOCKED` and 30 s visibility timeout, `Ack`, `Nack`, `Park`, `Release`, `DeadLetter`.

**Acceptance criteria**
- [x] Two concurrent claimers never receive the same envelope (itest)
- [x] Expired visibility makes an envelope claimable again
- [x] `Release(account, endpoint, limit)` releases in arrival order

### HUB-111 — Request state machine with guarded transitions
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-110, HUB-091 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-111-state-machine`

Implement states `accepted → in_flight → succeeded | retry_scheduled | parked | dead_lettered | expired` (plus terminal `failed` for sync errors, contract `RequestState`) with compare-and-set transitions (DESIGN §10.9).

**Acceptance criteria**
- [x] Every allowed transition succeeds and every other one is refused (table test)
- [x] Concurrent double-complete attempts result in exactly one terminal transition

### HUB-112 — Worker loop with re-resolve and poison detector
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-111, HUB-083, HUB-085, HUB-087 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-112-worker-loop`

Implement the §18.4 worker loop: claim, TTL check, re-resolve (active unless pinned/session), paused → nack, transform, upstream, classify → Ack/Nack/DeadLetter, record attempt. An envelope that crashes a worker twice is dead-lettered (DESIGN §10.6).

**Acceptance criteria**
- [x] Envelope holds canonical body only; worker re-transforms at send time
- [x] Poison envelope dead-lettered after two crashes
- [x] Each attempt row records the integration version used

### HUB-113 — Backoff, retry budget and DLQ policy
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-112 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-113-backoff-retry-budget`

Apply the §10.3 schedule (+1 s … +1 h, 8 attempts) with full jitter, per-integration override, `max(backoff, Retry-After)` for 429, a retry budget of ≤ 20 % of account live traffic, and dead-letter with reason, last request/response and history (DESIGN §10.2, §10.6).

**Acceptance criteria**
- [x] Jittered delays stay within the schedule bounds (property test)
- [x] 429 with `Retry-After: 10` waits ≥ 10 s
- [x] Retries beyond the budget are deferred and counted in `retry_budget_exhausted_total`
- [x] 401/403 and other 4xx dead-letter immediately

### HUB-114 — Scheduler role: leader election, due retries, TTL expiry
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-111 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-114-scheduler-ttl`

Implement the `scheduler` role as a leader-elected singleton (Postgres advisory lock) with hot standby that promotes due retries and expires envelopes past `expires_at` (DESIGN §10.7, §17).

**Acceptance criteria**
- [x] Only one scheduler is active; standby takes over within 15 s of leader death
- [x] Envelopes past TTL move to `expired` and emit `request.expired`
- [x] TTL defaults per capability, overridable per integration

### HUB-115 — Async mode and request status endpoint
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-110, HUB-111 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-115-async-mode-status`

`async` mode persists the envelope and returns 202 with `request_id`, `status`, `poll`; `GET /v1/requests/{request_id}` returns status and final canonical result (DESIGN §4.3, §5.2).

**Acceptance criteria**
- [x] 202 is returned only after the outbox insert committed
- [x] Async accept p99 ≤ 10 ms (benchmark)
- [x] Status endpoint is tenant-scoped (other tenants get 404)
- [x] Responses validate against the public contract

### HUB-116 — Wait mode
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-115, HUB-112 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-116-wait-mode`

`wait` persists first, attempts immediately in the gateway, returns 200 within `wait_ms` or 202 `in_flight` and lets the worker finish (DESIGN §5.2, D3).

**Acceptance criteria**
- [x] Vendor at 100 ms → 200 with result; vendor at 5 s with `wait_ms` 3000 → 202 `in_flight` then `succeeded` via worker
- [x] Gateway attempt and worker never both deliver (guarded transition test)

### HUB-117 — Async audit events and attempt records
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-111, HUB-092 · **Feature:** F-08 · **Stories:** US-08xx
- **Branch:** `feat/HUB-117-audit-async-events`

Emit `request.persisted`, `request.accepted`, `request.parked`, `policy.applied`, attempt events and terminal records written synchronously in the delivery transaction (DESIGN §13.1, §13.4).

**Acceptance criteria**
- [x] Async request timeline complete from accept to terminal state
- [x] Terminal record and status update commit atomically
- [x] `queue_wait_ms` recorded on the final record

### HUB-118 — Idempotency store
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-023 · **Feature:** F-10 · **Stories:** US-10xx
- **Branch:** `feat/HUB-118-idempotency`

Redis-backed idempotency scoped to tenant + integration, 24 h: completed requests replay the stored result with `X-Hub-Idempotent-Replay: true`; an in-progress async duplicate returns the existing request's 202, a sync one or a different body gets 409 `IDEMPOTENCY_CONFLICT`; integrations with `moves_money_or_quota: true` reject requests without a key (DESIGN §4.2, §5.1 step 4, §6.3).

**Acceptance criteria**
- [x] Replay returns identical body and status with the replay header (store: identical status, allowlisted headers, body and original request id, integration-tested on Redis; the edge sets `X-Hub-Idempotent-Replay` when wiring)
- [x] Missing key on a `moves_money_or_quota` integration → 400 `IDEMPOTENCY_KEY_REQUIRED` (edge behaviour; ticked by the lead when wiring)
- [x] Same key with a different body → 409 `IDEMPOTENCY_CONFLICT` (store: `Conflict` for another body or operation; the edge maps it to 409)
- [x] Redis down → fail closed for mandatory-key capabilities, pass-through otherwise (documented) (store returns the typed `ErrUnavailable`, contract in `internal/idempotency/doc.go`; the policy is edge behaviour, ticked by the lead when wiring)

Store commit: `feat(idempotency): add the Redis idempotency store (HUB-118)`.

### HUB-119 — Registrar endpoints and SSRF guard
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-042, HUB-023 · **Feature:** F-11 · **Stories:** US-11xx
- **Branch:** `feat/HUB-119-registrar-ssrf`

Store registrar endpoints (URL on allowlisted host, signing secret ref) and implement the SSRF guard: resolve host, refuse private/link-local ranges, no redirects, 5 s timeout (DESIGN §12.1).

**Acceptance criteria**
- [x] Callbacks to 10.0.0.0/8, 169.254.0.0/16, ::1, etc. refused (table test incl. DNS rebinding case)
- [x] Redirect responses are not followed
- [x] Signing secrets stored encrypted and registered for redaction

### HUB-120 — Subscriptions and signed callback delivery
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-119, HUB-111 · **Feature:** F-11 · **Stories:** US-11xx
- **Branch:** `feat/HUB-120-signed-callbacks`

On terminal states, enqueue `callback_deliveries` for each subscription and POST with `X-Hub-Signature: t=<unix>,v1=HMAC-SHA256(secret, t + "." + body)`; payload carries `request_id` and `idempotency_key`; own retry schedule and DLQ (DESIGN §12.1).

**Acceptance criteria**
- [x] Event types `request.completed|failed|expired` delivered per subscription
- [x] Failed callback retries independently of vendor delivery and dead-letters after the schedule
- [x] Payload validates against `contracts/schemas/events/`

### HUB-121 — async_callback mode end-to-end
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-120, HUB-115 · **Feature:** F-11 · **Stories:** US-11xx
- **Branch:** `feat/HUB-121-async-callback-mode`

`async_callback` persists → 202 → worker → signed POST of the result to the subscribed endpoint; publish/validation rejects the mode without a subscription (DESIGN §5.2).

**Acceptance criteria**
- [x] E2E: caller receives 202 then a signed callback with the canonical result
- [x] Integration with `async_callback` and no subscription fails validation

### HUB-122 — M4 tests: worker crash and duplicate submissions
- **Epic:** EP-05 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-112, HUB-116, HUB-118 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `test/HUB-122-m4-crash-duplicates`

Chaos-style tests for the first two M4 Accept items.

**Acceptance criteria**
- [x] Kill a worker mid-flight → envelope reclaimed after visibility timeout and delivered once from the caller's view
- [x] Duplicate submissions with the same key → exactly one vendor call (mockvendor recording), for sync, wait and async

### HUB-123 — M4 tests: callback signature and TTL expiry; M4 summary
- **Epic:** EP-05 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-121, HUB-114, HUB-117, HUB-122 · **Feature:** F-11 · **Stories:** US-11xx
- **Branch:** `test/HUB-123-m4-callback-ttl`

Remaining M4 Accept items plus the wrap-up.

**Acceptance criteria**
- [x] Callback signature verified by a test receiver; tampered body or timestamp > 5 min is rejected
- [x] TTL expiry produces `expired` status and an `request.expired` callback
- [x] Milestone summary posted; F-09…F-11 docs updated

### HUB-124 — Per-integration retry schedule override
- **Epic:** EP-05 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-113 · **Feature:** F-09 · **Stories:** US-09xx
- **Branch:** `feat/HUB-124-retry-override`

DESIGN §10.3 says the backoff schedule is overridable per integration. Add an optional `delivery.retry: { schedule: [...], max_attempts }` to integration versions (validated: increasing steps, 1–20 attempts, total under the TTL) and use it in the worker and the wait-mode settle. Found in M4 (HUB-113 shipped the default schedule only).

**Acceptance criteria**
- [ ] An integration version with a custom schedule retries on that schedule (worker itest)
- [ ] Invalid schedules are refused at snapshot build with a path-qualified error
- [ ] Integrations without an override keep DESIGN §10.3

## EP-06 — M5 Resilience

### HUB-140 — Breaker state machine (account, endpoint)
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-087 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-140-breaker-state-machine`

Local breaker per `(account, endpoint)`: open on > 50 % failures over 30 s with ≥ 20 calls (429 half weight) or 5 consecutive timeouts; open 30 s doubling to 10 min; half-open 3 probes (DESIGN §10.4).

**Acceptance criteria**
- [x] Table tests for every transition incl. 429 half weight and consecutive-timeout trip
- [x] Open window doubles on each re-open and caps at 10 min
- [x] Any half-open probe failure re-opens

### HUB-141 — Redis-shared breaker state with local cache
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-140, HUB-023 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-141-breaker-redis-shared`

Share breaker state via Redis so all nodes agree within ~1 s; the hot path reads only the local cache, sync with Redis happens off the request path (DESIGN §10.4, §18.5 rule 6).

**Acceptance criteria**
- [x] Two gateway nodes converge on open/closed within 1 s (itest)
- [x] Redis outage keeps last local state and never blocks requests
- [x] `breaker_state` and `breaker_transitions_total` exported

### HUB-142 — Integration breaker overrides and pause
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-141, HUB-088 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-142-breaker-overrides-pause`

Apply per-integration overrides that tighten thresholds (e.g. 3 consecutive timeouts) and the pause kill switch: sync/wait → 503 `INTEGRATION_PAUSED`, nothing parked; worker nacks paused envelopes (DESIGN §10.4).

**Acceptance criteria**
- [x] Override trips the integration's view at 3 timeouts while the shared breaker stays closed
- [x] Paused integration returns `INTEGRATION_PAUSED`; other integrations on the same account unaffected

### HUB-143 — Synthetic health probes while open
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-141, HUB-114 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-143-health-probes`

Scheduler runs the vendor's `health_probe` (dry where possible) every 30 s while a breaker is open so recovery is detected without live traffic (DESIGN §8.3, §10.4).

**Acceptance criteria**
- [x] Probe runs only while open/half-open
- [x] Successful probes move the breaker to half-open and then closed
- [x] Probe calls are visible in mockvendor recordings and never counted as tenant traffic

### HUB-144 — on_circuit_open: fail_fast
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-141, HUB-088 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-144-on-circuit-open-fail-fast`

Sync mode with an open circuit returns 503 `UPSTREAM_UNAVAILABLE` with `retry_after_ms` from the breaker window and `circuit: "open"`; nothing sent or queued (DESIGN §5.3, D4).

**Acceptance criteria**
- [x] Mockvendor records zero calls while open
- [x] `retry_after_ms` equals remaining open window ±1 s

### HUB-145 — on_circuit_open: failover and queue_if_allowed
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-144, HUB-115 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-145-failover-queue-if-allowed`

`failover` resolves the next failover target and repeats from Transform (idempotent endpoints or keyed requests only; sets `X-Hub-Served-By: failover:1`; exhausted → fail_fast). `queue_if_allowed` downgrades to async only with `Prefer: respond-async` or a callback subscription (DESIGN §5.3).

**Acceptance criteria**
- [x] Non-idempotent request without key never fails over
- [x] Exhausted chain returns fail_fast envelope
- [x] `queue_if_allowed` without the header or subscription returns fail_fast

### HUB-146 — Parking on open breaker
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-141, HUB-116, HUB-110 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-146-parking`

Wait mode with an open circuit returns 202 `status: queued, reason: circuit_open` at once; the worker parks envelopes tagged with account + endpoint in arrival order (DESIGN §5.2, §10.5).

**Acceptance criteria**
- [x] Wait returns 202 queued without attempting the vendor
- [x] Async envelopes move to `parked`; `parked_count` and `parked_oldest_age_s` exported

### HUB-147 — Account rate limiter (Redis token bucket)
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-086, HUB-023 · **Feature:** F-13 · **Stories:** US-13xx
- **Branch:** `feat/HUB-147-account-rate-limiter`

Shared Redis token bucket per account quota in the upstream client; live traffic has priority over drain/retries; workers wait for tokens instead of collecting 429s (DESIGN §10.8).

**Acceptance criteria**
- [x] Aggregate send rate across 3 workers stays within 5 % of the account rps
- [x] Live traffic is served first when drain and live compete
- [x] `ratelimit_rejections_total{layer="account"}` exported

### HUB-148 — Tenant limiter and bulkheads
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-081, HUB-086 · **Feature:** F-13 · **Stories:** US-13xx
- **Branch:** `feat/HUB-148-tenant-limiter-bulkheads`

Token bucket per tenant + integration at the edge (→ 429 `UPSTREAM_RATE_LIMITED`) and per-account concurrency limit (default 64 in flight) with a separate LLM worker pool (DESIGN §10.8).

**Acceptance criteria**
- [x] Tenant over budget gets 429 without touching the vendor
- [x] 65th concurrent call to one account waits or fails per config; other accounts unaffected
- [x] LLM pool exhaustion does not starve messaging workers

### HUB-149 — Ramped drain scheduler
- **Epic:** EP-06 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-146, HUB-143, HUB-147 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `feat/HUB-149-ramped-drain`

On breaker close release parked envelopes at 20 % of the account rate for 30 s, doubling every 30 s to normal; re-open mid-drain stops the release (DESIGN §10.5).

**Acceptance criteria**
- [x] Drain starts ≤ 60 s after recovery
- [x] Release rate follows 20 % → 40 % → 80 % → 100 % (±10 %)
- [x] Drained envelopes re-resolve at send time

### HUB-150 — Chaos 1: vendor down
- **Epic:** EP-06 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-144, HUB-146, HUB-029 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `test/HUB-150-chaos-1-vendor-down`

DESIGN §19 M5 Accept scenario 1 with mockvendor hard down.

**Acceptance criteria**
- [x] Breaker opens within the configured window
- [x] Sync gets 503 with `retry_after_ms` and `circuit: open`
- [x] Wait gets 202 `queued`; async envelopes park

### HUB-151 — Chaos 2: vendor recovers and drain ramps
- **Epic:** EP-06 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-149, HUB-150 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `test/HUB-151-chaos-2-recovery-drain`

Scenario 2: probes close the breaker and the parked backlog drains with a ramp.

**Acceptance criteria**
- [x] Probes close the breaker without live traffic
- [x] Drain starts ≤ 60 s after recovery
- [x] Observed send rate ramps 20 % → 100 % (measured from mockvendor recordings)

### HUB-152 — Chaos 3: re-open mid-drain
- **Epic:** EP-06 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-151 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `test/HUB-152-chaos-3-reopen-mid-drain`

Scenario 3: the vendor fails again during the drain.

**Acceptance criteria**
- [x] Breaker re-opens with doubled open window
- [x] Remaining envelopes stay parked; none dead-lettered or lost
- [x] Drain resumes after the next recovery

### HUB-153 — Chaos 4: two integrations on one account; pause isolation
- **Epic:** EP-06 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-142, HUB-150 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `test/HUB-153-chaos-4-shared-account`

Scenario 4: integrations sharing an account trip and recover together; pausing one does not affect the other.

**Acceptance criteria**
- [x] Both integrations see the open circuit within 1 s of each other
- [x] Both recover together
- [x] Pausing one returns `INTEGRATION_PAUSED` for it only

### HUB-154 — Chaos 5: failover target serves; M5 summary
- **Epic:** EP-06 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-145, HUB-150, HUB-151, HUB-152, HUB-153, HUB-148 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `test/HUB-154-chaos-5-failover`

Scenario 5 plus the milestone wrap-up.

**Acceptance criteria**
- [x] Failover policy serves from the failover target and sets `X-Hub-Served-By`
- [x] Audit records the failover attempt and target
- [x] All five chaos tests green in `make itest`
- [x] Milestone summary posted; F-12, F-13 docs updated


### HUB-155 — Drain quota follows elapsed time, not the nominal tick
- **Epic:** EP-06 · **Type:** fix · **Area:** backend
- **Status:** in-review · **Depends on:** HUB-149 · **Feature:** F-12 · **Stories:** US-12xx
- **Branch:** `fix/HUB-155-drain-elapsed-time`

`TestChaos2RecoveryAndRampedDrain` failed on two PRs that do not touch the drain (#18: ramp step 2 released 15/20; #19: step 4 released 39/50). Root cause: the drain released `rate × nominal tick` per scheduler tick. A `time.Ticker` drops ticks when the scheduler is busy (probes, expiry sweeps, slow Postgres), so the quota of a late or dropped tick was lost and the drain ran below the DESIGN §10.5 rate on a loaded node. This is a production behaviour bug, not only a test one.

**Acceptance criteria**
- [x] The quota uses the time elapsed since the key's previous drain, capped at 2 ticks so a stall never becomes a burst (`TestDrainQuotaUsesElapsedTime`)
- [x] Late ticks over 1.05 s at 50/s release 52, where the nominal-tick quota released 35
- [x] `TestChaos2RecoveryAndRampedDrain` and `TestChaos3ReopenMidDrain` pass 3/3 under load
## EP-07 — M6 Versioning & cutover

### HUB-170 — Publish pipeline with full validation
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-069, HUB-048, HUB-043 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-170-publish-pipeline`

Publish = validate (schemas compile, expressions type-check, goldens pass, response output conforms, referenced accounts/transformers exist and not retired, interactive → sync only, `supports` matches) → immutable version row → new snapshot (DESIGN §6.4, §8.2).

**Acceptance criteria**
- [x] Each validation failure blocks publish with a path-qualified error (table test per rule)
- [x] Published versions cannot be modified
- [x] Publish requires author and reason

### HUB-171 — Snapshot distribution to nodes
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-170 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-171-snapshot-distribution`

Nodes long-poll `/admin/v1/snapshots/latest` and listen to a Redis ping, download, compile in the background and swap atomically; a node that cannot fetch keeps the last good snapshot (DESIGN §6.4, §8.2).

**Acceptance criteria**
- [x] Publish reaches 3 nodes within 10 s (itest)
- [x] Control plane down → nodes keep serving last good
- [x] `snapshot_version` gauge exported per node

### HUB-172 — Rollback as a new version
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-170 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-172-rollback-new-version`

Rollback publishes a new version with the old content (v9 = v7); history stays linear, nothing is deleted (DESIGN §6.4).

**Acceptance criteria**
- [x] Rollback to v7 creates v9 with v7's spec and a reason
- [x] Version list remains strictly increasing with no gaps

### HUB-173 — Gradual cutover: weights and shadow
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-083, HUB-170, HUB-092 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-173-weights-shadow`

Support weight changes across successive versions (90/10 → 50/50 → 0/100) and `shadow` targets: serve from A, copy to B, log B's result, never return it; dry-run-only shadow for vendors with side effects (DESIGN §6.4).

**Acceptance criteria**
- [x] Shadow result recorded in audit and never returned to the caller
- [x] Side-effect vendors only receive dry-run shadow (no vendor call recorded)
- [x] Weight changes keep stickiness for unchanged buckets

### HUB-174 — Re-resolve vs pinned at send time
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-112, HUB-170 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-174-reresolve-pinned`

Worker uses the active version for `pending: re_resolve` (default) and `resolved_version` for `pinned` or session envelopes; every attempt records its version (DESIGN §6.3, §11.3, D10).

**Acceptance criteria**
- [x] Parked envelope re-resolves to v8 after publish (re_resolve)
- [x] Pinned envelope stays on v7
- [x] `request_attempts.integration_version` set on every attempt

### HUB-175 — Vendor session store and pinning
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-088, HUB-114 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `feat/HUB-175-vendor-sessions`

`vendor_sessions` table; step 1 of a multi-op capability returns `X-Hub-Session`; later steps route to the pinned version + account; scheduler expires sessions; unknown/expired → 404 `SESSION_NOT_FOUND` (DESIGN §3.1, §6.3, §14).

**Acceptance criteria**
- [x] Step 2 uses the version/account of step 1 even after a publish
- [x] Expired session → `SESSION_NOT_FOUND`
- [x] Session data never stores Aadhaar numbers (schema test)

### HUB-176 — Account lifecycle and retire guard
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-174, HUB-175, HUB-170 · **Feature:** F-15 · **Stories:** US-15xx
- **Branch:** `feat/HUB-176-account-lifecycle`

`active → draining → retired`: draining when no active version targets the account; retire refused while pinned requests or open sessions reference it; credentials deleted only at retire (DESIGN §6.5).

**Acceptance criteria**
- [x] Account auto-enters `draining` after the last referencing publish
- [x] Retire refused with counts of pinned requests and sessions
- [x] Retired account cannot be referenced by a new publish

### HUB-177 — Credential rotation (active / next)
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-084, HUB-171 · **Feature:** F-15 · **Stories:** US-15xx
- **Branch:** `feat/HUB-177-credential-rotation`

Accounts hold `active` and `next` credentials: add next → connection test → promote → delete old. In-flight calls keep the injected credential; later attempts (incl. drain) use `active` at send time; cache invalidated on snapshot ping (DESIGN §6.6, §9).

**Acceptance criteria**
- [x] Promote takes effect on all nodes within 10 s
- [x] A parked request drained after promotion uses the new credential
- [x] Every decrypt and credential change audited

### HUB-178 — Cutover scenario 1: completed before publish (account, vendor, transformer)
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-173, HUB-174, HUB-046 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-178-cutover-s1-completed`

Add cutover fixtures (second account, second mock vendor e.g. `moengage`, transformer `T1@8`) and test §6.3 row 1 for each change type.

**Acceptance criteria**
- [x] For account, vendor and transformer swaps: completed v7 requests keep v7, its account and payloads in audit
- [x] Publish rewrites no audit rows (row checksum test)

### HUB-179 — Cutover scenario 2: in-flight stays on v7
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-178 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-179-cutover-s2-inflight`

§6.3 row 2 for each change type, using mockvendor slow drip to hold a request in flight across publish.

**Acceptance criteria**
- [x] In-flight attempt completes on v7's target for all three change types
- [x] Attempt row records v7

### HUB-180 — Cutover scenario 3: parked/queued re-resolve vs pinned
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-178 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-180-cutover-s3-parked`

§6.3 row 3 for each change type.

**Acceptance criteria**
- [x] `re_resolve`: parked envelope delivered via v8's target
- [x] `pinned`: delivered via v7's target
- [x] Holds for account, vendor and transformer changes

### HUB-181 — Cutover scenario 4: retry with same key resolves fresh
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-178, HUB-118 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-181-cutover-s4-retry`

§6.3 row 4 for each change type.

**Acceptance criteria**
- [x] Retry of a failed v7 request with the same idempotency key lands on v8
- [x] A completed request with the same key is replayed, not re-sent

### HUB-182 — Cutover scenario 5: first new request uses v8
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-178 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-182-cutover-s5-new`

§6.3 row 5 for each change type.

**Acceptance criteria**
- [x] New request after propagation uses v8
- [x] Caller URL, body and response schema unchanged (contract validation)

### HUB-183 — Cutover scenario 6: session step 2 stays on v7
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-178, HUB-175 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `test/HUB-183-cutover-s6-session`

§6.3 row 6 for each change type.

**Acceptance criteria**
- [x] Step 2 of a session opened on v7 is served by v7's account and transformer
- [x] New sessions opened after publish use v8

Covered for an account + transformer swap (sync, wait; async step 2 in HUB-175's itest). The vendor-swap variant needs HUB-178's second mock vendor.

### HUB-184 — Retire guard and rollback acceptance; M6 summary
- **Epic:** EP-07 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-176, HUB-172, HUB-177, HUB-179, HUB-180, HUB-181, HUB-182, HUB-183 · **Feature:** F-15 · **Stories:** US-15xx
- **Branch:** `test/HUB-184-m6-acceptance`

Remaining M6 Accept items, mixed-version propagation, and the wrap-up.

**Acceptance criteria**
- [x] Retire refused while pinned requests or sessions exist; succeeds at zero
- [x] Rollback restores behaviour and leaves history linear
- [x] During propagation one node on v7 and another on v8 both record exact versions
- [x] Milestone summary posted; F-14, F-15 docs updated

### HUB-341 — Per-version golden fixtures for transformers
- **Epic:** EP-07 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-046 · **Feature:** F-14 · **Stories:** US-14xx
- **Branch:** `phase-2/m6-versioning`

Golden fixtures live in `transformers/<ID>/fixtures/`, shared by every version of the ID, so a new version that changes its output (T1@7 → T1@8) cannot pass them without breaking the old one. Found while building the M6 cutover fixtures. Let `transformers/<ID>/fixtures/v<N>/` hold the cases of version N when it exists; otherwise the shared directory applies (DESIGN §7.6).

**Acceptance criteria**
- [x] A version with its own `fixtures/v<N>/` runs only those cases
- [x] Versions without one keep using the shared fixtures
- [x] A failing per-version golden names `fixtures/v<N>/<case>`

## EP-08 — M7 Control plane & hubctl

### HUB-200 — Admin server, OIDC dev stub, RBAC and /me
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-080, HUB-006 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-200-admin-server-rbac`

Serve `/admin/v1` in the `admin` role with an OIDC stub for dev (static issuer, signed dev tokens), RBAC `admin | editor | viewer | tenant_viewer`, and `GET /admin/v1/me` (DESIGN §16.1).

**Acceptance criteria**
- [x] Unauthenticated → 401; insufficient role → 403 (table test per role)
- [x] `tenant_viewer` sees only its tenant's data
- [x] `/me` validates against `contracts/openapi/hub-admin.v1.yaml`

### HUB-201 — Change log with mandatory reason
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-200, HUB-047 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-201-change-log`

`change_log` table and middleware recording actor, action, object, before, after, reason for every admin mutation; mutations without a reason are rejected (DESIGN §16.1).

**Acceptance criteria**
- [x] Every mutating endpoint writes exactly one change_log row (enforced by a test that enumerates routes)
- [x] Missing reason → 400
- [x] `GET /admin/v1/change-log` with filters

### HUB-202 — Admin: vendors, capabilities, transformers
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-067 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-202-admin-vendors-caps-transformers`

CRUD/read for vendors and capabilities; transformers list/get/create-version plus `dry-run` and `run-goldens` (DESIGN §16.1).

**Acceptance criteria**
- [x] Transformer create refused when goldens fail
- [x] `run-goldens` returns per-fixture pass/fail with diffs
- [x] All responses validate against the admin contract

### HUB-203 — Admin: accounts, test-connection, rotate, lifecycle
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-176, HUB-177 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-203-admin-accounts`

Account endpoints incl. `test-connection`, credential `rotate` (add next / promote) and lifecycle transitions; secrets are write-only (DESIGN §16.1, §6.5, §6.6).

**Acceptance criteria**
- [x] No endpoint ever returns secret material
- [x] Retire returns the guard's blocking counts
- [x] Every call writes a change_log row

### HUB-204 — Admin: integrations read, version diff, snapshots
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-171 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-204-admin-integrations-read`

List/get integrations, list versions, diff two versions (structured + text), and snapshots list/get/latest long-poll (DESIGN §16.1, §8.2).

**Acceptance criteria**
- [x] Diff of v7 vs v8 shows exact changed paths
- [x] `snapshots/latest` long-poll returns within 1 s of a publish
- [x] Responses validate against the admin contract

### HUB-205 — Admin: publish, rollback, weights, pause
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-204, HUB-170, HUB-172, HUB-142 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-205-admin-integrations-mutate`

Mutating integration endpoints through the same publish pipeline: publish, rollback, set weights (new version), pause/resume; reason mandatory (DESIGN §16.1).

**Acceptance criteria**
- [x] Validation errors returned path-qualified in the contract's error shape
- [x] Pause takes effect on all nodes within 10 s
- [x] Each mutation in change_log with before/after

### HUB-206 — Admin: tenants, API keys, registrar endpoints
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-119 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-206-admin-tenants-keys`

Tenants CRUD, API keys (shown once on create, rotation with overlap, revoke), registrar endpoints CRUD with SSRF validation (DESIGN §16.1, §16.5).

**Acceptance criteria**
- [x] Key plaintext appears only in the create/rotate response
- [x] Old key keeps working during the overlap window then stops
- [x] Registrar URL on a private range rejected

### HUB-207 — Admin: request search and timeline
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-117 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-207-admin-requests`

Request explorer endpoints: search by tenant, integration, status, code, time range, idempotency key; timeline of events and attempts (DESIGN §16.1, §13).

**Acceptance criteria**
- [x] Search uses indexed columns and is paginated (cursor)
- [x] Timeline returns events in seq order with hashes and sizes
- [x] `tenant_viewer` restricted to its tenant

### HUB-208 — Sealed payload policy and audited unseal
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-207, HUB-093, HUB-084 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-208-sealed-unseal`

`sealed` policy: full raw bytes field-level encrypted with a per-tenant key (local key source in dev); `POST /admin/v1/requests/{id}/unseal` decrypts for an authorised admin with a reason, audited (DESIGN §13.2, D6).

**Acceptance criteria**
- [x] Sealed bodies unreadable in the DB (ciphertext test)
- [x] Unseal requires `admin` role and reason; writes change_log
- [x] Hashes and sizes match the original bytes

### HUB-209 — Admin: DLQ and breakers
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-201, HUB-113, HUB-141 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `feat/HUB-209-admin-dlq-breakers`

DLQ list, replay by filter (tenant, integration, account, reason, time range; re-resolves and re-transforms), purge; breaker state list and force-open/close (DESIGN §10.6, §16.1).

**Acceptance criteria**
- [x] Replay by filter moves matching entries to `accepted` and they deliver
- [x] Replay after a transformer fix uses the new transformer
- [x] Force-open is visible on all nodes within 1 s

### HUB-210 — hubctl scaffold, export and plan
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-204, HUB-202 · **Feature:** F-17 · **Stories:** US-17xx
- **Branch:** `feat/HUB-210-hubctl-plan`

`hubctl` with auth config; `export` round-trips the registry (minus secrets) to YAML; `plan` diffs a local config dir against live and prints exact changes (DESIGN §16.1 config as code).

**Acceptance criteria**
- [x] Export → plan against the same live shows no changes
- [x] Plan shows per-object path-level diffs
- [x] Secrets never written by export

### HUB-211 — hubctl apply
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-210, HUB-205, HUB-206 · **Feature:** F-17 · **Stories:** US-17xx
- **Branch:** `feat/HUB-211-hubctl-apply`

`hubctl apply` publishes through the same pipeline as the API and UI, with reason and author (DESIGN §8.2, §16.1).

**Acceptance criteria**
- [x] Apply of the dev config twice: second run makes no changes (idempotent)
- [x] Apply stops at the first validation failure with the path-qualified error
- [x] Each applied change writes change_log

### HUB-212 — hubctl dry-run and replay
- **Epic:** EP-08 · **Type:** feat · **Area:** backend
- **Status:** done · **Depends on:** HUB-210, HUB-209 · **Feature:** F-17 · **Stories:** US-17xx
- **Branch:** `feat/HUB-212-hubctl-dryrun-replay`

`hubctl dry-run <integration> <body.json>` and `hubctl replay --filter …` wrapping the admin endpoints (DESIGN §19 M7).

**Acceptance criteria**
- [x] Dry-run prints the vendor request with secrets redacted
- [x] Replay prints the count and requires `--yes` unless `--dry-run`

### HUB-213 — Admin contract tests and RBAC matrix
- **Epic:** EP-08 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-202, HUB-203, HUB-205, HUB-206, HUB-207, HUB-208, HUB-209 · **Feature:** F-16 · **Stories:** US-16xx
- **Branch:** `test/HUB-213-admin-contract-tests`

Validate every admin endpoint response against `contracts/openapi/hub-admin.v1.yaml` and test the full role × endpoint permission matrix.

**Acceptance criteria**
- [x] Every operation in the admin contract has at least one passing contract test
- [x] RBAC matrix test covers all four roles × all endpoints

### HUB-214 — M7 acceptance and milestone summary
- **Epic:** EP-08 · **Type:** test · **Area:** backend
- **Status:** done · **Depends on:** HUB-211, HUB-212, HUB-213 · **Feature:** F-17 · **Stories:** US-17xx
- **Branch:** `test/HUB-214-m7-acceptance`

Prove the M7 Accept line and post the wrap-up.

**Acceptance criteria**
- [x] `hubctl apply` of the dev config is idempotent
- [x] `plan` shows exact diffs
- [x] Every admin mutation appears in change_log
- [x] Milestone summary posted; F-16, F-17 docs updated

## EP-09 — M8 Observability & hardening

### HUB-230 — Metrics: full §16.2 set
- **Epic:** EP-09 · **Type:** feat · **Area:** backend
- **Status:** done · **Merged:** Merge pull request #16 from Donquixot-Doflamingo/feat/HUB-230-metrics-full · **Depends on:** HUB-027, HUB-154, HUB-123 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `feat/HUB-230-metrics-full`

Export every §16.2 metric with labels tenant, integration, vendor, account, version, with a cardinality guard; incl. `credential_expiry_days`, `queue_oldest_age_s`, `llm_*` placeholders.

**Acceptance criteria**
- [x] Every metric in §16.2 present on `/metrics` (test enumerates names): `TestDesignMetricsAreRegistered`
- [x] Label cardinality capped with an overflow bucket: `TestRequestsOverflow`, `TestQueueSampler` (`__overflow__`, `metric_series_overflow_total`)
- [x] `hub_overhead_ms` broken down by stage: `hub_stage_duration_ms{stage}` (HUB-027), unchanged

### HUB-231 — Tracing: edge, transform, upstream, callback spans
- **Epic:** EP-09 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-027, HUB-088, HUB-120 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `feat/HUB-231-tracing-spans`

OTel spans for edge, transform, upstream and callback; accept caller `traceparent`; forward it to vendors that accept it (DESIGN §16.4).

**Acceptance criteria**
- [ ] One trace links caller → gateway → worker → vendor → callback for an async request
- [ ] Spans carry no secrets or bodies (attribute allowlist test)

### HUB-232 — Alert rules and dashboards as code
- **Epic:** EP-09 · **Type:** chore · **Area:** infra
- **Status:** in-review · **Depends on:** HUB-230 · **Feature:** F-18 · **Stories:** US-1801, US-1804
- **Branch:** `chore/HUB-232-alerts-dashboards`

Prometheus alert rules for every §16.3 alert with `promtool` unit tests, and dashboards JSON for tenant, vendor and platform views in `deploy/`.

Shipped: `deploy/alerts/hub.rules.yaml` + `*_test.yml`, `deploy/dashboards/{tenant,vendor,platform}.json`, `make monitoring` and CI job `monitoring` (promtool 3.4.2 check + test; Go tests that parse every rule and panel query and fail on names the registry does not export). Rules use only exported metrics: ADR-0014 (auth/transform/DLQ sources until HUB-241), ADR-0015 (query check).

**Acceptance criteria**
- [x] Each §16.3 alert has a rule and a passing promtool test
- [ ] Dashboards load in Grafana (CI JSON lint + import check) — JSON lint, structure and exported-metrics check run in CI (`TestDashboardsAreValidAndUseExportedMetrics`); the Grafana import check is not done (no Grafana in CI or the dev container): HUB-243

### HUB-233 — PII deletion job by subject identifier
- **Epic:** EP-09 · **Type:** feat · **Area:** backend
- **Status:** in-review · **Depends on:** HUB-093, HUB-208 · **Feature:** F-18 · **Stories:** US-1805
- **Branch:** `feat/HUB-233-pii-deletion`

Job that purges stored payloads by subject identifier using `x-pii` tags while keeping hashes and metadata (DESIGN §16.5).

Shipped as the `hub erase-subject` maintenance command, because the admin contract has no erasure operation (CCR: HUB-247). See ADR-0012 and SECURITY §4.1. Follow-ups: HUB-245 (subject index) and HUB-246 (Redis idempotency replay records).

**Acceptance criteria**
- [x] After deletion, no stored body contains the subject identifier (grep test): `TestEraseSubjectPurgesEveryStoredBody` greps every bytea/json/jsonb/text column of every `hub` table and opens the sealed payloads
- [x] Hashes, sizes and timelines remain: same test, before/after comparison of events (sha256, bytes, type, time, meta), requests, attempts, dead-letter and callback metadata
- [x] Deletion recorded in change_log: same test, one `eraseSubjectPayloads` row per run with actor, reason, counts and `subject_ref`; the dry run writes none

### HUB-234 — Security hardening checks
- **Epic:** EP-09 · **Type:** chore · **Area:** backend
- **Status:** todo · **Depends on:** HUB-086, HUB-022 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `chore/HUB-234-security-hardening`

Add `gosec` and `govulncheck` to CI, verify TLS 1.2+ and egress allowlist enforcement, body limits and secret redaction across all roles (DESIGN §16.5, `docs/tech/SECURITY.md`).

**Acceptance criteria**
- [ ] CI fails on a high-severity gosec or known vulnerability
- [ ] Test proves TLS < 1.2 vendor handshake fails
- [ ] Secret grep over logs from the full itest run is empty

### HUB-235 — Load test: 500 sync RPS + 2 000 async msg/s
- **Epic:** EP-09 · **Type:** perf · **Area:** infra
- **Status:** todo · **Depends on:** HUB-097, HUB-154 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `perf/HUB-235-load-test`

k6 or vegeta scripts and a compose-based laptop-scale setup with mockvendor; profile and fix hotspots; report budgets vs §18.6 (DESIGN §19 M8).

**Acceptance criteria**
- [ ] Overhead p99 ≤ 5 ms at 500 sync RPS
- [ ] 2 000 async msg/s sustained for 10 min with queue age < 2 min
- [ ] Report with pprof findings committed under `docs/tech/`

### HUB-236 — Soak test with random vendor outages
- **Epic:** EP-09 · **Type:** test · **Area:** backend
- **Status:** todo · **Depends on:** HUB-235, HUB-152 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `test/HUB-236-soak-test`

Multi-hour soak with randomised mockvendor outages and worker restarts; reconcile accepted vs terminal requests (DESIGN §19 M8 Accept).

**Acceptance criteria**
- [ ] Zero lost async requests: every accepted request reaches a terminal state
- [ ] No duplicate callbacks for any request
- [ ] Results summarised with counts per terminal state

### HUB-237 — Runbooks and M8 summary
- **Epic:** EP-09 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-232, HUB-236, HUB-184, HUB-214 · **Feature:** F-18 · **Stories:** US-18xx
- **Branch:** `docs/HUB-237-runbooks-m8`

Runbooks for breaker open, DLQ spike, `UPSTREAM_AUTH_FAILED`, `TRANSFORM_FAILED`, credential rotation, snapshot lag; post the M8 wrap-up. The alert rules (HUB-232) link `docs/runbooks/<AlertName>.md` for all ten alerts in `deploy/alerts/hub.rules.yaml` (six page, four warn).

**Acceptance criteria**
- [ ] Each paging alert links a runbook
- [ ] Milestone summary posted; F-18 doc updated
- [ ] Release notes for v1.0.0 prepared per `docs/process/RELEASE.md`

### HUB-238 — Transformer time budget flakes under -race in the full itest run
- **Epic:** EP-09 · **Type:** test · **Area:** backend
- **Status:** in-review · **Depends on:** — · **Feature:** F-04 · **Stories:** US-04xx
- **Branch:** `test/HUB-238-transform-budget-flake`

During the HUB-230 `make itest` run, `TestCutoverCompletedAndNewRequests/transformer` failed once: the publish check ran goldens for `R2@1` (`fixtures/partial`), which took 8.08 ms against the 5 ms transform time budget under `-race` on a loaded machine. It passed 5/5 in isolation. The publish-time golden run should not be sensitive to scheduler load.

**Acceptance criteria**
- [x] Publish-time goldens either measure CPU time, apply the budget only outside `-race`, or retry once before failing on time alone: RunGoldens re-runs a case once on ErrTimeBudget (`TestGoldensRetryTimeBudgetOnce`)
- [ ] `make itest` passes 10 consecutive full runs on CI hardware

### HUB-245 — Hashed subject index for the erasure job
- **Epic:** EP-09 · **Type:** perf · **Area:** backend
- **Status:** todo · **Depends on:** HUB-233 · **Feature:** F-18 · **Stories:** US-1805
- **Branch:** `perf/HUB-245-subject-index`

`hub erase-subject` (HUB-233, ADR-0012) finds a subject's requests by scanning every event of the tenant in the window and opening every sealed payload, which is linear in the tenant's history. Record a keyed hash of each `x-pii` identifier value (HMAC with the erase-ref domain label, per tenant) per request, off the hot path: in the audit writer batch, not in the request goroutine. Store it in a day-partitioned `request_subjects` table owned by `audit`, so the job reads only the subject's request ids. The full scan stays as a `-scan` fallback for rows written before the index existed.

**Acceptance criteria**
- [ ] The erasure reads only the subject's rows when the index covers the window (EXPLAIN in an integration test)
- [ ] No per-request work on the sync path beyond the existing audit batch (benchmark unchanged within noise)
- [ ] HUB-233 grep test still passes with the index

### HUB-246 — Purge idempotency replay records of erased requests
- **Epic:** EP-09 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-233 · **Feature:** F-18 · **Stories:** US-1805
- **Branch:** `feat/HUB-246-erase-idempotency-records`

Redis idempotency records keep the replayable 200/202 body for 24 h (F-10). `hub erase-subject` (HUB-233) does not touch them: `Release` would let a retry with the same key run fresh, and for `moves_money_or_quota` integrations that can double-charge. Replace the stored body of an erased request's record with a tombstone that still answers the key, for example a 409 or a body-less replay. Keep the key reserved until its TTL, and make the change through an idempotency port that the erasure command calls after its transaction commits.

**Acceptance criteria**
- [ ] After erasure, the Redis record of an erased request holds no body, and a retry with the same key does not reach the vendor
- [ ] Decision on the replay answer recorded (ADR or F-10 update) and, if it changes the caller response, a CCR first

### HUB-247 — CCR: admin operation for subject erasure
- **Epic:** EP-09 · **Type:** contract · **Area:** contracts
- **Status:** todo · **Depends on:** HUB-233 · **Feature:** F-18 · **Stories:** US-1805
- **Branch:** `contract/HUB-247-erase-subject`

HUB-233 shipped the erasure as the `hub erase-subject` maintenance command because `hub-admin.v1.yaml` has no such operation (ADR-0012). An admin operation gives it RBAC, a console screen and the same `change_log` row.

### CCR: Subject erasure operation
- **Requested by**: backend-engineer (ticket HUB-233)
- **Why**: US-1805, F-18, DESIGN §16.5. Compliance must purge a data subject's stored payloads without shell access to a node, under admin RBAC.
- **Document**: hub-admin.v1.yaml
- **Operation / schema**: `eraseSubjectPayloads`, `POST /subjects/erase` (role `admin`; the identifier travels in the body, never in the URL or query, so access logs never see it)
- **Before**:
  ```yaml
  # does not exist
  ```
- **After**:
  ```yaml
  /subjects/erase:
    post:
      operationId: eraseSubjectPayloads
      tags: [requests]
      summary: Purge a data subject's stored payloads (audited)
      description: |
        admin only. Purges every stored body of the tenant's requests that hold the identifier
        (any stored form: raw, masked HMAC, inside sealed payloads), keeping hashes, sizes,
        timelines and metadata (DESIGN §13.3, §16.5). Requests still queued or with callbacks to
        deliver are deferred and listed; call again once they are terminal. One change_log row
        (action eraseSubjectPayloads) unless dry_run. The identifier is never stored or logged.
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/SubjectErasureRequest' }
            example: { tenant_id: lending-app, subject: ABCDE1234F, reason: "Erasure request PRIV-42", dry_run: false }
      responses:
        '200':
          description: Erasure done (possibly with deferred requests).
          content:
            application/json:
              schema: { $ref: '#/components/schemas/SubjectErasureResult' }
              example:
                change_id: chg_01JC0000000000000000000000
                subject_ref: subject:8c4dd35326648b733a76c00b7a83bf38
                tenant_id: lending-app
                dry_run: false
                erased_request_ids: [req_01JC0000000000000000000001]
                deferred_request_ids: []
                counts: { events: 6, results: 1, dead_letters: 0, callbacks: 1, sessions: 0 }
        '400': { $ref: '#/components/responses/ValidationFailed' }
        '401': { $ref: '#/components/responses/Unauthorized' }
        '403': { $ref: '#/components/responses/Forbidden' }
        '409': { description: 'CONFLICT: sealed payloads exist but no seal key is configured; nothing changed.' }
  components:
    schemas:
      SubjectErasureRequest:
        type: object
        required: [tenant_id, subject, reason]
        properties:
          tenant_id: { type: string }
          subject: { type: string, minLength: 3, maxLength: 256, description: 'The identifier. Never echoed back.' }
          reason: { type: string, minLength: 10, maxLength: 1024 }
          since: { type: [string, 'null'], format: date-time }
          until: { type: [string, 'null'], format: date-time }
          dry_run: { type: boolean, default: false }
      SubjectErasureResult:
        type: object
        required: [change_id, subject_ref, tenant_id, dry_run, erased_request_ids, deferred_request_ids, counts]
        properties:
          change_id: { type: [string, 'null'], description: 'null for a dry run' }
          subject_ref: { type: string, description: 'HMAC reference recorded in change_log; not a stored form of the identifier.' }
          tenant_id: { type: string }
          dry_run: { type: boolean }
          erased_request_ids: { type: array, items: { $ref: '#/components/schemas/RequestIdValue' } }
          deferred_request_ids: { type: array, items: { $ref: '#/components/schemas/RequestIdValue' } }
          counts:
            type: object
            required: [events, results, dead_letters, callbacks, sessions]
            properties:
              events: { type: integer, minimum: 0 }
              results: { type: integer, minimum: 0 }
              dead_letters: { type: integer, minimum: 0 }
              callbacks: { type: integer, minimum: 0 }
              sessions: { type: integer, minimum: 0 }
  # Optional, additive: add `subject` to ObjectType if change_log rows should name the subject
  # reference as their object (today: object_type request, object_id = id | bulk | none).
  ```
- **Breaking?**: no (additive). `info.version` 1.1.0 → 1.2.0
- **Consumers affected**: backend (`internal/control/adminapi` handler over `control.Erasure`), frontend (optional compliance screen), hubmcp (none: admin tools stay metadata-only, SECURITY §2.5)
- **Error codes**: VALIDATION_FAILED, UNAUTHORIZED, FORBIDDEN, CONFLICT (no seal key), INTERNAL_ERROR; no new code
- **Rollout**: contract PR first; the handler reuses `control.Erasure` unchanged

**Acceptance criteria**
- [ ] Steward reviewed and merged the contract change (or rejected with reasons)
- [ ] Backend handler + contract test; RBAC table row added (SECURITY §7)
### HUB-241 — Attempt-level and DLQ counters for the auth, transform and DLQ alerts
- **Epic:** EP-09 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-232 · **Feature:** F-18 · **Stories:** US-1801
- **Branch:** `feat/HUB-241-attempt-dlq-counters`

OBSERVABILITY §3.3 plans `vendor_attempts_total{vendor,account,endpoint,code}` (delivery, one per attempt) and `dlq_entries_total{account,reason}` (queue, one per dead-letter write); neither is exported, so HUB-232 reads `hub_requests_total` and the `dlq_count` gauge (ADR-0014). Add both counters, then switch `UpstreamAuthFailed`, `TransformFailed` and `DLQSurge` to them and supersede ADR-0014.

**Acceptance criteria**
- [ ] Both counters registered and pinned in the metrics catalogue test; incremented exactly once per attempt / dead-letter write (tests)
- [ ] The three rules and their promtool tests use the counters; the catalogue check passes
- [ ] OBSERVABILITY §3.3 and §11.1 updated; ADR-0014 superseded

### HUB-242 — Clear the parked gauges when the scheduler loses leadership
- **Epic:** EP-09 · **Type:** fix · **Area:** backend
- **Status:** todo · **Depends on:** — · **Feature:** F-13 · **Stories:** US-1801
- **Branch:** `fix/HUB-242-parked-gauges-leadership`

`delivery.Resilience` sets `parked_count` and `parked_oldest_age_s` on each leader tick but nothing resets them when leadership is lost (`Scheduler.lead` clears only the `QueueSampler`), so a former leader keeps exporting its last values. HUB-232 works around it in `ParkedOldestAgeHigh` and the dashboards with `and on (instance) (scheduler_leader == 1)`.

**Acceptance criteria**
- [ ] A scheduler that stops leading exports no `parked_*` series (test)
- [ ] The leader filter can be dropped from the rule and dashboards (promtool test for the former-leader case still passes)

### HUB-243 — Grafana import check for the dashboards in CI
- **Epic:** EP-09 · **Type:** chore · **Area:** infra
- **Status:** todo · **Depends on:** HUB-232 · **Feature:** F-18 · **Stories:** US-1804
- **Branch:** `chore/HUB-243-grafana-import-check`

HUB-232 lints the dashboards and checks their queries against the exported metrics, but does not import them into Grafana (not available where HUB-232 was built). Start a pinned Grafana image as a CI service, provision a Prometheus data source, import each `deploy/dashboards/*.json` through the HTTP API and fail on any error.

**Acceptance criteria**
- [ ] CI job imports all three dashboards into a pinned Grafana and fails on a non-200 or a dashboard with panel errors
- [ ] HUB-232's second acceptance criterion can be ticked

### HUB-244 — Additional alert rules (OBSERVABILITY §11.2)
- **Epic:** EP-09 · **Type:** chore · **Area:** infra
- **Status:** todo · **Depends on:** HUB-232 · **Feature:** F-18 · **Stories:** US-1801
- **Branch:** `chore/HUB-244-extra-alerts`

Rules beyond DESIGN §16.3 planned in OBSERVABILITY §11.2 (`SchedulerLeaderMissing`, `SnapshotApplyFailing`, `CredsCacheMiss`, `HeapNearLimit`, `GoroutinesGrowing`, `AuditBufferFilling`, `CallbackFailuresHigh`, availability burn rates). Rules whose metric is not exported (`ZeroLossViolation` → `hub_accepted_unaccounted`, `AcceptP99High` → `hub_accept_ms`) wait for those metrics.

**Acceptance criteria**
- [ ] Each rule whose metrics exist has a promtool test with a firing and a non-firing case and passes the catalogue check
- [ ] OBSERVABILITY §11.2 marks which rules shipped and which wait for a metric

## EP-10 — Admin console (SvelteKit)


### HUB-239 — Registry watch test reads a half-written config file
- **Epic:** EP-09 · **Type:** test · **Area:** backend
- **Status:** in-review · **Depends on:** — · **Feature:** F-03 · **Stories:** US-03xx
- **Branch:** `test/HUB-238-timing-flakes`

`TestWatchSwapsValidEditsAndKeepsLastGoodOnInvalid` failed on `main` (run 41415977028) with `accounts/ct-brandA-in.yaml: EOF`: the test edits files with `os.WriteFile`, which truncates before writing, and the 50 ms watcher sometimes read the empty file. The product behaviour is right (an unreadable edit is rejected and the last good snapshot keeps serving); the test raced its own write.

**Acceptance criteria**
- [x] The test writes config edits atomically (temp file outside the watched tree + rename), as editors do
- [x] 20 consecutive `-race` runs pass on a loaded machine
### HUB-250 — Scaffold SvelteKit admin console
- **Epic:** EP-10 · **Type:** chore · **Area:** frontend
- **Status:** done · **Depends on:** HUB-012 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `chore/HUB-250-fe-scaffold`

Create `frontend/` with SvelteKit, Svelte 5 runes, TypeScript strict, ESLint/Prettier, Vitest, Playwright, and npm scripts `dev build check test test:e2e`; CI job for the frontend.

**Acceptance criteria**
- [x] `npm run check && npm test` green on the scaffold
- [x] CI runs check, unit and build for `frontend/**` changes
- [x] Svelte 5 runes mode enforced

### HUB-251 — Generated API client (gen:api)
- **Epic:** EP-10 · **Type:** chore · **Area:** frontend
- **Status:** done · **Depends on:** HUB-250, HUB-006 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `chore/HUB-251-fe-gen-api`

`npm run gen:api` generates types from `contracts/openapi/hub-admin.v1.yaml` with openapi-typescript and wraps openapi-fetch in a typed client with auth and error-envelope handling (AGENTS §4.5).

**Acceptance criteria**
- [x] Generated types committed; CI fails if regeneration produces a diff
- [x] No hand-written API types in `src/` (lint rule or grep check)
- [x] Client surfaces the error envelope as a typed error

### HUB-252 — MSW mocks from the contract
- **Epic:** EP-10 · **Type:** test · **Area:** frontend
- **Status:** done · **Depends on:** HUB-251 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `test/HUB-252-fe-msw-mocks`

MSW handlers and typed fixtures for every admin operation so the console is built in parallel with the backend; dev server can run fully on mocks.

**Acceptance criteria**
- [x] Every admin operation has a handler typed by the generated client
- [x] `npm run dev:mock` runs the console without a backend
- [x] Fixtures validated against the contract in a unit test

### HUB-253 — Auth: OIDC stub login and /me
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-252 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-253-fe-auth`

Login via the dev OIDC stub, session handling, `/me` fetch and role-aware guards for `admin | editor | viewer | tenant_viewer`.

Shipped (frontend-engineer): no OIDC stub exists yet, so auth sits behind an `AuthProvider` interface with a dev provider (role picker; mock builds or `PUBLIC_AUTH_MODE=dev`) and a config-wired OIDC placeholder; real OIDC is HUB-282. Session is an HMAC-signed httpOnly cookie.

**Acceptance criteria**
- [x] Unauthenticated users redirected to login (unit: `(app)/+layout.server.ts`; e2e: `e2e/auth.test.ts`)
- [x] Controls for mutations hidden/disabled for `viewer` (`RoleGate` + `access`, component-tested per role; also hidden for `tenant_viewer` and when `/me` fails. No mutation control exists yet: HUB-259/260 must use `RoleGate`)
- [x] Unit tests for the guard per role (`src/lib/auth/roles.test.ts`, `RoleGate.test.ts`)

### HUB-254 — Layout and navigation
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-250 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-254-fe-layout-nav`

App shell with sidebar navigation (Integrations, Health, Requests, DLQ, Transformers, Tenants, Snapshots, Change log), breadcrumbs, user menu and light/dark theme.

**Acceptance criteria**
- [x] Every section reachable by keyboard
- [x] Active route highlighted; layout works at 1280 px and 768 px

### HUB-255 — Error, empty and loading states
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-251, HUB-254 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-255-fe-states`

Shared components for loading skeletons, empty states and error envelopes (code, message, request_id, retryable) used by every page.

**Acceptance criteria**
- [x] Every list/detail page uses the shared components (checked in review + test: `states.test.ts` scans every `(app)` page and both `+error.svelte`)
- [x] Error component shows `request_id` with copy action
- [x] Component tests for each state (`states.test.ts`, `shell.test.ts`, `sections.test.ts`)

### HUB-256 — Integrations list
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-253, HUB-255 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-256-fe-integrations-list`

Searchable, filterable list of integrations (tenant, capability, paused, active version) with pagination.

**Acceptance criteria**
- [x] Filters reflected in the URL (search, tenant, capability, paused, vendor, account, page size, cursor; the active-version filter waits for CCR-002 / HUB-283)
- [x] Paused integrations visibly marked ("Paused" badge, text + icon; also "No active version" and "draft")
- [x] Component tests against MSW (`routes/(app)/integrations/page.test.ts`, `filters.test.ts`)

### HUB-257 — Integration detail and version history
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-256 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-257-fe-integration-detail`

Detail page with active version spec (targets, weights, failover, delivery, breaker, webhooks, audit) and version history with author, reason, date.

**Acceptance criteria**
- [x] History shows every version newest first (follows `next_cursor` across pages, sorted by version)
- [x] Active version clearly marked ("Active" badge in text + icon, and the "Active version vN" section)
- [x] Component tests against MSW (`routes/(app)/integrations/[integration_id]/page.test.ts`)

### HUB-258 — Version diff view
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-257 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-258-fe-version-diff`

Compare any two versions using the admin diff endpoint, with side-by-side and path-list views.

**Acceptance criteria**
- [x] Changed, added and removed paths highlighted (symbol + text label + colour, side by side and in the path list)
- [x] Diff selectable from history rows (From/To radios + "Compare selected versions", and a per-row "vs vN" link to the version it was based on)

### HUB-259 — Publish and rollback flows with mandatory reason
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-257 · **Feature:** F-19 · **Stories:** US-1902
- **Branch:** `feat/HUB-259-fe-publish-rollback`

Publish a new version (spec editor with validation) and rollback-as-new-version, both requiring a reason and confirming in a dialog; path-qualified validation errors shown inline.

Shipped (frontend-engineer): publish page `/integrations/{id}/publish` (YAML/JSON spec editor; save creates or replaces the draft and runs `validateIntegrationVersion`; diff against the active version; publish in `ConfirmDialog` with `expected_active_version`) and "Roll back to vN" on published history rows (`expected_active_version`, 409 shows a reload prompt). Mutations are SvelteKit form actions with `use:enhance`; mock builds answer them server side (`hooks.server.ts` `handleFetch`, `hub_mock_scenario` cookie).

**Acceptance criteria**
- [x] Submit disabled until a reason is entered (`ConfirmDialog`: at least 10 characters, typed confirmation optional; the action re-checks it before calling the API; `ConfirmDialog.test.ts`, `e2e/publish-rollback.test.ts`)
- [x] Validation errors mapped to fields (400 `/reason` to the reason field, `/spec/...` and 422 `PUBLISH_VALIDATION_FAILED` pointers to spec editor lines with "Go to line", YAML syntax errors with their line; `publish/page.test.ts`)
- [x] Success shows the new version number ("Published v8 ... (was v7)", "Rolled back: v9 is now active, a copy of v7", with the change log id; unit + e2e)

### HUB-260 — Weights and pause flows with mandatory reason
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-257 · **Feature:** F-19 · **Stories:** US-1902
- **Branch:** `feat/HUB-260-fe-weights-pause`

Edit target weights (must total 100) and pause/resume an integration, each with mandatory reason and confirmation.

Shipped (frontend-engineer): "Set weights" on the integration page (shown when the active version has two or more targets) opens a weights dialog with the current weights, a live total and "Publish vN now" (default on; off creates a draft); `updateIntegrationWeights` with `expected_active_version`. A separate "Kill switch" group holds Pause / Resume (`pauseIntegration` / `resumeIntegration`); the contract gives neither a precondition nor an ETag, so no 412 path exists for them.

**Acceptance criteria**
- [x] Weights not summing to 100 cannot be submitted (live total, confirm disabled; the action re-checks and answers 400 without calling the API; `weights.test.ts`, `weights-pause.test.ts`, `e2e/weights-pause.test.ts`)
- [x] Pause shows impact text and requires reason (503 `INTEGRATION_PAUSED` within about 10 s, nothing new parked, workers hold queued requests, other integrations unaffected; resume names the pause reason)
- [x] Component tests against MSW (success, 400 with pointers, 403, 404, 409, 422 for weights; 400, 403, 404 for pause; 403 for resume; viewer and tenant_viewer see no controls)

### HUB-261 — Vendor and account health board
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1903
- **Branch:** `feat/HUB-261-fe-health-board`

Board of vendors/accounts with breaker state, error rate, parked count/age, DLQ count and lifecycle state, auto-refreshing.

Shipped (frontend-engineer): `/health` is a board of every breaker (`listBreakers`, all pages) joined with the account lifecycle state (`listAccounts`, all pages), grouped by vendor, worst first; state with "retry in N s" / "probe due" / forced hold / open since, failure rate, calls in window, parked count and oldest age, lifecycle, last update. Summary line announced politely; a failed refresh keeps the last board marked stale; tenant viewers get a note instead. **DLQ count is not shown**: no admin operation returns one (CCR-003, HUB-284).

**Acceptance criteria**
- [x] Open breakers sorted first and visually distinct (not color only): "Open" text badge with icon, a thick left rule and bold id on open rows, dashed rule on half-open; groups and rows ordered open, half-open, closed, then parked count and age (`board.test.ts`, `health/page.test.ts`, `e2e/health.test.ts`)
- [x] Auto-refresh interval configurable and pausable (5/10/30/60 s, default 5 s per F-19; Pause / Resume, Refresh now, "Updated N s ago"; skips ticks while the tab is hidden; fake-timer unit test and `page.clock` e2e)

### HUB-262 — Breaker force controls
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-261 · **Feature:** F-19 · **Stories:** US-1903
- **Branch:** `feat/HUB-262-fe-breaker-force`

Force-open / force-close per `(account, endpoint)` from the health board with mandatory reason, `admin` only.

Shipped (frontend-engineer): a "Force" column on the health board for admins (`RoleGate min="admin"`, contract `x-required-role: admin`): Force open, Force closed, and Clear force on forced breakers. Force open/closed need a reason, the account id typed to confirm (checked again server side) and a hold (15 min to 24 h, or until cleared; default 30 min). Auto-refresh holds while a dialog is open. The contract gives these operations no precondition (last write wins); clear-force 409 shows the reload prompt.

**Acceptance criteria**
- [x] Controls absent for non-admin roles (editor, viewer: no Force column or buttons; tenant viewers see no board; unit per role and e2e)
- [x] Confirmation dialog states the affected integrations (`getBreaker` `integrations` loaded when the dialog opens: "Affects 1 integration: int_evt_brandA"; a warning with code and request id if that call fails; force closed also states how many parked requests will drain)

### HUB-263 — Request explorer search
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1901
- **Branch:** `feat/HUB-263-fe-request-search`

Search requests by tenant, integration, status, code, time range and idempotency key with cursor pagination.

Shipped (frontend-engineer): `/requests` search form for tenant (a select of own tenants for tenant viewers), integration, status (checkboxes, any of), error codes, vendor and account (not for tenant viewers), idempotency key, From/To in UTC with quick ranges (last hour, 24 hours, 7 days as absolute times) and page size; `status` and `code` sent comma-separated as the contract's form/explode=false; a range over 31 days or reversed is refused before calling the API; cursor paging with Previous/Next.

**Acceptance criteria**
- [x] Search params in URL, shareable (names match `searchRequests`; removable chips and Clear all; reload restores the form; paging keeps the filters; `filters.test.ts`, `requests/page.test.ts`, `e2e/requests.test.ts`)
- [x] Empty and error states covered ("No requests yet" vs "No requests match this search" with Clear all filters; API errors with code, pointer details and request id; loading skeleton; the 31-day range problem shown on the time fields)

### HUB-264 — Request timeline
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-263 · **Feature:** F-19 · **Stories:** US-1901
- **Branch:** `feat/HUB-264-fe-request-timeline`

Timeline view of events and attempts with timings, versions, hashes/sizes and payload viewer that respects payload policy.

Shipped (frontend-engineer): `/requests/{request_id}` (linked from the explorer) loads `getRequest`, `getRequestTimeline` and `listRequestAttempts` in parallel: summary (status + code, tenant, integration + final version, capability, mode, vendor/account, timings, idempotency key, audit policy, snapshot, trace id, result/error per policy), the DESIGN §13.1 events and the attempts table. Payloads render as the API returns them (masked stays masked) behind a disclosure, with secret-looking fields that are not already masked hidden as a second line of defence; sealed shows a locked placeholder with size and SHA-256; metadata-only says no body was stored; truncated events name their object-storage reference. Unsealing stays with HUB-265.

**Acceptance criteria**
- [x] Events shown in seq order with durations (sorted by `seq` whatever the API order; "+212 ms" since the previous event and "at 1,999 ms" since the first; `timeline.test.ts`, `requests/[request_id]/page.test.ts`, `e2e/request-timeline.test.ts`)
- [x] Sealed payloads show a locked placeholder (lock icon, "Sealed payload. Encrypted at rest, 214 B", SHA-256 prefix; no payload in the DOM; also for the sealed result)
- [x] Attempt rows show integration version and account (plus endpoint, served by, start, duration, vendor status, error, breaker state, credential version; tenant viewers see the version but not vendor/account, per F-19 rule 5)

### HUB-265 — Unseal flow
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-264 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-265-fe-unseal`

Unseal a sealed payload with mandatory reason (`admin` only); unsealed content shown in-session only and never cached.

**Acceptance criteria**
- [ ] Reason required; action logged message shown
- [ ] Unsealed content cleared on navigation (test)

### HUB-266 — DLQ list
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1904
- **Branch:** `feat/HUB-266-fe-dlq-list`

DLQ list with filters (tenant, integration, account, reason, time range) and entry detail (last vendor request/response, attempt history).

Shipped (frontend-engineer, `feat(frontend): add DLQ list with URL filters and entry detail (HUB-266)`): `/dlq` on `listDeadLetters` with tenant (own tenants for tenant viewers), integration, vendor and account (not for tenant viewers), reason (code or `poison`, with suggestions), replayed, From/To in UTC (quick ranges up to 14 days; a reversed range is refused before calling) and page size; cursor paging. `/dlq/{request_id}` on `getDeadLetter`: reason and message first, replay state, link to the request timeline, attempt history (shared `AttemptsTable`, attempt order), canonical request and last vendor request/response as returned (`***` stays, secret-looking unmasked fields hidden); tenant viewers see no vendor, account or vendor exchange.

**Acceptance criteria**
- [x] Filters in URL (names match `listDeadLetters`; chips and Clear all; reload restores the form; the health board's "Dead letters" link opens `/dlq?account_id=`; `dlq/filters.test.ts`, `dlq/page.test.ts`, `e2e/dlq.test.ts`)
- [x] Entry detail shows the reason and attempt history (reason code and message in a highlighted block; attempts sorted by `attempt_no` with version, account, endpoint, vendor status, error, breaker; `dlq/[request_id]/page.test.ts`, e2e)

### HUB-267 — DLQ replay by filter and purge
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-266 · **Feature:** F-19 · **Stories:** US-1904
- **Branch:** `feat/HUB-267-fe-dlq-replay-purge`

Replay all entries matching the current filter, and purge, each showing the affected count and requiring a reason.

Shipped (frontend-engineer, `feat(frontend): add DLQ replay and purge by filter with exact counts (HUB-267)`): an "Act on every match" box on `/dlq` with "Replay matching..." (editor, `replayDeadLetters`) and, set apart, "Purge matching..." (admin, `purgeDeadLetters`). Each first runs the operation with `dry_run: true` and the list's URL filters (as `DlqFilter`, all pages), then opens `ConfirmDialog` with that count in the title and body, the filters used, and warnings (filters that include already-replayed entries; more than 10,000 matches per call). Confirm sends the filter, the operator's reason and the counted number; success shows the result, change log id and request id, and for replay a link to the replayed entries (`replayed=true`). 0 matches says there is nothing to do without a dialog. The contract requires `reason` on dry runs too, so the count step sends a fixed console text (no change log entry: dry runs have no `change_id`). US-1904's "reports replayed ids" is met by that link: `DlqBulkResult` returns counts, not ids.

**Acceptance criteria**
- [x] Confirmation shows exact count from the API before submit (the dry run's `matched`, capped at the per-call 10,000 with a note; `dlq/replay-purge.test.ts`, `dlq/bulk.test.ts`, `e2e/dlq.test.ts`)
- [x] Purge requires typing the count to confirm (checked again in the action, which also re-counts with a dry run and refuses the purge when the selection changed since it was counted)

### HUB-268 — Transformers viewer and goldens
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1905
- **Branch:** `feat/HUB-268-fe-transformers`

List transformers and versions, view YAML with syntax highlighting, and run goldens with per-fixture results and diffs.

Shipped (frontend-engineer, `feat(frontend): add transformer viewer with highlighted source and goldens (HUB-268)`): `/transformers` with kind, capability, vendor and search filters in the URL (chips, cursor paging); `/transformers/{id}` on `getTransformer` + `listTransformerVersions` (all pages): header, the latest version in full and the version history (goldens passing/failing in text, linked); `/transformers/{id}/versions/{n}` on `getTransformerVersion`. Each version shows author, reason, created, the cited vendor doc, the stored goldens summary, the DSL source (`CodeView`: line numbers and colours from a small text-only YAML tokenizer, no HTML), and the fixtures. Editors get "Run goldens for T1@7" (`runTransformerGoldens` with `ref`, a form action); viewers see why not; tenant viewers get a note (the API needs `viewer`). Read only: editing is Release 2.

**Acceptance criteria**
- [x] Golden diffs rendered per fixture (summary line, failing fixtures first, Passed/Failed in text + icon, the error when a fixture threw, its `diff` as a path table headed Expected / Actual via the shared `DiffPaths`; `[transformer_id]/page.test.ts`, `e2e/transformers.test.ts`)
- [x] Doc URL from the transformer header shown as a link (the version's `docs_url`, from `getTransformer`'s `latest` on the transformer page; opens in a new tab with `noopener`; only http(s) URLs become links)

### HUB-269 — Transformer dry-run
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-268 · **Feature:** F-19 · **Stories:** US-1905
- **Branch:** `feat/HUB-269-fe-transformer-dry-run`

Dry-run a transformer version with a pasted canonical body and show the resulting vendor request (secrets redacted).

Shipped (frontend-engineer, `feat(frontend): add transformer dry-run with client-side JSON checks (HUB-269)`): a "Dry run" section on every transformer version (editor; viewers see why not) posting `dryRunTransformer` with `ref` through the `dryRun` form action. Request and inbound transformers take a canonical `input` (plus optional caller `headers`), response transformers a `vendor_response` (`status`, `headers`, `body`); the body starts from the first fixture that has one, any fixture can be loaded, and an optional account id injects that account's config. Results show compile errors with their line, the vendor request, or the canonical response / error. Secrets are shown exactly as the API redacted them (`***`), values under secret-looking keys that arrive unmasked are hidden, and nothing is logged or stored.

**Acceptance criteria**
- [x] Invalid JSON input caught client-side (`$lib/utils/dry-run.ts` + `json-input.ts` on submit: syntax errors with line and column, non-objects, empty input, non-string headers, a bad account id; the field is marked invalid and focused and no request is sent; the form action repeats the checks for the no-JS path; `dry-run.test.ts`, `json-input.test.ts`, `e2e/transformers.test.ts`)
- [x] Result rendered as method, URL, headers, body (method and URL lines, a headers table, the body as JSON; API 400 pointers land on the field)

### HUB-270 — Tenants pages
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1906
- **Branch:** `feat/HUB-270-fe-tenants`

List, create and edit tenants (rate limits, grants), with mandatory reason on mutations.

Shipped (frontend-engineer, `feat(frontend): add tenant pages with create, edit and disable (HUB-270)`): `/tenants` (`listTenants`, search `q` and paging in the URL; "Create tenant" for admins, `createTenant`) and `/tenants/{id}` (`getTenant`: display name, description, contact, rate limit, disabled, integrations owned (linked to the integrations list), active keys; admins edit with `updateTenant` and disable / enable it, disable with the id typed). Edits are merge-patches with only the changed fields and send the loaded ETag as `If-Match`, so a concurrent change answers 412 with a Reload prompt (`client.ts` now returns `etag` on successes). Grants are not a tenant field in hub-admin: they live on each integration (`Integration.grants`, `updateIntegration`), so the tenant page links its integrations and says so instead of editing grants.

**Acceptance criteria**
- [x] Create/edit forms validated per contract (`tenants/tenant-form.ts` against `TenantCreate` / `TenantUpdate`: `Slug` id, display name required and at most 128, `rps` number >= 0, `burst` integer >= 0, empty description / contact sent as null; checked in the dialog (Confirm disabled, problem on the field) and again in the form actions; API `VALIDATION_FAILED` pointers land on their field; `tenant-form.test.ts`, `page.test.ts`, `[tenant_id]/page.test.ts`, `e2e/tenants.test.ts`)
- [x] `tenant_viewer` sees only its tenant (`listTenants` needs viewer, so the list reads each of the viewer's own tenants with `getTenant`; another tenant's page answers 404 without a call; no create, edit or disable controls; unit and e2e tested)

### HUB-271 — API keys with show-once
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-270 · **Feature:** F-19 · **Stories:** US-1906
- **Branch:** `feat/HUB-271-fe-api-keys`

Create, rotate (with overlap) and revoke tenant API keys; plaintext shown exactly once with copy action and never stored.

Shipped (frontend-engineer, `feat(frontend): add API keys with the secret shown exactly once (HUB-271)`): an "API keys" section on `/tenants/{id}` (`listApiKeys` for the tenant, all pages, state filter `key_state` in the URL): prefix, state in text, scopes, created, expires, last used, replaced by; never a secret. Admins create (`createApiKey`: scopes `invoke` / `dry_run` / `requests_read`, optionally narrowed to one integration, description, UTC expiry), rotate (`rotateApiKey`, overlap none / 1 h / 24 h / 7 d / 30 d) and revoke (`revokeApiKey`, prefix typed). The new secret is shown once in `SecretOnce` (read-only field, Copy with a polite "Copied", "This is the only time it is shown" warning; Escape does not close it, Done needs "I have stored it" ticked). Show-once is enforced by: `ConfirmDialog oneTimeSecret` (the action answer goes to the reveal only, page data reloads with `invalidateAll`, never applied as the page's `form`); `createKey` / `rotateKey` refuse non-enhanced posts (`requireEnhanced`, no full HTML page with the key in the browser history); the notice carries only the change and request ids; nothing logged, stored or put in a URL.

**Acceptance criteria**
- [x] Key plaintext not present in any store or after dialog close (test) (`api-keys.test.ts`: after Done the secret is in no DOM node, input value, local/session storage, URL, `history.state` or console call, `update()` was never called so it never became page `form`, nothing re-displays it; `e2e/api-keys.test.ts` repeats the checks in Chromium after Done, after reload and after Back/Forward, plus console messages and request URLs)
- [x] Rotation shows overlap expiry (the reveal and the notice say until when the old key works, from `old_key.expires_at`, with the overlap; unit and e2e)

### HUB-272 — Registrar endpoints
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-270 · **Feature:** F-19 · **Stories:** US-1906
- **Branch:** `feat/HUB-272-fe-registrar`

Manage registrar endpoints per tenant (URL, secret rotation), surfacing SSRF validation errors.

Shipped (frontend-engineer, `feat(frontend): add tenant callback endpoints with write-only secrets (HUB-272)`): a "Callback endpoints" section on `/tenants/{id}` (`listRegistrarEndpoints` for the tenant, all pages): id, enabled / disabled in text, URL and description, signing secret version (and the overlap while two secrets sign), subscribed versions, updated. Admins register (`createRegistrarEndpoint`: `Slug` id, `https://` URL checked in the browser and the action), edit URL / description (`updateRegistrarEndpoint`, only the changes), disable / enable, rotate the signing secret with an overlap (`rotateRegistrarSecret`) and delete (`deleteRegistrarEndpoint`, id typed, reason as query parameter, 409 while versions subscribe). The new signing secret from register / rotate is shown exactly once in `SecretOnce` (lead decision 2026-11-01: the API returns it only there and the tenant needs it to verify `X-Hub-Signature`, so "write-only in the UI" means never displayed again; follow-up `feat(frontend): show the callback signing secret once on create and rotate (HUB-272)`). Edits send no `If-Match`: the list returns no per-endpoint ETag (optional in the contract). Asked the steward for an SSRF error example (CCR-004).

**Acceptance criteria**
- [x] Private-range URL error from API shown on the field (`VALIDATION_FAILED` pointer `/url` -> the URL field, marked invalid and described by the message, on register and edit; mock scenario `registrar-url-private` = the contract 400 envelope with pointer `/url`; `callback-endpoints.test.ts`, `e2e/registrar.test.ts`)
- [x] Secret is write-only in the UI: never displayed again (no read returns it and the list shows only the version; the new secret from register / rotate is shown once in `SecretOnce` with the API-key guarantees: `oneTimeSecret` (never page `form`), `requireEnhanced` on both actions, dropped on Done; `callback-endpoints.test.ts` checks DOM, inputs, storage, URL, `history.state` and console after Done, `e2e/registrar.test.ts` repeats it after reload and Back/Forward, plus console messages and request URLs, axe light and dark)

### HUB-273 — Snapshots view
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1906
- **Branch:** `feat/HUB-273-fe-snapshots`

List snapshots with version, checksum, author, diff summary and time.

Shipped (frontend-engineer, `feat(frontend): add snapshots view with latest marked and diff summary (HUB-273)`): `/snapshots` (`listSnapshots`, newest first, cursor paging in the URL): version, summary, author, change (linked to its change log entry), checksum (short, full value in the title and a copy button), size, created. `getLatestSnapshot` (`wait=0`, no long-poll) fills a "Latest snapshot" panel and marks its row. Each row has a "Show changes in N" disclosure that reads `getSnapshot` on first open and lists the changed objects versus the previous snapshot (created / updated / deleted, linked to their console page). The blob (`getSnapshotBlob`) is not offered: it is the nodes' internal format.

**Acceptance criteria**
- [x] Latest snapshot highlighted (a "Latest" badge in text on its row plus a summary panel, from `getLatestSnapshot`; the list still renders when that call fails; `snapshots/page.test.ts`, `e2e/snapshots.test.ts`)
- [x] Diff summary expandable (`aria-expanded` disclosure per row, `getSnapshot` read once on demand, error with request id inside the row, keyboard tested in e2e)

### HUB-274 — Change log view
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** done · **Depends on:** HUB-255, HUB-253 · **Feature:** F-19 · **Stories:** US-1906
- **Branch:** `feat/HUB-274-fe-change-log`

Filterable change log (actor, action, object, time) with before/after diff and reason.

Shipped (frontend-engineer, `feat(frontend): add filterable change log with before/after diff (HUB-274)`): `/change-log` (`listChangeLog`: actor, action (operationId, with suggestions), object type, object id, UTC From/To with quick ranges, page size; chips, Clear all, cursor paging; reversed range refused before calling): change id linked to its entry, time, actor with roles, action, object (linked to its console page for integrations, transformers, tenants and requests), reason, snapshot. `/change-log/{change_id}` (`getChangeLogEntry`): what, reason, when, actor (linked to their changes), snapshot, "every change to this object", and the before / after as a path diff (`$lib/utils/json-diff.ts` -> `DiffPaths`: Added / Removed / Changed with Before / After values; creates and deletes say so) plus both objects in full. Every change notice now links its own entry.

**Acceptance criteria**
- [x] Before/after rendered as a diff (`jsonDiff` of `before` / `after` -> DiffOps -> `DiffPaths`; table-driven `json-diff.test.ts`, `[change_id]/page.test.ts` on the contract example, `e2e/change-log.test.ts`)
- [x] Filters in URL (`change-log/filters.ts`: names as in `listChangeLog`, validated, chips, survives reload; `filters.test.ts`, `page.test.ts`, e2e)

### HUB-275 — Accessibility pass
- **Epic:** EP-10 · **Type:** test · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-256, HUB-259, HUB-261, HUB-264, HUB-267, HUB-271 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `test/HUB-275-fe-a11y`

Audit all pages with axe and keyboard-only navigation; fix issues to WCAG 2.2 AA.

**Acceptance criteria**
- [ ] axe reports zero serious/critical violations on every route (Playwright + axe)
- [ ] All dialogs trap focus and restore it on close
- [ ] Status never conveyed by color alone

### HUB-276 — E2E suite on MSW
- **Epic:** EP-10 · **Type:** test · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-259, HUB-260, HUB-262, HUB-265, HUB-267, HUB-269, HUB-271, HUB-272, HUB-273, HUB-274 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `test/HUB-276-fe-e2e-msw`

Playwright suite covering the key flows (publish, rollback, pause, breaker force, unseal, DLQ replay, key create) against MSW.

**Acceptance criteria**
- [ ] Each listed flow has an e2e test
- [ ] Suite runs in CI in < 5 min

### HUB-277 — E2E against the real admin API
- **Epic:** EP-10 · **Type:** test · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-276, HUB-214 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `test/HUB-277-fe-e2e-real`

Run the e2e suite against a compose stack (hub admin + Postgres + Redis + mockvendor) once EP-08 is done.

**Acceptance criteria**
- [ ] All e2e tests pass against the real backend
- [ ] Any contract mismatch filed as a contract change request, not patched locally

### HUB-278 — CCR-001: declare X-Hub-Request-Id on every admin response
- **Epic:** EP-10 · **Type:** contract · **Area:** contracts
- **Status:** done · **Depends on:** HUB-006, HUB-251 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `contract/HUB-278-admin-request-id-header`

Filed by frontend-engineer during HUB-251. Full CCR text (before/after YAML): `frontend/CCR-NOTES.md` § CCR-001. `hub-public` declares `X-Hub-Request-Id` on every response but `hub-admin` does not, so the console cannot rely on it for successful calls or non-envelope failures, and backend contract tests do not require it. Additive, `hub-admin` 1.0.0 → 1.1.0.

**Acceptance criteria**
- [x] `components.headers.X-Hub-Request-Id` added and referenced by every `hub-admin` response; examples updated
- [x] `hub-admin` `info.version` 1.1.0 and `contracts/CHANGELOG.md` entry
- [x] Console removes the `TODO(CCR-001)` fallback after `npm run gen:api` (frontend-engineer, console round 4: `client.ts` documents the header as declared on every response, body `request_id` still wins on errors, an empty or missing header is `null`; success notices show the request id next to the change log id)
- [ ] Backend admin contract tests (EP-08) require the header

Steward note (2026-10-31): contract side shipped in hub-admin 1.1.0 (contract round 1, together with HUB-283 and HUB-284; notice `contracts/notices/2026-10-31-round-1.md`). The last two criteria belong to the frontend (`npm run gen:api`, remove `TODO(CCR-001)`) and the backend (EP-08 admin contract tests) and stay open until they land.

### HUB-279 — Evaluate SvelteKit 3 before form actions land
- **Epic:** EP-10 · **Type:** chore · **Area:** frontend
- **Status:** done · **Depends on:** HUB-250 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `chore/HUB-279-sveltekit-3-evaluation`

`npm audit` (2026-10-31) reports two advisories against SvelteKit 2.x: a redirect issue with enhanced form submission, and an XSS that only affects prerendered pages (the console prerenders nothing). npm's only fix is SvelteKit 3. Decide before the first form-action ticket (HUB-259+): upgrade, or document why 2.x is safe for this console. Re-check the ESLint/minimatch advisory chain at the same time.

**Acceptance criteria**
- [x] Advisories assessed against the console's actual use (form actions, prerendering), written up in F-19
- [x] Either upgraded to SvelteKit 3 with all checks green, or an ADR records why 2.x stays and until when (ADR-0009: stays on 2.70.3 with a `//` path guard; upgrade by 2027-01-31 at the latest, HUB-281)
- [x] `npm audit --omit=dev` has no unassessed high or critical findings (0 findings on 2026-10-31)

### HUB-280 — Run Playwright e2e in frontend CI
- **Epic:** EP-10 · **Type:** test · **Area:** infra
- **Status:** done · **Depends on:** HUB-250, HUB-254 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `test/HUB-280-fe-e2e-ci`

The e2e suite (axe a11y, keyboard, 768 px drawer, theme) runs only locally today. Add a CI job that builds with mocks (`build:mock`), installs the chromium headless shell with a cache, and runs `npm run test:e2e`, uploading the report on failure.

Shipped (frontend-engineer, `ci(frontend): run the Playwright e2e suite in frontend CI (HUB-280)`): job `e2e` in `.github/workflows/frontend.yml`, beside the existing `console` job (unchanged) and on the same triggers (`frontend/**`, the admin contract, the workflow). It installs with `npm ci`, reads the pinned `@playwright/test` version (exact in `package.json`, which pins the browser build), restores `~/.cache/ms-playwright` keyed by OS + that version, installs only the Chromium headless shell with its system packages on a miss (`install --with-deps --only-shell chromium`) or only the system packages on a hit, then runs `npm run test:e2e` (the config builds with `build:mock` and serves the preview). `timeout-minutes: 8`. On failure it uploads `playwright-report/` (HTML reporter added for CI) and `test-results/` (traces) for 14 days. The axe helper now waits until no view is refreshing (`aria-busy`), which removed a contrast false positive seen once in a full local run right after a mutation. Not run on GitHub yet: verified locally only, so the ticket stays `in-review` until the first PR run shows the cache and the time budget (see criteria).

**Acceptance criteria**
- [x] `frontend.yml` runs e2e on `frontend/**` PRs; a failing e2e test fails the PR (the job runs on the workflow's `pull_request` paths and `npm run test:e2e` exits 1 on a failing test, checked locally with a deliberate failure; making the check required is a branch-protection setting, not in the repo)
- [x] Browser binaries cached; job under 8 min (first GitHub run on PR #12, a cache miss that downloaded the browser: 5 min 01 s; later runs restore it from the cache; locally `CI=true npm run test:e2e` runs 99 tests in about 60 s including the mock build on 10 cores, 3 runs in a row green; not yet measured on a GitHub runner, which has fewer cores)
- [x] Playwright HTML report uploaded as an artifact on failure (`if: failure()` upload of `frontend/playwright-report/` and `frontend/test-results/`; the HTML report is produced under `CI`, checked locally)

### HUB-281 — Upgrade the console to SvelteKit 3
- **Epic:** EP-10 · **Type:** chore · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-279 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `chore/HUB-281-sveltekit-3`

Filed by frontend-engineer during HUB-279 (ADR-0009). Move to `@sveltejs/kit` 3 + `@sveltejs/adapter-node` 6 + TypeScript 6 once `openapi-typescript` (or a replacement) supports TypeScript 6, or by 2027-01-31 at the latest. Known migration steps from the HUB-279 trial: config from `svelte.config.js` into `sveltekit({...})` in `vite.config.ts` (also update `eslint.config.js` and `src/lib/runes-mode.test.ts`), `$lib` -> `#lib` subpath imports, `tsconfig.json` extends `$app/tsconfig`, `$env/*` and `$app/types` `Pathname` / `resolve()` changes. Update the skills and `frontend/AGENTS.md`, which say SvelteKit 2.

**Acceptance criteria**
- [ ] Upgraded without `overrides` or `--legacy-peer-deps`; lockfile committed
- [ ] `npm run check && npm run lint && npm test && npm run build` and `npm run test:e2e` green
- [ ] `npm audit` shows neither Kit advisory (GHSA-pfhg-q3qv-9q6r, GHSA-27mw-82g8-phjj); ADR-0009 marked superseded
- [ ] Skills, `frontend/AGENTS.md` and `frontend/README.md` say SvelteKit 3

### HUB-282 — Real OIDC sign-in for the console
- **Epic:** EP-10 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-253 · **Feature:** F-19 · **Stories:** US-19xx
- **Branch:** `feat/HUB-282-fe-oidc`

Filed by frontend-engineer during HUB-253. Replace the placeholder `oidcProvider` (`frontend/src/lib/server/auth/oidc.ts`) with Authorization Code + PKCE against the Hub issuer (`hub admin` OIDC stub, DESIGN §19 M7; contract `securitySchemes.oidc`). Needs the issuer to exist (backend M7) and a decision on how browser-side loads reach `/admin/v1` with the token: today only server-side fetches get it (`handleFetch`); client navigations call the admin API from the browser, so either a same-origin BFF proxy route that adds the bearer token from the session, or server loads for every screen.

**Acceptance criteria**
- [ ] Discovery, PKCE (S256), `state` and `nonce` checked; callback route sets the session cookie; `/me` read after login
- [ ] Browser-side admin API calls are authorised without exposing the token to JavaScript (BFF proxy or server loads), documented in an ADR
- [ ] Token expiry: silent refresh or redirect to login with `expired=1`; sign-out ends the IdP session when supported
- [ ] Tested against the dev issuer in e2e (HUB-277)
- [ ] The access (and refresh) token is never readable from the cookie value: today the session cookie is signed but **not encrypted** (base64 JSON). Encrypt the session payload (e.g. AES-256-GCM with a key derived from `HUB_SESSION_SECRET`) or keep tokens in a server-side session store keyed by an opaque id (lead review of HUB-253)

### HUB-283 — CCR-002: active-version filters on listIntegrations
- **Epic:** EP-10 · **Type:** contract · **Area:** contracts
- **Status:** done · **Depends on:** HUB-006, HUB-256 · **Feature:** F-19 · **Stories:** US-1902
- **Branch:** `contract/HUB-283-integrations-version-filters`

Filed by frontend-engineer during HUB-256. Full CCR text (before/after YAML): `frontend/CCR-NOTES.md` § CCR-002. HUB-256 asks for an "active version" filter on the integrations list, but `listIntegrations` has no version parameter, and filtering one cursor page client-side would miss matches. Proposed additive booleans `has_active_version` and `active_version_behind`. `hub-admin` minor bump.

**Acceptance criteria**
- [x] Both query parameters added to `listIntegrations` with examples; `info.version` minor bump and `contracts/CHANGELOG.md` entry
- [ ] Backend list handler implements them, covered by contract tests (EP-08)
- [x] Console adds the filter (URL params, chips) after `npm run gen:api` and removes `TODO(CCR-002)` (frontend-engineer, console round 4: "Active version" (Published / None) and "Newer version or draft" (Waiting / None waiting) selects, `has_active_version` / `active_version_behind` in the URL, removable chips, a note when "None" is combined with the second filter, which matches nothing per the contract)

Steward note (2026-10-31): contract side shipped in hub-admin 1.1.0 (contract round 1; notice `contracts/notices/2026-10-31-round-1.md`). `active_version_behind=true` means an active version exists and `latest_version > active_version` or `has_draft`; `false` means the active version is the latest with no draft; integrations without an active version match neither value. The backend and console criteria stay open for those sides.

### HUB-284 — CCR-003: dead-letter count per account on listAccounts
- **Epic:** EP-10 · **Type:** contract · **Area:** contracts
- **Status:** done · **Depends on:** HUB-006, HUB-261 · **Feature:** F-19 · **Stories:** US-1903
- **Branch:** `contract/HUB-284-account-dead-letter-count`

Filed by frontend-engineer during HUB-261. Full CCR text (before/after YAML): `frontend/CCR-NOTES.md` § CCR-003. The health board must show a DLQ count per account, but no admin operation returns one (`listDeadLetters` has no totals; counting pages client-side is wrong and too slow for a 5 s refresh). Proposed additive `dead_letter_count` and `dead_letter_oldest_at` on `AccountSummary` and `Account`. `hub-admin` minor bump.

**Acceptance criteria**
- [x] Both properties on `AccountSummary` and `Account` with examples; `info.version` minor bump and `contracts/CHANGELOG.md` entry
- [ ] Backend accounts handlers fill them from the DLQ store, covered by contract tests (EP-08)
- [x] Console health board adds a "Dead letters" column (count, oldest age, link to the filtered DLQ) after `npm run gen:api` and removes `TODO(CCR-003)` (frontend-engineer, console round 4: the account's figure on each of its endpoint rows, linked to `/dlq?account_id=`, "oldest N min ago" with the UTC time in a tooltip, 0 without a link, Unknown when the account or the figure is missing; the summary line adds the total, each account counted once)

Steward note (2026-10-31): contract side shipped in hub-admin 1.1.0 (contract round 1; notice `contracts/notices/2026-10-31-round-1.md`). Both properties are optional in the schema (additive); the backend should always fill them from 1.1.0 on. The backend and console criteria stay open for those sides.

### HUB-285 — ConfirmDialog keeps focus inside after a failed submit
- **Epic:** EP-10 · **Type:** fix · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-259 · **Feature:** F-19 · **Stories:** US-1902
- **Branch:** `fix/HUB-285-fe-dialog-focus-after-failure`

Filed by frontend-engineer during HUB-270 (e2e). While a `ConfirmDialog` submits, its buttons are `disabled`, so the focused confirm button drops focus to `<body>`. When the action fails (e.g. 412, 409, 403) the dialog stays open but focus is outside it: Escape no longer closes it (the key handler is on the dialog) and Tab starts from the page. Seen with `updateTenant:412`; affects every dialog.

**Acceptance criteria**
- [ ] After a failed submit, focus moves into the dialog (the error panel's heading or the reason field) and Escape closes it
- [ ] Component test and one e2e keyboard check

## EP-11 — MCP server + consumer skill + SDK

### HUB-300 — hubmcp scaffold with official MCP Go SDK (stdio)
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-020, HUB-006, HUB-094 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-300-hubmcp-scaffold`

Create `cmd/hubmcp` using the official MCP Go SDK with stdio transport, config (Hub URL, tenant key from env, optional admin token) and tool registration from the frozen MCP tool definitions.

**Acceptance criteria**
- [ ] `hubmcp` starts over stdio and lists tools matching the contract
- [ ] Tenant key read from env only and redacted in logs
- [ ] Calls go through `pkg/hubclient`

### HUB-301 — Streamable HTTP transport
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-300 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-301-hubmcp-http`

Add streamable HTTP transport with bearer auth for shared deployments.

**Acceptance criteria**
- [ ] Same tool list over HTTP and stdio
- [ ] Unauthenticated HTTP clients rejected

### HUB-302 — Discovery tools
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-300 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-302-mcp-discovery-tools`

Tools to list integrations, describe one, and fetch a capability's schemas, backed by the discovery endpoints (HUB-090 contract).

**Acceptance criteria**
- [ ] Tool outputs never contain vendor, account or version
- [ ] Tested against a contract-validated fake Hub

### HUB-303 — Invoke, request status and dry-run tools
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-300 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-303-mcp-invoke-tools`

Tools to invoke an integration (returning Result vs Queued explicitly), get request status, and dry-run.

**Acceptance criteria**
- [ ] A 202 is reported as queued with the poll id, never as success
- [ ] Error envelope surfaced with code, retryable, retry_after_ms

### HUB-304 — Confirm guard for money/quota integrations
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-303 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-304-mcp-confirm-guard`

For integrations with `moves_money_or_quota: true` (read via `getIntegration`), the invoke tool requires an explicit `confirm: true` and an idempotency key, and echoes a summary before calling.

**Acceptance criteria**
- [ ] Invoke without confirm returns a confirmation request and makes no Hub call
- [ ] Missing idempotency key rejected client-side

### HUB-305 — Admin read-only tools
- **Epic:** EP-11 · **Type:** feat · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-300 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `feat/HUB-305-mcp-admin-readonly`

Read-only admin tools (request search/timeline, breaker states, DLQ list, integration versions) using a viewer token against `contracts/openapi/hub-admin.v1.yaml`; no mutating tools.

**Acceptance criteria**
- [ ] Tool set contains no mutating operation (test)
- [ ] Fails clearly when no admin token is configured

### HUB-306 — MCP tests
- **Epic:** EP-11 · **Type:** test · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-301, HUB-302, HUB-303, HUB-304, HUB-305 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `test/HUB-306-mcp-tests`

Tool schema snapshot tests against the MCP contract, handler tests against a fake Hub, and an MCP Inspector smoke run.

**Acceptance criteria**
- [ ] Tool schemas equal the frozen definitions
- [ ] Every tool has success and error tests

### HUB-307 — MCP e2e against a running Hub
- **Epic:** EP-11 · **Type:** test · **Area:** mcp
- **Status:** todo · **Depends on:** HUB-306, HUB-097, HUB-123 · **Feature:** F-20 · **Stories:** US-20xx
- **Branch:** `test/HUB-307-mcp-e2e`

Run hubmcp against a compose Hub + mockvendor and exercise discovery, sync invoke, async invoke + status, dry-run.

**Acceptance criteria**
- [ ] All flows pass end to end
- [ ] No vendor identity in any tool output except `hub_dry_run`, the documented exception (grep)

### HUB-308 — hubclient polish: retries, polling, callback verifier
- **Epic:** EP-11 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-094 · **Feature:** F-21 · **Stories:** US-21xx
- **Branch:** `feat/HUB-308-hubclient-polish`

Add retry helper honouring `retryable`/`retry_after_ms`, idempotency-key helper, poll-until-terminal helper, `X-Hub-Signature` verifier for tenants, and examples (DESIGN §4.5, §12.1).

**Acceptance criteria**
- [ ] Retry helper never retries non-retryable errors
- [ ] Signature verifier rejects timestamps > 5 min old
- [ ] Runnable examples per delivery mode

### HUB-309 — Consumer skill polish and install docs
- **Epic:** EP-11 · **Type:** docs · **Area:** docs
- **Status:** todo · **Depends on:** HUB-008, HUB-303, HUB-308 · **Feature:** F-21 · **Stories:** US-21xx
- **Branch:** `docs/HUB-309-consumer-skill-polish`

Polish `clients/claude-skill/` (per-mode examples, error handling, the never-vendor rule) and write install docs for the skill and hubmcp in Claude Code / Claude Desktop.

**Acceptance criteria**
- [ ] Skill examples validate against the public contract
- [ ] Install docs tested on a clean machine
- [ ] Changes reflected in F-20/F-21 docs

### HUB-310 — hubclient SSE reader for llm.chat streams
- **Epic:** EP-11 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-308, HUB-333 · **Feature:** F-21 · **Stories:** US-21xx
- **Branch:** `feat/HUB-310-hubclient-sse`

Add an SSE reader to `pkg/hubclient` mapping canonical chunks (`delta.text`, `finish_reason`, `usage`) and terminal error events (DESIGN §15). Scheduled after streaming lands in Release 2.

**Acceptance criteria**
- [ ] Reader yields chunks in order and surfaces a terminal error event as an error
- [ ] Context cancellation closes the stream

## EP-12 — Release 2 (DESIGN §19 Phase 2)

### HUB-330 — Inbound vendor webhooks and correlation
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-330-inbound-webhooks`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §12.2: verify, correlate, inbound transformer, fan-out via subscriptions. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-331 — Worker micro-batching
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-331-micro-batching`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §7.5: group by account + endpoint in a 50–200 ms window; split per-record results. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-332 — OAuth2, HMAC signing and mTLS auth schemes
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-332-auth-oauth2-hmac-mtls`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §9: OAuth2 client credentials with single-flight refresh, HMAC request signing, per-account mTLS. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-333 — llm.chat with streaming, failover and token accounting
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-333-llm-chat-streaming`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §15: SSE relay, pre-first-byte failover, `supports` checks, token/cost metrics, budgets. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-334 — Vendor session flows (Aadhaar OTP)
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-334-aadhaar-otp-sessions`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §14: generate → verify via licensed KUA/ASA on the session store; OTP limits as account quota. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-335 — Redirect hand-offs (DigiLocker, eSign, eNACH)
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-335-redirect-handoffs`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §4.1, §14: `handoff_url`, `GET /handoff/return/{session_id}`, status webhook to tenant. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-336 — Long-running consent requests (Account Aggregator)
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-336-aa-consent`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §14: TTL in days, consent artefact store, inbound notifications as state-machine steps. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-337 — Sealed audit hardening with KMS-backed tenant keys
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-337-sealed-kms`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §13.2: production key management, 90-day retention and cold storage for sealed payloads. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-338 — Admin UI phase 2: transformer editor and UI-to-Git
- **Epic:** EP-12 · **Type:** feat · **Area:** frontend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-338-admin-ui-phase2`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §16.1, §19 Phase 2: transformer editor with live dry-run; UI changes land as Git commits. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-339 — Tenant self-service
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-339-tenant-self-service`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §19 Phase 2: tenants manage their own keys, registrar endpoints and subscriptions. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-340 — KMS and Vault credential sources
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-22xx
- **Branch:** `feat/HUB-340-kms-vault-creds`

Coarse placeholder for Release 2 (DESIGN §19 Phase 2). §9: `CredentialSource` implementations for cloud KMS and Vault transit behind the same interface. Split into ~1-day tickets (with stories and feature doc updates) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope and contracts

### HUB-342 — Use-case catalogue and a runnable OTP → account-verification example
- **Epic:** EP-12 · **Type:** docs · **Area:** docs
- **Status:** in-review · **Depends on:** — · **Feature:** F-22 · **Stories:** US-2205
- **Branch:** `docs/HUB-342-use-cases`

The product owner asked how calls work and how UX-style flows ("first an OTP, then the user gives the account number") are handled inside the Hub. Write `docs/use-cases/README.md`: a catalogue for non-engineers of what the Hub does today vs what is planned, with the caller's exact requests and responses (shapes from `contracts/openapi/hub-public.v1.yaml`, no vendor names), a sequence diagram, failure behaviour and a status line citing the test that proves it. Back the two-step flow with a runnable example under `examples/otp-account-flow/` (registry overlay kept out of `backend/config` and `contracts/`, fictional vendor `demo-kyc` on mockvendor) and an integration test through the real gateway. Gaps found on the way become their own tickets (HUB-343 … HUB-348).

**Acceptance criteria**
- [x] `docs/use-cases/README.md` covers: how a call flows (pipeline, DESIGN §18.6 latency budget), events sync/async with multi-account and a minimal response, vendor/account swap with zero caller change, vendor down (fail_fast / wait / async parking and drain), async with signed callback, idempotent retry, two-step OTP → verify → bank account, the DESIGN §14 lending patterns with their EP-12 tickets
- [x] Each use case has the caller's HTTP requests/responses, a mermaid sequence diagram, failure behaviour and a status line (Works today + test / Partly / Planned + ticket)
- [x] `examples/otp-account-flow/` overlay: two example capability schemas, vendor `demo-kyc`, two accounts, tenant `loan-onboarding`, integrations `int_demo_otp` (v1, v2) and `int_demo_bank_verify`, four transformers with goldens; nothing added to `backend/config` or `contracts/`
- [x] `TestExampleOTPConfigBuilds` (unit) builds the overlay and runs its goldens; `TestExampleOTPAccountFlow` (integration) runs step 1 (session opened, vendor txn id kept) → publish v2 → step 2 pinned to account A → step 3 bank verification, plus wrong OTP, vendor-expired OTP, expired session (`SESSION_NOT_FOUND`), double tap, vendor down, edge refusals, invalid account
- [x] `examples/otp-account-flow/README.md` with how to run it and the exact requests
- [x] Linked from the root `README.md` and `docs/features/README.md`; CHANGELOG entry

### HUB-343 — Per-operation transformers on integration version targets
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-344 · **Feature:** F-22 · **Stories:** US-2205
- **Branch:** `feat/HUB-343-per-operation-transformers`

Found while building HUB-342. A target has one transformer pair (`transformer: {request, response}`), and the attempter uses it for every operation (`internal/delivery/attempter.go`). Because a request transformer names one `vendor_endpoint` and is compiled against one operation's schema, a multi-operation capability (DESIGN §3.1 `kyc.aadhaar_otp@v1` with `generate` and `verify`, each on its own vendor endpoint) cannot be expressed today. Let a target map each operation to its own pair, keeping the current single pair for single-operation capabilities. Needs the admin contract change in HUB-344 first (integration versions are created and shown through `/admin/v1`).

**Acceptance criteria**
- [ ] A target may set `operations: {<op>: {request, response}}`; the publish check refuses a version that leaves an operation of its capability without a pair, or pairs a transformer compiled for another operation
- [ ] Sync, wait, async and dry-run select the pair of the invoked operation (envelope `operation`), including session steps and the failover chain
- [ ] Integration test: `generate` and `verify` on two mockvendor endpoints in one session; goldens per transformer
- [ ] `examples/otp-account-flow` moves to two operations (`send_otp`, `verify_otp`) and `docs/use-cases/README.md` drops the "one operation" note

### HUB-344 — Contract: per-operation transformers in admin integration versions
- **Epic:** EP-12 · **Type:** contract · **Area:** contracts
- **Status:** todo · **Depends on:** — · **Feature:** F-22 · **Stories:** US-2205
- **Branch:** `contract/HUB-344-per-operation-transformers`

Contract change request from HUB-342 for the `contract-steward`: the admin integration-version target needs an optional per-operation transformer map next to the existing `transformer` pair, so HUB-343 can be configured, published, diffed and shown by the console and `hubctl` (additive, admin minor bump). The caller API is unchanged: `invokeIntegrationOperation` already exists.

**Acceptance criteria**
- [ ] `hub-admin.v1.yaml` target schema gains optional `operations: {<operation name>: {request, response}}` (refs `^[A-Za-z][A-Za-z0-9_-]{0,63}@[1-9][0-9]*$`), with an example
- [ ] `info.version` minor bump, `contracts/CHANGELOG.md` entry, validation clean
- [ ] Backend and frontend notified

### HUB-345 — Refuse multi-operation integrations whose single pair serves every operation
- **Epic:** EP-12 · **Type:** fix · **Area:** backend
- **Status:** todo · **Depends on:** — · **Feature:** F-03 · **Stories:** US-0301
- **Branch:** `fix/HUB-345-multi-op-single-pair`

Found while building HUB-342. A capability with operations `send` and `verify`, bound to an integration whose target pair is compiled for `send` (`operation: send`), builds a snapshot without any problem. Invoking `verify` then runs the `send` transformers on a `verify` body and validates the result against the `send` response schema. Until HUB-343 lands, the registry should refuse such a version with a path-qualified error instead of failing at request time.

**Acceptance criteria**
- [ ] `registry.Build` refuses an integration version whose target transformers are compiled for a different operation than one the capability declares, or whose capability has more than one operation, naming the file and `targets[i].transformer`
- [ ] Table test in `internal/registry` (two-operation capability, one pair) fails before the fix and passes after
- [ ] `go test -race ./internal/registry/...` green

### HUB-346 — Transformer error rules map a canonical `reason`
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** — · **Feature:** F-04 · **Stories:** US-0405
- **Branch:** `feat/HUB-346-error-rule-reason`

Found while building HUB-342. An OTP screen must tell "wrong OTP, try again" from "OTP expired, resend" and "too many attempts". Today a transformer error rule can set `code`, `retryable`, `retry_after_ms` and `message` only, so callers can branch on nothing but free text (`upstream.code` is opaque by contract). Allow `reason:` on error rules. New reason values (e.g. `otp_mismatch`, `otp_expired`, `otp_attempts_exceeded`) must first be listed in the `reason` description of `contracts/schemas/events/canonical-error.json` (contract-steward, minor bump).

**Acceptance criteria**
- [ ] Contract change request filed and merged for the new reason values before the backend change
- [ ] Error rules accept `reason` (expression or literal, ≤ 64 chars); golden error cases can pin it
- [ ] Unit tests in `internal/transform`; contract test shows the reason in the 422 envelope

### HUB-347 — Per-integration result cache for sync lookups
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237 · **Feature:** F-22 · **Stories:** US-2205
- **Branch:** `feat/HUB-347-lookup-result-cache`

Coarse placeholder for Release 2, found while writing the HUB-342 use-case catalogue. DESIGN §14 lists an "optional per-integration result cache (e.g. PAN–Aadhaar link, 24 h)" for sync lookups; nothing implements it and no ticket tracked it. Split into ~1-day tickets (with stories, feature doc and any contract or config change) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 section updated with scope, cache key rules (tenant + integration + canonical body hash) and audit behaviour of a cache hit

### HUB-348 — Upload + job pattern (bank statement analysis, bulk CKYC)
- **Epic:** EP-12 · **Type:** feat · **Area:** backend
- **Status:** todo · **Depends on:** HUB-237, HUB-098 · **Feature:** F-22 · **Stories:** US-2205
- **Branch:** `feat/HUB-348-upload-job`

Coarse placeholder for Release 2, found while writing the HUB-342 use-case catalogue. DESIGN §14 "Upload + job": large payloads in object storage with pointers in the ledger, and job poll/webhook adapters (bank statement analysis, bulk CKYC). F-22 and EP-12 had no item for it. Split into ~1-day tickets (with stories, feature doc and contract change requests) before starting.

**Acceptance criteria**
- [ ] Split into ~1-day tickets in this range before work starts
- [ ] Feature doc F-22 gains the item, with the contract changes it needs (upload size limits, job status) listed as CCRs

## Parallel tracks

Contracts were frozen in HUB-006, so the three tracks below only meet at contract tests and the late e2e tickets.

| Track | Epics | Can start after | Joins the backend at |
| --- | --- | --- | --- |
| Backend core (sequential milestones) | EP-01 → EP-02 → EP-03 → EP-04 → EP-05 → EP-06 → EP-07 → EP-08 → EP-09 | HUB-012 | — |
| Admin console | EP-10 (HUB-250…HUB-276 on MSW mocks) | HUB-012 + HUB-006 | HUB-277 (needs HUB-214, end of M7) |
| MCP server + SDK + consumer skill | EP-11 (HUB-300…HUB-306, HUB-308, HUB-309 against a contract-validated fake Hub) | HUB-020 + HUB-094 (HUB-094 needs only HUB-006) | HUB-307 (needs HUB-097 and HUB-123, end of M4) |
| Infra / repo hygiene | HUB-013, HUB-014, HUB-015 | now (HUB-015 after HUB-012) | independent |
| Phase-1 decisions | HUB-016 | now | user availability |
| Release 2 | EP-12 | HUB-237 (end of M8) | — |

Concurrency inside the backend track (once the named prerequisite is done):

- **EP-02 ∥ EP-03 start:** HUB-060 (DSL parser) needs only HUB-040, so the transformer engine starts while loaders and validation are built.
- **EP-03 ∥ EP-04 groundwork:** HUB-086 (upstream client), HUB-091 (audit writer) and HUB-094 (hubclient) do not depend on the transformer engine.
- **EP-05 groundwork early:** HUB-110 (outbox) and HUB-119 (registrar/SSRF) need only migrations and loaders and can run during M3.
- **EP-06 limiters early:** HUB-147 and HUB-148 depend only on the upstream client and edge auth.
- **EP-08 read endpoints:** HUB-200/201 can start once HUB-080 exists; mutations wait for EP-07.
- **EP-09:** HUB-233/234 can run during M7.

### Critical path (Release 1)

Longest dependency chain among open tickets, counting each ticket as ~1 day: **19 tickets**.

HUB-012 → HUB-020 → HUB-040 → HUB-041 → HUB-043 → HUB-044 → HUB-084 → HUB-085 → HUB-088 → HUB-115 → HUB-116 → HUB-146 → HUB-149 → HUB-151 → HUB-152 → HUB-154 → HUB-230 → HUB-232 → HUB-237

- HUB-012 — Install toolchain on the dev machine
- HUB-020 — Create backend Go module and package layout
- HUB-040 — Config domain types
- HUB-041 — Loaders: capability (compiled JSON Schema), vendor, account
- HUB-043 — Cross-reference validation with path-qualified errors
- HUB-044 — Snapshot build and atomic swap
- HUB-084 — CredentialSource: local encrypted file, cache and redaction
- HUB-085 — Auth schemes: static headers, bearer, basic
- HUB-088 — Invoke handlers (sync pipeline)
- HUB-115 — Async mode and request status endpoint
- HUB-116 — Wait mode
- HUB-146 — Parking on open breaker
- HUB-149 — Ramped drain scheduler
- HUB-151 — Chaos 2: vendor recovers and drain ramps
- HUB-152 — Chaos 3: re-open mid-drain
- HUB-154 — Chaos 5: failover target serves; M5 summary
- HUB-230 — Metrics: full §16.2 set
- HUB-232 — Alert rules and dashboards as code
- HUB-237 — Runbooks and M8 summary

Anything that delays these tickets delays Release 1. The admin console and MCP tracks have slack until HUB-277 and HUB-307 respectively.
