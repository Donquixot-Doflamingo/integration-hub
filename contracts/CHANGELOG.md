# Contracts changelog

Contract versions, newest first. Each document has its own semver (see `README.md` § Versioning).
Entry format: `- YYYY-MM-DD · <document> <version> · <CCR / HUB-###> · <summary>`.

## 2026-10-31

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
