# Repo-wide skills (`.claude/skills/`)

Process skills that apply anywhere in the Integration Hub monorepo. Claude Code loads them
automatically for every contributor and agent. Folder-specific skills live next to the code:

- Backend (Go): [`backend/.claude/skills/README.md`](../../backend/.claude/skills/README.md)
- Frontend (SvelteKit admin console): [`frontend/.claude/skills/README.md`](../../frontend/.claude/skills/README.md)
- Consumer skill for teams that *use* the Hub (distributed, not loaded here): [`clients/claude-skill/`](../../clients/claude-skill/README.md)

| Skill | Origin | Use it when |
| --- | --- | --- |
| [`prd-writing`](prd-writing/SKILL.md) | repo | Changing goals, non-goals, FR/NFR, metrics, scope or deviations in `docs/product/PRD.md` |
| [`user-stories`](user-stories/SKILL.md) | repo | Writing `US-FFNN` stories with Given/When/Then in `docs/product/USER_STORIES.md` |
| [`feature-docs`](feature-docs/SKILL.md) | repo | Creating/updating `docs/features/F-XX-<slug>.md` and the index; status lifecycle |
| [`tickets`](tickets/SKILL.md) | repo | Creating or updating `HUB-###` tickets in `docs/tickets/TICKETS.md`; ID ranges per epic |
| [`commit-and-changelog`](commit-and-changelog/SKILL.md) | repo | Branching, Conventional Commits, per-commit `CHANGELOG.md` entry, `gh pr create`, merge policy |
| [`adr-writing`](adr-writing/SKILL.md) | repo | Recording a decision or deviation from `docs/DESIGN.md` in `docs/tech/adr/` |
| [`contract-change`](contract-change/SKILL.md) | repo | Requesting or making a change in `contracts/` (CCR, OpenAPI edit, semver, notify) |
| [`skill-creator`](skill-creator/SKILL.md) | vendored (see `LICENSE.txt`) | Creating or improving any skill, including evals and description tuning |

Feature work order (AGENTS.md §8): `user-stories` (+ `prd-writing` if scope changes) ->
`feature-docs` -> `adr-writing` (if design changes) -> `contract-change` (if shapes change) ->
`tickets` -> code -> `commit-and-changelog`.

Vendored skills are not edited here; they are listed in `THIRD_PARTY_SKILLS.md`.
