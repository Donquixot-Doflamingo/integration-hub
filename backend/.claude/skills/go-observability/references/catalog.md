# Metric catalog (DESIGN §16.2 + stage metrics)

Common labels per §16.2: `tenant, integration, vendor, account, version` — applied where marked "std". Keep this table in sync with `docs/tech/OBSERVABILITY.md`.

| Exported name | Type | Labels | Buckets / notes |
| --- | --- | --- | --- |
| `hub_requests_total` | counter | std + `mode`, `code` | `code` = canonical code or `OK` |
| `hub_overhead_ms` | histogram | std | 0.25 … 50 (see SKILL); excludes vendor time |
| `hub_stage_duration_ms` | histogram | `stage` (+ `role`) | 0.01 … 5; one series per middleware stage |
| `hub_accept_ms` | histogram | `tenant`, `integration`, `mode` | async/wait 202 latency, budget 10 ms |
| `vendor_latency_ms` | histogram | `vendor`, `account`, `endpoint`, `outcome` | 5 … 120000 |
| `transform_duration_ms` | histogram | `transformer`, `kind` | 0.05 … 5, budget 0.5 |
| `breaker_state` | gauge | `vendor`, `account`, `endpoint` | 0 closed, 1 half-open, 2 open |
| `breaker_transitions_total` | counter | `vendor`, `account`, `endpoint`, `from`, `to` | |
| `breaker_sync_errors_total` | counter | `role` | Redis sync failures (local-only fallback) |
| `queue_depth` | gauge | `status`, `account` | from outbox counts, sampled by scheduler |
| `queue_oldest_age_s` | gauge | `account` | page > 120 s |
| `parked_count` | gauge | `vendor`, `account`, `endpoint` | |
| `parked_oldest_age_s` | gauge | `vendor`, `account`, `endpoint` | page > 600 s |
| `drain_release_rate` | gauge | `account`, `endpoint` | current ramp fraction × rate; chaos test asserts it |
| `dlq_count` | gauge | `tenant`, `integration`, `account`, `reason` | page > 50 / 10 min / account (use `increase` on a counter `dlq_entries_total`) |
| `ratelimit_rejections_total` | counter | `layer` (`tenant`, `account`, `budget`), `tenant`, `integration` | |
| `retry_budget_exhausted_total` | counter | `account` | |
| `snapshot_version` | gauge | `role` | alert: node > 60 s behind latest |
| `snapshot_compile_ms` | histogram | | 10 … 10000 |
| `credential_expiry_days` | gauge | `account` | warn < 7 |
| `llm_tokens_total` | counter | std + `direction` (`input`/`output`), `model` | model from vendor price table (bounded) |
| `llm_cost_usd_total` | counter | std + `model` | |
| `audit_events_dropped_total` | counter | `role` | warn on any increase |
| `callback_delivery_ms` | histogram | `tenant`, `outcome` | |
| `idempotency_replays_total` | counter | `tenant`, `integration` | |

Go runtime metrics come from the OTel runtime instrumentation (`go.opentelemetry.io/contrib/instrumentation/runtime`); see `go-memory-performance` for which to watch.

When adding a metric: row here, row in `docs/tech/OBSERVABILITY.md`, feature doc, exported-name test, and an alert if someone must act on it.
