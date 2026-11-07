# SnapshotBehind

**Severity:** warn · **Rule group:** `hub-platform` · **Labels:** `instance`

## What fired
`(scalar(max(snapshot_version)) - snapshot_version) > 0` for 1 minute: this instance reports a lower `snapshot_version` than the highest one in the fleet. A publish should reach every node within 10 s (SLO S5).

`snapshot_version` is the **archive version** of the snapshot the node serves: the `version` that `getLatestSnapshot` returns (HUB-401). Every node serving the same publish reports the same number, restarts included. `0` means the node has not confirmed that its content is archived: it built the config directory or booted from its local cache and has not reached the archive since. A node that is up and can read the archive leaves `0` within a few seconds (it checks the archive at start, on every `hub:snapshot` ping and every 5 s).

## Impact
- If the node really is behind, it serves the previous config: requests it handles resolve to the old integration version, account, weights or pause state. Each request's audit records what it used, so nothing is lost, but a cutover, rollback or pause is not complete on that node.
- Callers see no error; only behaviour that the newest publish changed differs.

## First 5 minutes
1. Which version should it serve? Get the newest archived snapshot: `getLatestSnapshot`
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/snapshots/latest?wait=0"` (note `version`, `checksum`, `created_at`).
   Compare with the instance's `snapshot_version` and its last `config snapshot published` log line (fields `snapshot_version`, `checksum`) or `snapshot.applied from the archive` line (`archive_version`, `checksum`). A value of `0` that stays: the node cannot read the archive (see the causes below).
2. **Platform** dashboard (`hub-platform`): **Snapshot version per pod**, **Snapshot apply failures**, **Process restarts**.
3. Apply failures by reason: `sum by (instance, reason) (increase(snapshot_apply_failures_total[15m]))`. Where the node's snapshot came from: `snapshot_origin{instance="<instance>"} == 1` (`config`, `archive` or `cache`).
4. Logs on that instance: `archived snapshot not loaded: keeping the current one` (fetch failed), `config snapshot build failed` and `config problem` (does not compile, with `file`, `path`, `problem`), `booted from the local snapshot cache`. "Archive unreachable" is logged at DEBUG only, so silence in the logs does not prove the archive is reachable.
5. Readiness: `GET /readyz` on the ops port (`HUB_OPS_ADDR`, default `:9090`) names a failing check without host details.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Node cannot read the archive | No new `snapshot.applied` lines; `fetch_error` failures; node cannot reach Postgres (`hub.config_snapshots`) or the blob store (`HUB_BLOB_URL`) | Restore connectivity from that node. Nodes check the archive on every `hub:snapshot` Redis ping and every 5 s, so it catches up by itself once reachable. |
| Snapshot does not compile on this node | `invalid_config` / `build_error`, `config problem` lines | Usually a node on an older build during a rolling deploy (since ADR-0023 every schema is in the snapshot itself; `HUB_CONTRACTS_DIR` is unused). Finish the rollout. The node keeps the last good snapshot meanwhile. |
| Node booted from the local cache, or from its config directory, and cannot reach the archive | `snapshot_version` 0; `snapshot_origin{origin="cache"} == 1` and `booted from the local snapshot cache`, or `origin="config"` | It started while the archive was unreadable and cannot confirm what it serves. Fix the archive path above; it then reports the archive version, or adopts the newest snapshot. |

## Escalate when
- A real lag lasts more than 10 minutes, or affects more than one node.
- A rollback or a pause is not taking effect on part of the fleet (for example during an incident).
- Every node shows `config snapshot build failed` (bad publish: the control plane should have refused it).

## Related
- [runbook index](README.md)
- DESIGN §6.4 (publishing, keep last good snapshot), §8.2 (publish pipeline)
- [TECH_SPEC §7 Snapshot build and distribution](../tech/TECH_SPEC.md), [F-03 Config registry](../features/F-03-config-registry.md), [OBSERVABILITY §11.1](../tech/OBSERVABILITY.md)
