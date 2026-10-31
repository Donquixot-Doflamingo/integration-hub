---
name: adr-writing
description: Write Architecture Decision Records for the Integration Hub in docs/tech/adr/NNNN-slug.md from 0000-template.md - when an ADR is required (any deviation from docs/DESIGN.md, a §2 decision, architecture, data model, library or contract-shape choice), numbering, Proposed to Accepted to Superseded lifecycle, and how to record and link deviations (PRD DV-N, milestone Deviations, TECH_SPEC). Use whenever a design is ambiguous or wrong in practice, you pick an option DESIGN.md does not state, you change a decision, you add a significant dependency, or someone asks "why did we do X".
---

# ADRs

Location: `docs/tech/adr/NNNN-<slug>.md`, copied from `docs/tech/adr/0000-template.md`.
DESIGN.md is not edited casually: **deviations are recorded as ADRs** (AGENTS.md §2, §8.3).

## When an ADR is needed
Write one when any of these is true:
- You deviate from or interpret `docs/DESIGN.md` (paths, behaviour, a default) - including additive
  deviations listed in the PRD as `DV-N`.
- A §2 decision (D1-D12) would change, or one of its open alternatives is chosen (e.g. CEL vs expr).
- Architecture, data model, queue, storage, auth or security model changes.
- A significant library/tool is adopted or replaced (router, DB driver, test framework, UI library).
- A contract design choice that others must live with (pagination style, error code addition).
Not needed for: bug fixes, refactors without behavioural change, naming inside a package.
Unsure? Write a short one. Cheap now, expensive to reconstruct later.

## Numbering and naming
```bash
ls docs/tech/adr/ | sort | tail -1     # next number = last + 1, zero-padded to 4 digits
cp docs/tech/adr/0000-template.md docs/tech/adr/0007-cursor-pagination-admin-api.md
```
Slug is kebab-case and names the decision, not the problem. Numbers are never reused.

## Content (follow the template's headings; typical set)
```markdown
# 0007. Cursor pagination for admin list endpoints
- Status: Proposed            # Proposed | Accepted | Superseded by 00NN | Rejected
- Date: 2026-11-05
- Deciders: lead, contract-steward
- Related: DESIGN §16.1, PRD FR-44, F-16, HUB-203

## Context
What forces are at play; quote the DESIGN section; why the current text is insufficient or wrong.
## Decision
One clear statement in the active voice: "Admin list endpoints use opaque cursor pagination ...".
## Options considered
| Option | Pros | Cons |
## Consequences
Positive, negative, follow-ups (tickets), what becomes harder.
## Deviation from DESIGN.md
"None" or exactly which section/decision changes and how; DESIGN rules still respected.
```
Keep it to about a page. Facts and trade-offs, not essays.

## Lifecycle
- **Proposed**: written, under discussion (PR open). Code may be drafted, not merged against it.
- **Accepted**: merged; the decision holds. Changing it later needs a new ADR.
- **Superseded by NNNN**: a later ADR replaces it. Edit only the status line of the old one; the
  new ADR says `Supersedes NNNN`. Never delete or rewrite an accepted ADR.
- **Rejected**: kept for history when a proposal is turned down.
A change to a §2 decision or the caller invariant (vendor/account/version invisible) needs the
user's explicit approval before it can be Accepted.

## Recording a deviation (do all)
1. ADR with the "Deviation from DESIGN.md" section filled.
2. If scope/requirements change: add or update `DV-N` in `docs/product/PRD.md` §7 linking the ADR.
3. Update `docs/tech/TECH_SPEC.md` where it describes the affected design.
4. Link from the feature doc and tickets.
5. List it under **Deviations** (with the ADR link) in the milestone summary (AGENTS.md §10) and
   in the PR template's "Deviations from DESIGN.md".
6. Commit `docs(adr): record cursor pagination for admin lists (HUB-###)` + changelog entry.

## Anti-patterns
- Silent deviations ("I just changed the path").
- Editing DESIGN.md instead of writing an ADR.
- ADRs that list options without a decision, or decisions without the rejected options.
- Rewriting an accepted ADR's decision in place.
