# Integration Hub — Personas

Personas used across [PRD](PRD.md), [USER_STORIES](USER_STORIES.md) and the feature docs. Short names in **bold** are used in stories ("As a **service developer**…").

## Surface map

| Persona | Caller API | `hubclient` SDK | Admin API | Admin console | `hubctl` | `hubmcp` | Consumer skill |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Service developer | ● | ● | | ○ (`tenant_viewer`) | | ○ | ○ |
| Platform engineer | ○ (dry-run) | | ● | ● | ● | ○ (admin read) | |
| On-call SRE | | | ● | ● | ○ (`replay`) | ○ (admin read) | |
| Integration admin / reviewer | | | ● | ● | ● (`plan/apply`) | | |
| Compliance / audit officer | | | ● (read, unseal) | ● | | | |
| AI-assisted developer | via MCP | | | | | ● | ● |

● primary · ○ occasional

---

## 1. Service developer (tenant / caller)

**Who:** backend engineer on a product team, e.g. `orders-service`, `loans-origination`. Owns the tenant's API keys and callback endpoints. RBAC role in the console: `tenant_viewer`.

| | |
| --- | --- |
| **Goals** | Send "track this event" / "pull this bureau report" / "chat with an LLM" in one canonical call; never learn a vendor's API; get a clear answer (done, queued, failed and why); never ship code for a vendor or account change. |
| **Pains today** | Maintains a CleverTap client, a retry loop and a key in their own service; redeploys when brand B needs a new field; no idea whether a failure is theirs or the vendor's; outages lose events silently; duplicate submissions double-charge bureau pulls. |
| **How they touch the Hub** | `POST /v1/integrations/{id}/invoke` via `hubclient` (`Result` vs `Queued`); `GET /v1/requests/{id}` for polling; `Idempotency-Key` on every call to a `moves_money_or_quota` integration; registers a callback endpoint once and verifies `X-Hub-Signature`; reads their own requests in the console (`tenant_viewer`). |
| **Success looks like** | Integration in an afternoon; zero deploys when platform swaps vendors; every error code tells them whether to retry. |

## 2. Platform engineer (Hub owner, authors transformers)

**Who:** member of the platform team that owns the Hub (D7). Writes vendor definitions, accounts and transformers; reviews transformer PRs from other teams.

| | |
| --- | --- |
| **Goals** | Onboard a new API or vendor from the console in ≤ 2 days, without code (ADR-0023); prove a transformer correct with samples before it runs; keep Hub overhead ≤ 5 ms p99; operate one place for keys, limits and breakers. |
| **Pains today** | Vendor docs drift from what services actually send; no way to see what a team sent; every team reinvents throttling; key rotation means chasing N teams. |
| **How they touch the Hub** | Console: vendors and endpoints (request schema from a pasted sample), accounts, transformers with samples, integration operations; edits YAML under `backend/config/` in Git for local dev; `hubctl transform test` (samples), `hubctl dry-run`, `hubctl plan/apply`; admin API for accounts (test-connection, rotate, lifecycle); console transformer viewer with dry-run; benchmarks (`make bench`); mockvendor for every test. |
| **Success looks like** | Transformer + goldens + publish in one PR; type errors caught at compile; no secret ever in a log line. |

## 3. On-call SRE

**Who:** the engineer holding the Hub pager (platform team rotation).

| | |
| --- | --- |
| **Goals** | Know within minutes that a vendor/account is down, contain it (fail fast, park), confirm automatic recovery, explain any single failed request, replay dead letters after a fix. |
| **Pains today** | Outages reported by N teams with N symptoms; no shared breaker; manual replays from logs; retry storms on recovery. |
| **How they touch the Hub** | Alerts (§16.3) → console vendor & account health board (breaker states, parked count/age) → request explorer timeline → DLQ console (replay by filter) → breaker force-open/close, integration pause; `hubctl replay` for scripted replays; optionally `hub_admin_*` MCP tools from Claude. |
| **Success looks like** | Breaker opens before callers notice; drain starts ≤ 60 s after recovery with no 429 storm; MTTR measured in minutes. |

## 4. Integration admin / reviewer

**Who:** senior platform engineer or tech lead with `admin`/`editor` role who approves and executes cutovers.

| | |
| --- | --- |
| **Goals** | Publish a new integration version with confidence; canary by weight or shadow; roll back in one action; keep history linear and attributable. |
| **Pains today** | Vendor swaps are big-bang deploys across teams; no canary; rollback means another round of deploys; nobody knows who changed what. |
| **How they touch the Hub** | Console integration detail: version history, diff, publish, rollback, weights, pause; `hubctl plan` diff review in a PR then `hubctl apply`; change log screen; watches per-version metrics during canary. |
| **Success looks like** | Account A→B swap including canary in ≤ 1 working day, zero caller deploys; every change has an author and reason in `change_log`. |

## 5. Compliance / audit officer

**Who:** risk/compliance team member responsible for regulated data (bureau, Aadhaar, PAN, AA) and vendor audits.

| | |
| --- | --- |
| **Goals** | Prove exactly what was sent to and received from a regulated vendor for a given request; ensure PII is masked or sealed per policy; control and review every unseal; enforce retention and subject deletion. |
| **Pains today** | Evidence scattered across team logs, often containing raw PII; no record of who read sensitive payloads; retention unenforced. |
| **How they touch the Hub** | Console request explorer (masked timeline, sha256 + size proving losslessness); change log; audited unseal (Release 2, F-22) with reason; retention and deletion reports via admin API (`viewer` role, unseal permission). |
| **Success looks like** | Any audit question about a request answered from one timeline; unseal events are themselves audited; sealed payloads unreadable without it. |

## 6. AI-assisted developer

**Who:** a service developer (persona 1) working in Claude Code or another MCP client on their laptop.

| | |
| --- | --- |
| **Goals** | Ask "what can I call to send a WhatsApp message?" and get a real answer; generate a correct body from the operation's request schema and client code; dry-run before writing code; check a request's status without leaving the editor. |
| **Pains today** | Has to read the PRD, find what each integration expects, and guess integration IDs; AI assistants hallucinate vendor APIs and suggest calling CleverTap directly. |
| **How they touch the Hub** | Runs `hubmcp` locally (stdio) with `HUB_BASE_URL` + their tenant `HUB_API_KEY`; tools `hub_list_integrations`, `hub_describe_integration`, `hub_dry_run`, `hub_invoke` (with `confirm: true` for money/quota), `hub_get_request`; installs the consumer skill into `~/.claude/skills/integration-hub/` so Claude knows the rules (Idempotency-Key, `Queued` vs `Result`, signature verification). |
| **Success looks like** | From question to a working, idempotent `hubclient` call in one session, with no vendor names in the code and no accidental real side effects. |
