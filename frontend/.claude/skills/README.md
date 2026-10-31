# Frontend skills (`frontend/.claude/skills/`)

Skills for the SvelteKit 2 + Svelte 5 admin console (feature F-19, epic EP-10). Claude Code loads
them automatically when working under `frontend/`. Repo-wide process skills (tickets, commits,
changelog, contract changes, feature docs) live in [`../../../.claude/skills/`](../../../.claude/skills/README.md).

| Skill | Origin | Use it when |
| --- | --- | --- |
| [`svelte5-sveltekit`](svelte5-sveltekit/SKILL.md) | repo | Writing any `.svelte`, `+page`/`+layout`/hooks file; runes, snippets, load, form actions, auth hook, env, Svelte 4 -> 5 pitfalls |
| [`api-client-from-contract`](api-client-from-contract/SKILL.md) | repo | Calling `/admin/v1`, `npm run gen:api`, the `client.ts` wrapper, error envelope, MSW handlers from spec examples, filing a contract change request |
| [`frontend-testing`](frontend-testing/SKILL.md) | repo | Vitest + Testing Library, MSW, Playwright e2e for critical flows, axe, screenshot tests |
| [`admin-console-ux`](admin-console-ux/SKILL.md) | repo | Any screen or dialog: confirm + reason + change_log id, diffs, secrets, breaker status, tables, a11y |
| [`frontend-design`](frontend-design/SKILL.md) | vendored (see `LICENSE.txt`) | Visual direction: palette, typography, layout personality |
| [`webapp-testing`](webapp-testing/SKILL.md) | vendored (see `LICENSE.txt`) | Ad-hoc Python Playwright exploration of a running app (screenshots, console logs); not for CI tests |

Typical order for a new screen: ticket -> `api-client-from-contract` (regenerate, check fields exist)
-> `svelte5-sveltekit` -> `admin-console-ux` (+ `frontend-design`) -> `frontend-testing`.

Vendored skills are not edited here; they are listed in `THIRD_PARTY_SKILLS.md`. To add or improve a
skill, use the repo-wide `skill-creator` skill.
