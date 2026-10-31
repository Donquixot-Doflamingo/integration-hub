# Ticket backlog — how it works

All tickets live in [`TICKETS.md`](TICKETS.md). There is no external tracker yet. Ticket rules come from `AGENTS.md` §5 and the definition of done from `AGENTS.md` §9; this page explains how to use the file day to day.

## Ticket format

```
### HUB-### — <title>
- **Epic:** EP-xx · **Type:** feat|fix|chore|docs|contract|test|perf · **Area:** backend|frontend|contracts|mcp|docs|infra
- **Status:** todo · **Depends on:** HUB-###, … (or —) · **Feature:** F-XX · **Stories:** US-FFxx
- **Branch:** `<type>/HUB-###-<slug>`

<2–4 sentence description, citing DESIGN §>

**Acceptance criteria**
- [ ] …
```

- **Stories** use the feature's prefix (`US-05xx` for F-05) until exact ids from `docs/product/USER_STORIES.md` are filled in. The docs-keeper replaces the prefix with real ids when a ticket starts.
- **Branch** is the branch name to use (`AGENTS.md` §6). One ticket per branch / PR.

## Statuses

| Status | Meaning | Who sets it |
| --- | --- | --- |
| `todo` | Ready or waiting on dependencies | whoever creates the ticket |
| `in-progress` | Someone is working on its branch | the assignee, when the branch is created |
| `in-review` | PR open, CI running or review pending | the assignee, when the PR opens |
| `done` | PR merged; acceptance boxes ticked | the assignee, after merge; add `Merged: <merge commit subject>` under the ticket |
| `blocked` | Cannot proceed; add a `Blocked by:` line with the reason (ticket, contract change request, user decision) | anyone |

A ticket may start only when every ticket in **Depends on** is `done` (or `in-review` if you accept rebasing).

## How status changes are committed

- Status changes ride **inside the ticket's own PR**: the first commit on the branch sets `in-progress`; the PR description links the ticket. Never commit a status change directly to `main`.
- After merge, flip the ticket to `done` and record the merge commit subject in the next PR you open (or a small `docs(tickets): mark HUB-### done (HUB-###)` PR if nothing else is pending).
- Each such commit adds its `CHANGELOG.md` entry like any other commit (`AGENTS.md` §7).

## Counters

The totals in `TICKETS.md` and the tables below are derived. After adding tickets, changing a status, or merging a branch that touched `TICKETS.md`, run `python3 docs/tickets/recount.py` (`--check` exits 1 when they are stale).

## ID ranges

IDs are never reused. New tickets take the next free number **inside their epic's range**. If a range fills up, open a new epic rather than borrowing another range.

| Epic | Name | Features | ID range | Tickets | Done |
| --- | --- | --- | --- | --- | --- |
| EP-00 | Phase-1 foundation (docs, skills, agents, contracts, repo + GitHub setup) | F-01 | HUB-001–HUB-019 | 16 | 13 |
| EP-01 | M0 Scaffold | F-01, F-02 | HUB-020–HUB-039 | 14 | 14 |
| EP-02 | M1 Config registry | F-03 | HUB-040–HUB-059 | 10 | 10 |
| EP-03 | M2 Transformer engine | F-04 | HUB-060–HUB-079 | 12 | 12 |
| EP-04 | M3 Gateway sync path + audit | F-05, F-06, F-07, F-08 | HUB-080–HUB-109 | 19 | 18 |
| EP-05 | M4 Outbox, worker, modes, idempotency, callbacks | F-09, F-10, F-11 | HUB-110–HUB-139 | 14 | 0 |
| EP-06 | M5 Resilience | F-12, F-13 | HUB-140–HUB-169 | 15 | 0 |
| EP-07 | M6 Versioning & cutover | F-14, F-15 | HUB-170–HUB-199 | 15 | 0 |
| EP-08 | M7 Control plane & hubctl | F-16, F-17 | HUB-200–HUB-229 | 15 | 0 |
| EP-09 | M8 Observability & hardening | F-18 | HUB-230–HUB-249 | 8 | 0 |
| EP-10 | Admin console (SvelteKit) | F-19 | HUB-250–HUB-299 | 35 | 26 |
| EP-11 | MCP server + consumer skill + SDK | F-20, F-21 | HUB-300–HUB-329 | 11 | 0 |
| EP-12 | Release 2 (DESIGN §19 Phase 2) | F-22 | HUB-330–HUB-399 | 11 | 0 |
| **Total** | | | | **195** | **93** |

