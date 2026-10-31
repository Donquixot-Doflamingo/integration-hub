# Release Process

## Versioning
- Repo releases: semver tags `vX.Y.Z` on `main` (GIT_WORKFLOW §6). Pre-1.0 during phases 1–2: minor = milestone(s) shipped, patch = fixes.
- Contracts version independently via `info.version` and `contracts/CHANGELOG.md` (ADR-0004). Release notes list the contract versions included.
- Images are tagged with the git SHA and the release tag (TECH_SPEC §16).

## Changelog promotion
1. On a branch `chore/HUB-###-release-vX.Y.Z`, rename `## [Unreleased]` in `CHANGELOG.md` to `## [X.Y.Z] — YYYY-MM-DD` and add a new empty `## [Unreleased]` above it.
2. That commit carries its own changelog entry (in the new `[Unreleased]`): `chore(release): prepare vX.Y.Z (HUB-###)`.
3. PR, review, merge (merge commit).

## Tagging
```bash
git switch main && git pull
git tag -a vX.Y.Z -m "vX.Y.Z"   # on the merge commit of the release PR
git push origin vX.Y.Z
```
CI on the tag builds images, SBOM and provenance (SECURITY §8) and publishes release notes from the changelog section.

## Deploy checklist (pointer)
Before deploying a tag to staging/prod:
- [ ] CI green on the tag, including load L1–L5 in staging and the latest L6 soak (OBSERVABILITY §12, TESTING_STRATEGY §14).
- [ ] Migrations reviewed as expand/contract; pre-deploy job dry-run on staging.
- [ ] Rollout order and rollback per TECH_SPEC §16.
- [ ] Alerts and dashboards for new metrics deployed (`deploy/alerts`, `deploy/dashboards`).
- [ ] Use the `engineering:deploy-checklist` skill for the full checklist and record the outcome in the release ticket.

Hotfixes: GIT_WORKFLOW §7.
