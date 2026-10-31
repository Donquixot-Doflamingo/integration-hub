# Changelog

One entry **per commit**, newest first, added in the same commit (see `AGENTS.md` §7).
Format: `- YYYY-MM-DD · `type(scope)` · <commit subject summary> · HUB-### · F-XX`, followed by indented user-visible effects.
Find the SHA for an entry with `git log --grep "<summary>"`. Contract versions are tracked separately in `contracts/CHANGELOG.md`.

## [Unreleased]

- 2026-10-31 · `chore(skills)` · add backend Go skills and vendor mcp-builder · HUB-007 · F-01
  - Go idioms, architecture, system design, testing, memory/performance, logging, debugging, observability, Postgres, transformers, contract skills
  - Vendored Anthropic mcp-builder (Apache-2.0)
- 2026-10-31 · `contract` · freeze v1 public, admin, mockvendor and MCP contracts · HUB-006 · F-05, F-06, F-16, F-20
  - hub-public 1.0.0: invoke, dry-run, request status, discovery endpoints, outbound webhooks
  - hub-admin 1.0.0: 79 control-plane operations with RBAC and change_id
  - mockvendor 1.0.0 control API; MCP tool contract 1.0.0
  - events.track@v1 and callback event JSON Schemas
- 2026-10-31 · `docs(process)` · add git workflow, contract change and release process · HUB-005 · F-01
  - Branching, Conventional Commits, merge-commit PRs
  - Contract change request (CCR) flow between backend, frontend and contract-steward
  - Release and tagging
- 2026-10-31 · `docs(tech)` · add tech spec, observability plan, testing, security and ADRs · HUB-004 · F-01, F-18
  - TECH_SPEC with architecture, package rules, sequences, data model
  - OBSERVABILITY plan: SLOs, metric catalogue, log schema, tracing, profiling and leak detection, runbooks, alerts
  - TESTING_STRATEGY, SECURITY threat model, ADR 0001-0008
- 2026-10-31 · `docs(features)` · add feature docs F-01 to F-22 with template and index · HUB-003 · F-01..F-22
  - Feature template and index
  - One doc per feature with contract operationIds, observability, security, failure modes and tests
- 2026-10-31 · `docs(product)` · add PRD, personas and user stories · HUB-002 · F-01
  - PRD with FR/NFR, success metrics, release plan, risks, deviations DV-1..DV-6 and open questions
  - Six personas including the AI-assisted developer
  - Five end-to-end journeys and US-FFNN stories for F-01..F-22
- 2026-10-31 · `docs` · add agreed design, agent manual and repo skeleton · HUB-001 · F-01
  - docs/DESIGN.md saved unchanged as the behaviour source of truth
  - AGENTS.md / CLAUDE.md with language rule, working rules, decisions D1–D12, contract-first, tickets, git and changelog rules
  - backend/ and frontend/ AGENTS.md + CLAUDE.md; README, .gitignore, .editorconfig
