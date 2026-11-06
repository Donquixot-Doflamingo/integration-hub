# Contracts changelog

Contract versions, newest first. Each document has its own semver (see `README.md` § Versioning).
Entry format: `- YYYY-MM-DD · <document> <version> · <CCR / HUB-###> · <summary>`.

## 2026-11-07

- 2026-11-07 · `hubmcp-tools` 1.1.0 · HUB-314 · Admin write tools acting as the signed-in user, with no delete path (additive, minor)
  - 34 new `hub_admin_*` tools, each mapped to exactly one existing hub-admin operation. 18 are read tools or non-mutating checks: list/get tenants, vendors, vendor endpoints and accounts, list capabilities, get transformer, run goldens, transformer dry-run, list/get integrations, get version, diff versions, validate, get registrar endpoint. 16 are write tools: create/update tenant, create API key, create/update vendor, create vendor endpoint version, update account (config and quota), create transformer version, create integration, create/update draft, publish, set weights, rollback, create/update registrar endpoint. 42 tools in total. No new HTTP endpoint.
  - Write tools use the user's own `HUB_ADMIN_TOKEN`, are registered only on stdio, and require `reason` (3–1000 characters; hubmcp appends ` (via hubmcp)`). The Hub's `x-required-role` is the authority (`FORBIDDEN` comes back as an `isError` result).
  - New per-tool fields: `maps_to[].http_method` (on every tool), `requires.transport: ["stdio"]` on write tools, `requires_confirm` (`live_traffic`: publish, weights, rollback and the four updates; `mints_secret`: API key, callback endpoint), `sensitive_output` (`/secret`, `/signing_secret`). Get tools return the `etag`, and update tools take `if_match`.
  - New top-level fields: `forbidden_operations` (17: the DELETE, purge, replay, unseal, account lifecycle, `createAccount` (body requires credentials), the three credential operations, the three breaker-force operations, pause/resume, API-key revoke/rotate, signing-secret rotation), `not_exposed_operations` (24; every one of the 79 hub-admin operations is classified exactly once), `forbidden_input_properties` (secret names), `forbidden_mutation_input_properties` (`disabled`, `paused`, `state`), `secret_patterns` (hubmcp refuses arguments carrying a Hub key or signing secret: new MCP-only code `SECRET_IN_INPUT`).
  - `rules`, `server.description` and the `HUB_ADMIN_TOKEN` description are rewritten for write tools. Existing tools are unchanged apart from `http_method`. `contracts/tools/check.mjs` §7b enforces all of the above. `hub-public` and `hub-admin` are unchanged.
- 2026-11-07 · `hub-admin` 1.2.0 · HUB-344 · Per-operation transformer pairs on integration version targets (additive, minor)
  - New schema `TransformerPair` (`request`, `response`: `TransformerRef`, both required). `TargetRef.transformer` now references it; its shape is unchanged and it stays required, as the default pair.
  - New optional `TargetRef.operations`: map from operation name (`OperationName`) to `TransformerPair`, at least one entry. A listed operation uses its own pair; others use `transformer`. Applies to `targets[]`, `failover[]` and `shadow.target`, so `IntegrationSpec` in drafts, versions, validate, publish, diff and `Integration.active` carries it.
  - `ValidationCheck.check` names the new known check `operations_have_transformers` (unknown operation key, or a pair compiled for another operation); clients must tolerate unknown check names.
  - Examples: `createIntegrationDraft` request and `getIntegrationVersion` 200 move from `example` to named `examples` (the existing one stays first) and add `per_operation` for `kyc.aadhaar_otp@v1` (`generate` default, `verify` override).
  - `hub-public` unchanged: callers never see transformers. `hubmcp-tools` unchanged: no tool exposes the target shape.

## 2026-10-31

- 2026-10-31 · `hub-admin` 1.1.0 · CCR-003 / HUB-284 · Per-account dead-letter count and age on `AccountSummary` and `Account` (additive, minor; extends the pending 1.1.0)
  - New optional response properties `dead_letter_count` (integer ≥ 0) and `dead_letter_oldest_at` (date-time or null): dead letters whose last attempt used the account, not replayed, within the 14-day retention; may lag by up to a minute.
  - Returned by `listAccounts` (`AccountSummary`) and `getAccount` (`Account`). Examples updated: `ct-brandA-in` has 1 (matching the `listDeadLetters` example), a second list row shows 0 and null.
