# F-04 — Transformation engine & golden fixtures

## Summary
A declarative YAML mapping DSL with `expr-lang/expr` expressions, compiled and type-checked at snapshot build, that converts the caller's input to vendor requests and, optionally, vendor responses/errors back to what the caller gets. Every transformer ships with samples (golden fixtures) that gate creation, CI and publish (DESIGN §7, D8; ADR-0023, [F-23](F-23-schema-registry.md)).

## Status
`shipped` — 2026-10-31 (M2, HUB-060–071 on `phase-2/m2-transformers`; [M2 summary](../milestones/M2.md)). Notable: nil-safe field access, `res.text` for non-JSON bodies, budgets enforced by construction + post-run check.

## Owner
`backend-engineer` (engine) · platform engineers (transformer authors, D7).

## Milestone / Epic
M2 · EP-03 · HUB-060–079.

## Problem
Vendor mappings scattered in caller code are untested and diverge. Mappings in the Hub must be fast (≤ 0.5 ms), safe (pure, bounded) and provably correct before they serve traffic.

## Goals / Non-goals
**Goals**
- Parse → compile with typed env (`req`, `hdr`, `account`, `creds`, `session`, `res`, `now()`; DESIGN §7.2) → run.
- Standard library from DESIGN §7.3, extended only by code releases.
- Request mapping (method, path, headers, query, body), response mapping, `errors:` rules, dry-run.
- Golden runner `hubctl transform test` (also checks each request sample against the vendor endpoint's `request_schema`); creation and publish refuse failing or missing samples.
- Transformers T1, T2, R1, R2 with fixtures (DESIGN §7.4).

**Non-goals**
- `stream:` mapping and `batch:` execution (Release 2, F-22); in-browser editing (Release 2). `batch:` is parsed and validated now.

## User stories
- [US-0401](../product/USER_STORIES.md) — Goldens gate publish
- [US-0402](../product/USER_STORIES.md) — Compile-time type checking
- [US-0403](../product/USER_STORIES.md) — Bounded, pure execution
- [US-0404](../product/USER_STORIES.md) — Config over fork
- [US-0405](../product/USER_STORIES.md) — Error mapping

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | Transformers list/get, versions list/get, create new version only (`POST /admin/v1/transformers/{transformer_id}/versions`) | `contracts/openapi/hub-admin.v1.yaml#listTransformers`, `#getTransformer`, `#listTransformerVersions`, `#getTransformerVersion`, `#createTransformerVersion` |
| Admin API | `POST /admin/v1/transformers/dry-run`, `POST /admin/v1/transformers/run-goldens` (stored `ref` or `draft`) | `contracts/openapi/hub-admin.v1.yaml#dryRunTransformer`, `#runTransformerGoldens` |
| CLI | `hubctl transform test [path]`, `hubctl dry-run` | — |
| Files | `config/transformers/<name>@<v>.yaml`, `fixtures/<name>.{input,expected_request,vendor_response,expected_response}.json` | — |

## Behaviour & rules
1. Compile once per snapshot; never parse or compile per request (DESIGN §7.1).
2. Expressions type-check at compile. The transformer knows no caller schema (ADR-0023): its `req.*` / `hdr.*` reads (`Program.FieldRefs`) are checked at integration validate/publish against each operation that uses it (`schemas_compile`); request samples' produced requests are checked against the vendor endpoint's `request_schema` (`endpoint_schema`, credentials excluded) and their inputs against the operation (`input_schema`).
3. Purity: no network, no disk, no loops beyond `map`/`filter`; limits on expression depth, output bytes and runtime per transform (exact values in TECH_SPEC).
4. `creds.*` allowed only in header/query/auth targets unless `allow_creds_in_body: true` (DESIGN §7.2). Any `$`-prefixed identifier (`$env`) is rejected at compile, because `$env.creds` would reach the secrets past this rule (HUB-447).
5. Values that differ per account come from `account.config.*`; a new transformer only when shape changes (DESIGN §3.3).
6. Error rules evaluated in order; first match wins; no match → default classification of DESIGN §10.2.
7. A transformer version is immutable; references are `Name@version`.
8. Each production transformer file cites the vendor API doc URL it was verified against (DESIGN §0).
9. Benchmark `BenchmarkTransform4KB` ≤ 0.5 ms p99 (DESIGN §7.6.5).
10. Response transformers may declare a `session:` section (HUB-175, F-14): a mapping of session fields evaluated on success only, against `res`, `req` and `account` (`creds`, `hdr` and `session` are rejected at compile; `req.*` paths are checked against the operation's request schema). `Program.MapResponseSession` returns it (nil when every field is nil); it is bounded by `MaxSessionBytes` (16 KB) and the same time budget. Goldens pin it as `"session"` in `expected_response.json`; vendor-response fixtures may carry `_context.req`.
11. `currencyMinor` uses the full ISO 4217 minor-unit table (0 digits: BIF CLP DJF GNF ISK JPY KMF KRW PYG RWF UGX UYI VND VUV XAF XOF XPF; 3: BHD IQD JOD KWD LYD OMR TND; 4: CLF UYW; 2 for the rest). An amount with more decimals than the currency allows is refused, never rounded, and an unknown currency code is refused, never assumed to have 2 digits (HUB-447).
12. A NaN or ±Inf anywhere in an expression result (a division by zero, for example) fails the run with an `EvalError` (TRANSFORM_FAILED) in request body, headers, query, response output, session and error mapping; it is never encoded or sent as "+Inf" (HUB-447).
13. `format` converts whole-number float arguments (JSON numbers) to integers for `%d %v %s %x` and keeps floats for `%f %e %g`; a layout that does not fit its arguments (output would contain `%!`) is an `EvalError`, not text sent to the vendor (HUB-447).

## Data model impact
`transformers(transformer_id, version, kind, endpoint_id, yaml, fixtures)` immutable (DESIGN §18.2); the `capability` column was dropped by migration 00011 (ADR-0023). A response transformer has no endpoint and is optional in a target (pass-through without one).

## Config impact
Transformer YAML schema (kind `request|response|inbound`, `vendor_endpoint`, `request`, `response`, `errors`, `session`, `batch`, `stream`, `supports`, `allow_creds_in_body`).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_overhead_ms{stage="transform"}`, `{stage="response_map"}` | |
| Metric | `transform_failures_total{transformer,version}` | |
| Log | `transform_failed` | transformer, version, expression id (no payload values) |
| Alert | any `TRANSFORM_FAILED` | page (DESIGN §16.3) |

## Security & PII
`creds` placement rule enforced at compile; dry-run output redacts secrets; `x-pii` fields may be masked via `maskPAN`/`maskAadhaar` helpers.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Runtime exception / budget exceeded | Abort; audit `response.transform_failed` with failing expression | 500 `TRANSFORM_FAILED` (async: dead-lettered) |
| Missing optional field | `??` coalesce or null per expression | Normal |

## Testing
- **Unit**: each stdlib function (table tests); compile errors for missing fields; budget enforcement.
- **Golden**: T1, T2, R1, R2 fixtures; CI runs `hubctl transform test`.
- **Benchmark**: 4 KB payload.

## Rollout & migration
Engine lands in M2; first transformers target mockvendor in dev; production CleverTap transformers need vendor-doc verification first.

## Open questions
- Exact budget values (depth, output size, time) — proposed 32 / 1 MB / 2 ms.
- How fixtures are stored in the DB (`fixtures` JSONB vs blob ref).

## Tickets
HUB-060 – HUB-079.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-02 | `session:` section in response transformers (HUB-175) | backend-engineer |
| 2026-11-07 | No capability binding: field reads checked against operations, samples against endpoint schemas, response transformer optional (ADR-0023, HUB-437) | docs-keeper |
