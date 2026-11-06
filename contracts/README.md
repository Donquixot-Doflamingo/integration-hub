# contracts/: frozen API contracts

The documents here are the **only** interface between the backend (Go), the admin console (SvelteKit), the MCP server (`hubmcp`) and the SDKs (AGENTS.md §4). Behaviour comes from `docs/DESIGN.md`. Shapes come from this folder. Nobody codes against an endpoint, field or event that is not here.

## What's here

| Path | What | Version |
| --- | --- | --- |
| `openapi/hub-public.v1.yaml` | **Caller API**: invoke, request status, discovery, dry-run, inbound vendor webhooks (internal), hand-off return (planned), health, and the outbound callbacks (`webhooks:`) | 1.0.1 |
| `openapi/hub-admin.v1.yaml` | **Control plane** `/admin/v1`: vendors, accounts, capabilities, transformers, integrations, tenants, api-keys, registrar endpoints, snapshots, requests, DLQ, breakers, change log, `me` | 1.2.0 |
| `openapi/mockvendor.v1.yaml` | Control API of `cmd/mockvendor` (test double for every vendor) | 1.0.0 |
| `schemas/capabilities/` | Canonical capability request/response JSON Schemas (2020-12). See its `README.md` for `x-pii`, `interactive`, `operations` | per capability |
| `schemas/events/` | Outbound callback payloads (`envelope.json`, `request.completed.json`, `request.failed.json`, `request.expired.json`, `delivery.status.json`) and `canonical-error.json`, the canonical error object shared with the public spec | with hub-public |
| `mcp/hubmcp-tools.json`, `mcp/README.md` | MCP tool contract for `hubmcp` | 1.0.0 |
| `CHANGELOG.md` | Contract version history | |
| `notices/` | Contract notices to backend and frontend, one file per round (`docs/process/CONTRACT_CHANGES.md` §6) | |
| `tools/` | `make contracts-check` toolchain (pinned Redocly CLI + ajv, `check.mjs`, `version-check.mjs`), see "Validate locally" | |
| `AGENTS.md` | Rules for editing this folder | |

Operation counts (unchanged since 1.0.0): hub-public **15** (11 paths + 4 webhooks), hub-admin **79**, mockvendor **10**, MCP **8 tools**.

## Freeze policy

- Phase 1 froze these documents at 1.0.0 on 2026-10-31. From now on every change goes through a **contract change request (CCR)**: [`docs/process/CONTRACT_CHANGES.md`](../docs/process/CONTRACT_CHANGES.md).
- Only the `contract-steward` agent edits `contracts/`. Backend and frontend agents file CCRs and never edit here.
- A contract change lands **in its own commit, before** any code that depends on it: `contract(<area>): <change> (HUB-###)`.

## Versioning

Each document has its own semver (`info.version`, or `version` in `hubmcp-tools.json`). The file name carries only the major (`*.v1.yaml`).

| Change | Bump | Examples |
| --- | --- | --- |
| Additive | minor | New optional request field, new response field, new endpoint, new error code, new enum value in a **response**, new event type |
| Clarification only | patch | Descriptions, examples |
| Breaking | major (new file `*.v2.yaml`) | Remove/rename a field or endpoint, tighten a constraint, new required request field, new enum value in a **request** |

- **Caller API**: breaking changes are effectively forbidden. A breaking canonical change means a new capability major and a **new integration ID** (D11). The old one keeps working for at least 6 months.
- **Admin API**: breaking changes need the user's explicit approval.
- **Capability schemas** version independently (`events.track@v1` + `x-capability-minor`). See `schemas/capabilities/README.md`.
- Clients must ignore unknown response fields, and must handle unknown error `code` values by falling back to HTTP status and `retryable`. That is what makes additive changes safe.

## Non-negotiables enforced by the contract

1. **No vendor leakage in the caller API (D12).** No parameter, header or body field in `hub-public` selects or reveals a vendor, account or integration/transformer version. `X-Hub-Integration-Version` is a debug-only header, documented as "do not act on". `X-Hub-Served-By` only says `primary` or `failover:<n>`.
   - Documented exceptions: `dryRunIntegration` (`x-audience: integration-developers`, key scope `dry_run`) returns the upstream request with secrets redacted. `receiveVendorWebhook` (`x-internal: true`) is vendor-facing, so `{vendor}/{account}` appear in its path.
   - `vendor_ref` (opaque correlation id) and `raw` (opaque, `full` transformers only, documented as "never depend on it") in `events.track@v1` come from DESIGN §7.4. `vendor_ms` in timings is a duration name only.
