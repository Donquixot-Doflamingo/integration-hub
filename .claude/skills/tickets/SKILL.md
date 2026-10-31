---
name: tickets
description: Write and maintain HUB-### tickets in docs/tickets/TICKETS.md exactly per AGENTS.md §5 - required fields (title, epic, type, area, status, depends-on, feature, user stories, description, acceptance criteria checklist), ~1-day sizing, dependencies, status lifecycle todo to in-progress to in-review to done / blocked, epic mapping and the HUB ID range reserved for each epic. Use before starting any piece of work, when breaking a feature into tasks, when picking the next ticket ID, when a bug or idea appears mid-task, when a PR opens or merges, or when someone asks what to work on.
---

# Tickets (`docs/tickets/TICKETS.md`)

Every piece of work has a ticket **before** it starts (AGENTS.md §5). There is no external tracker:
the file is the backlog. IDs are `HUB-###` (three digits), **never reused**, even for dropped tickets.

## ID ranges per epic
Pick the next free ID **inside the epic's range**. If a range is full, ask the user; do not borrow
from another epic.
| Epic | Scope (milestone) | IDs |
| --- | --- | --- |
| EP-00 / EP-01 | Repo foundation (Phase 1) and M0 scaffold + mockvendor | HUB-001 - 039 |
| EP-02 | M1 config model and registry | HUB-040 - 059 |
| EP-03 | M2 transformer engine | HUB-060 - 079 |
| EP-04 | M3 gateway sync path, audit, caller discovery, `hubclient` | HUB-080 - 109 |
| EP-05 | M4 outbox, worker, wait/async/callback, idempotency | HUB-110 - 139 |
| EP-06 | M5 resilience (breaker, parking, drain, limits) | HUB-140 - 169 |
| EP-07 | M6 versioning and cutover, account lifecycle | HUB-170 - 199 |
| EP-08 | M7 control plane, admin API, `hubctl` | HUB-200 - 229 |
| EP-09 | M8 observability and hardening | HUB-230 - 249 |
| EP-10 | Admin console (frontend) | HUB-250 - 299 |
| EP-11 | `hubmcp`, consumer Claude skill, SDK | HUB-300 - 329 |
| EP-12 | Release 2 | HUB-330 - 399 |
The milestone column is the default mapping to DESIGN §19; if the epic table at the top of
TICKETS.md says otherwise, TICKETS.md wins.

Find the next free ID:
```bash
grep -oE 'HUB-1[1-3][0-9]' docs/tickets/TICKETS.md | sort -u | tail -1   # e.g. EP-05 range
```

## Ticket format (all fields required)
```markdown
### HUB-147 · Ramped drain of parked envelopes on breaker close
| Field | Value |
| --- | --- |
| Epic | EP-06 |
| Type | feat |
| Area | backend |
| Status | todo |
| Depends on | HUB-143, HUB-145 |
| Feature | F-13 |
| User stories | US-1302 |

**Description.** When the `(account, endpoint)` breaker closes, release parked envelopes at 20 % of
the account rate limit for 30 s, doubling every 30 s to 100 % (DESIGN §10.5). Live traffic keeps
priority on the shared limiter. Re-open mid-drain leaves the rest parked.

**Acceptance criteria**
- [ ] `Queue.Release(account, endpoint, limit)` called by the drain scheduler with the ramped limit.
- [ ] Integration test (real Postgres + Redis, mockvendor outage scenario) shows drain starts <= 60 s after recovery and no 429 storm.
- [ ] Re-open during drain stops release; remaining envelopes stay `parked`.
- [ ] Metrics `parked_count`, `parked_oldest_age_s` updated; listed in F-13 observability.
- [ ] `go test -race ./...` green.
```
Field values:
- **Type**: `feat | fix | chore | docs | contract | test | perf`
- **Area**: `backend | frontend | contracts | mcp | docs | infra`
- **Status**: `todo | in-progress | in-review | done | blocked`
- **Depends on**: ticket ids or `none`. **Feature**: `F-XX` (or `none` for pure chores).
  **User stories**: `US-FFNN` list or `none`.
- When done, add a row `| Merged | <merge commit subject> |`. When blocked, add `| Blocked by | ... |`.

## Sizing: about one day
- One PR, one branch (`<type>/HUB-###-slug`), roughly 1 day of work including tests and docs.
- Split when: more than one area (contract + backend + frontend = three tickets), more than ~5
  acceptance criteria, unknowns needing a spike (make a time-boxed `chore` spike ticket), or
  "and" in the title.
- Contract work is its own `contract` ticket (area `contracts`) that the backend and frontend
  tickets depend on.

## Acceptance criteria
- Checkbox list, each item verifiable by a test, a command or a visible artefact.
- Include the tests that must exist (`-race`, integration, contract, goldens, Playwright).
- Include doc obligations when relevant (feature doc observability, changelog, consumer skill).
- Never "works as expected".

## Status lifecycle
```
todo -> in-progress -> in-review -> done
   \-> blocked (with "Blocked by") -> back to todo/in-progress
```
- Start work: set `in-progress` (and the feature doc to `in-progress` if it was `proposed`).
- PR opened: `in-review`. PR merged: `done`, tick all criteria, add the merge commit subject.
- New idea or bug found mid-task: **add a new `todo` ticket**; do not grow the current one.
- Status edits go in the same commit/PR as the work, or a `docs(tickets): ...` commit.

## Breaking a feature into tickets
1. Read the feature doc and stories. List deliverables per area.
2. Order: `contract` ticket(s) first -> backend -> frontend / MCP -> docs/observability.
3. Fill dependencies so the order is explicit; frontend tickets can depend only on the contract
   ticket (they build against MSW), not on backend tickets.
4. Link the tickets back into the feature doc and stories.

## Anti-patterns
- Coding before the ticket exists. Reusing or renumbering IDs. IDs outside the epic range.
- A ticket spanning backend + frontend + contract. Missing fields ("Depends on" left blank).
- Marking `done` before the PR is merged.
