---
name: user-stories
description: Write and maintain US-FFNN user stories in docs/product/USER_STORIES.md for the Integration Hub - personas from docs/product/PERSONAS.md, As a / I want / so that, INVEST, Given/When/Then acceptance criteria, priority, links to F-XX features, FR requirements and HUB tickets, and how to split big stories. Use whenever a feature is requested or changed, a ticket needs a story reference, someone asks for acceptance criteria, or a story is too big, vague or untestable.
---

# User stories

Stories live in `docs/product/USER_STORIES.md`, grouped by feature. Every feature change starts here
(AGENTS.md §8 step 1). Tickets reference stories; stories reference features and requirements.

## ID scheme: `US-FFNN`
- `FF` = feature number (two digits, from `F-XX`), `NN` = sequence within that feature.
  `US-1403` = third story of `F-14` (versioning and cutover).
- Never reuse an ID. A dropped story stays, marked `Status: dropped (date, reason)`.
- If a story clearly spans two features, file it under the one that delivers the user-visible value
  and link the other.

## Personas (use the bold short names from PERSONAS.md)
**service developer** (tenant/caller) · **platform engineer** · **on-call SRE** ·
**integration admin** (reviewer) · **compliance officer** · **AI-assisted developer**.
Do not invent personas ("as a user", "as the system"). If none fits, propose a persona change to
PERSONAS.md first.

## Story template
```markdown
### US-1403 · Roll back an integration in one action
- **Persona:** integration admin
- **Story:** As an **integration admin**, I want to roll back an integration to a previous version in
  one action, so that a bad cutover is undone without any caller deploy.
- **Priority:** M  (M = must v1, S = should, C = could, R2 = Release 2)
- **Feature:** F-14 · **Requirements:** FR-38 · **Tickets:** HUB-162, HUB-271 · **Status:** todo
- **Acceptance criteria**
  - Given `int_evt_brandA` is active on v8 and v7 exists,
    When I roll back to v7 with reason "v8 drops campaign field",
    Then v9 is published with v7's content, v9 becomes active, and history shows v7, v8, v9.
  - Given the rollback succeeds, Then a `change_log` entry records actor, before (v8), after (v9) and the reason.
  - Given I omit the reason, Then the rollback is refused and nothing is published.
  - Given requests are parked with `pending: re_resolve`, When they drain, Then they use v9.
```
Status values: `todo | in-progress | done | dropped`. Set `done` when every linked ticket is done.

## INVEST check (apply to every story)
| Letter | Ask |
| --- | --- |
| Independent | Can it ship without another story landing in the same PR? |
| Negotiable | Does it state the need, not the implementation (no table names, no Go types)? |
| Valuable | Would the persona notice if it shipped? (pure tech work = ticket, not story) |
| Estimable | Do we know enough to break it into ~1-day tickets? |
| Small | <= ~5 tickets. Bigger -> split. |
| Testable | Every criterion is a Given/When/Then that a test (unit, contract, e2e) can check. |

## Acceptance criteria rules
- Concrete data from the Hub domain: real ids from dev config (`int_evt_brandA`, `orders-service`,
  `events.track@v1`), real codes (`UPSTREAM_UNAVAILABLE`, `retry_after_ms`), real headers.
- Cover the unhappy paths that matter: validation, auth/forbidden, circuit open, paused, idempotent
  replay, expiry.
- Never let a caller-facing criterion expose vendor, account or version (D12). Caller stories assert
  canonical results only; admin stories may name accounts.
- Observable outcomes only: response, state, audit event, metric, UI text. Not "the code calls X".

## Splitting a big story
Split by, in order of preference:
1. **Delivery mode** (sync / wait / async / async_callback).
2. **Happy path vs failure handling** (circuit open, 429, TTL expiry as separate stories).
3. **Surface** (caller API vs admin API vs console vs `hubctl` vs MCP).
4. **Operation** of a multi-operation integration (`generate` vs `verify`).
5. **Data/role variation** (`tenant_viewer` vs `admin`).
Do not split by technical layer ("backend story", "frontend story"); that is ticket-level.

## More Hub examples (short form)
- US-0902 · As a **service developer**, I want an async invoke to return 202 with a `request_id` and
  `poll` URL, so that I know my event was durably accepted. Given the vendor circuit is open, When I
  invoke `int_evt_brandA` in `async` mode, Then I get 202 `status: queued, reason: circuit_open`, and
  `GET /v1/requests/{id}` later shows `succeeded` after recovery.
- US-1201 · As an **on-call SRE**, I want sync calls to fail fast while a vendor is down, so that
  callers do not pile up timeouts. Given account breaker open, When a sync invoke arrives, Then 503
  `UPSTREAM_UNAVAILABLE` with `retry_after_ms` and `circuit: "open"`, nothing queued.
- US-2003 · As an **AI-assisted developer**, I want `hub_invoke` to demand `confirm: true` for
  money/quota integrations, so that Claude cannot trigger a bureau pull by accident.

## Workflow
1. Read the feature doc and PRD rows; pick the persona.
2. Write/extend stories; run INVEST; add Given/When/Then.
3. Link: feature (`F-XX`), requirements (`FR-NN`), tickets once they exist (`tickets` skill), and add
   the story ids to the feature doc.
4. Commit `docs(product): add rollback stories for F-14 (HUB-###)` + changelog entry.

## Anti-patterns
- "As a developer, I want a Postgres outbox" (implementation, not value).
- Criteria with "should work correctly", "fast", "user-friendly".
- One mega-story per feature. Stories without persona or without a feature link.
