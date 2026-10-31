---
name: feature-docs
description: Create or update docs/features/F-XX-slug.md from docs/features/_TEMPLATE.md, keep docs/features/README.md (the index) in sync, and drive the status lifecycle proposed to in-progress to shipped. Defines required sections for the Integration Hub - contract refs, observability (metrics/logs/traces/alerts), security/PII, failure modes, stories, tickets. Use whenever a feature is requested or changed, a ticket starts or ships for a feature, someone asks "is there a doc for X", or observability, security or failure behaviour of a feature must be written down.
---

# Feature docs

One doc per feature: `docs/features/F-XX-<slug>.md`, created from `docs/features/_TEMPLATE.md`,
listed in `docs/features/README.md`. It is the working spec for whoever builds the feature: it links
product (stories, FRs), design (DESIGN sections, ADRs), contracts and tickets, and states what to
observe and what can go wrong. DESIGN.md stays the source of truth for behaviour; cite it.

## Create a new feature doc
1. Check the index for an existing feature first; extend rather than duplicate.
2. Next free number: highest `F-XX` in the README + 1. Never reuse a number. Slug: kebab-case, short
   (`F-23-result-cache`).
3. `cp docs/features/_TEMPLATE.md docs/features/F-23-result-cache.md` and fill **every** section;
   write "None" with a reason rather than deleting a section.
4. Add a row to `docs/features/README.md` (id, title, status, epic, milestone, owner area).
5. Link stories (`US-23NN`, `user-stories` skill) and tickets (`tickets` skill) both ways.
6. Commit `docs(features): add F-23 result cache (HUB-###)` + changelog entry.

If `_TEMPLATE.md` and this list disagree, the template wins; tell the user so the skill gets fixed.

## Required sections (what good content looks like)
| Section | Must contain |
| --- | --- |
| Header | id, title, status, epic `EP-xx`, milestone, owner area, last updated |
| Summary / problem | 2-4 sentences, persona and value |
| Scope / non-scope | bullet lists; non-scope points to where it might land (Release 2...) |
| User stories | `US-FFNN` ids with one-line titles |
| Requirements | `FR-NN` / `NFR-NN` from the PRD |
| Design | DESIGN section refs (`§10.5`), ADR links, short description of the approach; diagrams if useful |
| **Contract refs** | OpenAPI file + operationIds / schemas / events touched, contract version that introduced them; "no contract change" if none |
| Data model | tables/columns or config objects added or changed (`migrations/...`) |
| **Observability** | metrics (name, type, labels), log events (msg + key fields), spans, alerts (condition, severity). Mirror into `docs/tech/OBSERVABILITY.md` (AGENTS.md §8 step 6) |
| **Security / PII** | data classes handled, `x-pii` tags, audit payload policy (`full/masked/sealed/metadata_only`), secrets path (`creds.*` only), RBAC role per action, SSRF/egress concerns |
| **Failure modes** | table: failure -> detection -> behaviour (canonical code, state transition) -> recovery |
| Testing | unit, integration (real Postgres/Redis), contract, goldens, e2e; mockvendor scenarios |
| Rollout | flags/config, migration order, how to roll back |
| Tickets | `HUB-###` list with status |
| Open questions | with owner |

### Observability example
```markdown
| Kind | Name | Details |
| --- | --- | --- |
| metric | `parked_count` (gauge) | labels tenant, integration, vendor, account; DESIGN §16.2 |
| metric | `parked_oldest_age_s` (gauge) | page when > 600 s (§16.3) |
| log | `drain.started` | account, endpoint, released, rate_pct; no payloads |
| span | `delivery.drain` | attrs account, endpoint, batch_size |
| alert | ParkedTooOld | `parked_oldest_age_s > 600` for 5 m -> page |
```
Labels never include vendor-identifying data in caller-facing surfaces, and never include secrets
or raw PII.

### Failure modes example
```markdown
| Failure | Detection | Behaviour | Recovery |
| --- | --- | --- | --- |
| Vendor 5xx burst | breaker > 50 % failures / 30 s | sync: `UPSTREAM_UNAVAILABLE` 503 + `retry_after_ms`; async: `parked` | probe every 30 s, ramped drain 20 % -> 100 % |
| Worker crash mid-attempt | visibility timeout | envelope re-claimed; guarded CAS prevents double complete | automatic |
| Transformer throws | `TRANSFORM_FAILED` | 500 sync / `dead_lettered` async; page | fix transformer + goldens, DLQ replay |
```

## Status lifecycle
`proposed -> in-progress -> shipped` (optionally `deprecated` for retired features, with replacement).
- `proposed`: doc written, stories exist, not started.
- `in-progress`: set when the **first** ticket of the feature moves to `in-progress`.
- `shipped`: all must-tickets `done`, observability live, docs match what shipped. Note the release
  or merge commit.
Update the status in the doc header **and** the README index in the same commit.

## Keep it true
- When behaviour changes in a PR, the feature doc changes in the same PR.
- A deviation from DESIGN found while building -> ADR (`adr-writing`) + link here + PRD DV if scope.
- If callers see the change, also update `clients/claude-skill/` and MCP tool docs (AGENTS.md §8.9).

## Anti-patterns
- Copying large DESIGN passages (they drift). Cite and summarise.
- "Observability: TBD" on a shipped feature.
- Failure modes that say "returns an error" without the canonical code and state.
- Feature docs not in the index; numbers reused after a feature was dropped.
