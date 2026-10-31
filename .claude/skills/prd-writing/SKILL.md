---
name: prd-writing
description: How to write and update docs/product/PRD.md for the Integration Hub - section order, FR-NN / NFR-NN numbering, goals vs non-goals, measurable success metrics, agreed deviations (DV-N), risks, dependencies and open questions, and when a feature change must touch the PRD. Use whenever someone asks to add or change a requirement, goal, non-goal, metric, scope/phase or deviation, when a new feature changes product scope, when reviewing the PRD, or when a user story or feature doc needs a requirement that does not exist yet.
---

# Writing and updating the PRD

`docs/product/PRD.md` says **what** the Hub must do and **why**. `docs/DESIGN.md` is the source of
truth for **behaviour**; the PRD cites it (`§5.3`, `D10`) instead of restating it. Personas live in
`PERSONAS.md`, stories in `USER_STORIES.md`, feature detail in `docs/features/F-XX-*.md`.

## When the PRD must change (AGENTS.md §8 step 1)
Update it when a change affects **scope, goals, non-goals, a requirement, a metric, a phase, or a
deviation from DESIGN.md**. Examples: a new caller endpoint (new FR + maybe DV), pulling a Release 2
item into v1 (scope table + DV), a new latency budget (NFR), dropping a feature (move to non-goals).
Do **not** touch it for implementation detail, refactors or bug fixes; those go in feature docs,
ADRs and tickets.

## Section order (keep it; do not renumber sections)
1. Header table (status, source of truth, related links, last updated)
2. Problem
3. Product summary (incl. the caller invariant: no vendor/account/version visible to callers)
4. Goals (`G1..`) with "Measured by"
5. Non-goals (v1)
6. Personas (summary table; detail in PERSONAS.md)
7. Scope by phase (Phase 1, Phase 2 M0-M8, EP-10, EP-11, Release 2, Release 3) with feature ids
8. Agreed deviations from DESIGN.md (`DV-N`, each with Why and Respects, each backed by an ADR)
9. Functional requirements (`FR-NN`, grouped by area, with feature ids in the group heading)
10. Non-functional requirements (`NFR-NN`, with category)
11. Success metrics
12. Release plan
13. Risks and mitigations
14. Dependencies
15. Open questions

Naming: the repo's *Phase 1/2* are not DESIGN's phases; DESIGN "Phase 2/3" = **Release 2/3**.

## Requirement rows
```markdown
| ID | Requirement | Priority | Source |
| --- | --- | --- | --- |
| FR-54 | `GET /v1/requests/{id}` includes `expires_at` for queued requests | S | §10.7, DV-3 |
| NFR-19 | Latency | Discovery endpoints p99 <= 5 ms from the snapshot | DV-3 |
```
Rules:
- IDs are global, sequential, **never reused or renumbered**. Append the next free number even if
  the row sits in an earlier group. A dropped requirement stays with `~~strikethrough~~` and
  "(dropped YYYY-MM-DD, reason)".
- Priority: `M` (must, v1), `S` (should, v1), `C` (could), `R2` (Release 2). Split rows like `M / R2`
  when part ships later, and say which part.
- One testable statement per row. "Shall be fast" is not a requirement; "p99 <= 10 ms" is.
- Source = DESIGN section, decision (`D1..D12`), deviation (`DV-N`) or open question.
- Use the canonical vocabulary: integration, capability, version, account, tenant, canonical body,
  `Queued`, error codes from DESIGN §4.4.

## Goals vs non-goals
- A goal is an outcome with a measure (`G2: accepted async work is never lost | 0 lost in soak test`).
- A non-goal is something a reader might reasonably expect and we deliberately exclude, with a pointer
  to where it might come later ("Transformer editing in the console: Release 2").
- If a new request contradicts a non-goal, do not quietly delete the non-goal: raise it with the user,
  then record the change (and a DV + ADR if DESIGN is affected).

## Success metrics
Each metric has: name, target, how it is measured (metric name from DESIGN §16.2 or test), and when.
```markdown
| Hub-added sync latency p99 | <= 5 ms | `hub_overhead_ms` p99 at 500 RPS load test (M8) | M8 exit |
```
No vanity metrics ("developer happiness") without a measurement method.

## Deviations (`DV-N`)
Any addition or change versus DESIGN.md goes here **and** gets an ADR (`adr-writing` skill).
State the deviation, why, and which DESIGN rule it still respects. A deviation that breaks a §2
decision or the caller invariant needs the user's explicit approval first.

## Risks and open questions
- Risk row: risk, likelihood (L/M/H), impact (L/M/H), mitigation, owner.
- Open question: `Q-N`, question, why it matters, default we proceed with, who answers, status.
  Mirror DESIGN §20 questions; close them with the answer and date, do not delete.

## Workflow for a scope change
1. Read the affected DESIGN sections and the current PRD rows.
2. Edit PRD: goals/non-goals/scope table/FR/NFR/DV as needed; bump "Last updated".
3. Add or update user stories (`user-stories` skill) and the feature doc (`feature-docs` skill).
4. ADR for any DV (`adr-writing`). Tickets for the work (`tickets`).
5. Commit as `docs(product): <summary> (HUB-###)` with a changelog entry (`commit-and-changelog`).

## Anti-patterns
- Restating DESIGN behaviour in prose that later drifts. Cite the section.
- Requirements naming a vendor in the caller contract ("callers pass `clevertap_account`") - forbidden.
- Renumbering FRs after deletions; links from stories and tickets break.
- Solutions disguised as requirements ("use Redis for X") unless it is a fixed decision (D-table).
