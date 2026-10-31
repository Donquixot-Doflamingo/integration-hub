# F-03 — Config registry & snapshots

## Summary
Loaders, validation and compilation of all registry objects into an immutable in-memory `Snapshot`, distributed to every node and swapped atomically. The snapshot is the only thing the hot path reads (DESIGN §8, §18.3, §18.5).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M1 · EP-02 · HUB-040–059.

## Problem
Routing, limits, transformers and policies must be readable in microseconds on every request, change within seconds on publish, and never be half-applied.

## Goals / Non-goals
**Goals**
- YAML/JSON loaders for capability, vendor (+ endpoints), account, transformer, integration (+ versions), tenant (+ keys hashed), registrar endpoint (DESIGN §8.1).
- Path-qualified validation errors, all reported at once.
- Snapshot build → compile schemas and transformers → atomic pointer swap.
- Distribution: `config_snapshots` row + blob; nodes long-poll `/admin/v1/snapshots/latest` or receive a Redis ping; ≤ 10 s propagation (DESIGN §8.2).
- Last-good-snapshot on failure; boot from object storage blob if the control plane is down (DESIGN §17).
- Dev registry in `backend/config/` with file watcher; dev objects from DESIGN §19 M1.

**Non-goals**
- The publish pipeline's full validation set (F-14), admin endpoints (F-16), Git round-trip (F-17).

## User stories
- [US-0301](../product/USER_STORIES.md) — Precise validation errors
- [US-0302](../product/USER_STORIES.md) — Atomic, race-free snapshot swap
- [US-0303](../product/USER_STORIES.md) — Propagation within 10 s
- [US-0304](../product/USER_STORIES.md) — Dev file watcher

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | `GET /admin/v1/snapshots/latest` (long-poll, `If-None-Match`) | `contracts/openapi/hub-admin.v1.yaml#getLatestSnapshot` (long-poll via `If-None-Match` + `?wait=<ms>`) |
| Admin API | `GET /admin/v1/snapshots`, `GET /admin/v1/snapshots/{snapshot_version}`, `GET .../blob` | `#listSnapshots`, `#getSnapshot`, `#getSnapshotBlob` |
| CLI | `hubctl validate <dir>` | — |

## Behaviour & rules
1. A `Snapshot` (DESIGN §18.3) is immutable after build; readers obtain it via `Registry.Snapshot()` (atomic pointer load).
2. Build steps: parse → reference resolution (accounts→vendors, versions→accounts/transformers, grants→tenants) → compile JSON Schemas (2020-12, `santhosh-tekuri/jsonschema`) → compile transformers (F-04) → index → checksum.
3. Snapshot `version` is monotonic; `checksum` covers canonical serialisation, so identical content yields identical checksums (enables idempotent `hubctl apply`).
4. Nodes compile in the background and swap only after full success; on failure they keep the current snapshot and raise `snapshot_build_failures_total`.
5. Nodes report their `snapshot_version`; a node > 60 s behind raises a warn alert (DESIGN §16.3).
6. Snapshots retained 90 days (DESIGN §8.2).
7. Secrets are never in the snapshot blob; accounts carry `credentials_ref` only (F-07).

## Data model impact
`config_snapshots(version, checksum, blob_ref, author, diff)`; registry tables from DESIGN §18.2 (`tenants`, `api_keys`, `vendors`, `vendor_endpoints`, `accounts`, `capabilities`, `transformers`, `integrations`, `integration_versions`, `integration_grants`, `registrar_endpoints`).

## Config impact
Defines the YAML formats in DESIGN §3.4, §7.4, §8.3. Capability schema extensions: `x-pii`, `interactive`, `operations` (DESIGN §8.1; `contracts/schemas/capabilities/README.md`). The money/quota flag is **not** a capability-schema extension: it is the integration field `moves_money_or_quota` (`hub-admin.v1.yaml#/components/schemas/Integration`), which resolved PRD Q9; the proposed `x-side-effects` is dropped.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `snapshot_version` | gauge per node |
| Metric | `snapshot_build_duration_ms`, `snapshot_build_failures_total` | |
| Log | `snapshot_swapped` | old, new version, checksum |
| Alert | node > 60 s behind latest snapshot | warn |

## Security & PII
Snapshot blobs contain no secrets; API keys only as hashes. Object storage bucket private.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Control plane down | Keep current snapshot; boot from last blob | Nothing |
| Invalid new snapshot | Rejected; old stays live | Nothing |
| Redis ping lost | Long-poll still delivers within 10 s | Nothing |

## Testing
- **Unit**: table tests of invalid configs → exact error paths; checksum stability.
- **Integration**: 3 in-process nodes, publish, assert all on new version ≤ 10 s.
- **Race**: concurrent readers during swap under `-race` (DESIGN §19 M1 accept).

## Rollout & migration
First migration set lands here. Dev config committed under `backend/config/`.

## Open questions
- Long-poll vs Redis pub/sub as primary; DESIGN allows either.
- Where the snapshot diff is computed (control plane at publish time, stored in `diff`).

## Tickets
HUB-040 – HUB-059.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
