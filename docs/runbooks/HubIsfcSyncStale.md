# HubIsfcSyncStale

**Severity:** warn · **Rule group:** `hub-isfc` · **Labels:** none

## What fired
`max(hub_isfc_sync_lag_seconds) > 900 or max(hub_isfc_sync_consecutive_failures) >= 3` for 1 minute. The worker that leads the ISFC reconcile loop (`HUB_TENANT_SOURCE=isfc`) has not finished a successful run for 15 minutes, or three runs in a row failed. Followers report 0 for both gauges, so the series that fires belongs to the current leader.

## Impact
- The Hub keeps its last good tenants and its last good ISFC caller keys. **Nothing is disabled because a fetch failed** (ADR-0027).
- New tenants linked in ISFC do not appear, renames and deactivations are not copied, and a caller key revoked in ISFC keeps working at the edge until a run succeeds (AUTH_PLAN G15).
- Hub tenant keys (`hk_live_`) and the Hub's own settings are not affected.

## First 5 minutes
1. Which worker leads: `hub_isfc_sync_lag_seconds > 0` names the instance. Its logs: `isfc.sync run did not complete` (fields `run`, `trigger`, `result`, `error`) and `isfc.sync leader election failed`.
2. Last runs: `SELECT id, trigger, result, error, started_at FROM hub.isfc_sync_runs ORDER BY started_at DESC LIMIT 10;` Quiet ticks leave no row; failed runs always do.
3. Can the worker reach the auth-service? `error` names the step (`console client metadata`, `list tenants`, `fetch own api key`) and the HTTP status. 401 or 403 after a key refresh: the Hub backend client lost its S2S permissions (`TENANT:READ`, `CLIENT:READ`, ISFC-CR-5) or its secret changed.
4. Try one run by hand: `syncTenants` (admin API, "Sync now"). A failed fetch answers 200 with `result: failed` and the error.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| auth-service down or unreachable from the worker | `error` has a connect or timeout cause, or 5xx | Restore connectivity; the next tick (`HUB_ISFC_RECONCILE_INTERVAL`) or "Sync now" recovers by itself. |
| Hub backend client credentials rejected | `fetch own api key` with 400 `AS_VE_09`, or 401/403 on every call | Fix `HUB_ISFC_CLIENT_ID` / `HUB_ISFC_CLIENT_SECRET`, or restore the client's key and permissions in ISFC. |
| No leader (lock held by a stuck session) | `isfc.sync leader election failed`; no instance reports `hub_isfc_sync_lag_seconds > 0` | Check Postgres connectivity of the workers; the advisory lock is released when its session ends. |
| Config problem on apply | `error` starts with `apply:` | A tenant file the sync wrote would not build; see the `config problem` log lines. |

## Escalate when
- The lag lasts more than an hour, or a revoked caller key must stop working now (disable the caller's tenant with `updateTenant` while ISFC is down).

## Related
- [runbook index](README.md), [ISFC authentication runbook](isfc-auth.md) (key revocation while the sync is stale)
- [AUTH_PLAN §6.2, §11](../tech/AUTH_PLAN.md), [F-25 Tenants from ISFC](../features/F-25-tenants-from-isfc.md), [OBSERVABILITY §15](../tech/OBSERVABILITY.md)
