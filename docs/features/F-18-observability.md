# F-18 — Observability: metrics, traces, alerts, dashboards, load tests

## Summary
Completes the metrics catalogue (DESIGN §16.2), alert rules as code (§16.3), OTel tracing (§16.4), tenant/vendor/platform dashboards, and the M8 load and soak tests that prove the latency and durability targets (§1, §18.6, §19 M8).

## Status
`in-progress` — 2026-11-07 (HUB-230 metrics, HUB-232 alerts/dashboards, HUB-233 PII deletion and HUB-234 security checks done; HUB-231 tracing in review; HUB-235/236 load and soak harness and first laptop-scale report in review; pprof part of HUB-403 done).

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
- Load test at 500 sync RPS + 2,000 async msg/s; soak with random mockvendor outages (`hubload` + `deploy/load/`, ADR-0016).
- Subject erasure (DESIGN §16.5): purge a data subject's stored bodies by identifier, keeping hashes, sizes and timelines (HUB-233, ADR-0012).

**Non-goals**
- LLM cost dashboards (Release 3), SLO burn-rate tooling beyond basic alerts.

## User stories
- [US-1801](../product/USER_STORIES.md) — Metrics and alerts as code
- [US-1802](../product/USER_STORIES.md) — Overhead breakdown
- [US-1803](../product/USER_STORIES.md) — Load and soak tests
- [US-1804](../product/USER_STORIES.md) — Dashboards
- [US-1806](../product/USER_STORIES.md) — One trace per request, across the queue
- [US-1805](../product/USER_STORIES.md) — Erase a data subject's stored payloads
- [US-1807](../product/USER_STORIES.md) — Runbook for every alert

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Metrics | `GET /metrics` (Prometheus) on every role | Ops endpoint, not in caller contract |
| CLI | `hub erase-subject -tenant -actor -reason [-since] [-until] [-dry-run] < identifier` (one-shot; JSON result; exit 0 / 3 deferred / 1 failed / 2 usage) | No admin operation yet: CCR in HUB-247 |
| Files | `deploy/alerts/hub.rules.yaml` + `deploy/alerts/*_test.yml`, `deploy/dashboards/{tenant,vendor,platform}.json`, `deploy/load/` (harness, registry overlay) | — |
| CLI | `hubload rate`, `soak`, `report`, `settle` (`backend/cmd/hubload`), `make load`, `make soak` | Uses only the contracted invoke API, `/metrics` and read-only SQL |
| Ops | `/debug/pprof/*` on the ops port of every role (HUB-403) | Ops endpoint, not in caller contract |

## Behaviour & rules
Alerts (DESIGN §16.3):

| Alert | Condition | Severity |
| --- | --- | --- |
| Breaker open | breaker not closed (open or half-open) > 5 min | page |
| Parked backlog | `parked_oldest_age_s` > 10 min | page |
| Queue backlog | `queue_oldest_age_s` > 2 min | page |
| DLQ burst | > 50 DLQ entries / 10 min / account | page |
| Vendor auth | any `UPSTREAM_AUTH_FAILED` | page |
| Transformer bug | any `TRANSFORM_FAILED` | page |
| Snapshot lag | node > 60 s behind | warn |
| Credential expiry | < 7 days | warn |
| Overhead | `hub_overhead_ms` p99 > 5 ms for 10 min | warn |
| Audit drops | any `audit_events_dropped_total` increase | warn |

Runbooks: one per alert, indexed in [`docs/runbooks/README.md`](../runbooks/README.md) (HUB-237), plus the [credential rotation](../runbooks/credential-rotation.md) procedure.

