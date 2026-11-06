# TransformFailed

**Severity:** page · **Rule group:** `hub-errors` · **Labels:** `integration`

## What fired
At least one request of this integration ended in `TRANSFORM_FAILED` in the last 5 minutes. It reads `hub_requests_total{code="TRANSFORM_FAILED"}` (`increase(...[5m]) > 0`, plus a branch for the first failure of a new label set). No `for`: one failure pages. On async paths a transformer time-budget overrun retries and is counted only if the request finally fails.

`TRANSFORM_FAILED` is always a Hub bug (DESIGN §4.4): a request, response or error transformer threw, or ran over its budget.

## Impact
- **sync** and **wait** callers get 500 `TRANSFORM_FAILED` (not retryable).
- **async** work is dead-lettered with reason `TRANSFORM_FAILED`; it can be replayed after the fix, and the replay re-transforms with the fixed version.
- A response-side failure means the vendor call may have succeeded while the caller got an error.

## First 5 minutes
1. **Tenant** dashboard (`hub-tenant`), pick the owner tenant and `integration`: **Error codes (excluding ok)**. **Platform**: **Requests by code** for the overall rate.
2. Rate and spread: `sum by (vendor, account, version) (increase(hub_requests_total{integration="<integration>", code="TRANSFORM_FAILED"}[15m]))`. One `version` only, starting at a publish, points at that version.
3. Find failing requests: `searchRequests`
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/requests?integration_id=<integration>&code=TRANSFORM_FAILED&from=<RFC3339>&to=<RFC3339>&limit=20"`.
4. Open one: `getRequestTimeline` (`/admin/v1/requests/<request_id>/timeline`). `response.transform_failed` holds the failing expression and its input when the payload policy keeps bodies. `sealed` needs `unsealRequest` (audited; only if you must).
5. Logs: `transform failed` / `response transform failed` (WARN, with `request_id`). What changed: `listChangeLog?object_type=integration&object_id=<integration>`.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| New integration or transformer version | Failures start at a `publishIntegrationVersion` / weight change | `rollbackIntegration` (`to_version` = last good, `expected_active_version` = current). Then fix forward. |
| Vendor changed its response shape | Response-side failures, `vendor.responded` body differs from the goldens | Fix the response transformer (new version via `createTransformerVersion`) with a golden built from the masked response; publish. |
| Caller input the transformer does not handle | Request-side failures for some bodies only; input valid against the schema | Reproduce: `hubctl dry-run <integration> body.json` (masked body). Add a golden for the case, fix, publish. |
| Time budget overrun under CPU pressure | Error mentions the time budget; CPU or GC high on the node ([HubOverheadP99High](HubOverheadP99High.md)) | Relieve CPU (more gateway replicas). If it is one heavy transformer, simplify it. |

Fix-forward checklist: build a golden fixture from the masked input, run `hubctl transform test <TRANSFORMER>` locally (or `runTransformerGoldens`), create the transformer version, publish an integration version that uses it (publishing re-runs every golden), then replay:
`hubctl replay -integration <integration> -error-code TRANSFORM_FAILED -reason "<fix>" -dry-run`, then `-yes`.

Stopgap for async-heavy integrations: `pauseIntegration` holds queued work instead of dead-lettering it, but sync and wait callers then get 503 `INTEGRATION_PAUSED`. Use only when that trade is acceptable; `resumeIntegration` after the fix.

## Escalate when
- Rollback does not stop the failures (the bug is in shared code, not config).
- Failures span several integrations at once (a Hub release or the expression stdlib).
- The integration is `moves_money_or_quota` and failures are response-side (the vendor may have acted): reconcile with the vendor before any replay.

## Related
- [DLQSurge](DLQSurge.md), [HubOverheadP99High](HubOverheadP99High.md), [runbook index](README.md)
- DESIGN §4.4, §6.4 (publishing rules, rollback), §7.6 (testing and safety rails), §10.6 (DLQ replay)
- [F-04 Transformation engine](../features/F-04-transformation-engine.md), [F-14 Versioning and cutover](../features/F-14-versioning-cutover.md), [OBSERVABILITY §9.5](../tech/OBSERVABILITY.md)
