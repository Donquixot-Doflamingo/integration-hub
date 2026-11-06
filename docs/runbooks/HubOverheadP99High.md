# HubOverheadP99High

**Severity:** warn · **Rule group:** `hub-platform` · **Labels:** none (fleet-wide)

## What fired
`histogram_quantile(0.99, sum by (le) (rate(hub_overhead_ms_bucket{mode=~"sync|wait"}[5m]))) > 5` for 10 minutes: more than 1 % of sync and wait requests spent over 5 ms inside the Hub (server time minus vendor time and deliberate waits). This is SLO S1. Async accepts have their own 10 ms target and are not included.

## Impact
- Callers see slower responses by the extra overhead; nothing fails. Sync callers with tight `X-Request-Deadline` values may start to hit their own timeouts.
- Under heavy CPU pressure, transformers can overrun their time budget and requests end `TRANSFORM_FAILED` ([TransformFailed](TransformFailed.md)).

## First 5 minutes
1. **Platform** dashboard (`hub-platform`): **S1 overhead p99**, **Hub overhead p50 / p99 (sync + wait)** and **Overhead by stage p99**. Which stage moved? Stages: `http`, `auth`, `rate_limit`, `body_limit`, `validate`, `resolve`, `idempotency`, `transform`, `admission`, `response_map`, `audit` (`wait` and `upstream` are excluded from overhead).
2. One integration or all? `histogram_quantile(0.99, sum by (le, integration) (rate(hub_overhead_ms_bucket{mode=~"sync|wait"}[5m])))` (also **Tenant** dashboard, **Hub overhead p99 by integration**).
3. Node resources, same dashboard, row *Go runtime*: **CPU**, **Scheduling latency p99** (high = CPU throttling), **GC pause max**, **Heap in use vs GOMEMLIMIT**, **Goroutines**.
4. Stage-specific panels: `idempotency` → **Redis op latency p99**; `audit` → **Audit buffer fill**; `admission` → **Rate-limit rejections by layer**.
5. Did it start with a deploy or a publish? **Process restarts**, and `listChangeLog` for `publishIntegrationVersion` around the start time.

## Likely causes → fix
| Stage or signal | Likely cause | Fix |
| --- | --- | --- |
| `idempotency` | Redis slow or far away (budget 1 ms) | Check Redis latency and load; fix Redis. The stage only runs for requests with an `Idempotency-Key`. |
| `validate` | Larger payloads or a bigger capability schema (budget 0.8 ms) | Find the integration (step 2). If a tenant started sending much larger bodies, talk to its owner. |
| `transform` | A new heavy transformer version (budget 0.5 ms) | Step 2 finds it; `rollbackIntegration` if it came with a publish, then simplify the transformer. |
| `audit` | Audit buffer near full, Postgres slow | See [AuditEventsDropped](AuditEventsDropped.md). |
| Every stage up together | CPU throttling or GC pressure on gateways | Add gateway replicas or raise CPU limits; check `GOMEMLIMIT` is set (85 % of the memory limit). |
| Started with a release | Code regression | Roll back the release; compare benchmarks (`make bench`, `benchstat`) against the previous one. |

CPU profiles: OBSERVABILITY §8.1 plans pprof on the ops port, but the ops port serves only `/healthz`, `/readyz` and `/metrics` today, so a live profile is not available; use the stage and runtime panels.

## Escalate when
- p99 is above 10 ms, or the overhead is pushing callers into timeouts.
- It started with a release and a rollback is not possible.
- No stage stands out and resources look normal.

## Related
- [AuditEventsDropped](AuditEventsDropped.md), [TransformFailed](TransformFailed.md), [runbook index](README.md)
- DESIGN §18.5 (hot-path rules), §18.6 (latency budget)
- [OBSERVABILITY §2 (S1), §9.1](../tech/OBSERVABILITY.md), [F-18 Observability](../features/F-18-observability.md)
