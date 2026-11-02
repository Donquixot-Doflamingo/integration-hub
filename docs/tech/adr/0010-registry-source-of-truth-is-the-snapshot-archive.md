# ADR-0010: The registry's source of truth is the versioned config tree in the snapshot archive

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-02 |
| Deciders | lead |
| Tickets | HUB-170, HUB-171, HUB-172, HUB-176 |
| DESIGN refs | §6.4, §8.2, §17; TECH_SPEC §6.1, §7 |

## Context
TECH_SPEC §7 describes publishing as "TX: registry rows + config_snapshots N+1 + change_log", with the registry tables of migration 00002 as the source of truth. Since M1 the Hub has worked differently: the registry is a tree of YAML files (`backend/config`, plus capability schemas from `contracts/`), `registry.Build` validates and compiles it (goldens included), and every snapshot's *sources* are packed into a blob and recorded in `hub.config_snapshots` (HUB-048). Nodes already boot from that archive. The registry tables were created but nothing reads them.

M6 needs a publish pipeline (immutable versions, rollback, account lifecycle). Making the tables authoritative would mean a second loader for every object kind and a translation between rows and the files the transformer goldens live next to, ahead of the admin API (M7) that would write them.

## Decision
The registry's source of truth is **the config tree of the latest archived snapshot**. A publish:
1. takes the latest archived sources (the config directory when the archive is empty),
2. applies the change as file edits (a new `versions/<n>.yaml`, `active_version`, account `state`),
3. runs the full `registry.Build` (every DESIGN §6.4 check, goldens included) on the result,
4. archives the new snapshot with author and reason, writes a `change_log` row in the same flow,
5. pings `hub:snapshot` on Redis; nodes fetch the archive and swap.

`hubctl plan|apply` (M7) and the admin API (M7) produce the same file edits. The registry tables of 00002 stay unused; a later ADR may mirror the tree into them for queries.

## Consequences
- Config-as-code holds end to end: what nodes run is exactly a reviewed tree, checksummed and archived; rollback is "publish the old file again".
- Versions are immutable because a published `versions/<n>.yaml` is never rewritten in a later tree (the publisher refuses to touch an existing version file).
- The dev config directory is a bootstrap: once the archive holds a published snapshot, the admin role no longer archives the directory at boot (it would undo publishes); with `HUB_CONFIG_WATCH` it still archives directory edits in dev.
- Listing and filtering registry objects (admin API) reads the snapshot in memory, not SQL.