2. **One error envelope** `{"error": {...}}` with `request_id` on every error response, in all three specs.
3. **Every admin mutation** takes a required `reason` (body, or `?reason=` for DELETE) and returns `change_id`.
4. Every operation has a unique camelCase `operationId`, a tag, a summary, a description, and examples for request and response bodies.

## Conventions

- IDs: requests `req_` + ULID, change log `chg_`, events `evt_`, callback deliveries `dlv_`, vendor sessions `ses_`, API keys `key_`, mock recordings `rec_`. ULID = 26 Crockford base32 characters (`[0-9A-HJKMNP-TV-Z]`).
- Registry ids (tenant, integration, vendor, account, registrar endpoint) match `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`. They are **case-sensitive**, because DESIGN uses `int_evt_brandA` and `ct-brandA-in`. Transformer ids match `^[A-Za-z][A-Za-z0-9_-]{0,63}$` (`T1`), refs are `T1@7`, and vendor endpoints are `upload@1`.
- Timestamps: RFC 3339, UTC. Durations: integer milliseconds (`*_ms`), including TTLs and retention. The one exception is the HTTP `Retry-After` header (seconds).
- Pagination: `?cursor=&limit=` (1–200, default 50), and the response carries `{items, next_cursor}` (`null` on the last page).
- Optimistic concurrency (admin): `ETag` / `If-Match` gives 412 `PRECONDITION_FAILED`. Publish, rollback and weights use `expected_active_version` and give 409 `CONFLICT`.

## Error-code catalogue

### Caller API (`hub-public`, `schemas/events/canonical-error.json`)

| Code | HTTP | Retryable | Source | When |
| --- | --- | --- | --- | --- |
| `VALIDATION_FAILED` | 400 | no | DESIGN §4.4 | Body fails the capability schema (`details[].pointer`), bad params |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | no | **added** | Integration has `moves_money_or_quota` and no `Idempotency-Key` was sent |
| `UNAUTHORIZED` | 401 | no | §4.4 | Bad or missing key, or bad inbound-webhook signature |
| `FORBIDDEN` | 403 | no | §4.4 | Not owner or grantee (`reason: not_granted`), missing scope (`reason: missing_scope`) |
| `INTEGRATION_NOT_FOUND` | 404 | no | §4.4 | Unknown integration (also unknown inbound-webhook route) |
| `OPERATION_NOT_FOUND` | 404 | no | **added** | Capability has no such `{operation}` |
| `CAPABILITY_NOT_FOUND` | 404 | no | **added** | Unknown capability, or not visible to the tenant |
| `REQUEST_NOT_FOUND` | 404 | no | **added** | Unknown request id, or another tenant's request |
| `SESSION_NOT_FOUND` | 404 | no | §4.4 | Unknown or expired `X-Hub-Session` / hand-off session |
| `IDEMPOTENCY_CONFLICT` | 409 | no | **added** | Same key with a different body, or the original is still in progress |
| `IDEMPOTENT_REPLAY` | 200/202 | n/a | §4.4 | Never in an error body. Signalled by `X-Hub-Idempotent-Replay: true` |
| `PAYLOAD_TOO_LARGE` | 413 | no | **added** | Body over 1 MB (4 MB LLM) |
| `UPSTREAM_REJECTED` | 422 | no | §4.4 | Vendor 4xx (not auth or rate limit) |
| `UPSTREAM_RATE_LIMITED` | 429 | yes | §4.4 | Vendor 429 / account quota / tenant limit / budget (`reason`) |
| `TRANSFORM_FAILED` | 500 | no | §4.4 | Transformer threw (Hub bug, alerts) |
| `INTERNAL_ERROR` | 500 | yes | **added** | Any other unexpected Hub failure |
| `UPSTREAM_AUTH_FAILED` | 502 | no | §4.4 | Vendor rejected Hub credentials (pages) |
| `INTEGRATION_PAUSED` | 503 | yes | §4.4 | Kill switch (`reason: paused`) or no active version yet (`reason: no_active_version`) |
| `UPSTREAM_UNAVAILABLE` | 503 | yes | §4.4 | Circuit open (`circuit: open`) or vendor 5xx |
| `NOT_READY` | 503 | yes | **added** | `/readyz` only |
| `UPSTREAM_TIMEOUT` | 504 | yes | §4.4 | Vendor exceeded timeout / `X-Request-Deadline` |
| `EXPIRED` | n/a | no | §4.4 | Only in request status and `request.expired` callbacks |

### Admin API (`hub-admin`, `AdminError`)

