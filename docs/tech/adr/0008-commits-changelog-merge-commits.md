# ADR-0008: Per-commit changelog, Conventional Commits, merge-commit PRs

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead, user |
| DESIGN refs | §0 working rules (commit per milestone); AGENTS §6, §7 |

## Context
Several agents commit in parallel. Reviewers and the user need to see what each change shipped, find the commit for any changelog line, and keep contract → backend → frontend ordering visible in history.

## Options considered
1. **Squash merges + release-time changelog** — tidy `main`, but loses the contract-first commit order and per-commit traceability.
2. **Rebase merges** — linear history, but PR boundaries vanish.
3. **Merge commits + Conventional Commits + one changelog entry per commit.**

## Decision
Option 3:
- Commit subject `<type>(<scope>): <summary ≤ 72 chars> (HUB-###)` with `Why/What/Tests/Refs` body (AGENTS §6).
- Every commit adds its own entry to `CHANGELOG.md` `## [Unreleased]`, summary identical to the subject (AGENTS §7); CI enforces "every commit touches CHANGELOG.md".
- PRs merge with a merge commit; no squash. Branches rebase on `main` before review to keep history readable.
- Details: `docs/process/GIT_WORKFLOW.md`, `docs/process/RELEASE.md`.

## Consequences
- `git log --grep "<summary>"` finds the SHA for any changelog line.
- Changelog grows quickly and has frequent merge conflicts at the top of `[Unreleased]`; resolve by keeping both entries (newest first). Consider a `merge=union` gitattribute for `CHANGELOG.md`.
- Merge commits themselves do not need changelog entries (CI checks `--no-merges`).

## Deviation from DESIGN.md
None.
