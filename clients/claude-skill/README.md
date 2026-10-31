# Integration Hub consumer skill for Claude

A Claude Code skill for developers on other teams who **use** the Integration Hub. It teaches Claude
to discover integrations, build canonical bodies, dry-run first, invoke with `Idempotency-Key`,
handle `200` vs `202 Queued`, follow the retry rules, verify callback signatures, use the Go
`hubclient` SDK and the `hubmcp` MCP tools, and never move money or quota without your confirmation.

This folder is distributed; it is not loaded by Claude Code inside the Hub repo itself.

## Contents
```text
clients/claude-skill/
  README.md                    # this file
  integration-hub/
    SKILL.md                   # the skill (name: integration-hub)
    references/
      api-quickref.md          # endpoints, headers, error codes, request states
      mcp-setup.md             # adding hubmcp to Claude Code
      examples.md              # events.track@v1 end to end (curl + Go)
```

## Install
Personal (all your projects):
```bash
mkdir -p ~/.claude/skills
cp -R clients/claude-skill/integration-hub ~/.claude/skills/integration-hub
```
Per repository (shared with your team via Git):
```bash
mkdir -p .claude/skills
cp -R /path/to/integration-hub-repo/clients/claude-skill/integration-hub .claude/skills/integration-hub
```
Then set `HUB_BASE_URL` and `HUB_API_KEY` in your environment (from your secret manager, never in
code) and, optionally, add the MCP server: see `integration-hub/references/mcp-setup.md`.
Start a new Claude Code session; ask something like "how do I send an order_created event through
the Hub?" to check the skill triggers.

## Version and updates
- Skill version: **0.1.0** (Phase 1 draft; matches the caller contract as documented in the Hub repo).
- The skill is maintained by the Hub's `contract-steward` and docs owners. It is updated in the same
  PR as any caller-visible change (Hub `AGENTS.md` §8 step 9), so `main` of the Hub repo always holds
  the current version.
- Updates are announced in the Hub repo's `CHANGELOG.md` (entries with scope `skill` or
  `clients`) and caller-contract changes in `contracts/CHANGELOG.md`.
- To update, re-run the copy command (it overwrites the folder). Keep local edits out of the copied
  folder, or contribute them back via a PR to the Hub repo.

## Feedback
Wrong or missing guidance? Open a ticket/PR in the Hub repo (area `docs`, epic EP-11).
