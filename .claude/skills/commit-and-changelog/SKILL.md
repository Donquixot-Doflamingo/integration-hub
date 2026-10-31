---
name: commit-and-changelog
description: Git workflow for the Integration Hub monorepo - Conventional Commit messages per AGENTS.md §6 (type(scope) summary (HUB-###) + Why/What/Tests/Refs body), branch naming type/HUB-###-slug, never committing to main, the per-commit CHANGELOG.md entry per AGENTS.md §7 added in the same commit with summary equal to the commit subject, a pre-commit checklist, opening a PR with gh pr create and the repo template, and the merge-commit (no squash) policy. Use whenever you are about to commit, write a commit message, create a branch, update CHANGELOG.md, open or describe a PR, or merge.
---

# Commits, changelog and PRs

Full process: `docs/process/GIT_WORKFLOW.md`. This skill is the short, exact version.
Only commit or push when the user has asked you to.

## Branches
- **Never commit or push to `main`.** `main` changes only by merging a PR.
- Name: `<type>/HUB-###-<short-slug>`: `feat/HUB-147-ramped-drain`, `fix/HUB-212-dlq-filter-utc`,
  `contract/HUB-081-discovery-endpoints`. Phase-wide work may use `phase-N/<slug>`.
```bash
git switch main && git pull --ff-only
git switch -c feat/HUB-147-ramped-drain
```

## Commit message
```
<type>(<scope>): <imperative summary, <= 72 chars> (HUB-###)

Why: <the problem or motivation>
What:
- <change 1>
- <change 2>
Tests: <what was run and the result>

Refs: HUB-###[, HUB-###]
```
- Types: `feat fix perf refactor test docs contract chore build ci`.
- Scope: package or folder: `edge`, `registry`, `transform`, `delivery`, `upstream`, `frontend`,
  `contracts`, `docs`, `mcp`, `deploy`, ...
- Subject: imperative ("add", not "added"/"adds"), no trailing period, the whole first line
  (including ` (HUB-###)`) <= 72 chars.
- One logical change per commit; tests and docs ride in the same commit as the code.
- Contract changes are alone in their commit, first: `contract(<area>): ...`.

Good:
```
feat(delivery): release parked envelopes with ramped drain (HUB-147)

Why: after a vendor outage all parked work was released at once and hit 429s.
What:
- drain scheduler ramps 20% -> 100% of account limit, doubling every 30 s
- stop releasing when the breaker re-opens
- parked_count / parked_oldest_age_s gauges
Tests: make test (race) ok; make itest outage scenario ok

Refs: HUB-147
```
Bad (and why):
- `fixed stuff` - no type, scope, ticket or meaning.
- `feat: Added drain logic and also refactored queue and updated UI (HUB-147)` - past tense, several changes, no scope.
- `feat(delivery): release parked envelopes with ramped drain` - missing `(HUB-###)`.
- `contract(caller)+feat(edge): add discovery` - contract mixed with code.

## CHANGELOG.md: one entry per commit, in the same commit
Add at the **top** of `## [Unreleased]`:
```
- 2026-11-04 · `feat(delivery)` · release parked envelopes with ramped drain · HUB-147 · F-13
  - Parked requests drain gradually after a vendor recovers instead of all at once
  - New gauges parked_count and parked_oldest_age_s
```
- Date = commit date (YYYY-MM-DD). The summary text **equals the commit subject summary** (without
  `type(scope):` and without ` (HUB-###)`), so `git log --grep "<summary>"` finds the SHA.
- Feature `F-XX` last; use `—` if the commit has no feature (pure chore).
- Bullets are user/operator-visible effects, not file lists.
- Amending the subject? Amend the changelog line too.
- Releases: `[Unreleased]` becomes `## [x.y.z] — YYYY-MM-DD`. Contract versions are separate in
  `contracts/CHANGELOG.md`.

## Pre-commit checklist
- [ ] On a `<type>/HUB-###-slug` branch, not `main` (`git branch --show-current`).
- [ ] Ticket exists and is `in-progress`; status/criteria updated if this commit completes something.
- [ ] Backend: `cd backend && make test` (race) and `make itest` green; `make lint` clean.
- [ ] Frontend: `cd frontend && npm run check && npm run lint && npm test` green.
- [ ] Contract touched? It is in its own earlier commit, version bumped, `contracts/CHANGELOG.md` updated.
- [ ] No secrets, tokens, real customer data or real vendor keys in diff, fixtures or logs (`git diff --cached`).
- [ ] No vendor/account/version leaking into the caller API.
- [ ] Feature doc / stories / ADR updated if behaviour changed.
- [ ] `CHANGELOG.md` entry added at the top of `[Unreleased]`, summary == subject.
```bash
git add -p && git diff --cached --stat
git commit   # write the message in the editor, or -F a file; never --no-verify
```

## Opening a PR
The PR body follows `.github/pull_request_template.md` (Ticket(s), What and why, Contract impact,
Checklist, Deviations from DESIGN.md).
```bash
git push -u origin feat/HUB-147-ramped-drain
gh pr create --base main \
  --title "feat(delivery): release parked envelopes with ramped drain (HUB-147)" \
  --body-file /path/to/pr-body.md     # filled copy of .github/pull_request_template.md
```
- Title = the main commit subject (or the ticket's headline commit for multi-commit PRs).
- Fill every template section; tick only what is true; Deviations "None" or ADR links.
- Set the ticket to `in-review`.

## Merging
- **Merge commit only** (`gh pr merge --merge`), never squash or rebase-merge: per-commit history and
  per-commit changelog entries must survive.
- After merge: ticket `done` with the merge commit subject; feature status updated if it shipped.

## Anti-patterns
- Committing to `main`, force-pushing shared branches, `--no-verify`.
- A "changelog catch-up" commit adding entries for earlier commits.
- One PR containing several tickets' worth of unrelated changes.
