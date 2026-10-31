# F-14 — Integration versioning & cutover (weights, shadow, rollback)

## Summary
Immutable integration versions behind a stable integration ID; the full publish validation pipeline; rollback as a new version; weighted gradual cutover and shadow mode; re-resolve vs pinned for pending work; per-attempt version recording; vendor-session pinning (DESIGN §6, D10, D11, D12).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M6 · EP-07 · HUB-170–199 (shared with F-15).

## Problem
Vendor and account swaps are big-bang, multi-team deploys today, with no canary and no clean rollback.

## Goals / Non-goals
**Goals**
- Integration version = targets (vendor + account + transformers, weights) + failover + delivery + breaker overrides + webhooks + audit (DESIGN §3.4).
- Publish validation (DESIGN §6.4): schemas compile, expressions type-check, goldens pass, response output conforms to capability response schema, references exist and are not retired, interactive → sync only, async modes have poll/callback path, LLM `supports` matches.
- Rollback = publish old content as a new version; history linear.
- Weights hashed on idempotency key or request id; shadow (logged only; dry-run-only shadow for side-effecting vendors).
- Re-resolve at send time by default; `pending: pinned` opt-in; sessions pinned to opening version + account.
- Every attempt records `integration_version`.

**Non-goals**
- Caller-selected versions (never, D12). Capability schema major versions (new integration ID, D11).

## User stories
- [US-1401](../product/USER_STORIES.md) — Immutable versions
- [US-1402](../product/USER_STORIES.md) — Full publish validation
- [US-1403](../product/USER_STORIES.md) — Weighted canary
- [US-1404](../product/USER_STORIES.md) — One-action rollback
- [US-1405](../product/USER_STORIES.md) — Pending work follows the cutover
- [US-1406](../product/USER_STORIES.md) — Shadow mode

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | `GET /admin/v1/integrations/{id}/versions`, `GET .../versions/{v}` | `contracts/openapi/hub-admin.v1.yaml#listIntegrationVersions`, `#getIntegrationVersion` |
| Admin API | `POST .../versions` (draft), `PUT .../versions/{version}` (edit draft), `POST .../versions/{version}/validate` | `#createIntegrationDraft`, `#updateIntegrationDraft`, `#validateIntegrationVersion` |
| Admin API | `POST .../versions/{version}/publish`, `POST .../rollback`, `POST .../weights`, `GET .../diff?from=&to=` | `#publishIntegrationVersion`, `#rollbackIntegration`, `#updateIntegrationWeights`, `#diffIntegrationVersions` |
| CLI / console | `hubctl plan/apply`; F-19 integration detail | — |

## Behaviour & rules
DESIGN §6.3 request handling at publish of v8:

| Request | Lands on |
| --- | --- |
| Completed before publish | v7 (audit unchanged) |
| In flight | v7 for the current attempt |
| Parked/queued | v8 (`re_resolve`) or v7 (`pinned`) |
| Retry of failed v7 request, same key | v8 |
| First new request | v8 |
| Step 2 of a session opened on v7 | v7 |

1. Versions are immutable; the only mutable integration fields are `active_version` and `paused`.
2. A weight change is a new version (so it is diffable and in the change log). Weights may span targets in one version (DESIGN §6.4 example "v8: A 90/B 10").
3. Weighted choice is deterministic: `hash(idempotency_key ?? request_id) mod 100`.
4. Shadow: primary result returned; shadow target result recorded in audit as `policy.applied` + shadow attempt; never returned; shadow on non-idempotent vendors runs as dry-run only.
5. During propagation two nodes may serve different versions; per-request version recording keeps audit exact.
6. Publish produces a new snapshot (F-03) and a `change_log` row with author and reason.

## Data model impact
`integrations(active_version, paused)`, `integration_versions(spec JSONB, author, reason, published_at)` immutable, `request_attempts.integration_version`, `vendor_sessions.version` (DESIGN §18.2).

## Config impact
Integration YAML `versions[]`, `targets[].weight`, `targets[].shadow` (proposed field), `delivery.pending`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_requests_total{version}`, `vendor_latency_ms{version}` | canary comparison |
| Metric | `shadow_mismatch_total{integration}` | proposed |
| Log | `integration_published` | id, from, to, author, reason |

## Security & PII
Versions never surface to callers except debug header (F-06). Publish requires `editor`/`admin` (F-16).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Publish validation fails | Rejected with all errors | Nothing |
| Bad version goes live | Rollback (new version) | Errors until propagation ≤ 10 s |

## Testing
- **Integration**: the six §6.3 scenarios for each change type (account, vendor, transformer) (DESIGN §19 M6 accept).
- **Unit**: weight distribution ± 1 % over 10,000; same key → same target.
- **Integration**: rollback restores behaviour; history linear.

## Rollout & migration
CleverTap pilot cutover (journey J-B) is the first production exercise.

## Open questions
- Shadow field shape in the version spec is not defined in DESIGN.
- DESIGN §6.4 describes weights "across versions" while §3.4 places `weight` on targets within a version; this doc treats weights as per-target within one version.

## Tickets
HUB-170 – HUB-199 (EP-07, filter `feature: F-14`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
