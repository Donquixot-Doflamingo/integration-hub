# F-18 — Observability: metrics, traces, alerts, dashboards, load tests

## Summary
Completes the metrics catalogue (DESIGN §16.2), alert rules as code (§16.3), OTel tracing (§16.4), tenant/vendor/platform dashboards, and the M8 load and soak tests that prove the latency and durability targets (§1, §18.6, §19 M8).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M8 · EP-09 · HUB-230–249.

## Problem
Without consistent metrics, labels and alerts, outages are found by callers and latency regressions go unnoticed; the success metrics cannot be proven.

## Goals / Non-goals
**Goals**
- All §16.2 metrics with labels `tenant, integration, vendor, account, version` (bounded cardinality).
- Per-stage `hub_overhead_ms{stage}` for every middleware (DESIGN §18.5.5).
- Alert rules in `deploy/alerts/` (Prometheus format); dashboards JSON in `deploy/dashboards/`.
- OTel spans `edge`, `transform`, `upstream`, `callback`, `worker.claim`; accept `traceparent`, forward to vendors that accept it.
- Load test (k6 or vegeta) at 500 sync RPS + 2,000 async msg/s; soak with random mockvendor outages.

**Non-goals**
- LLM cost dashboards (Release 3), SLO burn-rate tooling beyond basic alerts.

## User stories
- [US-1801](../product/USER_STORIES.md) — Metrics and alerts as code
- [US-1802](../product/USER_STORIES.md) — Overhead breakdown
- [US-1803](../product/USER_STORIES.md) — Load and soak tests
- [US-1804](../product/USER_STORIES.md) — Dashboards

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Metrics | `GET /metrics` (Prometheus) on every role | Ops endpoint, not in caller contract |
| Files | `deploy/alerts/*.yaml`, `deploy/dashboards/{tenant,vendor,platform}.json`, `backend/loadtest/` | — |

## Behaviour & rules
Alerts (DESIGN §16.3):

| Alert | Condition | Severity |
| --- | --- | --- |
| Breaker open | `breaker_state == open` > 5 min | page |
| Parked backlog | `parked_oldest_age_s` > 10 min | page |
| Queue backlog | `queue_oldest_age_s` > 2 min | page |
| DLQ burst | > 50 DLQ entries / 10 min / account | page |
| Vendor auth | any `UPSTREAM_AUTH_FAILED` | page |
| Transformer bug | any `TRANSFORM_FAILED` | page |
| Snapshot lag | node > 60 s behind | warn |
| Credential expiry | < 7 days | warn |
| Overhead | `hub_overhead_ms` p99 > 5 ms for 10 min | warn |
| Audit drops | any `audit_events_dropped_total` increase | warn |

1. Metric names and labels follow `docs/tech/OBSERVABILITY.md`; each feature doc lists its additions.
2. `tenant` and `integration` labels allowed; `request_id` never a label.
3. Logs are structured `slog` JSON via the redacting handler; every log line carries `request_id` and trace ids when present.
4. Load-test acceptance: overhead p99 ≤ 5 ms at target load; async accept p99 ≤ 10 ms; soak: zero lost accepted requests (ledger reconciliation of accepted vs terminal).

## Data model impact
None.

## Config impact
`HUB_OTEL_ENDPOINT`, sampling ratio; alert thresholds as rule parameters.

## Observability
This feature *is* the observability layer; see table above and DESIGN §16.2 list: `hub_requests_total{mode,code}`, `hub_overhead_ms`, `vendor_latency_ms`, `breaker_state`, `breaker_transitions_total`, `queue_depth`, `queue_oldest_age_s`, `parked_count`, `parked_oldest_age_s`, `dlq_count`, `ratelimit_rejections_total{layer}`, `retry_budget_exhausted_total`, `snapshot_version`, `credential_expiry_days`, `llm_tokens_total`, `llm_cost_usd_total`, `audit_events_dropped_total`.

## Security & PII
No PII or secrets in labels, span attributes or logs; span attributes allowlisted.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| OTel collector down | Exporter drops spans, never blocks | Nothing |
| Metric cardinality spike | Label allowlist prevents unbounded labels | — |

## Testing
- **Unit**: alert rules tested with `promtool test rules`.
- **Load**: k6/vegeta scenarios in CI-optional job; results recorded in the M8 summary.
- **Soak**: random mockvendor outages over ≥ 1 h; reconciliation script.

## Rollout & migration
Dashboards imported into the target Grafana once hosting is decided (PRD Q8).

## Open questions
- "Laptop-scale setup" target hardware for the load test is unspecified.
- Monitoring stack (Prometheus/Grafana vs vendor) depends on hosting.

## Tickets
HUB-230 – HUB-249.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
