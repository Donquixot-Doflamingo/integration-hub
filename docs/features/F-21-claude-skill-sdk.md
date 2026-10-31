# F-21 — Consumer Claude skill & Go SDK (`hubclient`)

## Summary
Two caller-side deliverables: (1) a distributable Claude skill, `clients/claude-skill/integration-hub/SKILL.md`, that developers copy to `~/.claude/skills/integration-hub/` so Claude uses the Hub correctly (PRD DV-5); (2) the Go SDK `backend/pkg/hubclient` with distinct `Result` / `Queued` types, typed errors, idempotency helper and callback signature verifier (DESIGN §4.5, §18.1).

## Status
`proposed` — 2026-10-31. `hubclient` core types ship in M3 (F-06); polish and skill in EP-11.

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

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| SDK | `hubclient.Invoke`, `InvokeOp`, `GetRequest`, `DryRun`, `ListIntegrations`, `GetCapability` | `contracts/openapi/hub-public.v1.yaml#invokeIntegration`, `#invokeIntegrationOperation`, `#getRequestStatus`, `#dryRunIntegration`, `#listIntegrations`, `#getCapability` |
| SDK | `VerifySignature` | `contracts/openapi/hub-public.v1.yaml#/components/parameters/CallbackSignature`; shared signature test vectors are not in `contracts/` yet (to be added via CCR) |
| Skill | `clients/claude-skill/integration-hub/SKILL.md` (+ `references/` examples) | — |

## Behaviour & rules
1. The SDK decodes 202 into `Queued{RequestID, Status, Reason, Poll}`; callers must type-switch.
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
SDK sends `traceparent` when a span is in context and `User-Agent: hubclient-go/<version>`.

## Security & PII
Signature verification uses constant-time comparison; API key from env, never logged.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Unknown response shape | SDK returns a decode error with request id | — |
| Stale callback timestamp | `VerifySignature` returns `ErrStale` | — |

## Testing
- **Unit**: decode table for 200/202/each error code; signature vectors (valid, tampered, stale).
- **Contract**: SDK types generated or checked against `hub-public.v1.yaml`.
- **Skill eval**: skill-creator evals — prompts like "send an order event" must produce code with idempotency and `Queued` handling and no vendor names.

## Rollout & migration
Skill versioned with the caller contract version in its front matter.

## Open questions
- Distribution channel for the skill beyond copy (plugin marketplace?).
- Should the SDK be its own Go module so callers do not import the backend module?

## Tickets
HUB-300 – HUB-329 (EP-11, filter `feature: F-21`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