| Code | HTTP | When |
| --- | --- | --- |
| `VALIDATION_FAILED` | 400 | Bad body or params (including missing `reason`, SSRF-guard rejection) |
| `UNAUTHORIZED` | 401 | Missing or expired OIDC token |
| `FORBIDDEN` | 403 | Role below `x-required-role`, or outside the tenant_viewer scope |
| `NOT_FOUND` | 404 | Object missing or not visible |
| `CONFLICT` | 409 | Already exists, `expected_*` mismatch, invalid transition, object in use, draft already exists |
| `RETIRE_BLOCKED` | 409 | Account retire refused. `meta` carries `pinned_requests`, `open_sessions`, `active_versions` |
| `PRECONDITION_FAILED` | 412 | `If-Match` mismatch |
| `PUBLISH_VALIDATION_FAILED` | 422 | Publish, transformer-version or capability-version validation failed. `meta.report` holds the report |
| `RATE_LIMITED` | 429 | Admin API throttling (reserved, with `retry_after_ms`) |
| `INTERNAL_ERROR` | 500 | Unexpected |

### Mockvendor
`VALIDATION_FAILED` (400), `NOT_FOUND` (404), `INTERNAL_ERROR` (500). Same envelope shape, but without `request_id`.

## RBAC (admin)

`x-required-role` on each operation is the **minimum** role. `admin` ⊃ `editor` ⊃ `viewer`. `tenant_viewer` is `viewer` limited to the tenants in the token's `hub_tenants` claim. In 1.0.0, `editor` covers capabilities, transformers, integration drafts, publish, rollback, weights, pause/resume, test-connection and DLQ replay. `admin` covers vendors, accounts, credentials, lifecycle, tenants, API keys, registrar endpoints, breaker force, DLQ purge and unseal.

## Validate locally

```bash
make contracts-check                          # from the repo root; Node 22 (/opt/homebrew/opt/node@22/bin)
make contracts-version-check BASE=origin/main # versions + changelog vs the merge base with BASE
```

The first run installs the pinned toolchain in `contracts/tools/` (`npm ci` from `package-lock.json`: `@redocly/cli` 2.64.1, `ajv` 8.20.0, `ajv-formats` 3.0.1, `yaml` 2.9.1); later runs reinstall only when the lockfile changes. Both steps of `contracts-check` always run, so one pass shows every problem, and the target exits non-zero if either fails. CI runs the same two targets (`.github/workflows/contracts.yml`, HUB-014).

**1. Redocly lint** of `hub-public.v1.yaml`, `hub-admin.v1.yaml` and `mockvendor.v1.yaml` with `contracts/tools/redocly.yaml`: `recommended-strict` (every recommended rule is an error) plus example, `$ref`, path, operationId, summary/description and license rules. Changes from `recommended-strict`:

| Rule | Setting | Why |
| --- | --- | --- |
| `no-server-example.com` | off | The `servers` lists name localhost on purpose (local ports of `hub`, `hub admin`, `mockvendor`) |
| `tag-description` | warn | The 14 hub-admin tags have no description yet; adding them is a docs-only patch. Reported, not failing |
| `operation-4xx-response` | ignored for 6 operations | `getHealthz`, `getReadyz` and the mockvendor `listRoutes`, `resetMock`, `clearRecordedRequests`, `getMockHealth` take no input, so there is no 4xx to declare |
| `operation-2xx-response` | ignored for `handoffReturn` | A browser redirect target: it answers 302, never 2xx |

The ignores live in `contracts/tools/.redocly.lint-ignore.yaml`, keyed by operation, so both rules still fail for any new operation.

**2. `contracts/tools/check.mjs`** (Redocly does not cover these):

