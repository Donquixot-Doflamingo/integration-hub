# Contracts changelog

Contract versions, newest first. Each document has its own semver (see `README.md` § Versioning).
Entry format: `- YYYY-MM-DD · <document> <version> · <CCR / HUB-###> · <summary>`.

## 2026-10-31

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
