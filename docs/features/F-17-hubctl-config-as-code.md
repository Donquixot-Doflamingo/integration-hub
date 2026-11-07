# F-17 — hubctl & config-as-code

## Summary
`hubctl` (`backend/cmd/hubctl`) makes Git the reviewable source of the registry: `plan` diffs local YAML against live, `apply` publishes through the same pipeline, plus `transform test`, `dry-run`, `replay` and `validate` (DESIGN §16.1, §19 M7).

## Status
`shipped` — 2026-11-02 (M7, `docs/milestones/M7.md`): login, whoami, export, plan, apply (subset, see Deviations), dry-run, replay. `hubctl transform test` shipped with F-04 (M2).

## Owner
`backend-engineer`.

## Milestone / Epic
M7 · EP-08 · HUB-200–229.

## Problem
Config changed through a UI alone is hard to review and reproduce. Platform engineers want PR review of exact diffs and an idempotent apply in CI.

## Goals / Non-goals
**Goals**
- `hubctl validate <dir>`: offline validation (F-03 loaders, F-04 compile, goldens).
- `hubctl plan`: field-level diff vs live, resulting new version numbers, objects created/changed.
- `hubctl apply`: publishes via admin API; idempotent (no-op when checksums match).
- `hubctl transform test`, `hubctl dry-run <integration> <body.json>`, `hubctl replay --filter …`.
- `hubctl export`: live registry → YAML (no secrets).
- Auth via OIDC device flow or service token; `reason` flag required for mutations.

**Non-goals**
- UI changes landing as Git commits (DESIGN §16.1 says Release 2).
- Managing secret values (only `credentials_ref`; credentials go through the write-only admin endpoint).

## User stories
- [US-1701](../product/USER_STORIES.md) — Plan before apply
- [US-1702](../product/USER_STORIES.md) — Idempotent apply
- [US-1703](../product/USER_STORIES.md) — Round-trip without secrets
- [US-1704](../product/USER_STORIES.md) — Scripted dry-run and replay

## UX / API surface
| Command | Admin API used (operationIds in `contracts/openapi/hub-admin.v1.yaml`) |
| --- | --- |
| `hubctl plan` | `getLatestSnapshot`, `diffIntegrationVersions`, list endpoints |
| `hubctl apply` | `createVendor`, `createAccount`, `createTransformerVersion`, `createIntegrationDraft`, `validateIntegrationVersion`, `publishIntegrationVersion`, … |
| `hubctl transform test` | local; or `runTransformerGoldens` with `--remote` |
| `hubctl dry-run` | `dryRunTransformer` (transformer + account); integration-level admin dry-run (resolve active version, then transform) (missing — needs CCR) |
| `hubctl replay` | `listDeadLetters`, `replayDeadLetters` |
| `hubctl export` | list/get endpoints |

Example:
```text
$ hubctl plan --dir backend/config --reason "brand B campaign field"
~ integration int_evt_brandB   v2 → v3
    targets[0].transformer.request: T1@7 → T2@3
+ transformer T2@3 (4 goldens: pass)
Plan: 1 to publish, 1 to create, 0 unchanged.
```

## Behaviour & rules
1. `apply` executes the plan in dependency order: vendors → accounts → transformers → tenants/registrar → integrations. Since M7 it applies only account config/quota, tenant settings, integration metadata and pause, and new integration versions; new vendors, endpoints, accounts, transformers and integrations are refused (console or admin API). `hubctl transform test` also checks request samples against the vendor endpoint's `request_schema` (ADR-0023).
2. Nothing in Git is a secret; apply refuses any YAML containing a key named like a credential value outside `credentials_ref` (lint).
3. `apply` stops at the first rejected publish and reports all validation errors for that object.
4. `replay` prints the match count and requires `--yes` or interactive confirmation.
5. Exit codes: 0 no changes / success, 2 changes present (`plan`), 1 error — for CI gating.
6. Output modes: human text and `--json`.

## Data model impact
None (client).

## Config impact
Repo layout `backend/config/{vendors,accounts,transformers,integrations,tenants,registrar}/` (DESIGN §18.1); `capabilities/` was removed by ADR-0023 (schemas live in integration versions and vendor endpoints).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Log | CLI prints change log ids returned by apply | |
| Audit | All mutations appear in `change_log` with `actor` = token subject | |

## Security & PII
Tokens stored in OS keychain or env; never written to config files. `dry-run` output redacts secrets.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Partial apply | Earlier objects applied; re-run is safe (idempotent) | — |
| Live drift (UI edit) | `plan` shows it as a diff | — |

## Testing
- **Integration**: apply dev config twice → second run no-op (DESIGN §19 M7 accept); plan diff golden outputs.
- **Unit**: diff engine table tests.

## Rollout & migration
CI job runs `hubctl plan` on PRs touching `backend/config/` and posts the diff.

## Open questions
- Should `apply` run from CI on merge (GitOps) or only by humans in v1?
- Handling of drift created in the console before UI→Git commits exist.

## Tickets
HUB-200 – HUB-229 (EP-08, filter `feature: F-17`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-02 | Shipped in M7 (see docs/milestones/M7.md) | backend-engineer |
| 2026-11-07 | No capabilities directory or apply step; transform test checks endpoint schemas (ADR-0023, HUB-437) | docs-keeper |
