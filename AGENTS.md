# AGENTS.md — Integration Hub

This is the operating manual for every AI agent and human contributor working in this repo. `CLAUDE.md` imports it.
`backend/AGENTS.md`, `frontend/AGENTS.md` and `contracts/AGENTS.md` add rules for their own folders. When they conflict with this file, the more specific file wins inside its folder.

## 1. Language
Always respond in **English**, even when the user writes in Hindi or Hinglish. Code, docs, commits and tickets are in English too.

## 2. Repository map
| Path | What lives there | Owner agent |
| --- | --- | --- |
| `docs/DESIGN.md` | The agreed design (source of truth for *behaviour*). Do not edit it casually: record deviations in an ADR. | lead |
| `docs/product/` | `PRD.md`, `USER_STORIES.md`, `PERSONAS.md` | lead / docs |
| `docs/features/` | One doc per feature, `F-XX-<slug>.md`, from `_TEMPLATE.md`. `README.md` is the index. | whoever builds the feature |
| `docs/tech/` | `TECH_SPEC.md`, `OBSERVABILITY.md`, `TESTING_STRATEGY.md`, `SECURITY.md`, `AUTH_PLAN.md` (ISFC identity, tenancy and authorization plan, EP-15), `adr/NNNN-*.md` | lead / backend |
| `docs/tickets/TICKETS.md` | The ticket backlog (no external tracker yet). | everyone |
| `docs/process/` | Git workflow, contract-change process, release process | lead |
| `contracts/` | **Frozen API contracts**: OpenAPI 3.1 YAML (admin, public caller API), the MCP tool contract and JSON Schemas for webhook events | `contract-steward` |
| `backend/` | Go module `github.com/Donquixot-Doflamingo/integration-hub/backend`: `hub`, `hubctl`, `mockvendor`, `hubmcp` binaries | `backend-engineer` |
| `frontend/` | SvelteKit admin console (control plane UI) | `frontend-engineer` |
| `clients/claude-skill/` | Distributable consumer skill for teams that *use* the Hub (works in Claude, Codex, Cursor and other agents) | `contract-steward` + docs |
| `deploy/` | docker-compose, k8s manifests, alert rules, dashboards | `backend-engineer` |
| `.claude/agents/` | Sub-agent definitions | lead |
| `.claude/skills/`, `backend/.claude/skills/`, `frontend/.claude/skills/` | Skills. Repo-wide skills at the root; backend/frontend skills **inside their folder** | owner of the folder |

## 3. Non-negotiable product rules (from DESIGN.md §0)
1. **Callers never see vendor names, account names or versions.** Caller contract = integration ID + operation + a request body (and headers) that fit the operation's request schema → the response transformer's output, or the vendor's body passed through (ADR-0023). If a change would leak vendor identity into the caller contract, stop and ask the user. The one documented exception is the scope-gated caller dry-run (`dryRunIntegration`, key scope `dry_run`, PRD DV-6); see `contracts/README.md`.
2. **Hot path discipline**: no DB or registry reads on the sync path, only the in-memory snapshot. Every middleware publishes its own duration metric.
3. **Never call real vendors in tests.** Use `mockvendor`. Verify real vendor payloads against current vendor docs and cite the URL in the transformer file.
4. **Every transformer ships with golden fixtures.** You must not be able to publish a transformer whose goldens fail.
5. **Never log secrets.** Credentials only enter through `creds.*` in header/query/auth targets. The logger redacts by value.
6. Table-driven tests. `go test -race` in CI. Integration tests use real Postgres and Redis.
7. **ISFC is the identity and tenant source** (planned, EP-15; today the dev issuer and local tenants still apply, `docs/tech/AUTH_PLAN.md`, F-24, F-25). **Never call a real ISFC (or Entra) in tests**: use `mockisfc` and a fake OPA or OPA container, as with vendors and `mockvendor`. Never store or log an ISFC API key in plaintext; the Hub keeps only hashes. No OPA or ISFC call on the `/v1` sync path.

## 4. Contract-first workflow (frontend and backend run in parallel)
The contracts in `contracts/` are the only interface between backend, frontend, MCP server and SDKs.
1. **A contract changes first, in its own commit**, before any code that depends on it: `contract(<area>): <change> (HUB-###)`.
2. Only the `contract-steward` agent edits `contracts/`. Backend or frontend agents that need a change **ask** the steward with a written change request, never edit it themselves. See `docs/process/CONTRACT_CHANGES.md`.
3. Every contract change bumps `info.version` (semver: additive = minor, breaking = major) and adds an entry to `contracts/CHANGELOG.md`.
4. Breaking changes to the **caller** API are almost never allowed (D11: new integration ID instead). Breaking changes to the **admin** API need the user's approval.
5. After a contract change, the steward notifies the backend and frontend agents. The backend implements and the contract tests prove it. The frontend regenerates its typed client (`npm run gen:api`) and adapts. Nobody codes against an endpoint that is not in the contract.
6. The backend runs contract tests: every response is validated against the OpenAPI document. The frontend never hand-writes API types; it generates them.

## 5. Tickets (`docs/tickets/TICKETS.md`)
- Every piece of work has a ticket **before** it starts. IDs are `HUB-###`, never reused.
- Tickets are grouped under epics (`EP-xx`) that map to milestones in DESIGN.md §19.
- Each ticket has: title, epic, type (`feat|fix|chore|docs|contract|test|perf`), area (`backend|frontend|contracts|mcp|docs|infra`), status (`todo|in-progress|in-review|done|blocked`), depends-on, feature (`F-XX`), user stories (`US-FFNN`, FF = feature number), description, acceptance criteria (checkbox list).
- A ticket is small enough to finish in about a day. Split anything larger.
- When you start a ticket, set `in-progress`. When its PR merges, set `done` and add the merge commit subject.
- New idea or bug found mid-task: add a new `todo` ticket. Do not grow the current one.

