# Integration Hub consumer skill (any AI agent)

A skill for developers on other teams who **use** the Integration Hub. It teaches their AI assistant
to discover integrations, build canonical bodies, dry-run first, invoke with `Idempotency-Key`,
handle `200` vs `202 Queued`, run two-step flows (OTP send then verify with `X-Hub-Session`), follow
the retry rules, verify callback signatures, use the Go `hubclient` SDK and the `hubmcp` MCP tools,
and never move money or quota without the user's confirmation.

**One skill folder, every agent.** `integration-hub/` follows the open Agent Skills layout
(`SKILL.md` with `name` + `description` frontmatter, plus `references/`). Claude Code, claude.ai,
OpenAI Codex and Cursor all load that same folder, so there is no per-tool copy to keep in sync. For
assistants without skill support, paste [`PASTE-INTO-ANY-ASSISTANT.md`](PASTE-INTO-ANY-ASSISTANT.md).

This folder is distributed; it is not loaded by agents inside the Hub repo itself (operators of the
Hub use `.claude/skills/hub-operator/` instead). The folder keeps its historical name
`clients/claude-skill/` so existing links keep working; nothing in it is Claude-specific.

## Contents
```text
clients/claude-skill/
  README.md                       # this file: install per tool
  PASTE-INTO-ANY-ASSISTANT.md     # one-file version for chat assistants, AGENTS.md or Cursor rules
  integration-hub/
    SKILL.md                      # the skill (name: integration-hub)
    references/
      api-quickref.md             # endpoints, headers, error codes, request states
      mcp-setup.md                # hubmcp in Claude Code, Claude Desktop, Codex CLI, Cursor
      examples.md                 # events.track@v1 end to end; two-step OTP (curl + Go)
```

## Install

Run these from a checkout of the Hub repo (`HUB_REPO` = its path). Copy, or symlink if you want
`git pull` in the Hub repo to update the skill.

| Tool | Personal (all your projects) | Per repository (shared via Git) | Docs |
| --- | --- | --- | --- |
| Claude Code | `~/.claude/skills/integration-hub/` | `.claude/skills/integration-hub/` | [code.claude.com/docs/en/skills](https://code.claude.com/docs/en/skills) |
| OpenAI Codex (CLI, IDE, app) | `~/.agents/skills/integration-hub/` | `.agents/skills/integration-hub/` (scanned from the working directory up to the repo root) | [Codex: build skills](https://developers.openai.com/codex/skills) |
| Cursor | `~/.cursor/skills/integration-hub/` (also reads `~/.agents/skills/`, `~/.claude/skills/`) | `.cursor/skills/integration-hub/` (also reads `.agents/skills/`, `.claude/skills/`) | [cursor.com/docs/context/skills](https://cursor.com/docs/context/skills) |
| claude.ai / Claude Desktop | Upload a ZIP: Customize → Skills → **+** → Create skill → Upload a skill (code execution must be on) | Team/Enterprise owners can provision it for the org | [Use skills in Claude](https://support.claude.com/en/articles/12512180-use-skills-in-claude) |
| Any other assistant | Paste `PASTE-INTO-ANY-ASSISTANT.md` into its custom instructions or the chat | Append it to the repo's `AGENTS.md` | — |

### Claude Code
```bash
mkdir -p ~/.claude/skills
cp -R "$HUB_REPO/clients/claude-skill/integration-hub" ~/.claude/skills/integration-hub
# or, per repository:
mkdir -p .claude/skills && cp -R "$HUB_REPO/clients/claude-skill/integration-hub" .claude/skills/
```
Start a new session and ask "how do I send an order_created event through the Hub?"; the skill
should load (it is also available as `/integration-hub`).

### OpenAI Codex
Codex reads `SKILL.md` with `name` and `description` (both present here) from `.agents/skills`, and
follows symlinked skill folders.
```bash
mkdir -p ~/.agents/skills
cp -R "$HUB_REPO/clients/claude-skill/integration-hub" ~/.agents/skills/integration-hub
# or, per repository:
mkdir -p .agents/skills && cp -R "$HUB_REPO/clients/claude-skill/integration-hub" .agents/skills/
```
Restart Codex if it does not appear; invoke it explicitly with `$integration-hub` or `/skills`, or
let Codex pick it from the description.

### Cursor
Cursor loads Agent Skills (the `name` must match the folder name, which it does).
```bash
mkdir -p .cursor/skills && cp -R "$HUB_REPO/clients/claude-skill/integration-hub" .cursor/skills/
```
If your team already installed it under `.claude/skills/` or `.agents/skills/`, Cursor reads it
there too; do not install it twice. Prefer a rule instead? Create `.cursor/rules/integration-hub.mdc`
(project rules must use `.mdc`, [cursor.com/docs/context/rules](https://cursor.com/docs/context/rules))
with this frontmatter, followed by the content of `PASTE-INTO-ANY-ASSISTANT.md`:
```markdown
---
description: Calling third-party vendors through the company Integration Hub (int_... ids, hubclient, Idempotency-Key, X-Hub-Session, callbacks)
alwaysApply: false
---
```

### claude.ai and Claude Desktop
```bash
cd "$HUB_REPO/clients/claude-skill" && zip -r integration-hub.zip integration-hub
```
Upload `integration-hub.zip` under Customize → Skills. The folder name must match the skill name.

### Any assistant, or a repo `AGENTS.md`
`PASTE-INTO-ANY-ASSISTANT.md` is self-contained. Paste it as custom instructions, or append it to
your service repo's `AGENTS.md` (read by Codex, Cursor and many other agents; Codex reads
`~/.codex/AGENTS.md` and every `AGENTS.md` from the repo root down to the working directory, 32 KiB
in total by default: [Codex: AGENTS.md](https://developers.openai.com/codex/guides/agents-md)).

### Then, for every tool
1. Set `HUB_BASE_URL` and `HUB_API_KEY` in your environment from your secret manager, never in code
   or committed files. Ask the platform team for a key of your tenant (scope `invoke`; a developer
   key may add `dry_run`).
2. Optional: add the `hubmcp` MCP server so the assistant can discover, dry-run and invoke for you:
   [`integration-hub/references/mcp-setup.md`](integration-hub/references/mcp-setup.md).

## Version and updates
- Skill version: **0.2.0** (caller contract `hub-public.v1` 1.0.1; MCP tools `hubmcp-tools` 1.1.0).
- The skill is maintained by the Hub's `contract-steward` and docs owners. It is updated in the same
  PR as any caller-visible change (Hub `AGENTS.md` §8 step 9), so `main` of the Hub repo always holds
  the current version.
- Updates are announced in the Hub repo's `CHANGELOG.md` (entries with scope `skill` or
  `clients`) and caller-contract changes in `contracts/CHANGELOG.md`.
- To update, re-run the copy command (it overwrites the folder) or use a symlink. Keep local edits
  out of the copied folder, or contribute them back via a PR to the Hub repo.

## Feedback
Wrong or missing guidance? Open a ticket/PR in the Hub repo (area `docs`, epic EP-11).