- 2026-10-31 · `hub-admin` 1.1.0 · CCR-002 / HUB-283 · Active-version filters on `listIntegrations` (additive, minor; extends the pending 1.1.0)
  - New optional boolean query parameters `has_active_version` (published or never published) and `active_version_behind` (a newer version or a draft waits behind the active one), with examples. `VALIDATION_FAILED` for a non-boolean value, as for `paused`.
  - The operation description says filters combine with AND and apply before pagination.
- 2026-10-31 · `hub-admin` 1.1.0 · CCR-001 / HUB-278 · Every admin response declares `X-Hub-Request-Id` (additive, minor)
  - New `components.headers.X-Hub-Request-Id` (`req_` + ULID, required, equals `error.request_id` on errors), referenced by all 80 inline responses of the 79 operations and by the 9 shared `components.responses`.
  - `info.description` documents the header. No body, parameter or schema change.
  - 1.1.0 is the pending version of contract round 1: HUB-283 and HUB-284 extend it in the same release.
- 2026-10-31 · `hub-public` 1.0.1 · HUB-033 · `getReadyz` 503 example matches what `/readyz` returns (patch, example and description only)
  - The example has one `details` entry with `message` only (`redis unreachable`), as the backend emits it; the old entry carried a `pointer` into a request body that `/readyz` does not have.
  - The example drops the `circuit`/`reason`/`upstream` nulls the backend omits, and keeps its `request_id` (already present in 1.0.0; the CCR's "before" snippet predates the freeze).
  - The description says each entry names one failing check and that `request_id` equals `X-Hub-Request-Id`. No schema change.
- 2026-10-31 · `hub-public` 1.0.0 · Phase 1 · Initial frozen contract (Phase 1)
  - Invoke (single and multi-operation), dry-run (integration-developers, scope `dry_run`), request status, discovery (`/v1/integrations`, `/v1/capabilities/{capability}`), inbound vendor webhooks (internal), hand-off return (planned, Release 2), `/healthz`, `/readyz`.
  - Outbound callbacks `request.completed`, `request.failed`, `request.expired`, `delivery.status` with `X-Hub-Signature`.
  - Canonical error envelope plus additive codes `IDEMPOTENCY_KEY_REQUIRED`, `OPERATION_NOT_FOUND`, `CAPABILITY_NOT_FOUND`, `REQUEST_NOT_FOUND`, `IDEMPOTENCY_CONFLICT`, `PAYLOAD_TOO_LARGE`, `INTERNAL_ERROR`, `NOT_READY`.
  - Schemas: `schemas/events/*` (envelope, 4 events, canonical error) and `schemas/capabilities/events.track.v1.{request,response}.json`.
- 2026-10-31 · `hub-admin` 1.0.0 · Phase 1 · Initial frozen contract (Phase 1)
  - 79 operations covering DESIGN §16.1: vendors and endpoints, accounts (test-connection, credential rotation, lifecycle with retire guard), capabilities, transformers (dry-run, run-goldens), integrations (versions, drafts, validate, publish, rollback, diff, pause/resume, weights), tenants, api-keys, registrar endpoints, snapshots (long-poll), requests (search, timeline, attempts, unseal), DLQ, breakers, change log, `me`.
- 2026-10-31 · `mockvendor` 1.0.0 · Phase 1 · Initial frozen contract (Phase 1)
  - Route behaviours (latency, status, error ratio, 429 + Retry-After, hard down, slow drip, malformed), default behaviour, reset, recordings, inbound-webhook trigger.
- 2026-10-31 · `hubmcp-tools` 1.0.0 · Phase 1 · Initial frozen contract (Phase 1)
  - 5 tenant tools (`hub_list_integrations`, `hub_describe_integration`, `hub_dry_run`, `hub_invoke`, `hub_get_request`) and 3 read-only admin tools (`hub_admin_search_requests`, `hub_admin_request_timeline`, `hub_admin_breaker_states`).
