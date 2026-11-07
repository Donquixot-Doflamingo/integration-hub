# F-14 — Integration versioning & cutover (weights, shadow, rollback)

## Summary
Immutable integration versions behind a stable integration ID; the full publish validation pipeline; rollback as a new version; weighted gradual cutover and shadow mode; re-resolve vs pinned for pending work; per-attempt version recording; vendor-session pinning (DESIGN §6, D10, D11, D12).

## Status
`shipped` — 2026-11-02 (M6, `docs/milestones/M6.md`).

## Owner
`backend-engineer`.

## Milestone / Epic
M6 · EP-07 · HUB-170–199 (shared with F-15).

## Problem
Vendor and account swaps are big-bang, multi-team deploys today, with no canary and no clean rollback.

## Goals / Non-goals
**Goals**
- Integration version = targets (vendor + account + transformers, weights) + failover + delivery + breaker overrides + webhooks + audit (DESIGN §3.4).
- Publish validation (DESIGN §6.4, ADR-0023): operation schemas compile and transformer field reads are declared in them, expressions type-check, samples pass (`goldens_pass`), sample inputs fit the operation (`input_schema`) and produced requests fit the endpoint (`endpoint_schema`), references exist and are not retired, interactive → sync only, async modes have poll/callback path, LLM `supports` matches.
- Rollback = publish old content as a new version; history linear.
- Weights hashed on idempotency key or request id; shadow (logged only; dry-run-only shadow for side-effecting vendors).
- Re-resolve at send time by default; `pending: pinned` opt-in; sessions pinned to opening version + account.
- Every attempt records `integration_version`.

**Non-goals**
- Caller-selected versions (never, D12). Breaking request schema changes should use a new integration ID (D11, guidance since ADR-0023); a version can change an operation's schema and the publish diff shows it.

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
| Admin API (1.2.0) | `TargetRef.operations` (per-operation transformer pairs) and the validation check `operations_have_transformers` in every `IntegrationSpec` (HUB-343, contract HUB-344) | `#/components/schemas/TargetRef`, `#/components/schemas/TransformerPair`, `#/components/schemas/ValidationCheck` |

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

