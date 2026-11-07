# Git Workflow

Summary in AGENTS §6; rationale in ADR-0008. This file is the full procedure.

## 1. Branches
- **`main`** is protected. It changes only by merging a PR. Nobody (human or agent) commits or pushes to it directly.
- Work branches: **`<type>/HUB-###-<short-slug>`**, lower-case kebab slug, one ticket per branch.
  - `feat/HUB-031-tenant-auth`, `fix/HUB-077-claim-double-ack`, `contract/HUB-040-discovery-endpoints`, `docs/HUB-005-tech-spec`
  - `<type>` is one of the commit types below.
- Phase-wide work (e.g. the phase-1 documentation foundation) may use **`phase-N/<slug>`**: `phase-1/foundation`.
- Hotfix branches: `fix/HUB-###-<slug>` cut from the release tag (§7).
- Delete branches after merge.

## 2. Commit messages (Conventional Commits)
Format (copied from AGENTS §6):
```
<type>(<scope>): <imperative summary, ≤ 72 chars> (HUB-###)

Why: <the problem or motivation>
What: <bullet list of the changes>
Tests: <what was run and the result>

Refs: HUB-###[, HUB-###]
```
- Types: `feat fix perf refactor test docs contract chore build ci`.
- Scopes: package or folder — `edge`, `registry`, `transform`, `resolve`, `creds`, `upstream`, `queue`, `delivery`, `audit`, `callbacks`, `webhooks`, `control`, `telemetry`, `hubctl`, `mockvendor`, `hubmcp`, `hubclient`, `frontend`, `contracts`, `deploy`, `docs`.
- Breaking change: `!` after the scope (`feat(control)!: …`) and a `BREAKING CHANGE:` footer. Breaking caller-API changes are almost never allowed (D11); breaking admin-API changes need the user's approval (AGENTS §4.4).
- One logical change per commit; tests and docs in the same commit as the code.
- **Every commit adds its own `CHANGELOG.md` entry** with the identical summary (AGENTS §7). CI fails a PR containing a non-merge commit that does not touch `CHANGELOG.md`.
- Agent-authored commits end with the attribution trailer configured for the session (e.g. `Co-Authored-By:`).

### Good
```
feat(upstream): open breaker after consecutive timeouts (HUB-052)

Why: DESIGN §10.4 requires tripping on 5 consecutive timeouts, with
integration overrides able to tighten it; only the ratio rule existed.
What:
- add consecutive-timeout counter per (account, endpoint)
- honour breaker.override.open_after_consecutive_timeouts
- publish breaker_transitions_total{from,to}
Tests: make test (race) ok; chaos C1 ok; BenchmarkBreakerRecord 41 ns/op

Refs: HUB-052
```
```
contract(admin): add operations to integration versions (HUB-432)

Why: integrations own their request schema once capabilities are gone (ADR-0023).
What:
- IntegrationSpec.operations with request_schema and validate_input
- info.version 1.2.0 -> 2.0.0 (owner-approved break); contracts/CHANGELOG.md entry
Tests: redocly lint ok; examples validate

Refs: HUB-432
```

### Bad
| Message | Problem |
| --- | --- |
| `fixed stuff` | no type, scope, ticket or body |
| `feat: Added breaker and also refactored the queue and updated docs (HUB-052)` | past tense, two logical changes, no scope, > 72 chars |
| `feat(upstream): open breaker after timeouts` | no ticket id, no body |
| `contract(public)+feat(edge): discovery endpoints (HUB-040)` | contract and code in one commit (must be separate, contract first) |
| `fix(creds): log token for debugging (HUB-090)` | violates "never log secrets" — reject in review |

## 3. Pull requests
1. Rebase the branch on the latest `main` (`git fetch && git rebase origin/main`), run `make check` (and `make itest` for backend).
2. Open the PR with `.github/pull_request_template.md`: tickets, what/why with feature doc and story links, contract impact, checklist, deviations with ADR links.
3. Title = the main commit subject (or `phase-N: <summary>` for phase branches).
4. Order inside a PR train: **contract commit → backend commits → frontend commits** (`docs/process/CONTRACT_CHANGES.md` §5). A contract change can also be its own PR merged first.
5. Reviews: at least one approval; CODEOWNERS: `contracts/` → contract-steward owner, `backend/internal/transform/`, `backend/config/transformers/` → platform team (D7).
6. CI must be green (TESTING_STRATEGY §14).
7. Ticket status: `in-review` on opening; `done` + merge commit subject after merge (AGENTS §5).

## 4. Merging
- **Merge commit only** (no squash, no rebase-merge) so per-commit history and changelog lines map 1:1 to SHAs.
- Merge commit message: `Merge pull request #N from <branch>` + PR title (GitHub default is fine).
- If `main` moved after approval, rebase again (not a merge from `main` into the branch) and re-run CI.

## 5. Rebasing rules
- Rebase your own branch freely before review; after review starts, prefer adding fix-up commits, then `git rebase -i --autosquash` only with the reviewer's agreement (agents cannot use interactive rebase; use `git commit --fixup` + `GIT_SEQUENCE_EDITOR=: git rebase -i --autosquash origin/main`).
- Never rewrite `main` or someone else's branch. `git push --force-with-lease` only on your own branch.
- `CHANGELOG.md` conflicts at the top of `[Unreleased]`: keep both entries, newest first.

## 6. Release tags
- Semver tags **`vX.Y.Z`** on `main` merge commits, annotated (`git tag -a v0.3.0 -m "v0.3.0"`). Pre-releases `vX.Y.Z-rc.N`.
- Contracts carry their own `info.version`; they are not the repo tag.
- Procedure: `docs/process/RELEASE.md`.

## 7. Hotfixes
1. Branch `fix/HUB-###-slug` from the release tag being patched (`git switch -c fix/HUB-123-x v0.3.0`).
2. Fix with tests + changelog entry; PR into `main` first (normal process).
3. If production needs it before the next release: create `release/v0.3.x` from the tag, cherry-pick the merged commit(s) with `-x`, tag `v0.3.1`, deploy. Never commit directly on `release/*` without a PR.

## 8. Suggested GitHub branch protection (`main`)
- Require a pull request before merging; ≥ 1 approval; dismiss stale approvals on new commits; require review from CODEOWNERS.
- Require status checks: `hygiene`, `contracts`, `backend-lint`, `backend-test`, `backend-itest`, `frontend-check`, `frontend-test`, `e2e-smoke` (names as CI defines them); require branches to be up to date.
- Require conversation resolution.
- Allow **merge commits only** (disable squash and rebase merging in repository settings).
- Require linear history: **off** (incompatible with merge commits).
- Block force pushes and deletions; include administrators.
- Require signed commits: recommended once all contributors (including agent runners) have signing keys.
- Tag protection for `v*`: only maintainers can create.
