---
name: go-observability
description: "OpenTelemetry metrics and traces for the Integration Hub Go backend (DESIGN 16.2-16.4, 18.4-18.6) - Prometheus naming and units, histogram buckets for millisecond budgets, label cardinality rules, per-middleware duration metrics, hub_overhead_ms breakdown, span names for edge/transform/upstream/callback, traceparent propagation, exemplars, alert rules in deploy/alerts and dashboards in deploy/dashboards. Use whenever you add or change a metric, span, attribute, middleware, alert or dashboard, touch internal/telemetry, or a feature doc needs its observability section."
---

# Observability (metrics, traces, alerts)

Package `internal/telemetry` builds the MeterProvider/TracerProvider and exposes `/metrics` on the **internal** port. Every new metric, span and alert is listed in the feature doc and `docs/tech/OBSERVABILITY.md` (AGENTS.md §8.6). Catalog of DESIGN metrics with types and buckets: [references/catalog.md](references/catalog.md).

## Setup (once, in main)
```go
exp, err := prometheus.New(prometheus.WithoutUnits()) // go.opentelemetry.io/otel/exporters/prometheus
if err != nil { return err }
mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp), sdkmetric.WithResource(res))
otel.SetMeterProvider(mp)

tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(otlpExporter), sdktrace.WithResource(res),
	sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.TraceRatio))))
otel.SetTracerProvider(tp)
otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
// shutdown: mp.Shutdown(ctx), tp.Shutdown(ctx) — see go-architecture shutdown order
```
Pass the `metric.Meter` / `trace.Tracer` into constructors (`otel.Meter("github.com/Donquixot-Doflamingo/integration-hub/backend/internal/upstream")`); don't call `otel.Meter` on the hot path.

## Naming and units
- Use the DESIGN §16.2 names exactly (`hub_requests_total`, `hub_overhead_ms`, `vendor_latency_ms`, `queue_oldest_age_s` …). Units are in the name; DESIGN uses **ms** for latency histograms because budgets are in ms.
- The OTel Prometheus exporter rewrites names (unit suffixes, `_total` on counters) depending on version and options. We use `WithoutUnits()` and name instruments with their unit already in the name. For counters, name the instrument without `_total` if the exporter appends it. **Pin this with a test** that scrapes `/metrics` and asserts the exact exported names in the catalog; a library upgrade that changes naming must fail CI, not silently break dashboards.
- New metrics: `hub_` prefix for Hub-wide, subsystem prefix otherwise (`breaker_`, `queue_`, `audit_`), snake_case, base noun + unit: `callback_delivery_ms`, `snapshot_compile_ms`.
- Counter for events, histogram for durations/sizes, gauge (observable) for current state (`queue_depth`, `breaker_state`, `parked_count`).

## Labels and cardinality
Allowed label values must come from a bounded set (config or enum):
- `tenant`, `integration`, `vendor`, `account`, `version` (§16.2) — bounded by the registry.
- `mode`, `code` (canonical), `stage`, `layer`, `endpoint`, `outcome`, `role`, `route` (chi route pattern, never the raw path).

