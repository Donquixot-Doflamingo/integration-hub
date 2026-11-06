# DLQSurge

**Severity:** page · **Rule group:** `hub-delivery` · **Labels:** `account`

## What fired
Unreplayed dead letters for one account grew by more than 50 in 10 minutes (no `for`: it fires on the first evaluation). It reads the `dlq_count` gauge (scheduler leader, every 10 s): `sum by (account) (dlq_count) - sum by (account) (dlq_count offset 10m)`, or the whole current count for an account that had none in the 10 minutes before. Replays inside the window hide as many new entries. `account=""` means requests that failed before a target was resolved.

## Impact
- **async**, **async_callback** and the tail of **wait** requests ended `dead_lettered` with a canonical code (status and callback carry it). Nothing retries them automatically.
- Dead letters are kept 14 days and can be replayed by filter; a replay re-resolves and re-transforms, so a fix applies (DESIGN §10.6).

## First 5 minutes
1. **Vendor and account** dashboard (`hub-vendor`), set `account`: **Dead letters by reason (unreplayed)**. **Platform**: **Dead letters (unreplayed) by reason**.
2. Which integrations and reasons? `sum by (integration, reason) (dlq_count{account="<account>"})`.
3. List and open one: `listDeadLetters`
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/dlq?account_id=<account>&replayed=false&limit=20"`,
   then `getDeadLetter` (`/admin/v1/dlq/<request_id>`): `last_vendor_response`, `attempt_history` (version, `cred_version`, breaker state, error code per attempt).
4. Count what a replay would take, without replaying:
   `hubctl replay -account <account> -error-code <reason> -dry-run -reason "count for DLQSurge"`.
5. Did something change just before? `listChangeLog` (`/admin/v1/change-log?limit=20`): publishes, credential promotions, weight changes.

## Likely causes → fix
| Reason | Usual cause | Fix |
| --- | --- | --- |
| `UPSTREAM_AUTH_FAILED` | Credentials rejected | [UpstreamAuthFailed](UpstreamAuthFailed.md), then replay. |
| `TRANSFORM_FAILED` | Transformer bug | [TransformFailed](TransformFailed.md), then replay. |
| `UPSTREAM_REJECTED` | Vendor 4xx: a transformer change, a vendor API change, or bad caller data | Read `last_vendor_response`. Hub side: `rollbackIntegration` or a fixed transformer version, then replay. Caller data: tell the tenant (integration ID and code only); `purgeDeadLetters` only for junk such as test traffic. |
| `UPSTREAM_TIMEOUT`, `UPSTREAM_UNAVAILABLE`, `UPSTREAM_RATE_LIMITED` | Retries exhausted (8 attempts) during a long vendor problem | Check [BreakerOpenTooLong](BreakerOpenTooLong.md). Replay once the vendor is healthy. |
| `resolve_failed`, `INTEGRATION_NOT_FOUND` | Integration removed, or its target account retired, while work was queued | Restore a valid active version (`rollbackIntegration` / publish), then replay. |
| `SESSION_NOT_FOUND` | Vendor session expired before a later step was delivered | Do not replay; the caller must restart the flow from step 1. |
| `poison` | Envelope could not be decoded or crashed a worker twice | Do not replay. Escalate with a `request_id`. |

Replay after the fix, dry run first:
```sh
hubctl replay -account <account> -error-code <reason> [-integration <id>] [-from <RFC3339>] [-to <RFC3339>] -reason "<what was fixed>" -dry-run
hubctl replay -account <account> -error-code <reason> [-integration <id>] -reason "<what was fixed>" -yes
```
(`replayDeadLetters` under the hood; one `change_id` per page of up to 10 000.)

## Escalate when
- Reason `poison`, or a reason not in the table.
- A `moves_money_or_quota` integration has `UPSTREAM_TIMEOUT` dead letters: the vendor may have done the work already, so confirm with the vendor before any replay.
- The surge continues after the fix (new entries keep arriving).

## Related
- [UpstreamAuthFailed](UpstreamAuthFailed.md), [TransformFailed](TransformFailed.md), [BreakerOpenTooLong](BreakerOpenTooLong.md), [runbook index](README.md)
- DESIGN §10.2 (retry policy by outcome), §10.6 (DLQ), §4.4 (codes)
- [F-09 Durable delivery](../features/F-09-durable-delivery.md), [F-17 hubctl](../features/F-17-hubctl-config-as-code.md), [OBSERVABILITY §9.2, §9.4](../tech/OBSERVABILITY.md)
