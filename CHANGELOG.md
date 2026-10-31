# Changelog

One entry **per commit**, newest first, added in the same commit (see `AGENTS.md` §7).
Format: `- YYYY-MM-DD · `type(scope)` · <commit subject summary> · HUB-### · F-XX`, followed by indented user-visible effects.
Find the SHA for an entry with `git log --grep "<summary>"`. Contract versions are tracked separately in `contracts/CHANGELOG.md`.

## [Unreleased]

- 2026-10-31 · `docs` · add agreed design, agent manual and repo skeleton · HUB-001 · F-01
  - docs/DESIGN.md saved unchanged as the behaviour source of truth
  - AGENTS.md / CLAUDE.md with language rule, working rules, decisions D1–D12, contract-first, tickets, git and changelog rules
  - backend/ and frontend/ AGENTS.md + CLAUDE.md; README, .gitignore, .editorconfig