1. Metric names and labels follow `docs/tech/OBSERVABILITY.md`; each feature doc lists its additions.
2. `tenant` and `integration` labels allowed; `request_id` never a label.
3. Logs are structured `slog` JSON via the redacting handler; every log line carries `request_id` and trace ids when present.
4. **Metrics (HUB-230, done):** every §16.2 metric is registered; `TestDesignMetricsAreRegistered` (roles) enumerates the names from one registry wired like the data plane. `hub_requests_total` is counted once per request: in the gateway for requests that end there (dry-runs excluded) and in delivery for each committed terminal transition of a queued request. The outbox gauges (`queue_depth`, `queue_oldest_age_s`, `dlq_count`) come from `delivery.QueueSampler` on the scheduler leader every 10 s. `credential_expiry_days` is computed at scrape time. Capped metrics fold excess label combinations into `__overflow__` (OBSERVABILITY §3.1). `hub_overhead_ms` is broken down by stage in `hub_stage_duration_ms` (since HUB-027).
5. **Alerts and dashboards (HUB-232, in review):** `deploy/alerts/hub.rules.yaml` has one rule per alert above (names and expressions: OBSERVABILITY §11.1), each with `severity`, `summary`, `description` and `runbook_url` → `docs/runbooks/<AlertName>.md` (HUB-237). Rules query only exported metrics: auth and transform alerts read `hub_requests_total{code}` and the DLQ alert the growth of `dlq_count` until `vendor_attempts_total` / `dlq_entries_total` exist (ADR-0014, HUB-241). "Any X" rules also catch the first event of a new label set. The breaker alert fires when the circuit has not closed for 5 min (open or half-open), the parked alert reads the current scheduler leader only (HUB-242), the overhead alert covers sync and wait (S1). Dashboards `tenant.json`, `vendor.json`, `platform.json` (OBSERVABILITY §10) use the `${datasource}` variable; panels whose metric does not exist yet are left out and listed there.
6. **Subject erasure (HUB-233, in review; ADR-0012, SECURITY §4.1):** `hub erase-subject` reads the identifier from stdin only and computes its stored forms with the gateway's masker. The forms are the raw value, its JSON-escaped forms, the `identifier`/`ip_address`/`device_id` HMAC and the `email` local-part HMAC@domain. The tenant's rows are searched in `request_events` (sealed payloads opened with the seal key), `request_results`, `outbox`, `dead_letters`, `callback_deliveries` and `vendor_sessions`. Then, in one transaction, every body of every matched request is purged: event payload → NULL, `payload_state = omitted`, `meta.pii_erased = change_id`; result/error → NULL; DLQ canonical → null and last vendor exchange dropped (replay refused); terminal callback payload emptied; matching sessions deleted. One `change_log` row is appended (`eraseSubjectPayloads`, `subject_ref`, counts, ids). Requests still queued or with callbacks to deliver are deferred (exit 3) for a re-run. Missing seal key with sealed rows present → fail, nothing changed.
7. **Security checks (HUB-234, in review):** CI jobs `gosec` (`make gosec`, gosec v2.30.0, fails on medium/high findings not triaged with `#nosec`) and `govulncheck` (`make vuln`, v1.9.0, fails on reachable known vulnerabilities, stdlib included; toolchain pinned to go1.26.10). Both egress clients enforce an explicit TLS 1.2 floor (`TestTLS`, `TestGuardRefusesTLSBelow12`). Body limits are proven through the gateway handler (`TestBodyLimitsAtTheEdge`). Every role's logs from the `roles` integration run are grepped for every secret the tests used (`TestRoleLogsCarryNoSecrets` + package `TestMain`). The redaction set also holds the DSN password and audit mask key. Details and the triage table: `docs/tech/SECURITY.md` §5, §8.1, §9.
8. Load-test acceptance: overhead p99 ≤ 5 ms at target load; async accept p99 ≤ 10 ms; soak: zero lost accepted requests (ledger reconciliation of accepted vs terminal).
9. **Load and soak harness (HUB-235, HUB-236, in review; ADR-0016):** `deploy/load/run.sh` builds, resets the outbox and Redis of a database whose name contains `load`, copies `backend/config` plus the `deploy/load/config/` overlay (tenant `load-tenant`, account `ct-load`, integrations `int_load_sync`, `int_load_async`, `int_load_cb`), generates throw-away credentials, starts mockvendor, gateway, worker and scheduler, waits for the 1-minute load average to drop below 1.0, and runs the phases: `compare` (direct to mockvendor vs through the Hub at several vendor latencies), `sync` (L1 ramp + gateway CPU profile), `async` (L2 ramp with drain + worker and gateway profiles), `soak` (async_callback traffic with random outages: connection refused, reset, 503, timeouts). `hubload` is open loop and measures latency from the intended send time. Per step it reports client quantiles, outcomes by status and canonical code, metric deltas, the audit ledger's overhead for the window, outbox depth and oldest age, CPU per process and the load average. The soak reconciles every accepted request id against `hub.requests` and the callback receiver: lost, non-terminal, duplicate outcomes, redeliveries. Server-side overhead comes from the audit ledger until HUB-405 fixes the `hub_overhead_ms` metric. Worker restarts in the soak stay off until HUB-406. Results: `docs/tech/LOAD_TEST_REPORT.md`.

