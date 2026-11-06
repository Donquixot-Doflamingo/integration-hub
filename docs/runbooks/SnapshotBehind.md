# SnapshotBehind

**Severity:** warn · **Rule group:** `hub-platform` · **Labels:** `instance`

## What fired
`(scalar(max(snapshot_version)) - snapshot_version) > 0` for 1 minute: this instance reports a lower `snapshot_version` than the highest one in the fleet. A publish should reach every node within 10 s (SLO S5).

**Known false positive (read first).** `snapshot_version` is a per-process counter: it goes up by one each time that process swaps in a snapshot (`registry.Store.Swap`), and starts again at 1 after a restart. It is not the archive version that `getLatestSnapshot` returns. A node restarted after any publish therefore reports a lower number for good, even when it serves the newest config. Step 1 below tells the two cases apart.

## Impact
- If the node really is behind, it serves the previous config: requests it handles resolve to the old integration version, account, weights or pause state. Each request's audit records what it used, so nothing is lost, but a cutover, rollback or pause is not complete on that node.
- Callers see no error; only behaviour that the newest publish changed differs.

## First 5 minutes
1. False positive or real? Get the newest archived snapshot: `getLatestSnapshot`
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/snapshots/latest?wait=0"` (note `version`, `checksum`, `created_at`).
   Then find the instance's last `config snapshot published` log line (fields `snapshot_version`, `checksum`) and any `snapshot.applied from the archive` line (`archive_version`, `checksum`). Same checksum: the node is current; it is the false positive. Silence the alert for that `instance`.
2. **Platform** dashboard (`hub-platform`): **Snapshot version per pod**, **Snapshot apply failures**, **Process restarts**. A restart after the last publish confirms the false positive.
3. Apply failures by reason: `sum by (instance, reason) (increase(snapshot_apply_failures_total[15m]))`. Where the node's snapshot came from: `snapshot_origin{instance="<instance>"} == 1` (`config`, `archive` or `cache`).
4. Logs on that instance: `archived snapshot not loaded: keeping the current one` (fetch failed), `config snapshot build failed` and `config problem` (does not compile, with `file`, `path`, `problem`), `booted from the local snapshot cache`. "Archive unreachable" is logged at DEBUG only, so silence in the logs does not prove the archive is reachable.
5. Readiness: `GET /readyz` on the ops port (`HUB_OPS_ADDR`, default `:9090`) names a failing check without host details.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Node restarted after a publish (false positive) | Checksums match; restart after the last publish | Silence for that `instance`. Engineer fix pending: the gauge should report the archive version, not a per-process count. |
| Node cannot read the archive | No new `snapshot.applied` lines; `fetch_error` failures; node cannot reach Postgres (`hub.config_snapshots`) or the blob store (`HUB_BLOB_URL`) | Restore connectivity from that node. Nodes check the archive on every `hub:snapshot` Redis ping and every 5 s, so it catches up by itself once reachable. |
| Snapshot does not compile on this node | `invalid_config` / `build_error`, `config problem` lines | Usually a node on an older build during a rolling deploy, or a different `HUB_CONTRACTS_DIR`. Finish the rollout or align the contracts directory. The node keeps the last good snapshot meanwhile. |
| Node booted from the local cache | `snapshot_origin{origin="cache"} == 1`, `booted from the local snapshot cache` | It started while the config and archive were unreadable. Fix the archive path above; it then adopts the newest snapshot. |

## Escalate when
- A real lag lasts more than 10 minutes, or affects more than one node.
- A rollback or a pause is not taking effect on part of the fleet (for example during an incident).
- Every node shows `config snapshot build failed` (bad publish: the control plane should have refused it).

## Related
- [runbook index](README.md)
- DESIGN §6.4 (publishing, keep last good snapshot), §8.2 (publish pipeline)
- [TECH_SPEC §7 Snapshot build and distribution](../tech/TECH_SPEC.md), [F-03 Config registry](../features/F-03-config-registry.md), [OBSERVABILITY §11.1](../tech/OBSERVABILITY.md)