## 6. Git workflow
Full detail: `docs/process/GIT_WORKFLOW.md`.
- **Never commit or push directly to `main`.** `main` changes only by merging a PR.
- Branch names: `<type>/HUB-###-<short-slug>` (e.g. `feat/HUB-031-tenant-auth`). Phase-wide work can use `phase-N/<slug>`.
- Commits follow **Conventional Commits**:
  ```
  <type>(<scope>): <imperative summary, ≤ 72 chars> (HUB-###)

  Why: <the problem or motivation>
  What: <bullet list of the changes>
  Tests: <what was run and the result>

  Refs: HUB-###[, HUB-###]
  ```
  Types: `feat fix perf refactor test docs contract chore build ci`. Scopes: package or folder (`edge`, `registry`, `transform`, `frontend`, `contracts`, `docs`, …).
- One logical change per commit. Tests and docs go in the same commit as the code they cover.
- PRs use `.github/pull_request_template.md`. Merge with a **merge commit** (no squash), so per-commit history and the changelog stay meaningful.

## 7. Changelog (`CHANGELOG.md`): one entry per commit
The changelog tracks **commits, not tickets**, so anyone can see what each change shipped.
- **Every commit adds its own entry** at the top of the `## [Unreleased]` section, in the same commit.
- Entry format:
  ```
  - YYYY-MM-DD · `<type>(<scope>)` · <commit summary> · HUB-### · F-XX
    - <user-visible effect 1>
    - <user-visible effect 2>
  ```
  The summary must match the commit subject, so `git log --grep "<summary>"` finds the SHA.
- At a release, `[Unreleased]` becomes `## [x.y.z] — YYYY-MM-DD`.
- `contracts/CHANGELOG.md` separately tracks contract versions.

## 8. Documenting a new feature (do this every time)
When the user asks for a new feature or a change to an existing one:
1. **Product**: add or extend user stories in `docs/product/USER_STORIES.md` (`US-FFNN`, *As a… I want… so that…*, with Given/When/Then acceptance criteria). If scope, goals or non-goals change, update `docs/product/PRD.md`.
2. **Feature doc**: create `docs/features/F-XX-<slug>.md` from `_TEMPLATE.md` (or update the existing one). Add it to `docs/features/README.md`. Set status `proposed → in-progress → shipped`.
3. **Design**: if it changes architecture, the data model or a §2 decision, write an ADR in `docs/tech/adr/` and update `docs/tech/TECH_SPEC.md`.
4. **Contract**: if any API or event shape changes, run the contract-first workflow (§4) **before** coding.
5. **Tickets**: break the work into `HUB-###` tickets, each linked to the feature and stories.
6. **Observability**: list new metrics, logs, traces and alerts in the feature doc, and add them to `docs/tech/OBSERVABILITY.md`.
7. **Code + tests**, one ticket per branch or PR.
8. **Changelog**: one entry per commit (§7).
9. Update the consumer skill (`clients/claude-skill/`) and the MCP tools if callers see the change.

## 9. Definition of done (per ticket)
- [ ] Acceptance criteria in the ticket are met and checked.
- [ ] `make test` (unit, `-race`) and `make itest` are green for backend changes; `npm run check && npm test` for frontend changes.
- [ ] Contract tests pass. No endpoint is used that is missing from `contracts/`.
- [ ] Feature doc, user stories and (if needed) ADR are updated.
- [ ] New metrics, logs and traces exist as listed in the feature doc. No secret appears in logs.
- [ ] `CHANGELOG.md` entry exists for each commit.
- [ ] PR opened from a feature branch, never pushed to `main`.

## 10. Milestone wrap-up
At the end of each milestone (DESIGN.md §19), post a short summary: what was built, what was deferred, and **Deviations** (with the ADR link). Then continue unless the user said to stop.

## 11. Sub-agents
Defined in `.claude/agents/`:
- `contract-steward`: sole editor of `contracts/`. Reviews change requests, keeps OpenAPI valid, versions it, notifies others.
- `backend-engineer`: works in `backend/` and `deploy/`. Implements against the contract. Uses skills in `backend/.claude/skills/`.
- `frontend-engineer`: works in `frontend/`. Uses the generated client only. Uses skills in `frontend/.claude/skills/`.
- `docs-keeper`: keeps PRD, stories, feature docs, tickets and changelog consistent after each change.

Communication between agents goes through written artifacts: contract change requests, tickets and feature docs. If the backend finds it needs a different response shape, it opens a change request to the steward and waits. It does not ship a shape the frontend has not seen.

## 12. Skills
- Repo-wide (`.claude/skills/`): PRD writing, user stories, feature docs, tickets, commits and changelog, ADRs, skill-creator, and `hub-operator` (operating the Hub: tenants, keys, vendors, accounts, callbacks, cutover, transformers, multi-step flows; also linked from `.agents/skills/` for Codex).
- Backend (`backend/.claude/skills/`): Go idioms, Go architecture, Go system design, Go testing, memory and performance profiling, structured logging, debugging, observability, Postgres/outbox patterns, MCP builder.
- Frontend (`frontend/.claude/skills/`): Svelte 5 and SvelteKit, frontend design, typed API client from OpenAPI, frontend testing, accessibility.
- Third-party skills are vendored with their `LICENSE.txt` and listed in `THIRD_PARTY_SKILLS.md`. Do not add a skill from an unknown source without the user's approval.