- **Parse**: every YAML/JSON file under `contracts/` (except `tools/`). Duplicate keys, anchors and aliases fail, and so does any value a YAML 1.1 parser reads differently (unquoted timestamps, `yes`/`no`/`on`/`off`, octals).
- **`$ref`**: every `$ref` in every file resolves (file and JSON pointer).
- **Schemas**: every JSON Schema under `schemas/`, every OpenAPI schema (components and inline) and every MCP tool schema is meta-validated and compiled with ajv in draft 2020-12 strict mode, so unknown keywords and unknown formats fail. Allowed extras: `x-*` extensions, the OpenAPI annotations `example`, `discriminator`, `xml`, `externalDocs`, and the capability keywords `interactive` and `operations`.
- **Examples**: every example is validated against its schema with real 2020-12 semantics (`type: [x, 'null']` with `null`, `const`, `$ref` with siblings, `format: date-time`/`uri`). That covers media-type `example`/`examples` (requests and responses), parameter and header examples, schema `example`/`examples`, JSON Schema `examples` and MCP tool examples. Response examples must not carry `writeOnly` values, request examples must not carry `readOnly` values, and every `req_`/`chg_`/`evt_`/`dlv_`/`ses_`/`key_`/`rec_` id must be a well-formed ULID. Unlike kin-openapi (README "Backend: contract tests"), it does not apply 3.0 nullability rules.
- **Rules** from `contracts/AGENTS.md`: OpenAPI 3.1.0 with the 2020-12 dialect, semver `info.version`, unique camelCase `operationId`s, declared tags, summary and description, required path parameters, an example on every request and response body, the canonical envelope on every 4xx/5xx, `x-required-role` on admin operations, a required `reason` and a `change_id` on admin mutations, and, from hub-admin 1.1.0, `X-Hub-Request-Id` on every hub-admin response.
- **D12 vendor-leak check** (hub-public): no parameter, response header or body field (recursively, through `$ref`s into `schemas/`) is named after a vendor, account, version or transformer, and no example value contains a vendor or account id used in hub-admin examples. The same name check covers `schemas/capabilities/`, `schemas/events/` and the tenant MCP tools. Allow-listed operations: `dryRunIntegration` and `receiveVendorWebhook`. Allow-listed names: `vendor_ref`, `vendor_ms`, `spec_version` (callback envelope format) and the debug-only `X-Hub-Integration-Version`.
- **MCP**: every `maps_to` operationId exists in its spec, tenant tools map only to hub-public and admin tools (`hub_admin_*`) only to hub-admin, and tool names are unique.

To prove a check fails, run it on a copy: `cp -R contracts /tmp/c && <break /tmp/c/...> && make contracts-check CONTRACTS_DIR=/tmp/c` (use a path without spaces). Never commit broken files.

**3. `contracts/tools/version-check.mjs`** (`make contracts-version-check`): compares the working tree with the merge base of `BASE` and `HEAD`. Every contract document whose parsed content changed (formatting and comments alone do not count) needs a higher version and a line added to `contracts/CHANGELOG.md` naming it: `` `hub-public` 1.0.1 `` for the OpenAPI files (`info.version`), `` `hubmcp-tools` <version> `` for the MCP contract, a hub-public bump for `schemas/events/*`, and a higher `x-capability-minor` plus an entry naming the capability key for `schemas/capabilities/*`. Deleting a contract document fails; a new document needs an entry naming it.

## Backend: contract tests (kin-openapi)

```go
loader := openapi3.NewLoader()
loader.IsExternalRefsAllowed = true // hub-public refs ../schemas/events/*.json
doc, err := loader.LoadFromFile("../contracts/openapi/hub-public.v1.yaml")
// router, _ := gorillamux.NewRouter(doc)
// for every recorded request/response in e2e tests:
//   openapi3filter.ValidateRequest(ctx, reqInput)
//   openapi3filter.ValidateResponse(ctx, &openapi3filter.ResponseValidationInput{...})
```

- Run the tests against all three specs. Every handler response in e2e and integration tests must validate. The admin spec's `servers` include `/admin/v1`, so its paths are relative to that.
- **3.1 caveat:** these documents use 3.1 / 2020-12 features: `type: [x, "null"]`, `const`, schema `examples` arrays, top-level `webhooks`, and `$ref` with siblings. If the pinned kin-openapi version cannot load or validate them, file a CCR. Do not edit the spec. A fallback is `github.com/pb33f/libopenapi-validator`.
- Capability bodies are `type: object` in OpenAPI. Validate them against `schemas/capabilities/*.json` with `santhosh-tekuri/jsonschema` (DESIGN §18.7). Validate callback payloads against `schemas/events/*.json`.

## Frontend: typed client

```bash
# frontend/package.json  ->  "gen:api": "openapi-typescript ../contracts/openapi/hub-admin.v1.yaml -o src/lib/api/schema.d.ts"
npm run gen:api
```

- The console uses **only** `hub-admin.v1.yaml`. It is self-contained (no external refs). Never hand-write API types. Use `openapi-fetch` (or equivalent) with the generated `paths`.
- The generated file `frontend/src/lib/api/schema.d.ts` is committed and regenerated after every contract bump.

## Change requests

[`docs/process/CONTRACT_CHANGES.md`](../docs/process/CONTRACT_CHANGES.md). The CCR states the problem, the proposed shape, additive or breaking, the affected operationIds, and examples. The steward reviews it against DESIGN and D12, edits the files, bumps the version, adds a `CHANGELOG.md` entry, validates, and notifies backend and frontend.