## Data model impact
No migration. The erasure reuses `payload_state = 'omitted'` and adds `meta.pii_erased` on purged events. A hashed subject index is HUB-245.
7. **Tracing (HUB-231, in review; ADR-0013):** the edge names the otelhttp server span after the route and tags it with request id, tenant, integration, version, mode and canonical code; `hub.transform.request` / `hub.transform.response` are created only under a recording span; async, wait and park requests open `hub.queue.enqueue`, whose `traceparent` the envelope stores; the worker's `hub.worker.attempt` is a new root linked to it, parent of the transform and upstream spans; terminal transitions store their span's `traceparent` on each callback row and `hub.callback.deliver` links to it. The link-aware sampler keeps linked roots sampled when the accepting trace was. Vendors listed in `HUB_TRACE_FORWARD_VENDORS` receive the upstream span's `traceparent`. `hub.requests.trace_id` is filled. Every span attribute key is in `telemetry.AllowedSpanAttributes`; the tests reject any other key and any value containing a credential, the API key or a body fragment. Remaining spans (`hub.validate`, `hub.resolve`, `hub.idempotency`, webhook inbound, snapshot apply, admin, upstream attempt/breaker attributes): HUB-240.

## Data model impact
`hub.callback_deliveries.traceparent` (nullable, W3C format check; migration `00010_callback_trace.sql`, HUB-231). The envelope's existing `trace.traceparent` now holds the Hub's enqueue span instead of the caller's raw header; `hub.requests.trace_id` (existing column) is now filled by the edge.

## Config impact
`HUB_OTLP_ENDPOINT`, `HUB_TRACE_SAMPLE_RATIO`, `HUB_TRACE_FORWARD_VENDORS` (vendors that receive `traceparent`, default none; HUB-231); alert thresholds as rule parameters.

## Observability
Spans and the attribute allowlist: `docs/tech/OBSERVABILITY.md` §5.1–5.2 (HUB-231 added the edge span tags, `hub.transform.*`, `hub.queue.enqueue`, `hub.worker.attempt`, the callback link and vendor forwarding). No new metrics or log events for tracing.
Alerts: the ten rules in OBSERVABILITY §11.1. Dashboards: OBSERVABILITY §10. No new metrics, logs or spans in HUB-232.

This feature *is* the observability layer; see table above and DESIGN §16.2 list: `hub_requests_total{mode,code}`, `hub_overhead_ms`, `vendor_latency_ms`, `breaker_state`, `breaker_transitions_total`, `queue_depth`, `queue_oldest_age_s`, `parked_count`, `parked_oldest_age_s`, `dlq_count`, `ratelimit_rejections_total{layer}`, `retry_budget_exhausted_total`, `snapshot_version`, `credential_expiry_days`, `llm_tokens_total`, `llm_cost_usd_total`, `audit_events_dropped_total`.

## Security & PII
No PII or secrets in labels, span attributes or logs; span attributes allowlisted (`telemetry.AllowedSpanAttributes`, enforced by `SpanAttributeViolations` in `TestSyncSpans` and `TestAsyncTraceLinksCallerToCallback`). Span statuses carry an error class only, never error text. Our trace ids reach a vendor only when it is listed in `HUB_TRACE_FORWARD_VENDORS`.
No PII or secrets in labels, span attributes or logs; span attributes allowlisted. Subject erasure: SECURITY §4.1 (input from stdin only, redacted from logs, fail closed without the seal key, `change_log` keeps a domain-separated `subject_ref`, never a stored form).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| OTel collector down | Exporter drops spans, never blocks | Nothing |
| Stored traceparent invalid or missing | Worker attempt / callback span starts unlinked | Nothing |
| Metric cardinality spike | Label allowlist prevents unbounded labels | — |
| Erasure while a request is in flight | Request deferred, listed, exit 3; re-run after it is terminal | Its delivery completes normally |
| Erasure without the seal key, sealed rows present | Fails, transaction never opened, nothing changed | — |

