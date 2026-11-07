# Integration Hub

One vendor-agnostic API through which every internal service talks to every third party: engagement tools, SMS/WhatsApp, credit bureaus, KYC, Account Aggregators, payment rails and LLMs.

Callers send a **stable integration ID**, an operation and a **body that matches the operation's request schema**. The Hub picks the vendor, the account and the transformer, applies delivery policy, rate limits and circuit breakers, calls the vendor now or via a durable queue, maps the response back (or passes the vendor's answer through), and keeps a lossless audit trail. Operators onboard a new API from the admin console without code: the integration owns the caller's input schema, the vendor endpoint owns its request schema, and samples prove the transformer between them ([F-23](docs/features/F-23-schema-registry.md)). Swapping a vendor or an account is a config publish. Callers change nothing in their requests.

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
| [`contracts/`](contracts/) | Frozen OpenAPI 3.1 contracts, event schemas, MCP tool contract |
| [`backend/`](backend/) | Go: `hub` (gateway, worker, scheduler, admin), `hubctl`, `mockvendor`, `hubmcp` |
| [`frontend/`](frontend/) | SvelteKit admin console |
| [`clients/claude-skill/`](clients/claude-skill/) | Claude skill for teams that use the Hub |
| [`deploy/`](deploy/) | docker-compose, alert rules, dashboards |
| [`examples/`](examples/) | Runnable examples (registry overlays + tests), e.g. [`otp-account-flow`](examples/otp-account-flow/README.md) |

## Working here
Read [`AGENTS.md`](AGENTS.md) first: contract-first workflow, tickets, branch and commit rules, the per-commit changelog, and how to document a feature. AI agents get the same rules through `CLAUDE.md` and the skills under `.claude/`, `backend/.claude/` and `frontend/.claude/`. A new contributor needs no personal setup.

## Run the whole stack locally (manual testing)
One command starts Postgres and Redis, applies migrations and the dev seed, and runs mockvendor, the four hub roles (admin with the dev OIDC issuer, gateway, worker, scheduler) and the admin console against the real admin API:

```bash
make dev-up      # or scripts/dev-up.sh
make dev-down    # or scripts/dev-down.sh; --purge also deletes .dev-stack/ (logs, dev credentials, blobs)
```

- **Needs**: Go, Node 22 (`brew install go node@22` on macOS), curl. Postgres and Redis come from `deploy/docker-compose.yml` when Docker runs (on macOS: `colima start`); otherwise set `HUB_PG_DSN` and `HUB_REDIS_ADDR` to services you already run (default `postgres://hub:hub@localhost:5432/hub` and `localhost:6379`).
- **It prints** the console URL (http://localhost:5173), the admin API, gateway, mockvendor and ops URLs, and the dev tenant API key for `orders-service`. Sign in to the console by picking a role (admin, editor, viewer, or tenant viewer with tenant `orders-service`); the admin's dev issuer signs the token, there is no password. A few sample requests (and one dead letter) are sent so every screen has data; `DEV_SEED_TRAFFIC=0` skips them.
- **"Could not load integrations" (`INTERNAL_ERROR`) on most pages** means the dev database kept registry snapshots from an earlier run (the compose volume survives a fresh clone or a deleted `.dev-stack/`) while their files under `.dev-stack/blobs/` are gone. `dev-up` now stops with this explanation; `DEV_RESET=1 make dev-up` recreates the compose Postgres and Redis and starts clean. A database you named with `HUB_PG_DSN` is never dropped.
- **Ports** move with `DEV_CONSOLE_PORT`, `DEV_GATEWAY_PORT`, `DEV_ADMIN_PORT`, `DEV_MOCKVENDOR_PORT`, `DEV_MOCKVENDOR_CONTROL_PORT`, `DEV_OPS_PORT_BASE`; `DEV_SKIP_BUILD=1` reuses the last build. Logs: `.dev-stack/logs/<name>.log`. Details: header of [`scripts/dev-up.sh`](scripts/dev-up.sh) and [`frontend/README.md`](frontend/README.md#against-a-running-hub-hub-286).

## Using the Hub from your laptop
Copy [`clients/claude-skill/integration-hub`](clients/claude-skill/) into `~/.claude/skills/` and add the `hubmcp` MCP server. See the skill's `references/mcp-setup.md`.
