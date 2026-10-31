# F-04 — Transformation engine & golden fixtures

## Summary
A declarative YAML mapping DSL with `expr-lang/expr` expressions, compiled and type-checked at snapshot build, that converts canonical requests to vendor requests and vendor responses/errors back to canonical form. Every transformer ships with golden fixtures that gate CI and publish (DESIGN §7, D8).

## Status
`proposed` — 2026-10-31.

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
- Golden runner `hubctl transform test`; publish refuses failing or missing goldens.
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
2. Type-check against the capability request schema (and vendor schema where available); unknown fields fail compile (DESIGN §7.6.4).
3. Purity: no network, no disk, no loops beyond `map`/`filter`; limits on expression depth, output bytes and runtime per transform (exact values in TECH_SPEC).
4. `creds.*` allowed only in header/query/auth targets unless `allow_creds_in_body: true` (DESIGN §7.2).
5. Values that differ per account come from `account.config.*`; a new transformer only when shape changes (DESIGN §3.3).
6. Error rules evaluated in order; first match wins; no match → default classification of DESIGN §10.2.
7. A transformer version is immutable; references are `Name@version`.
8. Each production transformer file cites the vendor API doc URL it was verified against (DESIGN §0).
9. Benchmark `BenchmarkTransform4KB` ≤ 0.5 ms p99 (DESIGN §7.6.5).

## Data model impact
`transformers(transformer_id, version, kind, capability, endpoint_id, yaml, fixtures)` immutable (DESIGN §18.2).

## Config impact
Transformer YAML schema (kind `request|response|inbound`, `vendor_endpoint`, `request`, `response`, `errors`, `batch`, `stream`, `supports`, `allow_creds_in_body`).

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