## Testing
- **Unit**: alert rules tested with `promtool test rules`.
- **Tracing (HUB-231)**: `TestSyncSpans`, `TestFailedRequestTagsCode`, `TestUnsampledRequestHasNoStageSpans`, `TestSpanHelpersDoNotAllocateUnsampled` (edge), `TestTransformSpans` (delivery), `TestSamplerKeepsLinkedRootsSampled`, `TestTraceparentRoundTripsIntoALink`, `TestSpanAttributeViolations` (telemetry); integration `TestAsyncTraceLinksCallerToCallback` (edge: caller → gateway → envelope → worker → vendor `traceparent` → callback, allowlist over all spans).
- **Unit**: alert rules tested with `promtool test rules` (`deploy/alerts/*_test.yml`, a firing and a non-firing case per alert; `TestAlertRulesCoverDesign` enforces coverage of every §16.3 alert). `TestAlertRulesUseExportedMetrics` and `TestDashboardsAreValidAndUseExportedMetrics` parse every rule and panel query and fail on any metric or label name the registry does not export (ADR-0015). `make monitoring` runs all of it; CI job `monitoring`. Grafana import is not run in CI yet (HUB-243).
- **Load**: `make load` (laptop scale, about 25 min on 2 CPUs; full-length settings in `deploy/load/README.md`). `hubload` unit tests: `TestGeneratorIsOpenLoop` (a stalled server is charged to every arrival behind it), `TestGeneratorDropsBeyondMaxInFlight`, `TestHistogramDeltaQuantiles` (native and classic), `TestHistogramQuantileWithNegativeObservations`, `TestRenderReportComparison`. `TestLoadOverlayConfigBuilds` (registry) keeps the overlay valid; the archtest keeps `hubload` out of `internal/`.
- **Soak**: `make soak`; random mockvendor outages, reconciliation of accepted ids against `hub.requests` and received callbacks (`TestReceiverCountsDuplicatesAndRedeliveries`, `TestChaosLoopAlternatesAndRestoresTheVendor`). Laptop run 4 min; staging 1–24 h.
- **Security (HUB-234)**: `make security` (gosec + govulncheck); `TestTLS`, `TestTLSFloorIsExplicit`, `TestGuardRefusesTLSBelow12`, `TestBodyLimitsAtTheEdge`, `TestRoleLogsCarryNoSecrets`, `TestRunRegistersConfigSecrets`; egress `TestUndeclaredHostRefusedBeforeDial`, `TestDialerChecksEgress`.
- **Erasure**: `TestEraseSubjectPurgesEveryStoredBody` (control, integration) greps every bytea/json/jsonb/text column of every `hub` table, partitions included, for the subject's stored forms before (present in 8 columns) and after erasure (only the deferred outbox envelope and undelivered callback, then nothing after the re-run), also opens the sealed payloads, and compares hashes/sizes/timelines/metadata before and after. It also checks the `change_log` row, the dry run and the DLQ replay refusal. `TestEraseSubjectScope` covers tenant and subject isolation, fail-closed without the seal key, the window and validation. `TestSubjectFormsFindMaskedPayloads` (audit, unit) proves the forms match what `masked` stores per x-pii kind.

## Rollout & migration
Dashboards imported into the target Grafana once hosting is decided (PRD Q8): pick the Prometheus data source in the `datasource` variable. Load `deploy/alerts/hub.rules.yaml` as a Prometheus rule file. With native histograms enabled, set `always_scrape_classic_histograms: true` (rules and panels use `_bucket`).

## Open questions
- "Laptop-scale setup" target hardware for the load test is unspecified. The first report ran on a 2-CPU container shared by every process, which cannot reach the M8 rates; the 10-minute criteria need a run on proper hardware (`deploy/load/README.md`).
- Monitoring stack (Prometheus/Grafana vs vendor) depends on hosting.

## Tickets
HUB-230 – HUB-249.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-06 | HUB-230 shipped: full §16.2 metric set, cardinality guard, status in-progress | backend-engineer |
| 2026-11-06 | HUB-231: tracing across edge, transform, upstream, outbox, worker and callbacks; ADR-0013; US-1806 | backend-engineer |
| 2026-11-07 | HUB-234: security gates (gosec, govulncheck), TLS floor, body-limit and role-log secret-grep tests | backend-engineer |
| 2026-11-06 | HUB-233: subject erasure job (`hub erase-subject`, ADR-0012), US-1805 | backend-engineer |
| 2026-11-06 | HUB-232: alert rules with promtool tests, tenant/vendor/platform dashboards, exported-metrics check; ADR-0014, ADR-0015 | backend-engineer |
| 2026-11-07 | HUB-237: runbooks for the ten alerts and the credential rotation procedure (`docs/runbooks/`), US-1807; breaker alert condition corrected to "not closed" | docs-keeper |
| 2026-11-07 | HUB-235/236: `hubload`, `deploy/load/` harness, load test report; HUB-403 pprof on the ops port; ADR-0016 | backend-engineer |
