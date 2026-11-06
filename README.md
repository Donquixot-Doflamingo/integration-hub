# Integration Hub

One vendor-agnostic API through which every internal service talks to every third party: engagement tools, SMS/WhatsApp, credit bureaus, KYC, Account Aggregators, payment rails and LLMs.

Callers send a **stable integration ID** and a **canonical body**. The Hub picks the vendor, the account and the transformer, applies delivery policy, rate limits and circuit breakers, calls the vendor now or via a durable queue, maps the response back, and keeps a lossless audit trail. Swapping a vendor or an account is a config publish. Callers change nothing.

**What can it do for my team?** See the [use-case catalogue](docs/use-cases/README.md): how a call flows, events, vendor swaps, outages, callbacks, retries, two-step OTP flows and the lending patterns, each with the exact requests and whether it works today or is planned.

> **Status: Phase 2, M0–M2 done**: scaffold, config registry, transformer engine ([M0](docs/milestones/M0.md), [M1](docs/milestones/M1.md), [M2](docs/milestones/M2.md)). Next: M3 gateway sync path and audit.

```bash
make doctor                 # toolchain check
make up                     # Postgres 16 + Redis 7
make -C backend test itest  # unit (-race) + integration
deploy/smoke/m0.sh          # M0 acceptance smoke test
make -C backend goldens     # transformer golden fixtures
```

## Monorepo
| Path | Contents |
| --- | --- |
| [`docs/DESIGN.md`](docs/DESIGN.md) | Agreed design: behaviour source of truth |
| [`docs/product/`](docs/product/) | PRD, personas, user stories |
| [`docs/use-cases/`](docs/use-cases/README.md) | Use-case catalogue: what works today vs planned, with the caller's exact requests |
| [`docs/features/`](docs/features/) | One doc per feature (`F-XX`) |
| [`docs/tech/`](docs/tech/) | Tech spec, observability plan, testing strategy, security, ADRs |
| [`docs/tickets/TICKETS.md`](docs/tickets/TICKETS.md) | Ticket backlog (`HUB-###`) |
| [`docs/process/`](docs/process/) | Git workflow, contract changes, releases |
| [`contracts/`](contracts/) | Frozen OpenAPI 3.1 contracts, capability and event schemas, MCP tool contract |
| [`backend/`](backend/) | Go: `hub` (gateway, worker, scheduler, admin), `hubctl`, `mockvendor`, `hubmcp` |
| [`frontend/`](frontend/) | SvelteKit admin console |
| [`clients/claude-skill/`](clients/claude-skill/) | Claude skill for teams that use the Hub |
| [`deploy/`](deploy/) | docker-compose, alert rules, dashboards |
| [`examples/`](examples/) | Runnable examples (registry overlays + tests), e.g. [`otp-account-flow`](examples/otp-account-flow/README.md) |

## Working here
Read [`AGENTS.md`](AGENTS.md) first: contract-first workflow, tickets, branch and commit rules, the per-commit changelog, and how to document a feature. AI agents get the same rules through `CLAUDE.md` and the skills under `.claude/`, `backend/.claude/` and `frontend/.claude/`. A new contributor needs no personal setup.

## Using the Hub from your laptop
Copy [`clients/claude-skill/integration-hub`](clients/claude-skill/) into `~/.claude/skills/` and add the `hubmcp` MCP server. See the skill's `references/mcp-setup.md`.
