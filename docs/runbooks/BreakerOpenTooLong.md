# BreakerOpenTooLong

**Severity:** page · **Rule group:** `hub-resilience` · **Labels:** `vendor`, `account`, `endpoint` (for example `upload@1`)

## What fired
`max by (vendor, account, endpoint) (breaker_state) >= 1` for 5 minutes: the circuit for one `(account, endpoint)` has been open (2) or half-open (1) on at least one node for 5 minutes. A half-open probe that fails re-opens the breaker, so "not closed" is what is measured. Every node reports its own view; `max` takes the worst one.

## Impact
- **sync** callers follow the integration's `on_circuit_open` (DESIGN §5.3): `fail_fast` (default) returns 503 `UPSTREAM_UNAVAILABLE` with `retry_after_ms` and nothing is sent; `failover` is served by the next target; `queue_if_allowed` returns 202 when the caller allows async.
- **wait** callers get 202 `queued` (reason `circuit_open`). **async** and **async_callback** callers get 202; the work parks and drains after the breaker closes (see [ParkedOldestAgeHigh](ParkedOldestAgeHigh.md)).
- Other accounts and endpoints are not affected (breaker scope is `(account, endpoint)`, D5).

## First 5 minutes
1. **Vendor and account** dashboard (`hub-vendor`), set `vendor`, `account`, `endpoint` from the alert. Read **Breaker state**, **Breaker transitions**, **Health probes**, **Vendor calls by outcome** and **Vendor latency p99 by outcome**.
2. Is the vendor answering? Query:
   `sum by (account, result) (increase(health_probes_total{vendor="<vendor>"}[15m]))` and
   `sum by (outcome) (rate(vendor_latency_ms_count{vendor="<vendor>", account="<account>"}[5m]))`.
   All `unhealthy` probes and `5xx` / `timeout` / `connect` outcomes mean the vendor (or the path to it) is down.
3. Breaker detail: `getBreaker`
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/breakers/<account>/<endpoint>"`.
   Check `forced`, `transitions[].cause` (`failure_ratio`, `probe_failed`, ...), `last_probe`, `reopen_count`, `parked_count` and `integrations`.
4. Logs: `breaker.opened` (WARN, with `open_until`), `breaker.half_open`, `breaker.closed`, `probe.failed`, `probe.recovered`. The `breaker` field is `<account>|<vendor>.<endpoint>`.
5. Is only one instance not closed? Compare `breaker_state{vendor="<vendor>", account="<account>"}` per instance. One node alone points at Redis (state sync): look for `breaker publish failed (local state kept)`.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Vendor outage | Probes `unhealthy`, vendor calls `5xx` / `timeout` | Nothing to fix in the Hub: async work parks and drains on recovery. Check the vendor status page. If a second target exists, shift traffic: `updateIntegrationWeights` or `rollbackIntegration` to a version on another account. |
| Held open by an operator | `getBreaker` shows `forced: open` | Check why: `listChangeLog?object_type=breaker&action=forceOpenBreaker`. If the reason is over, `clearBreakerForce`. |
| Probe path broken, real traffic fine | Probes fail but `testAccountConnection` (`slot: active`) is `ok: true` | `forceCloseBreaker` with a `duration_ms` (starts the ramped drain), then fix the vendor's `defaults.health_probe` with `updateVendor`; `clearBreakerForce` after. |
| No health probe configured | `health_probes_total` flat for this vendor | Without `defaults.health_probe` no synthetic probe runs, so recovery waits for live traffic. Confirm the vendor works (`testAccountConnection`), then `forceCloseBreaker`. |
| Vendor slower than the endpoint timeout | `timeout` outcomes, latency p99 near `timeout_ms`; cause `consecutive_timeouts` | Not an on-call change: endpoint versions are immutable, so a new `timeout_ms` needs a new endpoint version (`createVendorEndpoint`) and an integration version that uses it. Hand to the integration owner; meanwhile the breaker protects the vendor. |
| Egress or network from the Hub | `connect` outcomes on several vendors at once; **Egress refused** panel > 0 | Egress refused: the host is not declared in the vendor's regions; fix with `updateVendor`. Wider network failure: escalate. |

## Escalate when
- The vendor is healthy on its status page and `testAccountConnection` passes, yet the breaker keeps re-opening.
- Breakers for several vendors open at the same time (Hub-side network, DNS or Redis).
- A sync, interactive or `moves_money_or_quota` integration is affected for more than 15 minutes with no failover target.
- Parked work is close to its TTL (requests will end `EXPIRED`).

## Related
- [ParkedOldestAgeHigh](ParkedOldestAgeHigh.md), [DLQSurge](DLQSurge.md), [runbook index](README.md)
- DESIGN §10.4 (breaker), §10.5 (parking and ramped drain), §5.3 (`on_circuit_open`)
- [F-12 Circuit breaker and drain](../features/F-12-circuit-breaker-drain.md), [OBSERVABILITY §9.3](../tech/OBSERVABILITY.md)
