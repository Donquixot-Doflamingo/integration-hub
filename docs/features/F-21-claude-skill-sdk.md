# F-21 — Consumer Claude skill & Go SDK (`hubclient`)

## Summary
Two caller-side deliverables: (1) a distributable Claude skill, `clients/claude-skill/integration-hub/SKILL.md`, that developers copy to `~/.claude/skills/integration-hub/` so Claude uses the Hub correctly (PRD DV-5); (2) the Go SDK `backend/pkg/hubclient` with distinct `Result` / `Queued` types, typed errors, idempotency helper and callback signature verifier (DESIGN §4.5, §18.1).

## Status
`in-progress` — 2026-10-31. SDK core shipped (HUB-094, M3): `Invoke` with sealed `Result` / `Queued`, typed `*Error`, request status, dry-run, discovery, `NewIdempotencyKey`. Still to do in EP-11: `VerifySignature`, `Poll`, a derived `IdempotencyKey(namespace, businessID)` (US-2104), opt-in retries, automatic `traceparent` from the context span, and the consumer skill.

## Owner
`contract-steward` + docs (skill); `backend-engineer` (SDK).

## Milestone / Epic
M3 (SDK core) and EP-11 · HUB-300–329 (shared with F-20).

## Problem
Callers misuse async APIs (treating 202 as done), skip idempotency keys, and verify signatures incorrectly. AI assistants do not know Hub rules unless taught.

## Goals / Non-goals
**Goals — skill content**
- When to use the Hub (never call vendors directly; no vendor names in code).
- Discovery: `GET /v1/integrations`, `GET /v1/capabilities/{capability}` or the `hub_*` MCP tools.
- Building canonical bodies from the capability schema; `dry-run` first.
- `Idempotency-Key` rules (mandatory for money/quota; derive from business ids; no PII).
- Handling 200 vs 202 (`accepted|queued|in_flight`), polling `GET /v1/requests/{id}`, error codes and `retryable` / `retry_after_ms`.
- Verifying `X-Hub-Signature` (5-min window, constant-time compare), dedupe on `X-Hub-Delivery-Id`.
- Wiring `hubmcp` (config snippet, env vars, `confirm: true` semantics).
- Go examples with `hubclient`; HTTP examples for other languages.

**Goals — operator skill** (HUB-316)
- One repo skill, `.claude/skills/hub-operator/`, for the people who run the Hub: tenants and keys, vendors, endpoints and accounts, callback endpoints, config changes, cutover (versions, weights, shadow, rollback, D10), transformers and goldens, multi-step flows.
- Every task shown three ways (console, `hubctl`, `hubmcp`), plus the admin API where the others do not reach yet; gaps stated, never papered over.
- MCP never deletes and never carries secrets; the skill sends those actions to the console.

**Goals — SDK**
- `Invoke(ctx, integrationID, body, opts) (Outcome, error)` where `Outcome` is `Result` or `Queued`, never both.
- Typed `*hubclient.Error{Code, Retryable, RetryAfter, RequestID}`.
- `IdempotencyKey(namespace, businessID)` helper; `Poll`; `VerifySignature(secret, header, body, now)`.
- Ignores `X-Hub-Integration-Version` (DESIGN §4.3).

**Non-goals**
- Java/Node SDKs (Release 3); SSE reader for LLM streaming (Release 2, F-22).

## User stories
- [US-2101](../product/USER_STORIES.md) — Skill teaches correct usage
- [US-2102](../product/USER_STORIES.md) — Callback verification sample
- [US-2103](../product/USER_STORIES.md) — `Queued` is not success
- [US-2104](../product/USER_STORIES.md) — Idempotency helper
- [US-2105](../product/USER_STORIES.md) — Operator skill for any AI
- [US-2106](../product/USER_STORIES.md) — Consumer skill in any assistant

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| SDK | `Client.Invoke` (with `WithOperation` for `/invoke/{op}`), `GetRequest`, `DryRun`, `ListIntegrations` / `AllIntegrations`, `GetIntegration`, `GetCapability` (shipped, HUB-094) | `contracts/openapi/hub-public.v1.yaml#invokeIntegration`, `#invokeIntegrationOperation`, `#getRequestStatus`, `#dryRunIntegration`, `#listIntegrations`, `#getIntegration`, `#getCapability` |
| SDK | `VerifySignature` | `contracts/openapi/hub-public.v1.yaml#/components/parameters/CallbackSignature`; shared signature test vectors are not in `contracts/` yet (to be added via CCR) |
| Skill | `clients/claude-skill/integration-hub/SKILL.md` (+ `references/` examples); installs unchanged in Claude Code, claude.ai, Codex and Cursor; `clients/claude-skill/PASTE-INTO-ANY-ASSISTANT.md` for other assistants (HUB-317) | `contracts/openapi/hub-public.v1.yaml` |
| Operator skill | `.claude/skills/hub-operator/SKILL.md` (+ `references/`), linked from `.agents/skills/hub-operator` for Codex (HUB-316) | `contracts/openapi/hub-admin.v1.yaml`, `contracts/mcp/hubmcp-tools.json` |

