# F-23 — Schema registry: operations on the integration, request schemas on the endpoint

## Summary
An operator onboards a third-party API entirely from the panel. The integration owns the caller's input (per operation: body and headers, each field required or optional), the vendor endpoint owns the request it accepts (query, headers, body, path), a request transformer between them is proven by samples, and a response transformer is optional (pass-through without one). Replaces capabilities ([ADR-0023](../tech/adr/0023-remove-capabilities-integration-owns-its-request-schema.md)).

## Status
`shipped` — 2026-11-07 (code on `refactor/HUB-431-ep14-combined`, PR #43; contracts 2.0.0).

## Owner
lead (ADR), `contract-steward` (contracts 2.0.0), `backend-engineer` (registry, edge, delivery, admin API), `frontend-engineer` (console editors), `docs-keeper` (skills and docs).

## Milestone / Epic
post-M8 · EP-14 · HUB-431–HUB-459.

## Problem
Every integration had to bind to a **capability**: a vendor-neutral request and response schema under `contracts/schemas/capabilities/`, changed only by PR through the contract-steward. The first real onboarding from the panel (an age estimate from agify.io) needed a code change before any integration could exist. The platform engineer and integration admin ([PERSONAS](../product/PERSONAS.md)) could not onboard an API without a developer, and vendors without a response schema could not be used at all.

## Goals / Non-goals
**Goals**
- Onboard an API from the console alone: vendor, endpoint with its request schema, account (or none), transformers with samples, integration with operations, publish. No PR, no code, no contract change.
- Build every schema from a pasted sample, then mark fields required or optional.
- Refuse a transformer whose samples do not produce the expected request or break the endpoint's schema, naming the sample and the field.
- Check live caller input against the operation's schema (`validate_input`, default on), with no extra cost on the hot path for the vendor side.
- Return a response transformer's output unchanged; without one, return the vendor's answer unchanged (pass-through).
- Support public APIs (auth scheme `none`, accounts without credentials).
- Callers still never see vendor, account or version in the contract or in headers.

**Non-goals**
- A canonical output shared across vendors: switching vendor can change what callers get unless the new response transformer reproduces it.
- Enforcing the vendor's response schema (it is informational only).
- Re-checking the vendor request on live calls.
- Hiding what a vendor body says about itself in pass-through (the integration owner's choice).
- An explicit LLM body-limit / pool setting (HUB-439, [ADR-0024](../tech/adr/0024-llm-body-limit-and-pool-without-capability-names.md)).

## User stories
- [US-2301](../product/USER_STORIES.md#us-2301--onboard-a-public-api-from-the-panel-without-code) — Onboard a public API from the panel without code
- [US-2302](../product/USER_STORIES.md#us-2302--build-a-schema-from-a-pasted-sample) — Build a schema from a pasted sample
- [US-2303](../product/USER_STORIES.md#us-2303--prove-a-request-transformer-with-samples) — Prove a request transformer with samples
- [US-2304](../product/USER_STORIES.md#us-2304--validate-transformers-against-the-operation-schema) — Validate transformers against the operation schema
- [US-2305](../product/USER_STORIES.md#us-2305--check-live-caller-input-validate_input) — Check live caller input (`validate_input`)
- [US-2306](../product/USER_STORIES.md#us-2306--return-the-response-transformers-output-unchanged) — Return the response transformer's output unchanged
- [US-2307](../product/USER_STORIES.md#us-2307--pass-through-without-a-response-transformer) — Pass-through without a response transformer
- [US-2308](../product/USER_STORIES.md#us-2308--public-apis-with-auth-scheme-none) — Public APIs with auth scheme `none`
- [US-2309](../product/USER_STORIES.md#us-2309--callers-discover-operations-and-request-schemas) — Callers discover operations and request schemas
- [US-2310](../product/USER_STORIES.md#us-2310--switch-vendor-behind-the-same-operation-schema) — Switch vendor behind the same operation schema
- [US-2311](../product/USER_STORIES.md#us-2311--existing-data-keeps-working-after-the-upgrade) — Existing data keeps working after the upgrade
- [US-2312](../product/USER_STORIES.md#us-2312--skills-and-docs-teach-the-new-model) — Skills and docs teach the new model
- [US-2313](../product/USER_STORIES.md#us-2313--llm-integrations-get-their-body-limit-and-pool-back) — LLM integrations get their body limit and pool back (planned, HUB-439)

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | Create vendor (auth `none`) | `contracts/openapi/hub-admin.v1.yaml#createVendor` |
| Admin API | Create endpoint with `request_schema` (required) and `response_schema` (optional) | `hub-admin.v1.yaml#createVendorEndpoint`, `#getVendorEndpoint`, `#listVendorEndpoints` |
| Admin API | Account without credentials | `hub-admin.v1.yaml#createAccount` |
| Admin API | Transformer version with samples (`fixtures`, `input_headers`), refusals with `sample` and `field` | `hub-admin.v1.yaml#createTransformerVersion`, `#runTransformerGoldens`, `#dryRunTransformer` |
| Admin API | Integration `interactive`; version `operations` with `request_schema {body, headers}` and `validate_input`; optional response transformer | `hub-admin.v1.yaml#createIntegration`, `#createIntegrationDraft`, `#updateIntegrationDraft`, `#validateIntegrationVersion`, `#publishIntegrationVersion` |
| Caller API | Discovery returns `operations[]` (`OperationDescriptor`) | `contracts/openapi/hub-public.v1.yaml#listIntegrations`, `#getIntegration` |
| Caller API | Invoke: `VALIDATION_FAILED` on input; pass-through response with `X-Hub-Pass-Through: true` | `hub-public.v1.yaml#invokeIntegration`, `#invokeIntegrationOperation`, `#getRequestStatus` |
| MCP | `hub_list_integrations`, `hub_describe_integration` (operations with request schemas); `hub_invoke` / `hub_dry_run` `headers` input; `hub_invoke` `pass_through`; `hub_admin_create_vendor_endpoint` `request_schema` | `contracts/mcp/hubmcp-tools.json` 2.0.0 |
| Console | Vendors → **Create vendor** (auth **None**), **Add endpoint** (schema editor per part), Accounts → **Create account** (no secret fields for `none`), Transformers → **New transformer** (samples, **Run samples on this draft**), Integrations → draft **Operations** (body/headers from a sample, **Check live calls against the request schema**), optional response transformer | — |
| CLI | `hubctl transform test` runs `endpoint_schema` on file samples; `-contracts-dir` ignored | — |
| Removed | `/capabilities*` admin operations, `GET /v1/capabilities/{capability}`, `capability` fields and filters, `CAPABILITY_NOT_FOUND`, `response_conforms`, `hub_admin_list_capabilities` | contracts/CHANGELOG.md 2.0.0 |

Walk-through with every click and call: `.claude/skills/hub-operator/references/onboard-an-api.md`; runnable: `examples/agify-age/`.

## Behaviour & rules
1. Every integration version declares `operations` (≥ 1; `default` for single-operation integrations). Each has `request_schema.body` (required, JSON Schema 2020-12), optional `request_schema.headers`, `idempotent`, optional caller-facing `description`, and `validate_input` (default `true`). Fields in `required` are required, all others optional.
2. Every vendor endpoint version declares `request_schema` with optional `path`, `query`, `headers` (non-credential) and `body` parts; `{}` means nothing besides method and path. `response_schema` is optional and never enforced.
3. A request transformer names its `vendor_endpoint` and no capability or operation; a response transformer names no endpoint. Both need at least one sample.
4. Transformer creation refuses (422, nothing stored) when a sample's output differs from the expected one (`goldens_pass`) or a request sample's produced request breaks the endpoint's schema (`endpoint_schema`). Each refusal names the sample and the field pointer (e.g. `/query/name`). Credential headers and query values from `creds.*` are not part of the check.
5. Integration validate/publish also checks: operation schemas compile, every `req.*` read is declared in the body schema and every `hdr.*` in the headers schema (`schemas_compile`; `hdr.idempotency_key` always allowed), each request sample's input fits the operation (`input_schema`), samples still pass (`goldens_pass`, `endpoint_schema`), and `operations_have_transformers`. `response_conforms` is gone.
6. Live calls: with `validate_input` on, the body and declared headers are checked against the operation's compiled schema before anything else runs (400 `VALIDATION_FAILED`, one `details[]` entry per violation). With it off, nothing is checked live. Only declared caller headers reach the transformer as `hdr.*`; the Hub's own headers can never be declared. The vendor request is never re-checked live.
7. A response transformer's `response` output is returned unchanged (200), with no schema check; its `errors` rules map failures to `UPSTREAM_*` as before.
8. Pass-through (no response transformer), sync answers only: a vendor 2xx returns the vendor's status and body unchanged (non-JSON keeps its `Content-Type`); a vendor non-2xx keeps the default retry and breaker classification (timeouts, 429, 5xx retryable) and, once retries are exhausted, returns the vendor's status and body. Only `Content-Type` and `Retry-After` vendor headers pass, plus the Hub's own and `X-Hub-Pass-Through: true`. Timeouts, open circuits and Hub failures keep the canonical envelope. Requests completed after a 202 store the vendor body as the result (non-JSON as a JSON string) and keep `UPSTREAM_*` codes for failures.
9. Auth scheme `none`: accounts have no credentials (`credentials` omitted or `{}`; anything else is 400 at `/credentials`), credential operations answer 409, and no auth is applied at send time.
10. `interactive` is an integration flag fixed at creation; interactive integrations allow only `sync`.
11. `delivery.ttl` defaults to the Hub default. D11 is guidance: a request schema change that breaks callers should use a new integration id; the publish diff and change log show schema changes.
12. `x-pii` tags on operation schemas drive audit masking, sealing and deletion (they used to come from the capability).

## Data model impact
- Migration `00011` drops `hub.capabilities` and every `capability` column, copies existing capability schemas into integration versions' `operations`, and writes `pool: llm` into queued `llm.*` envelopes before dropping their capability key (ADR-0024). Append-only.
- Snapshot archive, drafts and pending integrations written before ADR-0023 are upgraded in code (`registry.UpgradeLegacy`, `control.UpgradeLegacyArchive`), and re-archived once by the admin role as blob format 2 ([ADR-0025](../tech/adr/0025-pre-adr-0023-data-upgraded-in-code.md)).
- The change log keeps old rows with object type `capability` (append-only).

## Config impact
- Integration version YAML: `operations.<name>.{description, idempotent, validate_input, request_schema.{body, headers}}`; `targets[].transformer.response` optional.
- `integration.yaml`: `interactive` (new), no `capability`.
- Vendor YAML: `auth: none` allowed; `endpoints.<name@N>.request_schema` (required; `{}` allowed) and `response_schema` (optional).
- Account YAML: no `credentials_ref` for a `none` vendor.
- Transformer YAML: no `capability` or `operation`; `vendor_endpoint` only on request transformers. Fixtures `_context.hdr` holds the sample's caller headers.
- `config/capabilities/` and the examples' capability overlays are gone. Config files that still name a `capability` fail strict loading (unknown field); only archived snapshot blobs, drafts and pending integrations are upgraded in code.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | none new | `hub_requests_total` counts pass-through with `code=ok` for a vendor 2xx and the default classification's code otherwise; `hub.requests.http_status` holds the vendor status. Validation time stays in the `validate` stage of `hub_overhead_ms` |
| Metric | `snapshot_apply_failures_total{reason="archive_error"}` | existing; also counts a failed one-time legacy upgrade |
| Log | `request failed` | INFO, existing line, with the vendor `status` and `pass_through=true` for a pass-through vendor error (no body) |
| Log | `legacy snapshot upgraded and archived (ADR-0023)` | INFO, admin, once: `archive_version`, `drafts_skipped` |
| Log | `legacy draft not upgraded: edit it to declare operations` | WARN, admin: `integration` |
| Log | `legacy snapshot not upgraded` | ERROR, admin: `error` (nodes keep upgrading in memory) |
| Audit | `response.transformed`, `request.failed` meta `pass_through: true`, vendor `status` | per request |
| Trace | none new | |
| Alert | none new | |

Listed in `docs/tech/OBSERVABILITY.md` §4.4.

## Security & PII
- **Vendor identity (DESIGN §0).** The caller contract, discovery and MCP tenant tools carry no vendor, account or version. Pass-through drops every vendor header except `Content-Type` and `Retry-After`. **The pass-through body is the vendor's own**: it may name the vendor, echo input or carry vendor links. The integration owner accepts this by skipping the response transformer; the hub-operator skill tells operators to warn them. The dry-run exception (DV-6) is unchanged.
- **Secrets.** Credentials still enter only through `creds.*` in header/query/auth targets and are excluded from `endpoint_schema` checks and from `expected_request` in API samples. Auth `none` accounts hold no secrets; MCP still cannot create accounts (`createAccount` stays forbidden).
- **PII.** `x-pii` tags move to the operation schemas; sealed and masked policies read them from there. Samples must use fake data.
- **Header allowlist.** Only declared caller headers reach transformers; the Hub's own headers cannot be declared, and hubmcp refuses them in `headers`.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Caller body misses a required field (`validate_input` on) | Refused at the edge, nothing queued or sent | 400 `VALIDATION_FAILED`, `details[]` with pointers |
| Same with `validate_input` off | Transformer runs on the body; missing reads are nil | Whatever the vendor answers (or `TRANSFORM_FAILED` if an expression throws) |
| Transformer sample breaks the endpoint schema | Create refused, nothing stored | — (operator sees 422 with sample and field) |
| Transformer reads an undeclared field | Validate/publish refused (`schemas_compile`) | — |
| Live vendor request the vendor rejects | Not checked by the Hub; vendor 4xx | Mapped error, or the vendor's 4xx in pass-through |
| Pass-through vendor 5xx/429 | Retried per policy, then returned | Vendor status and body, `X-Hub-Pass-Through: true` |
| Pass-through on a request completed after 202 | Body stored as result; failures classified | `GET /v1/requests/{id}` with `UPSTREAM_*` on failure |
| Pre-ADR-0023 snapshot blob | Upgraded in memory on load; archived once by admin | Nothing |
| Pre-ADR-0023 draft with an unknown capability | Left as is, logged | — (operator edits it) |
| Credentials sent for a `none` vendor | 400 at `/credentials` | — |

## Testing
- **Unit**: registry operations and sample checks (`internal/registry/operations_test.go`: `TestSampleInputProblemCarriesSampleAndField`, `TestOperationsValidShapes`), legacy blob upgrade (`TestUnpackUpgradesLegacyBlobs`), transform field refs and golden runner with `_context.hdr`.
- **Integration** (real Postgres/Redis): `TestAgifyOnboardedWithoutCapabilities` (admin API only: vendor `none`, endpoint schema, credential-less account, `endpoint_schema` refusal at `/query/name`, `goldens_pass`, response transformer output, pass-through, `validate_input` on and off, discovery), `TestE2EPassThrough` (gateway, contract-validated), `TestWorkerPassThrough` (async), `TestAuthNoneAccounts`, `TestRemoveCapabilitiesMigration` (00011 up/down), `TestUpgradeLegacyArchive`.
- **Contract**: every changed admin and caller response validated against the 2.0.0 OpenAPI; pass-through validated against the `default` PassThrough response.
- **Golden**: every seed transformer's samples, including `endpoint_schema`, via `hubctl transform test`.
- **Console**: `frontend/e2e/onboarding.test.ts` (agify-like API onboarded from scratch and published; a failing sample names its field and keeps Create locked).
- **Latency**: `make latency-budget` (HUB-433): sync added p50/p99 1.2/5.7 ms with a 100 ms vendor.

## Rollout & migration
Breaking change to the caller and admin contracts, approved by the product owner on 2026-11-07 (pre-1.0, nothing released): both at 2.0.0, MCP contract 2.0.0. Deploy order: migration 00011, then any role (legacy blobs are readable by every role, ADR-0025). The legacy reader can be removed 90 days after every environment booted this version once. Migration 00011 has a tested down migration (`TestRemoveCapabilitiesMigration`).

## Open questions
- Show response samples of the old and new response transformers side by side in the publish diff when a target changes (ADR-0023 consequence; not verified in the console today).
- Explicit LLM integration setting (HUB-439).

## Tickets
HUB-431 – HUB-439 (see `docs/tickets/TICKETS.md`, filter by `feature: F-23`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-11-07 | Created (HUB-437) | docs-keeper |