**Never** as labels: `request_id`, `idempotency_key`, `session_id`, user ids, phone/email/PAN, raw URL paths, vendor error messages, `trace_id` (that's an exemplar), free text. They go in logs/audit/traces.
- Budget: keep series per metric < ~10k per pod. `version` multiplies everything — use it only on `hub_requests_total` and latency histograms, not on every metric.
- Precompute attribute sets per integration at snapshot build: `metric.WithAttributeSet(set)` avoids per-request attribute allocation.

## Histogram buckets (ms budgets)
Set explicit buckets per instrument (`metric.WithExplicitBucketBoundaries(...)`); default buckets are useless for sub-ms stages.
| Instrument | Buckets (ms) |
| --- | --- |
| `hub_stage_duration_ms{stage}` (per middleware) | 0.01 0.025 0.05 0.1 0.25 0.5 1 2.5 5 |
| `hub_overhead_ms` | 0.25 0.5 1 2 3 4 5 7.5 10 25 50 |
| async accept (`hub_accept_ms`) | 1 2.5 5 7.5 10 15 25 50 100 |
| `vendor_latency_ms` | 5 10 25 50 100 250 500 1000 2500 5000 10000 30000 60000 120000 |
| `transform_duration_ms` | 0.05 0.1 0.25 0.5 1 2.5 5 |
Each budget (0.1, 0.5, 5, 10) must be a bucket boundary so "share over budget" is exact.

## Per-middleware duration and hub_overhead_ms breakdown
Every stage in the §18.4 chain records `hub_stage_duration_ms{stage=...}`. A fixed-size per-request timer avoids allocation:
```go
type Stage uint8

const (
	StageAuth Stage = iota
	StageRateLimit
	StageValidate
	StageResolve
	StageIdempotency
	StageTransform
	StageBreaker
	StageUpstream // vendor time: excluded from overhead
	StageResponseMap
	numStages
)

type Timings struct {
	start  time.Time
	stages [numStages]time.Duration
}

func (t *Timings) Observe(s Stage, since time.Time) { t.stages[s] += time.Since(since) }

// Overhead = wall time minus vendor time (DESIGN §1 success metric).
func (t *Timings) Overhead() time.Duration { return time.Since(t.start) - t.stages[StageUpstream] }
```
At the end of the request, record each stage into its histogram and `hub_overhead_ms` once, with the request's ctx (for exemplars). The same numbers go into the terminal audit record (`hub_overhead_ms`, `vendor_ms`, `queue_wait_ms`).

## Traces
Span names are low-cardinality; ids go in attributes.
| Span | Kind | Name | Key attributes |
| --- | --- | --- | --- |
| Edge | Server | `{method} {route}` e.g. `POST /v1/integrations/{id}/invoke` (otelhttp + chi route) | `hub.request_id`, `hub.tenant`, `hub.integration_id`, `hub.integration_version`, `hub.mode`, `hub.code` |
| Transform | Internal | `transform.request` / `transform.response` | transformer id@version |
| Upstream | Client | `upstream {vendor}.{endpoint}` | `hub.account`, attempt, `http.response.status_code`, breaker state, limiter/bulkhead wait |
| Queue | Producer/Consumer | `outbox.enqueue` / `worker.attempt` | request id, attempt |
| Callback | Client | `callback.deliver` | endpoint id, attempt, status |
- Incoming: accept the caller's `traceparent` (otelhttp handler + propagator).
- Outgoing to vendors: inject `traceparent` only for vendors whose definition says they accept it; otherwise strip it (don't leak our trace ids to every third party). Use `otelhttp.NewTransport` on the per-account transport, or `otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))`.
- Async: the envelope stores `trace.traceparent` (§11.3). The worker starts a new trace per attempt with a **span link** to the stored context, and sets `hub.request_id` so all attempts are findable.
- Record errors with `span.RecordError(err)` + `span.SetStatus(codes.Error, string(code))`; never put bodies or secrets in attributes or events.

## Exemplars
Recording histograms with the request `ctx` lets the OTel SDK attach the sampled trace id as an exemplar (trace-based exemplar filter). Prometheus only exposes exemplars in the OpenMetrics format; verify with `curl -H 'Accept: application/openmetrics-text' localhost:9090/metrics | grep '# {'` and enable exemplar storage in Prometheus. Confirm the exporter/SDK version supports this before relying on it in dashboards.

## Alerts as code (`deploy/alerts/`)
Prometheus rule files, one per area (`breaker.yaml`, `queue.yaml`, `audit.yaml`, `overhead.yaml`), every DESIGN §16.3 alert present with `severity: page|warn`, a `summary`, and a `runbook_url` into `docs/`.
```yaml
groups:
  - name: hub-overhead
    rules:
      - alert: HubOverheadP99High
        expr: histogram_quantile(0.99, sum by (le) (rate(hub_overhead_ms_bucket[5m]))) > 5
        for: 10m
        labels: { severity: warn }
        annotations:
          summary: "Hub overhead p99 above 5 ms for 10 min"
          runbook_url: "docs/tech/OBSERVABILITY.md#hub-overhead"
      - alert: BreakerOpenTooLong
        expr: max by (vendor, account, endpoint) (breaker_state) == 2
        for: 5m
        labels: { severity: page }
        annotations:
          summary: "Breaker open > 5 min for {{ $labels.account }}/{{ $labels.endpoint }}"
          runbook_url: "docs/tech/OBSERVABILITY.md#breaker-open"
```
Validate in CI: `promtool check rules deploy/alerts/*.yaml` and unit-test rules with `promtool test rules deploy/alerts/tests/*.yaml`. Keep the `breaker_state` encoding (0 closed, 1 half-open, 2 open) documented in the catalog.

## Dashboards (`deploy/dashboards/`)
Grafana JSON, one per audience: tenant (requests, codes, latency per integration), vendor/account (vendor latency, breaker, parked, DLQ, rate-limit rejections), platform (overhead breakdown by stage, snapshot lag, audit drops, Go runtime). Rules: datasource as a template variable (no hard-coded UIDs), `$tenant`/`$integration`/`$account` variables, panels link to traces via exemplars and to logs by `trace_id`. Export with stable `uid`s and review JSON diffs in PRs.

## Checklist
- [ ] Metric in catalog + `docs/tech/OBSERVABILITY.md` + feature doc; exported name pinned by test.
- [ ] Labels bounded; no ids; attribute sets precomputed on hot path.
- [ ] Buckets include the budget boundaries.
- [ ] New middleware records `hub_stage_duration_ms{stage}`.
- [ ] Span name low-cardinality; no secrets/bodies in attributes.
- [ ] Alert has severity, summary, runbook, and a `promtool test`.
