# ADR-0025: Data written before ADR-0023 is upgraded in code, not only by the SQL migration

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | backend-engineer (for lead review) |
| Tickets | HUB-433, HUB-434 |
| DESIGN refs | §8 (snapshots); ADR-0010 (config tree in the snapshot archive); ADR-0023 |

## Context
ADR-0023 asks for "a new migration [that] drops [`hub.capabilities`] and the `capability` columns, and copies existing capability schemas into integration versions' `operations`". Migration 00011 does that for the relational tables. But since ADR-0010 the registry nodes run is not in those tables: it is the **snapshot archive**, `hub.config_snapshots` rows pointing at gzip blobs in the blob store (a directory today, object storage later), plus `hub.integration_drafts` and `hub.integration_pending`, which hold YAML documents. Before ADR-0023 a blob (format 1) packed the config tree **and** the capability schemas it used, and its checksum covered both.

SQL cannot rewrite blob files, and rewriting the YAML documents in SQL would duplicate the registry's parser. Rewriting an archived blob in place would also break its recorded checksum and the history that rollback and diff read.

## Options considered
1. **SQL only.** Leaves every archived snapshot unreadable by the new code: nodes and the admin would refuse the archive (`ErrCorruptBlob`) and fall back to the config directory, losing every change published through the admin API.
2. **A one-off Go command run by the operator before upgrading.** Easy to forget; a node started first fails.
3. **Upgrade in code, where the data is read.** `registry.Unpack` verifies a format-1 blob against its legacy checksum (the old algorithm, reproduced exactly) and converts the tree in memory (`registry.UpgradeLegacy`: capability schemas become each version's `operations`, `interactive` moves to the integration, endpoints get an empty `request_schema`, `capability`/`operation` leave transformers). Once, at boot, the admin role re-archives the upgraded snapshot as a new archive version (format 2) and rewrites drafts and pending integrations (`control.UpgradeLegacyArchive`). Migration 00011 still handles the relational tables and the queued envelopes.

## Decision
Option 3. Every role can read old blobs at any time, so start order does not matter; the admin's one-time step makes the latest archive version current, so diffs and new publishes work on the new layout. A draft whose capability is unknown is left as it is and logged (`legacy draft not upgraded`); the operator edits it.

## Consequences
- An existing database comes up working with no manual step (checked with a dev database built by the pre-ADR-0023 `dev-up`; `TestUpgradeLegacyArchive`, `TestUnpackUpgradesLegacyBlobs`).
- The legacy reader (`registry/legacy.go`, `control/legacy.go`, `registry/testdata/legacy`) stays in the code while archived format-1 blobs can still be read: snapshots are kept 90 days (DESIGN §8.2), so it can be removed 90 days after every environment has booted this version once. Removing it earlier makes rollback to an older archive version impossible.
- Callers and the contracts see nothing of this.

## Deviation from DESIGN.md / ADR-0023
- ADR-0023's "a new migration copies existing capability schemas" is done by migration 00011 for the tables and by code for the archive, drafts and pending integrations.
