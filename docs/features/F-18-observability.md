# F-18 — Observability: metrics, traces, alerts, dashboards, load tests

## Summary
Completes the metrics catalogue (DESIGN §16.2), alert rules as code (§16.3), OTel tracing (§16.4), tenant/vendor/platform dashboards, and the M8 load and soak tests that prove the latency and durability targets (§1, §18.6, §19 M8).

## Status
`in-progress` — 2026-11-06 (HUB-230 metrics done; HUB-231 tracing in review; alerts, PII deletion, security, load and soak tests to come).

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
- OTel spans `edge`, `transform`, `upstream`, `callback`, `worker.attempt`; accept `traceparent`, forward to vendors that accept it.
- Load test (k6 or vegeta) at 500 sync RPS + 2,000 async msg/s; soak with random mockvendor outages.

**Non-goals**
- LLM cost dashboards (Release 3), SLO burn-rate tooling beyond basic alerts.

## User stories
- [US-1801](../product/USER_STORIES.md) — Metrics and alerts as code
- [US-1802](../product/USER_STORIES.md) — Overhead breakdown
- [US-1803](../product/USER_STORIES.md) — Load and soak tests
- [US-1804](../product/USER_STORIES.md) — Dashboards
- [US-1805](../product/USER_STORIES.md) — One trace per request, across the queue

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
4. **Metrics (HUB-230, done):** every §16.2 metric is registered; `TestDesignMetricsAreRegistered` (roles) enumerates the names from one registry wired like the data plane. `hub_requests_total` is counted once per request: in the gateway for requests that end there (dry-runs excluded) and in delivery for each committed terminal transition of a queued request. The outbox gauges (`queue_depth`, `queue_oldest_age_s`, `dlq_count`) come from `delivery.QueueSampler` on the scheduler leader every 10 s. `credential_expiry_days` is computed at scrape time. Capped metrics fold excess label combinations into `__overflow__` (OBSERVABILITY §3.1). `hub_overhead_ms` is broken down by stage in `hub_stage_duration_ms` (since HUB-027).
5. **Tracing (HUB-231, in review; ADR-0012):** the edge names the otelhttp server span after the route and tags it with request id, tenant, integration, version, mode and canonical code; `hub.transform.request` / `hub.transform.response` are created only under a recording span; async, wait and park requests open `hub.queue.enqueue`, whose `traceparent` the envelope stores; the worker's `hub.worker.attempt` is a new root linked to it, parent of the transform and upstream spans; terminal transitions store their span's `traceparent` on each callback row and `hub.callback.deliver` links to it. The link-aware sampler keeps linked roots sampled when the accepting trace was. Vendors listed in `HUB_TRACE_FORWARD_VENDORS` receive the upstream span's `traceparent`. `hub.requests.trace_id` is filled. Every span attribute key is in `telemetry.AllowedSpanAttributes`; the tests reject any other key and any value containing a credential, the API key or a body fragment. Remaining spans (`hub.validate`, `hub.resolve`, `hub.idempotency`, webhook inbound, snapshot apply, admin, upstream attempt/breaker attributes): HUB-240.
6. Load-test acceptance: overhead p99 ≤ 5 ms at target load; async accept p99 ≤ 10 ms; soak: zero lost accepted requests (ledger reconciliation of accepted vs terminal).

## Data model impact
`hub.callback_deliveries.traceparent` (nullable, W3C format check; migration `00010_callback_trace.sql`, HUB-231). The envelope's existing `trace.traceparent` now holds the Hub's enqueue span instead of the caller's raw header; `hub.requests.trace_id` (existing column) is now filled by the edge.

## Config impact
`HUB_OTLP_ENDPOINT`, `HUB_TRACE_SAMPLE_RATIO`, `HUB_TRACE_FORWARD_VENDORS` (vendors that receive `traceparent`, default none; HUB-231); alert thresholds as rule parameters.

## Observability
Spans and the attribute allowlist: `docs/tech/OBSERVABILITY.md` §5.1–5.2 (HUB-231 added the edge span tags, `hub.transform.*`, `hub.queue.enqueue`, `hub.worker.attempt`, the callback link and vendor forwarding). No new metrics or log events for tracing.

This feature *is* the observability layer; see table above and DESIGN §16.2 list: `hub_requests_total{mode,code}`, `hub_overhead_ms`, `vendor_latency_ms`, `breaker_state`, `breaker_transitions_total`, `queue_depth`, `queue_oldest_age_s`, `parked_count`, `parked_oldest_age_s`, `dlq_count`, `ratelimit_rejections_total{layer}`, `retry_budget_exhausted_total`, `snapshot_version`, `credential_expiry_days`, `llm_tokens_total`, `llm_cost_usd_total`, `audit_events_dropped_total`.

## Security & PII
No PII or secrets in labels, span attributes or logs; span attributes allowlisted (`telemetry.AllowedSpanAttributes`, enforced by `SpanAttributeViolations` in `TestSyncSpans` and `TestAsyncTraceLinksCallerToCallback`). Span statuses carry an error class only, never error text. Our trace ids reach a vendor only when it is listed in `HUB_TRACE_FORWARD_VENDORS`.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| OTel collector down | Exporter drops spans, never blocks | Nothing |
| Stored traceparent invalid or missing | Worker attempt / callback span starts unlinked | Nothing |
| Metric cardinality spike | Label allowlist prevents unbounded labels | — |

## Testing
- **Unit**: alert rules tested with `promtool test rules`.
- **Tracing (HUB-231)**: `TestSyncSpans`, `TestFailedRequestTagsCode`, `TestUnsampledRequestHasNoStageSpans`, `TestSpanHelpersDoNotAllocateUnsampled` (edge), `TestTransformSpans` (delivery), `TestSamplerKeepsLinkedRootsSampled`, `TestTraceparentRoundTripsIntoALink`, `TestSpanAttributeViolations` (telemetry); integration `TestAsyncTraceLinksCallerToCallback` (edge: caller → gateway → envelope → worker → vendor `traceparent` → callback, allowlist over all spans).
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
| 2026-11-06 | HUB-230 shipped: full §16.2 metric set, cardinality guard, status in-progress | backend-engineer |
| 2026-11-06 | HUB-231: tracing across edge, transform, upstream, outbox, worker and callbacks; ADR-0012; US-1805 | backend-engineer |