## Behaviour & rules
1. The SDK decodes 202 into `Queued{RequestID, Status, Reason, Poll, IdempotentReplay}`; callers must type-switch. `Outcome` is sealed (an unexported method), so `*Result` and `*Queued` are its only implementations and a 202 cannot be observed as success. A 202 or 200 with a malformed body is an error, never an outcome.
1a. Every 4xx/5xx becomes `*hubclient.Error{Status, Code, Message, Retryable, RetryAfter, RequestID, Reason, Circuit, Upstream, Details}`. `RetryAfter` comes from `retry_after_ms`, else the `Retry-After` header (seconds or HTTP-date, capped at 24 h). A failure without a Hub envelope (proxy HTML 502, empty 503) still yields an `*Error` with code `INTERNAL_ERROR`, retryable only for 429/502/503/504. `IsRetryable(err)` is false for transport and context errors.
1b. The API key is private to the client: never in errors, `String`/`GoString` or slog output. `X-Hub-Integration-Version` is ignored.
2. SDK retries only when `Retryable` is true and an idempotency key is set, honouring `RetryAfter`; off by default.
3. Skill instructs Claude to prefer MCP tools when available, else HTTP discovery; to dry-run before generating code; never to invent integration IDs.
4. Skill instructs that `hub_invoke` with real side effects requires the user's explicit go-ahead before passing `confirm: true`.
5. Skill content must change whenever caller-visible contracts change (AGENTS.md §8.9).
6. Skill never contains vendor names, account names or real keys.

## Data model impact
None.

## Config impact
SDK options: base URL, API key, timeout, `X-Request-Deadline`, user agent.

## Observability
SDK sends `User-Agent: hubclient-go/<version>` (plus an optional product via `WithUserAgent`) and `traceparent` when the caller passes `WithTraceparent`. Deriving `traceparent` from the span in the context is not built yet (EP-11). The SDK emits no metrics or logs of its own.

## Security & PII
Signature verification uses constant-time comparison; API key from env, never logged.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Unknown response shape | SDK returns a decode error with request id | — |
| Stale callback timestamp | `VerifySignature` returns `ErrStale` | — |

## Testing
- **Unit**: decode table for 200/202/each error code; signature vectors (valid, tampered, stale).
- **Contract**: every SDK exchange in `pkg/hubclient` tests runs through `internal/testkit/contract` (`Check`), with responses taken from the contract's own examples; a test fails if the SDK's code constants differ from the `canonical-error.json` enum.
- **Skill eval**: skill-creator evals — prompts like "send an order event" must produce code with idempotency and `Queued` handling and no vendor names.

## Rollout & migration
Skill versioned with the caller contract version in its front matter.

## Open questions
- Distribution channel for the skill beyond copy (plugin marketplace?). Copy or symlink into each tool's skills folder works today (HUB-317).
- Should the SDK be its own Go module so callers do not import the backend module?

## Tickets
HUB-300 – HUB-329 (EP-11, filter `feature: F-21`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | SDK core shipped (HUB-094); status `in-progress`; behaviour rules 1a/1b; remaining SDK items listed in Status | backend-engineer |
| 2026-11-07 | Operator skill `hub-operator` (HUB-316, US-2105) | docs agent |
| 2026-11-07 | Consumer skill portable to Codex, Cursor and any assistant; two-step OTP flow; real hubclient API (HUB-317, US-2106) | docs agent |