## Sizing

- A ticket is **about one day** of focused work including tests and docs. If it will take more, split it before starting.
- Milestone acceptance tickets (`M0 acceptance`, …) prove DESIGN §19 **Accept** lines and carry the milestone summary (`AGENTS.md` §10).
- EP-12 tickets are deliberately coarse placeholders; split each one into ~1-day tickets before work starts.

## Adding or splitting a ticket

1. Found new work or a bug mid-task? Add a new `todo` ticket; do not grow the current one (`AGENTS.md` §5).
2. Take the next free id in the right epic range and fill every field of the format above.
3. Acceptance criteria must be concrete and testable (a command, a test name, a measurable number). Lift them from DESIGN §19 Accept lines where they exist.
4. Dependencies must be real: frontend and MCP tickets depend on contracts (HUB-006 or a later contract ticket), not on backend implementation, except for explicit e2e-against-real-backend tickets.
5. When splitting, keep the original id for the first part, give new ids to the rest, and update every `Depends on` that pointed at the original.
6. A change to `contracts/` always gets its own `contract` ticket in the area `contracts`, handled by the contract-steward, and the code tickets depend on it (`AGENTS.md` §4).
7. Update the epic table counts at the top of `TICKETS.md` and in this file.

## Features referenced

Feature ids map to `docs/features/F-XX-*.md` (index: `docs/features/README.md`, authoritative for names and story ids `US-FFNN`).

| Feature | Name | Epic(s) | Tickets |
| --- | --- | --- | --- |
| F-01 | Repo foundation, dev environment & CI | EP-00, EP-01 | 27 |
| F-02 | Mock vendor (programmable fake vendor) | EP-01 | 3 |
| F-03 | Config registry & snapshots | EP-02 | 10 |
| F-04 | Transformation engine & golden fixtures | EP-03 | 12 |
| F-05 | Tenant auth, grants & edge validation | EP-04 | 3 |
| F-06 | Sync invoke path & canonical error envelope | EP-04 | 8 |
| F-07 | Credentials & vendor auth schemes | EP-04 | 2 |
| F-08 | Audit trail | EP-04, EP-05 | 6 |
| F-09 | Durable delivery: outbox, worker, wait/async/async_callback | EP-05 | 8 |
| F-10 | Idempotency | EP-05 | 1 |
| F-11 | Outbound callbacks & registrar (signed webhooks, SSRF guard) | EP-05 | 4 |
| F-12 | Circuit breaker, parking & ramped drain | EP-06 | 13 |
| F-13 | Rate limits & bulkheads | EP-06 | 2 |
| F-14 | Integration versioning & cutover (weights, shadow, rollback) | EP-07 | 12 |
| F-15 | Account lifecycle & credential rotation | EP-07 | 3 |
| F-16 | Admin API, RBAC & change log | EP-08 | 11 |
| F-17 | hubctl & config-as-code | EP-08 | 4 |
| F-18 | Observability: metrics, traces, alerts, dashboards, load tests | EP-09 | 8 |
| F-19 | Admin console (SvelteKit frontend) | EP-10 | 35 |
| F-20 | Hub MCP server (`hubmcp`) | EP-11 | 8 |
| F-21 | Consumer Claude skill & Go SDK (`hubclient`) | EP-04, EP-11 | 4 |
| F-22 | Release-2 capabilities | EP-12 | 11 |

## Parallel work and critical path

See the **Parallel tracks** section at the end of `TICKETS.md`. In short: backend milestones run in order, the admin console (EP-10) and MCP/SDK (EP-11) run in parallel against the frozen contracts with MSW mocks or a contract-validated fake Hub, and join the backend only in their final e2e tickets.
