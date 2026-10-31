# Hub failure signatures

| Symptom | Likely cause | Where to look |
| --- | --- | --- |
| `TRANSFORM_FAILED` (500) | Expression threw on an input shape the goldens don't cover (nil field without `??`, wrong type) | `response.transform_failed` audit event has the failing expression + input; add a golden for it |
| `UPSTREAM_REJECTED` for every call after a publish | Transformer emits a field/shape the vendor no longer accepts, or wrong account config value | Diff `request.transformed` of a failing vs a pre-publish request; `integration.resolved` shows the version |
| `UPSTREAM_AUTH_FAILED` right after rotation | `next` promoted before the vendor activated it, or wrong slot cached | Credential cache version on the pod; `account_credentials` slots; rotation runbook (§6.6) |
| All requests `UPSTREAM_UNAVAILABLE` with `circuit: open`, vendor looks fine | Breaker stuck open: health probe not running (scheduler not leader / probe misconfigured) or Redis state not syncing | Breaker Redis key, scheduler leader lock, `breaker_transitions_total`, probe logs |
| One node fails, others fine | Node on an old snapshot or its local breaker diverged (Redis sync errors) | `snapshot_version` per pod, `breaker_sync_errors_total` |
| Requests stuck `in_flight` for long | Worker died; visibility timeout too long; or heartbeats extending a hung call | `outbox.claimed_by/claimed_until`, worker pod restarts, goroutine dump of the worker |
| Same request sent twice to the vendor | Visibility timeout shorter than the vendor call; reclaim after a worker crash (expected, at-least-once) | `request_attempts` count, attempt timestamps vs `claimed_until`; vendor idempotency header present? |
| Two callbacks for one request | Missing CAS check on terminal transition, or callback enqueued outside the winning transaction | Transition code path; `callback_deliveries` rows for the request |
| `parked_count` never drains after recovery | Breaker closed but drain job not running (no leader), or drain limit computed as 0 (live traffic consuming all tokens) | Scheduler logs, leader lock, drain rate metric |
| Drain hits vendor 429s | Ramp ignoring live traffic, or shared token bucket key differs between drain and live path | Redis bucket key names, `ratelimit_rejections_total{layer}` |
| `queue_oldest_age_s` rising, workers idle | Claim query not matching (status/next_attempt_at predicate, timezone), or workers' bulkheads full of hung calls | Run the claim query by hand with `EXPLAIN`; worker goroutine dump |
| Sync p99 jumps for requests without `Idempotency-Key` | Something added Redis/DB to the sync path | Trace spans under edge; §18.5 rules |
| `audit_events_dropped_total` > 0 | Audit writer slower than ingest (DB slow, batch too small) | Writer batch latency metrics, Postgres write latency, partition existence for today |
| Inserts fail at midnight UTC | Next day's partition not created | Partition maintenance job; `\d+ requests` |
| `EXPIRED` callbacks during a long outage | Working as designed: TTL passed while parked | Integration TTL; consider longer TTL for that capability |
| 202 returned to an interactive capability | Mode not `sync` — publish validation hole | Publish pipeline validation (§5.4) |
| 404 `INTEGRATION_NOT_FOUND` just after create | Snapshot not yet propagated (≤ 10 s) or node can't fetch | `snapshot_version` lag alert |
| Secret visible in a log line | Value not registered in the secret set (new auth scheme, token minted after warm) or logged via `[]byte`/struct | `telemetry.SecretSet.Update` call sites; canary test coverage |
| Flaky test only under `-race` or `-shuffle` | Shared state between tests, timing assumptions, `time.Sleep` waits | Use `synctest`/`require.Eventually`, isolate data |