### Vendor sessions (HUB-175, HUB-183) — implementation notes
- **Open (step 1)**: a response transformer with a `session:` section (F-04) maps session data on success. The edge stores it with `sessions.Create` (`ses_` + ULID, TTL 10 min) pinned to tenant, integration, the version that served and the account that served (failover included), and answers `X-Hub-Session` on the 200 (sync, or wait within the window). An async step 1 opens nothing. A later step never opens another session.
- **Use (step ≥ 2)**: `X-Hub-Session` → `sessions.Get` (unknown, expired, other tenant or integration → 404 `SESSION_NOT_FOUND`) → `resolve.ResolveSession` (exact version + the target on the session's account, no failover; version/account gone → 404 `SESSION_NOT_FOUND`); the request transformer reads `session.*`. Wait and async envelopes carry `session_id` and `resolved_version`; the worker reloads the session and pins the same way, and dead-letters the request as `SESSION_NOT_FOUND` when the session expired in the queue. Dry-run with `X-Hub-Session` uses the session too.
- **Never stored**: the store and the table refuse a 12-digit (4-4-4) number anywhere in the data or `vendor_txn_id` (DESIGN §14); `creds.*` is refused in `session:` at compile; data ≤ 16 KB.
- **Audit**: `integration.resolved` meta `session: {id, event: used, version, account}`; `response.transformed` meta `session: {id, event: opened, version, account, expires_at}` (or `event: refused`). Never the data.
- **For the lead**: the scheduler calls `sessions.Expire(ctx, db, now)` each tick; the retire guard calls `sessions.CountOpen(ctx, db, accountID)`.
- **Deviation**: one PK read per step ≥ 2 request on the sync path — [ADR-0011](../tech/adr/0011-vendor-session-lookup-on-the-request-path.md).

### Per-operation transformers (HUB-343, HUB-345) — implementation notes
- **Shape.** A target (`targets[]`, `failover[]`, `shadow.target`) has a required default pair `transformer: {request, response}` and an optional `operations: {<operation>: {request, response}}` (registry `Target.Operations`, YAML key `operations`; admin `TargetRef.operations`, hub-admin 1.2.0). A listed operation uses its own pair, both request and response; every other operation uses `transformer`. A single-operation integration omits `operations`. Because a request transformer names one `vendor_endpoint`, this is how one integration sends `send_otp` and `verify_otp` (or `generate` and `verify`, DESIGN §3.1) to different vendor endpoints.
- **Selection.** `resolve.Resolution.Operation` is the invoked operation (path `/invoke/{operation}`, envelope `operation`); `Resolution.Pair()` = `Target.Pair(Operation)`. The edge sets it after resolve (sync, wait, dry-run with `?operation=`, session steps through `resolve.ResolveSession`); the worker sets it from the envelope when it re-resolves at send time (async, `re_resolve` and `pinned`). Failover and shadow copy the resolution and swap only `Target`, so they select the same way. Cost on the hot path: one lookup in a small map.
- **Publish check `operations_have_transformers`** (registry `checkOperations`, run by `registry.Build`, so by config files, `hubctl apply`, the admin `validate` and `publish` and every node's snapshot build alike). It fails:
  - for an `operations` key the version's `operations` does not declare — pointer `/targets/<i>/operations/<op>` (same for `/failover/<i>/…` and `/shadow/target/…`).
  Since ADR-0023 transformers name no operation, so "compiled for another operation" no longer exists. **HUB-345** is covered instead by checking each selected pair against its own operation: the request transformer's `req.*` / `hdr.*` reads must be declared in that operation's request schema (`schemas_compile`) and its samples' inputs must fit it (`input_schema`). The pair references (exist, right kind, the target's vendor) are checked for every pair under `references_exist` as before. `operations: {}` is refused (name at least one operation, or omit it).
- **Shadow `send`** must be safe for every pair of the shadow target: any non-idempotent request endpoint forces `dry_run`.
- **Copies.** Rollback (`CopyVersion`) and weight changes (`reweigh`) copy the version through its YAML, so `operations` is kept. Breakers listed by the admin API, transformer usage counts and the paired endpoint of a response transformer walk every pair (`Target.Pairs()`).
- **Not covered:** `hubctl dry-run <integration>` still shows the default pair of the heaviest target (HUB-350). The caller dry-run (`?operation=`) selects per operation.

## Data model impact
`integrations(active_version, paused)`, `integration_versions(spec JSONB, author, reason, published_at)` immutable, `request_attempts.integration_version`, `vendor_sessions.version` (DESIGN §18.2).

## Config impact
Integration YAML `versions[]`, `targets[].weight`, `targets[].shadow` (proposed field), `delivery.pending`, `targets[].operations` / `failover[].operations` / `shadow.target.operations` (HUB-343). Each operation is declared in the version's `operations` with its own `request_schema` (ADR-0023); transformer files no longer carry `operation:` or `capability:`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_requests_total{version}`, `vendor_latency_ms{version}` | canary comparison |
| Metric | `shadow_total{integration, mode, outcome}` | shadow copies (HUB-173); results in the request's audit block at seq 900000+ |
| Log | `archived snapshot not loaded: keeping the current one` (warn) | node follower (HUB-171): read or build failure, last good snapshot kept |
| Change log | `hub.change_log` row per publish, rollback, retire | actor, reason, before/after (HUB-170) |
| Log | `integration_published` | id, from, to, author, reason |
| Log | `session lookup failed`, `session create failed` (error), `session target gone`, `session data refused` (warn) | integration + error only; never session data |
| Audit meta | `session` on `integration.resolved` / `response.transformed` | id, event, version, account (HUB-175) |
| Audit meta | `transformers` on `integration.resolved` (edge) | the pair of the invoked operation (HUB-343); `request.transformed` meta `transformer`/`endpoint` likewise. No new metric, log or span |

## Security & PII
Versions never surface to callers except debug header (F-06). Publish requires `editor`/`admin` (F-16).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Publish validation fails | Rejected with all errors | Nothing |
| Bad version goes live | Rollback (new version) | Errors until propagation ≤ 10 s |

## Testing
- **Integration**: the six §6.3 scenarios for each change type (account, vendor, transformer) (DESIGN §19 M6 accept).
- **Per-operation transformers (HUB-343, HUB-345)**: fixture `internal/testkit/multiop` (operations `send` and `verify` on two endpoints, goldens per transformer; it held a capability `test.two_step@v1` before ADR-0023). Registry: `TestPerOperationPairsBuild`, `TestOperationsHaveTransformers` (undeclared key, one pair for two operations caught because its sample does not fit the second operation = HUB-345, failover, shadow, empty map), `TestOperationsValidShapes`, `TestShadowSendChecksEveryOperationEndpoint`, `TestTargetOperationsRoundTrip`; resolve: `TestPairFollowsTheOperation`; edge: `TestPerOperationSyncSelectsThePair`, `TestPerOperationFailoverSelectsThePair`, `TestPerOperationDryRunSelectsThePair`, `TestPerOperationShadowSelectsThePair`, and (integration) `TestPerOperationWaitMode`, `TestPerOperationAsyncWorker`; admin: `TestSpecOperationsRoundTrip`, `TestOperationsProblemsMapToTheirCheck`, `TestPerOperationEndpointsAreListed`, (integration, contract-validated) `TestPerOperationSpecs`. Session steps on two endpoints: `TestExampleOTPAccountFlow`, `TestExampleOTPRefusesOnePairForBothOperations` (examples/otp-account-flow).
- **Unit**: weight distribution ± 1 % over 10,000; same key → same target.
- **Integration**: rollback restores behaviour; history linear.

## Rollout & migration
CleverTap pilot cutover (journey J-B) is the first production exercise.

## Open questions
- Shadow field shape in the version spec is not defined in DESIGN.
- DESIGN §6.4 describes weights "across versions" while §3.4 places `weight` on targets within a version; this doc treats weights as per-target within one version.

## Tickets
HUB-170 – HUB-199 (EP-07, filter `feature: F-14`). Per-operation transformers: HUB-343 (backend), HUB-344 (admin contract), HUB-345 (refusal), HUB-350 (`hubctl dry-run --operation`, todo).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-02 | Vendor session implementation notes (HUB-175, HUB-183) | backend-engineer |
| 2026-11-02 | Shipped in M6: publisher, distribution, shadow, cutover tests | backend-engineer |
| 2026-11-07 | Per-operation transformers and the `operations_have_transformers` check (HUB-343, HUB-345) | backend-engineer |
| 2026-11-07 | Publish checks and per-operation pairs after ADR-0023 (no capability, no transformer `operation:`; HUB-437) | docs-keeper |
