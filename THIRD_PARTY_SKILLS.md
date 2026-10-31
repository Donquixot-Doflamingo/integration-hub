# Third-party skills

These skills are vendored unchanged from Anthropic's official repository [anthropics/skills](https://github.com/anthropics/skills), commit `ca45fc496a3436251f191354091d557da31c15b5` (fetched 2026-10-31). Each keeps its `LICENSE.txt` (Apache-2.0).

| Skill | Location | Why |
| --- | --- | --- |
| `skill-creator` | `.claude/skills/skill-creator/` | Create, test and improve the repo's own skills |
| `mcp-builder` | `backend/.claude/skills/mcp-builder/` | Design guidance for the `hubmcp` server |
| `frontend-design` | `frontend/.claude/skills/frontend-design/` | Visual direction for the admin console |
| `webapp-testing` | `frontend/.claude/skills/webapp-testing/` | Playwright-driven checks of the running console |

All other skills in the repo are first-party, written for this project.

**Policy:** add a third-party skill only from a known source with a clear license, record it here with the source commit, and do not edit it in place. Wrap it with a first-party skill instead.
To update one, re-copy it from a newer commit and update the commit hash above in the same PR.
