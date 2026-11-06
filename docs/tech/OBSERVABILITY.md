# Integration Hub — Observability Plan

| | |
| --- | --- |
| Status | Phase 1 (plan; implemented in M0 skeleton, M3 audit, M8 hardening) |
| Date | 2026-10-31 |
| Behaviour source | DESIGN §13 (audit), §16.2 (metrics), §16.3 (alerts), §16.4 (tracing), §18.6 (budget) |
| Implementation home | `backend/internal/telemetry` (leaf package, TECH_SPEC §4), `deploy/alerts/`, `deploy/dashboards/`, `deploy/load/` |

Every new metric, log event or span added by a feature must be listed here and in the feature doc (AGENTS §8.6).

---

## 1. Signals and what each is for

| Signal | Question it answers | Store | Retention | Contains payloads? |
| --- | --- | --- | --- | --- |
| **Audit trail** | "What exactly happened to request X, byte for byte?" (DESIGN §13) | Postgres ledger → object storage | 30 d hot (sealed 90 d), metadata 1 y | yes, subject to payload policy (`full`/`masked`/`sealed`/`metadata_only`) |
| **Metrics** | "Is the system healthy, how fast, how much?" | Prometheus | 15 d raw, 13 mo downsampled | never |
| **Logs** | "Why did this component misbehave?" | log pipeline (Loki or equivalent) | 14 d | never bodies, never secrets |
| **Traces** | "Where did time go in this request across services?" | Tempo / OTLP backend | 7 d | never bodies; ids and small attributes only |
| **Profiles** | "Which code burns CPU / memory / goroutines?" | on demand via pprof; optional continuous profiler | 7 d | no |

Rule: **the audit trail is the only place payloads live.** Logs and traces reference a request by `request_id` and link to the request explorer (`/requests/{request_id}` in the console).

---

## 2. SLIs and SLOs

| # | SLI | Definition | SLO | Source |
| --- | --- | --- | --- | --- |
| S1 | Hub overhead, sync | p99 of `hub_overhead_ms` (server time − vendor time − deliberate waits) on sync and wait-mode immediate attempts | ≤ 5 ms over 30 d windows, evaluated per 10 min | DESIGN §1, §18.6 |
| S2 | Async accept latency | p99 of `hub_accept_ms` (edge receive → 202 written) for `wait`(202 path), `async`, `async_callback` | ≤ 10 ms | DESIGN §1 |
| S3 | Gateway availability | 1 − (requests ending in a **Hub-caused** failure / all requests). Hub-caused = `TRANSFORM_FAILED`, Hub-internal 5xx, and 5xx returned without an `UPSTREAM_*` code. `UPSTREAM_*` and `INTEGRATION_PAUSED` are correct behaviour and count as good. | 99.9 % (phase 1), 99.95 % (phase 2) over 30 d | DESIGN §1 |
| S4 | Zero loss | `hub_accepted_unaccounted` = accepted requests with no terminal status (`succeeded`, `dead_lettered`, `expired`) and no live outbox row, older than `expires_at + 15 min` | **0**, always | DESIGN §1 |
| S5 | Config propagation | p100 of `snapshot_apply_delay_seconds` (node swap time − snapshot `created_at`) across nodes per publish | ≤ 10 s | DESIGN §1, §8.2 |
| S6 | Drain start | `drain_start_delay_seconds` = first released parked envelope − breaker `closed` transition; plus in chaos tests, mock recovery → first release | ≤ 60 s from vendor recovery (probe period 30 s + 3 half-open probes + release tick) | DESIGN §1, §10.5 |

Error-budget alerting on S3 uses multi-window burn rates (§11.2). S4 is a correctness invariant: any non-zero value pages.

---

## 3. Metrics catalogue

Conventions:
- Names follow DESIGN §16.2 exactly (including the `_ms` and `_s` suffixes, so alerts and dashboards match the design). Additional metrics below use Prometheus base units (`_seconds`, `_bytes`) unless they are the per-stage breakdown of `hub_overhead_ms`.
- Library: `prometheus/client_golang` (TECH_SPEC §13). Histograms are native histograms where the backend supports them, with classic bucket fallback `{0.1,0.25,0.5,1,2,3,5,7.5,10,25,50,100,250,500,1000,2500,5000}` ms.
- Exposed on the ops port `/metrics` (`:9090`), never on the public port.
- Common label `role` (gateway | worker | scheduler | admin) is added by the scrape config, not by code.

### 3.1 Cardinality budget
Label value sets (phase 1 planning numbers, revisit at open question DESIGN §20.2):

| Label | Max values | Notes |
| --- | --- | --- |
| `tenant` | 100 | |
| `integration` | 500 | each integration has exactly one owner tenant, so `tenant × integration` ≈ 500, not 50,000 |
| `vendor` | 30 | |
| `account` | 200 | |
| `version` | 3 live per integration | only versions with traffic in the last 24 h are emitted; older series expire (`version` is an internal label; never exposed to callers) |
| `endpoint` | 5 per vendor | |
| `mode` | 4 | |
| `code` | 14 | DESIGN §4.4 + `ok` |
| `stage` | 12 | §11 of TECH_SPEC |

**Cardinality guard (HUB-230):** `hub_requests_total`, `llm_tokens_total`, `llm_cost_usd_total` and `dlq_count` have a hard series cap. Past it, new label combinations are recorded with every bounded label set to `__overflow__` (enum labels `mode`, `code`, `kind` keep their values), and `metric_series_overflow_total{metric}` counts them. A combination admitted once stays admitted.

**Budget: ≤ 50,000 active series per process type; ≤ 250,000 total.** Histograms with the full `{tenant, integration, vendor, account, version}` label set are capped to one metric (`vendor_latency_ms`); everything else that is a histogram drops `tenant`/`version`. CI runs a cardinality check against the dev config + synthetic expansion (TESTING_STRATEGY §10).

### 3.2 DESIGN §16.2 metrics
| Metric | Type | Unit | Labels | Est. series | Emitting package |
| --- | --- | --- | --- | --- | --- |
| `hub_requests_total` | counter | requests | `tenant, integration, vendor, account, version, mode, code` (`code` = `ok` or a §4.4 code; `vendor`/`account` = the target that served the last attempt, empty when refused before resolve) | ~500 × 1.5 versions × 1 mode × 6 codes seen ≈ 4,500 (capped at 10,000, then `__overflow__`) | `edge` (every request that ends in the gateway, dry-runs excluded), `delivery` (each committed terminal transition of a queued request: wait tail, async; once, since a lost race does not commit) |
| `hub_overhead_ms` | histogram | ms | `integration, mode` | 500 × 2 × buckets ≈ 17,000 (classic) | `edge` |
| `vendor_latency_ms` | histogram | ms | `vendor, account, endpoint, outcome` (HUB-086: `upstream` only knows transport facts, so `outcome` is the status class `1xx`–`5xx` or the transport kind `timeout`, `connect`, `reset`, `canceled`, `protocol`; canonical codes live on `vendor_attempts_total`) | 200 × 2 × 4 × 17 ≈ 27,000 (classic; native ≈ 1,600) | `upstream` |
| `breaker_state` | gauge (0 closed, 1 half_open, 2 open) | enum | `vendor, account, endpoint` | ≤ 1,000 | `upstream` (every node reports its local view) |
| `breaker_transitions_total` | counter | transitions | `vendor, account, endpoint, from, to` | ≤ 4,000 | `upstream` |
| `queue_depth` | gauge | envelopes | `pool, status` (`accepted`, `retry_scheduled`, `in_flight`; every pair reported, 0 when empty) | ≤ 6 | `delivery` (`QueueSampler`, scheduler leader, every 10 s; cleared when leadership is lost) |
| `queue_oldest_age_s` | gauge | s | `pool` | 2 | `delivery` (`QueueSampler`): age of the oldest **due, unclaimed** envelope (`now − min(next_attempt_at)` over accepted/retry_scheduled with `next_attempt_at ≤ now`); envelopes waiting out a backoff do not count, so the alert fires only when workers fall behind (HUB-230) |
| `parked_count` | gauge | envelopes | `account, endpoint` (`endpoint` = full ref `vendor.endpoint@v`) | ≤ 1,000 | `delivery` (scheduler leader, each tick) |
| `parked_oldest_age_s` | gauge | s | `account, endpoint` | ≤ 1,000 | `delivery` (scheduler leader, each tick). A former leader keeps its last values until HUB-242; alerts and dashboards keep only the current leader's series (`and on (instance) (scheduler_leader == 1)`) |
| `dlq_count` | gauge | entries | `tenant, integration, account, reason` (unreplayed only) | ≤ 5,000 (capped; the rest fold into one `__overflow__` series) | `delivery` (`QueueSampler`, every 10 s) |
| `ratelimit_rejections_total` | counter | rejections | `layer` (`tenant`, `account`, `budget`); no `tenant`/`integration` labels in code today | ≤ 3 | `edge` (tenant), `upstream` (account), `edge` (LLM budget) |
| `retry_budget_exhausted_total` | counter | events | `account` | 200 | `delivery` |
| `snapshot_version` | gauge | version | — (per pod via scrape labels) | 1 per pod | `registry` (roles): the **archive version** (`hub.config_snapshots.version`, what `getLatestSnapshot` returns) of the served snapshot, equal on every node serving the same publish, restarts included; 0 until the node has confirmed its content is archived (config directory or local cache, archive not reached yet). The audit `snapshot_version` (`integration.resolved` meta, `hub.requests.snapshot_version`, NULL for 0) carries the same number (HUB-401) |
| `credential_expiry_days` | gauge | days | `account, slot` (`active`, `next`; only slots with an expiry; negative once expired) | 400 | `creds` (computed at scrape time from the cache generation, so it counts down between reloads) |
| `llm_tokens_total` | counter | tokens | `tenant, integration, account, model, kind` (`input`/`output`) | ≤ 2,000 (capped at 4,000) | `telemetry.RecordLLMUsage`; registered now, emitted once `llm.chat` lands (HUB-333) |
| `llm_cost_usd_total` | counter | USD | `tenant, integration, account, model` | ≤ 1,000 (capped at 2,000) | same |
| `audit_events_dropped_total` | counter | rows | `reason` (`buffer_full`, `write_error`, `encode_error`) | 3 | `audit` |

### 3.3 Additional Hub metrics (not in §16.2; needed for SLIs, alerts and the runbook)
| Metric | Type | Unit | Labels | Package | Why |
| --- | --- | --- | --- | --- | --- |
| `hub_build_info` | gauge (always 1) | — | `role, version` | `telemetry` | build and role identification on every scrape (HUB-027) |
| `hub_stage_duration_ms` | histogram | ms | `stage` | `edge`, `delivery` | per-stage breakdown required by DESIGN §18.4/§18.5; low-cardinality on purpose |
| `hub_accept_ms` | histogram | ms | `mode` | `edge` | S2 |
| `hub_wait_ms` | histogram | ms | `kind` (`account_limiter`, `bulkhead`) | `upstream` | waits excluded from overhead. `account_limiter` is only time spent waiting for the account's tokens (bucket empty); a lease reply a call waited for is `admission` overhead (HUB-408) |
| `account_limiter_leases_total` | counter | leases | `result` (`granted`, `empty` = nothing above the class's reserve, `error` = Redis failed: fail open 1 s) | `upstream` | token leases taken from Redis by a background goroutine, never on the request (HUB-408, ADR-0019). In steady state a busy node leases about 10 times per second per account; `error` rising means Redis trouble and an unenforced account quota |
| `vendor_attempts_total` | counter | attempts | `vendor, account, endpoint, code` | `delivery` | **planned (HUB-241), not exported yet.** Attempt-level codes for auth/transform alerts; until then they read `hub_requests_total` (ADR-0014) |
| `dlq_entries_total` | counter | entries | `account, reason` | `queue` | **planned (HUB-241), not exported yet.** Rate alert "> 50 / 10 min / account"; until then `DLQSurge` reads the net growth of `dlq_count` (ADR-0014) |
| `hub_accepted_unaccounted` | gauge | requests | — | `delivery` (scheduler reconciler, every 5 min) | S4 |
| `snapshot_apply_delay_seconds` | histogram | s | — | `registry` | S5 |
| `snapshot_apply_failures_total` | counter | failures | `reason` (`invalid_config`, `build_error`; later `fetch_error`) | `registry` (roles) | keep-last-good visibility (HUB-045) |
| `drain_start_delay_seconds` | histogram | s | — | `delivery` (scheduler) | S6 |
| `drain_release_rate` | gauge | envelopes/s | `account, endpoint` | `delivery` | observe 20 % → 100 % ramp (M5 #2) |
| `outbox_claim_duration_seconds` | histogram | s | `pool` | `queue` | Postgres contention |
| `outbox_reclaimed_total` | counter | envelopes | `pool` | `delivery` | visibility-timeout reclaims (crashed workers / gateways) |
| `worker_inflight` | gauge | attempts | `pool` | `delivery` | saturation |
| `bulkhead_inflight` | gauge | calls | `account` | `upstream` | DESIGN §10.8 |
| `upstream_egress_denied_total` | counter | calls | `vendor` | `upstream` | calls refused before dialing: host not declared in the vendor's regions (HUB-086); any increase is a config or security problem |
| `callback_deliveries_total` | counter | attempts | `tenant, outcome` (`delivered`, `retry_scheduled`, `dead_lettered`; one per recorded attempt, HUB-120) | `callbacks` | outbound webhook health |
| `callback_latency_seconds` | histogram | s | `tenant` (buckets 0.01–5 s) | `callbacks` | attempts that sent a request |
| `callback_ssrf_blocked_total` | counter | attempts | `class` (`private`, `loopback`, `link_local`, `cgnat`, `multicast`, `unspecified`, `documentation`, `translation`, `reserved`) | `callbacks` | SSRF guard refusals at delivery (HUB-119/120); any increase is a misconfigured or hostile endpoint |
| `callback_queue_oldest_age_s` | gauge | s | — | `callbacks` (each dispatcher, every 15 s) | age of the oldest due callback not yet sent |
| `callback_dlq_count` | gauge | deliveries | — | `callbacks` (each dispatcher, every 15 s) | callback DLQ size (`status='dead_lettered'`) |
| `metric_series_overflow_total` | counter | observations | `metric` | `telemetry` | observations folded into `__overflow__` because a capped metric reached its series cap (HUB-230); any increase means the cap or the label set needs review |
| `callback_enqueued_total` | counter | deliveries | `event` | `callbacks` | deliveries written by `Enqueue` inside the state-change transaction (HUB-120) |
| `callback_enqueue_skipped_total` | counter | subscriptions | `reason` (`unknown_version`, `unknown_endpoint`, `tenant_mismatch`) | `callbacks` | subscriptions that could not be honoured; any increase is a config problem |
| `webhook_inbound_total` | counter | webhooks | `vendor, account, outcome` (`accepted`, `bad_signature`, `replay`, `uncorrelated`) | `webhooks` | |
| `transform_duration_ms` | histogram | ms | `kind` (`request`,`response`,`error`,`stream`) | `transform` | M2 budget in prod |
| `transform_budget_exceeded_total` | counter | events | `transformer, limit` (`nodes`,`output_size`,`time`) | `transform` | |
| `idempotency_hits_total` | counter | hits | `result` (`miss` = key claimed, `in_progress`, `replay`, `conflict` = same key with another body, `error` = Redis unavailable) | `idempotency` (HUB-118) | dedupe effectiveness; `error` feeds the Redis-down fail-closed/pass-through decision of the edge |
| `idempotency_writes_total` | counter | writes | `op` (`complete`, `release`), `result` (complete: `stored`, `not_owner`, `too_large`, `not_replayable`, `error`; release: `released`, `missing`, `not_owner`, `error`) | `idempotency` (HUB-118) | `not_owner` = a request outlived its 5 min lease; `too_large` = response over 1 MiB, not replayable |
| `creds_cache_miss_total` | counter | misses | `account` | `creds` | must stay 0 on the hot path |
| `creds_decrypts_total` | counter | loader calls | — | `creds` | decrypting loads happen only at warm-up and background refresh (HUB-084); a rise with traffic is a bug. Each load also logs `credentials decrypted` (account@version list, never values; HUB-177) |
| `shadow_total` | counter | copies | `integration, mode, outcome` (`ok`, `vendor_error`, `failed`, `dropped`) | `delivery` | shadow cutover health (HUB-173); `dropped` means the 64-copy bound was hit. Replaces the proposed `shadow_mismatch_total` |
| `redis_op_duration_seconds` | histogram | s | `op` (`idempotency_begin`, `idempotency_complete`, `idempotency_release`, `limiter_lease`) | `telemetry` registers it once per process; `idempotency` (HUB-118) and the account limiter's background leases (`upstream`, HUB-408) observe | one observation per Redis round trip; buckets 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 100, 500 ms so the 1 ms idempotency budget (DESIGN §18.6) is a boundary |
| `pg_pool_acquire_duration_seconds`, `pg_pool_in_use` | histogram, gauge | s, conns | `pool` | cmd wiring (pgxpool stats) | |
| `http_server_inflight` | gauge | requests | `server` (`public`,`admin`) | `edge`, `control` | |
| `scheduler_leader` | gauge (0/1) | — | — | `delivery` | exactly one leader |
| `parked_count` | gauge | envelopes | `account, endpoint` | `delivery` (scheduler, each tick) | parked backlog (HUB-146) |
| `parked_oldest_age_s` | gauge | s | `account, endpoint` | `delivery` (scheduler) | how long the oldest parked envelope waited |
| `drain_released_total` | counter | envelopes | `account, endpoint` | `delivery` (scheduler) | ramped drain releases (HUB-149) |
| `health_probes_total` | counter | probes | `vendor, account, result` | `delivery` (scheduler) | synthetic probes while a breaker is open (HUB-143) |
| `admin_mutations_total` | counter | mutations | `action, outcome` | `control` | |
| `publish_validation_failures_total` | counter | failures | `check` | `control` | |
| `audit_buffer_fill_ratio` | gauge | ratio | — | `audit` | early warning before drops |
| `audit_write_failures_total` | counter | writes | — | `audit` | failed batch writes; their rows also count in `audit_events_dropped_total{reason="write_error"}` (HUB-091) |

### 3.4 Go runtime metrics
Registered via `collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(...))` and `collectors.NewProcessCollector`:
`go_goroutines`, `go_sched_latencies_seconds`, `go_gc_duration_seconds`, `go_memstats_heap_inuse_bytes` (or `go_memory_classes_heap_objects_bytes`), `go_gc_gomemlimit_bytes`, `go_gc_gogc_percent`, `go_sched_gomaxprocs_threads`, `go_gc_heap_goal_bytes`, `process_resident_memory_bytes`, `process_open_fds`, `process_cpu_seconds_total`.

---

## 4. Structured logging

### 4.1 Schema (`log/slog`, JSON, one object per line)
| Field | Type | Always | Meaning |
| --- | --- | --- | --- |
| `ts` | RFC 3339 nano UTC | yes | |
| `level` | `DEBUG`/`INFO`/`WARN`/`ERROR` | yes | |
| `msg` | string, stable event name (`snapshot.applied`, `breaker.opened`, `attempt.failed`) | yes | machine-searchable; free text goes in attributes |
| `role` | gateway/worker/scheduler/admin/hubmcp/mockvendor | yes | |
| `component` | package name (`edge`, `upstream`, …) | yes | |
| `version` | build version / git SHA | yes | |
| `request_id` | `req_…` | when request-scoped | |
| `tenant` | tenant id | when request-scoped | |
| `integration_id` | | when request-scoped | |
| `integration_version` | int | after resolve | internal only |
| `trace_id`, `span_id` | hex | when a span is active (sampled or not) | log↔trace correlation |
| `vendor`, `account`, `endpoint` | | upstream-related | internal only |
| `attempt` | int | worker | |
| `error`, `error_code` | string, canonical code | on failures | wrapped Go error text after redaction |
| `duration_ms` | float | on completion events | |

Request-scoped fields are attached once via a `slog.Logger` stored in the context (`telemetry.Logger(ctx)`), not re-added per call.

### 4.2 Levels policy
- `ERROR`: a Hub bug or a condition needing human action (`TRANSFORM_FAILED`, `UPSTREAM_AUTH_FAILED`, snapshot compile failure, poison envelope, panic recovered).
- `WARN`: degraded but self-healing (breaker opened, retry budget exhausted, audit drop, snapshot fetch retry, Redis timeout).
- `INFO`: lifecycle and state changes (start/stop, snapshot applied, leader acquired, breaker closed, drain started/finished, admin mutation). **No per-request INFO logs on the hot path**; the audit trail and metrics cover requests.
- `DEBUG`: off in prod; enabled per process via ops endpoint `PUT /debug/loglevel` (internal port) with automatic revert after 15 min.

### 4.3 Redaction-by-value handler
- `telemetry.RedactHandler` (HUB-026) wraps the JSON handler, under `telemetry.EnrichHandler` (request fields). Every message, string attribute, group member, `error` text, `[]byte` and stringified `any` value is scanned for registered secret values (TECH_SPEC §9). Each occurrence becomes `***`. Values attached with `Logger.With` are redacted per record, against the current set, so a secret registered after a logger was built is still caught.
- The set (`telemetry.SecretSet`) supports `Add`, `Remove` and `Replace` at runtime (credential warm-up and rotation). Readers are lock-free (an atomic pointer to a `strings.Replacer`, longest secret first). Values shorter than 6 bytes are ignored.
- Keys named `authorization`, `proxy_authorization`, `cookie`, `set_cookie`, `api_key`, `password`, `secret`, `token` or `passcode` (any case) are always replaced with `***`, whatever their value.
- Measured overhead (Apple M-series, Go 1.27, `BenchmarkRedactHandler`, 4 attrs, 50 registered secrets): ~1.0 µs and 21 allocs per line, vs ~0.57 µs and 4 allocs for the bare JSON handler. The cost is off the per-stage budget, since hot-path logging is debug-level and sampled (§4.2). Revisit with an Aho-Corasick matcher if the set grows into the thousands.
- What is registered: decrypted vendor credentials and registrar signing secrets (credential cache, at warm-up and rotation) and the process's config secrets, the Postgres DSN password and `HUB_AUDIT_MASK_KEY` (`roles.Run`, HUB-234).
- Tenant API keys never reach logs: edge logs only `key_id`.
- Test: the M3 grep test asserts no registered secret appears in captured logs or stored audit events (TESTING_STRATEGY §3.4). HUB-234 adds the role-log secret grep: all roles' logs from the `roles` integration run are captured and grepped for every secret the tests used (SECURITY.md §9). No new metrics, log lines or spans.

### 4.4 Bodies
Logs **never** contain request or response bodies, canonical or vendor (backend/AGENTS.md). For debugging, use the audit timeline (subject to payload policy) or `dry-run`. The only exception is `mockvendor`, which may log bodies because it only sees synthetic data.

Per-operation transformers (HUB-343, F-14): no new metric, log or span. The `transformers` meta of `integration.resolved` and the `transformer` / `endpoint` meta of `request.transformed` name the pair and endpoint of the invoked operation, so the audit timeline shows which endpoint each operation used; breakers stay per (account, endpoint), one per operation endpoint.

Vendor sessions (HUB-175, F-14): session data is never logged either. The edge logs `session lookup failed` / `session create failed` (ERROR, Postgres errors), `session target gone` (WARN, the session's version or account left the snapshot) and `session data refused` (WARN, the store refused an Aadhaar-like number or oversize data) with the integration and the error only; the worker logs `session lookup failed`. Session open/use is in the audit trail as `session` meta (id, event, version, account) on `response.transformed` / `integration.resolved`. No new metrics: the lookup is timed in the `resolve` stage of `hub_overhead_ms`.

Subject erasure (HUB-233, `hub erase-subject`, ADR-0012) writes one INFO line, `erase-subject finished`, with `tenant`, `change_id`, `dry_run` and the counts (`erased`, `deferred`, `events`, `results`, `dead_letters`, `callbacks`, `sessions`). On failure it writes one ERROR line, `erase-subject failed, nothing changed`, with `tenant` and `error`. The line never carries the identifier or its `subject_ref`. The identifier, the mask key and the DSN password are registered with the redacting handler as defence in depth. It records no metric: the command is one-shot and never scraped. `change_log` (`action = eraseSubjectPayloads`) is the system of record for deletions, and the JSON result on stdout is the operator's report.

### 4.5 Sampling
- Hot-path WARN events that can storm (Redis timeout, limiter rejection) are rate-limited per `(msg, account)` key: first 10 per 10 s, then 1 in 100, with `sampled_out` count on the next emitted line.
- ERROR is never sampled.

### 4.6 `hubmcp` (F-20)
`hubmcp` (the MCP server, `backend/cmd/hubmcp`) writes JSON logs to **stderr** (stdout carries the MCP stdio stream) with `role: hubmcp` and `version`. It has no metrics endpoint: it is a client process, and the Hub's own metrics count its traffic.

| Event (`msg`) | Level | Attributes | When |
| --- | --- | --- | --- |
| `hubmcp started` | INFO | `transport`, `hub_base_url`, `admin_tools` (bool), `contract_version`, `version` | start-up |
| `admin tools disabled: HUB_ADMIN_TOKEN is not set` | INFO | — | start-up without an admin token |
| `hubmcp listening` | INFO | `addr`, `path` (`/mcp`) | HTTP transport ready |
| `tool_called` | INFO | `tool`, `is_error`, `duration_ms`, and when known `integration_id`, `request_id`, `outcome` (`completed`/`queued`/`error`/`refused`) | every tool call |
| `hubmcp stopped` | INFO | — | clean shutdown |

- Every outbound call carries `User-Agent: hubclient-go/<sdk version> hubmcp/<version>` (admin calls: `hubmcp/<version>`), so the Hub's logs attribute MCP traffic.
- Redaction: `HUB_API_KEY`, `HUB_ADMIN_TOKEN` and, over HTTP, each client's own key are redacted by value from every string attribute and from every tool result (`***`). Bodies are never logged; `tool_called` carries ids only.

---

## 5. Tracing

- OpenTelemetry Go SDK, OTLP/gRPC to the collector, W3C `traceparent`/`tracestate` propagation.
- **Sampling** (`telemetry.Sampler`): parent-based; root ratio `HUB_TRACE_SAMPLE_RATIO` (default 5 %). Caller's sampled flag is respected. A new root that is **linked** to a sampled span (worker attempt, callback delivery) is sampled too, so an async request sampled at the gateway keeps its worker and callback spans. Collector-side tail sampling keeps 100 % of traces with an error status or duration > 1 s.
- **Async continuity** (HUB-231, ADR-0013): the edge opens a `hub.queue.enqueue` producer span and the envelope stores *its* `traceparent` in `trace.traceparent` (DESIGN §11.3), in the same transaction as the outbox row. The worker's attempt span is a new root **linked** (span link) to it, so long-delayed retries do not create hour-long traces. Terminal transitions write their span's `traceparent` on each `hub.callback_deliveries` row; the dispatcher's `hub.callback.deliver` span is a new root linked to it. One async request is therefore one linked chain, every span tagged with `hub.request_id`: caller → edge → enqueue ⇢ worker attempt → transform / upstream → vendor ⇢ callback.
- **To vendors**: the upstream client injects the Hub's own `traceparent` (the upstream span) only for vendors listed in `HUB_TRACE_FORWARD_VENDORS` (comma-separated registry vendor names, default none; ADR-0013 until a vendor-definition field is contracted). `tracestate` is never sent, and a `traceparent`/`tracestate` mapped by a transformer is always dropped.
- **Back to callers**: never return internal span attributes; `X-Hub-Request-Id` is the caller-facing correlation id.
- **Request explorer**: `hub.requests.trace_id` is the edge span's trace id (sync, wait and async), so `/requests/{request_id}` links to the trace.

### 5.1 Spans
| Span (name) | Kind | Where | Key attributes | Status |
| --- | --- | --- | --- | --- |
| `{method} {route}`, e.g. `POST /v1/integrations/{integration_id}/invoke` (edge server span) | server | `telemetry.HTTPHandler` (otelhttp) + `edge` | `hub.request_id`, `hub.tenant`, `hub.integration_id`, `hub.integration_version`, `hub.mode`, `hub.code`, `http.route`, `http.response.status_code` (+ otelhttp semconv keys below) | HUB-231 |
| `hub.transform.request` / `hub.transform.response` | internal | `delivery` (Attempter) | `hub.transformer`, `hub.transformer_version`; status `transform_failed` / `time_budget` | HUB-231 |
| `upstream {vendor}.{endpoint}` | client | `upstream` | `hub.vendor`, `hub.account`, `hub.endpoint`, `server.address`, `http.request.method`, `http.response.status_code`, `hub.transport_error` | HUB-088, forwarding HUB-231 |
| `hub.queue.enqueue` | producer | `edge` (async, wait, park) | `hub.pool`; status `persist_failed` | HUB-231 |
| `hub.worker.attempt` | consumer (new root, linked) | `delivery` (Worker) | `hub.request_id`, `hub.tenant`, `hub.integration_id`, `hub.pool`, `hub.attempt`, `hub.queue_wait_ms`, `hub.outcome` (final state or short reason); status `transition_failed` | HUB-231 |
| `hub.callback.deliver` | client (new root, linked) | `callbacks` | `hub.endpoint_id`, `hub.callback.attempt`, `hub.callback.event`, `hub.request_id`, `http.response.status_code`, `hub.outcome` | HUB-120, link HUB-231 |
| `hub.validate`, `hub.resolve`, `hub.idempotency` | internal / client | `edge` | `hub.capability`; `hub.snapshot_version`, `hub.target_index`, `hub.failover`; `hub.idempotency.result` | planned (HUB-240) |
| upstream `hub.attempt`, `hub.breaker_state`, `hub.wait.limiter_ms`, `hub.wait.bulkhead_ms` | — | `upstream` | — | planned (HUB-240) |
| `hub.webhook.inbound` | server | `webhooks` | `hub.vendor`, `hub.account`, `hub.event_type`, `hub.correlated` | planned (HUB-240) |
| `hub.snapshot.apply` | internal | `registry` | `hub.snapshot_version`, `hub.compile_ms` | planned (HUB-240) |
| `hub.admin.*` | server | `control` | `hub.actor`, `hub.action` | planned (HUB-240) |
| `hubmcp.tool` | server | `hubmcp` | `mcp.tool`, `mcp.transport` | HUB-300 |

Stage spans (`transform.*`) are created only when the parent span is recording; unsampled requests pay a context lookup (0 allocations, `TestSpanHelpersDoNotAllocateUnsampled`, `TestTransformSpans`) plus the histogram cost. `hub.vendor`/`hub.account` are internal attributes; the trace backend is internal-only.

### 5.2 Attribute allowlist
Span attributes never include body content, credential material or header values, with one exception: otelhttp's `user_agent.original` (the client software string, never a credential). Every key the Hub may emit is listed in `telemetry.AllowedSpanAttributes`: the `hub.*` keys above plus the otelhttp semantic-convention keys (`http.route`, `http.request.method`, `http.request.method_original`, `http.response.status_code`, `url.scheme`, `url.path`, `server.address`, `server.port`, `network.protocol.version`, `network.peer.address`, `network.peer.port`, `client.address`, `user_agent.original`, `http.request.body.size`, `http.response.body.size`). `telemetry.SpanAttributeViolations` fails a test on any key outside the list, and on any attribute, event, link or status value that contains a forbidden string. `TestSyncSpans` and `TestAsyncTraceLinksCallerToCallback` run it over every span of a request with the tenant API key and its hash, the vendor credentials, the callback signing secret and body fragments as forbidden values. Error text never goes into a span (no `RecordError`): statuses carry a short class only. Adding an attribute means adding it to the list and to the table above.

---

## 6. Audit vs logs vs metrics (decision guide)
| You want to record… | Use |
| --- | --- |
| What the caller sent / what the vendor returned | Audit event (policy applies) |
| That a request reached a state (`parked`, `expired`) | Audit event **and** a metric counter; no log |
| Latency or count of anything | Metric |
| A component state change (breaker, leader, snapshot) | Log (INFO/WARN) **and** a metric |
| An unexpected error inside the Hub | Log (ERROR) + metric + span status; the request's audit gets `response.transform_failed` / `request.failed` |
| An admin mutation | `change_log` row (authoritative) + `admin_mutations_total` + INFO log |

---

## 7. Health endpoints
- `/healthz`: process alive (no dependency checks).
- `/readyz`: snapshot loaded, credentials warm, Postgres reachable (gateway: cached check, refreshed every 2 s, not per probe), Redis reachable; false during shutdown (TECH_SPEC §12.4).

---

## 8. Profiling and memory

### 8.1 pprof
- `net/http/pprof` handlers registered **only on the ops mux** (`:9090/debug/pprof/*`), never on the public or admin muxes (`roles.opsHandler`, HUB-403). `TestPprofOnlyOnOpsPort` asserts the ops port serves them for the gateway, admin and worker roles and the public listener answers 404.
- Access: `kubectl port-forward pod/<p> 9090` only; NetworkPolicy blocks ingress to 9090 except from Prometheus.
- Block and mutex profiling off by default; enable with `runtime.SetBlockProfileRate`/`SetMutexProfileFraction` via the ops endpoint during an investigation.

### 8.2 Continuous profiling (optional, phase 2)
Pyroscope (or Grafana Alloy `pyroscope.scrape`) pulling CPU, heap, goroutine profiles from `:9090` every 60 s at < 2 % overhead. Enabled per environment; on in staging for load tests.

### 8.3 Memory and GC settings
- **GOMEMLIMIT** = 85 % of the container memory limit (set from the downward API `limits.memory` in the entrypoint). **GOGC** = 100 (default). Tune gateway to `GOGC=200` only if profiles show GC CPU > 10 % with headroom under GOMEMLIMIT.
- **GOMAXPROCS**: Go 1.25 is container-aware (honours the cgroup CPU limit), so no `automaxprocs`. Set CPU limits on gateway pods so this applies.
- Soft heap target alert: `go_memstats_heap_inuse_bytes > 0.8 × go_gc_gomemlimit_bytes` for 15 min (warn).

### 8.4 `sync.Pool` review
Pools exist only for: request body buffers, response serialisation buffers, audit encode buffers. Review checklist (PR template + skill `go-memory-performance`):
- Objects larger than 64 KB are not returned to a pool (prevents pinning large buffers).
- Pooled objects are reset before `Put`; no references escape after `Put` (tests with `-race` and a poison-on-put debug build tag).
- Each pool has a benchmark proving it reduces allocs (`-benchmem`); otherwise remove it.

### 8.5 Leak detection
- **Tests**: `goleak.VerifyTestMain` in every package that starts goroutines; shutdown test per role asserts zero leftover goroutines (TECH_SPEC §12.4).
- **Prod alerts**: `go_goroutines` growth (`deriv(go_goroutines[1h]) > 0` and > 2× the 7-day median for 1 h, warn); heap growth (`deriv(go_memstats_heap_inuse_bytes[6h]) > 0` while traffic is flat, warn).
- **Soak**: 24 h soak in staging must show flat goroutine count and heap after warm-up (§12).

### 8.6 Benchmark regression
`make bench` runs `-count=10`; CI compares against the `main` baseline with `benchstat`; > 10 % slowdown (p < 0.05) on any hot-path benchmark fails the job (TESTING_STRATEGY §9).

---

## 9. Debugging runbook

Each alert's `runbook_url` points at `docs/runbooks/<AlertName>.md`; the index is [`docs/runbooks/README.md`](../runbooks/README.md) (HUB-237). The runbooks are the on-call procedure (checks, causes, fixes, escalation); the sections below are the debugging background they build on.

### 9.1 High Hub overhead (`HubOverheadP99High`)
1. Platform dashboard → "Overhead by stage": which `hub_stage_duration_ms{stage}` moved?
2. `validate` up → a new or larger capability schema / payload size shift (check `http_request_size_bytes`). `idempotency` up → Redis latency (`redis_op_duration_seconds`). `audit` up → audit channel near full (`audit_buffer_fill_ratio`). `admission` up → breaker or limiter cost; the limiter serves from a local lease, so a rise means calls are waiting for lease replies (Redis slow: `redis_op_duration_seconds{op="limiter_lease"}`), never for tokens (those are in `hub_wait_ms`).
3. Check GC: `go_gc_duration_seconds`, GC CPU; check `go_sched_latencies_seconds` (CPU throttling → raise limits or replicas).
4. Grab a 30 s CPU profile from one hot pod: `go tool pprof http://localhost:9090/debug/pprof/profile?seconds=30`.
5. Compare with the last release's benchmarks (`benchstat`).

### 9.2 Stuck queue (`QueueOldestAgeHigh`)
1. `queue_depth{status}`: growing `accepted` with flat `in_flight` → workers not claiming (check `worker_inflight`, pod count, `outbox_claim_duration_seconds`).
2. Workers saturated? `worker_inflight == HUB_WORKER_CONCURRENCY` → scale or check slow vendor (`vendor_latency_ms`).
3. Throttled by account limiter? `hub_wait_ms{kind="account_limiter"}` high → expected; vendor quota is the bottleneck.
4. Postgres: `pg_stat_activity` for long transactions; `outbox` bloat (`n_dead_tup`), autovacuum running?
5. Reclaims rising (`outbox_reclaimed_total`) → workers crashing; check restarts and poison detector logs (`envelope.poisoned`).

### 9.3 Parked backlog not draining (`ParkedOldestAgeHigh`)
1. `breaker_state` for the `(account, endpoint)`: still open → vendor still down; check health-probe logs (`probe.failed`) and the vendor status page.
2. Breaker closed but `drain_release_rate == 0` → scheduler: is there a leader (`sum(scheduler_leader) == 1`)? leader logs `drain.*`.
3. Release happening but envelopes re-park → breaker flapping; check `breaker_transitions_total`.
4. Integration paused? Paused integrations are not parked (nack), but check `integrations.paused`.
5. TTL: envelopes expiring rather than draining show up as `request.expired` in audit and `hub_requests_total{code="EXPIRED"}`.

### 9.4 Auth failures (`UpstreamAuthFailed`)
1. Which `(vendor, account)`? Was there a recent rotation (change_log `account.rotate`/`promote`)?
2. `credential_expiry_days` for the account; `creds_cache_miss_total`.
3. Run `test-connection` from the admin API with `active` and `next` slots.
4. Roll back the promotion (promote previous) if rotation-related; otherwise contact the vendor. Async requests are dead-lettered (DESIGN §10.2): replay after the fix by filter `reason=UPSTREAM_AUTH_FAILED, account=…`.

### 9.5 Transform failures (`TransformFailed`)
Always a Hub bug (DESIGN §4.4).
1. Find a request id from the alert's exemplar or `request.failed` audit; open its timeline: `response.transform_failed` holds the failing expression and input (policy permitting).
2. Reproduce locally with `hubctl dry-run` / a new golden fixture built from the (masked) input.
3. Fix the transformer, publish a new version (goldens must pass); replay the DLQ by filter.

### 9.6 Memory growth (`HeapNearLimit`, heap growth)
1. `go tool pprof -sample_index=inuse_space http://localhost:9090/debug/pprof/heap`; take two heap profiles 10 min apart and `-diff_base`.
2. Usual suspects: old snapshots still referenced (a goroutine holding `*Snapshot` — check goroutine profile for long-lived holders), unbounded maps keyed by request id (sessions, singleflight), large buffers retained by `sync.Pool`, audit channel backlog.
3. Check `GOMEMLIMIT` is set (`go_gc_gomemlimit_bytes`); if GC is thrashing near the limit, it's a real leak or under-provisioning.

### 9.7 Goroutine leak (`GoroutinesGrowing`)
1. `curl localhost:9090/debug/pprof/goroutine?debug=2 > g.txt`; group by stack (`go tool pprof -traces`).
2. Common causes: HTTP response bodies not closed (bodyclose lint), wait-mode attempts not bound to `workCtx`, long-poll waiters not released, timers not stopped, Redis subscriptions not closed on snapshot reconnect.
3. Add a regression test with `goleak` around the offending component.

---

## 10. Dashboards (`deploy/dashboards/*.json`)

Shipped in HUB-232: `tenant.json` (uid `hub-tenant`), `vendor.json` (`hub-vendor`), `platform.json` (`hub-platform`). Grafana JSON model, schemaVersion 39, `id: null`, datasource only through the `${datasource}` variable, tag `integration-hub` (dashboards link to each other). `TestDashboardsAreValidAndUseExportedMetrics` lints them and checks every query against the exported metrics (ADR-0015); Grafana import is not checked in CI yet (HUB-243). Panels below that need a metric the code does not export are left out until it exists: they are marked *(not built: …)*.

Prometheus must ingest classic histogram buckets (`_bucket`): with native histograms enabled, set `always_scrape_classic_histograms: true` on the Hub scrape job.

### 10.1 Tenant dashboard (variable: `tenant`, `integration`)
Requests by code (stacked) · success ratio · requests by integration · mode mix · overhead p50/p99 by integration · DLQ count by reason · error codes · callback delivery outcome and latency · LLM tokens and cost (if any). *No vendor/account panels on the tenant dashboard* (tenant_viewer may see it in the console). *(not built: end-to-end latency — no per-tenant latency histogram; 202 vs 200 for wait mode — not distinguished by any metric; tenant rate-limit rejections — `ratelimit_rejections_total` has no `tenant` label, see the platform dashboard; the tenant limiter's refusals show in "Requests by code")*.

### 10.2 Vendor / account dashboard (variables: `vendor`, `account`, `endpoint`)
Breaker state timeline · breaker transitions · health probe results · vendor latency p50/p99 by outcome (status class or transport failure) · vendor calls by outcome (`vendor_latency_ms_count`) · finished requests by code · egress refused · deliberate waits (all accounts; `hub_wait_ms` has only `kind`) · parked count / oldest age (current scheduler leader) · drain release rate · DLQ by account and reason · retry budget exhaustions · credential expiry days · credential cache misses. Parked, drain and DLQ panels filter by account only: their `endpoint` label is the full ref `vendor.endpoint@v`, while `breaker_state` and `vendor_latency_ms` use `endpoint@v`. *(not built: attempts by canonical code — `vendor_attempts_total`, HUB-241; 429 rate and `Retry-After`; bulkhead in-flight vs limit — `bulkhead_inflight` not exported)*.

### 10.3 Platform dashboard
SLO tiles S1 (overhead p99, sync + wait) and S3 (availability, Hub-caused failures) · breakers not closed · scheduler leaders · requests by mode and code · overhead p50/p99 and by stage · rate-limit rejections by layer · idempotency results · Redis op latency · shadow copies · queue depth / oldest age per pool · DLQ by reason · parked total · callback queue, DLQ, outcomes, SSRF refusals, skipped subscriptions · audit buffer fill, drops and write failures · snapshot version per pod and apply failures · credential decrypts and cache misses · metric series overflow · Go runtime (goroutines, heap in-use vs GOMEMLIMIT, GC pause, sched latency, CPU) · process restarts. *(not built: S2, S4, S5, S6 tiles and burn rates — `hub_accept_ms`, `hub_accepted_unaccounted`, `snapshot_apply_delay_seconds`, `drain_start_delay_seconds` not exported; outbox claim latency and reclaims — `outbox_*` not exported; Postgres pool — `pg_pool_*` not exported)*.

---

## 11. Alerts as code (`deploy/alerts/hub.rules.yaml`)

### 11.1 DESIGN §16.3 rules (shipped, HUB-232)
Groups `hub-resilience`, `hub-delivery`, `hub-errors`, `hub-platform`. Every alert has `severity` (`page`/`warn`), `summary`, `description` and `runbook_url` = `https://github.com/Donquixot-Doflamingo/integration-hub/blob/main/docs/runbooks/<AlertName>.md` (runbooks: [index](../runbooks/README.md), HUB-237). Unit tests: `deploy/alerts/{resilience,delivery,errors,platform}_test.yml`, a firing and a non-firing case per alert. The file is the source of truth; the table is a summary.

| Alert | Expression (PromQL, abridged) | For | Severity | Debugging |
| --- | --- | --- | --- | --- |
| `BreakerOpenTooLong` | `max by (vendor, account, endpoint) (breaker_state) >= 1` (not closed: a failing half-open probe re-opens, so `== 2` would reset the timer) | 5m | page | §9.3 |
| `ParkedOldestAgeHigh` | `max by (account, endpoint) (parked_oldest_age_s and on (instance) (scheduler_leader == 1)) > 600` | 1m | page | §9.3 |
| `QueueOldestAgeHigh` | `max by (pool) (queue_oldest_age_s) > 120` | 1m | page | §9.2 |
| `DLQSurge` | `(sum by (account) (dlq_count) - sum by (account) (dlq_count offset 10m) or sum by (account) (dlq_count) unless sum by (account) (max_over_time(dlq_count[10m] offset 10m))) > 50` | 0m | page | §9.2 / §9.4 |
| `UpstreamAuthFailed` | `sum by (vendor, account) (increase(hub_requests_total{code="UPSTREAM_AUTH_FAILED"}[5m])) > 0` **or** new series (below) | 0m | page | §9.4 |
| `TransformFailed` | `sum by (integration) (increase(hub_requests_total{code="TRANSFORM_FAILED"}[5m])) > 0` **or** new series | 0m | page | §9.5 |
| `SnapshotBehind` | `(scalar(max(snapshot_version)) - snapshot_version) > 0` | 1m | warn | TECH_SPEC §7 |
| `CredentialExpiringSoon` | `min by (account, slot) (credential_expiry_days) < 7` | 1h | warn | §9.4 |
| `HubOverheadP99High` | `histogram_quantile(0.99, sum by (le) (rate(hub_overhead_ms_bucket{mode=~"sync\|wait"}[5m]))) > 5` (S1 scope; async accepts have their own target) | 10m | warn | §9.1 |
| `AuditEventsDropped` | `sum by (reason) (increase(audit_events_dropped_total[5m])) > 0` **or** new series | 0m | warn | §9.1 step 2 |

Notes:
- **New series.** `hub_requests_total` and `audit_events_dropped_total` create a series on its first increment, and `increase()` needs two samples, so a plain `increase() > 0` misses the first failure of a new label set. The "any" rules add `sum by (…) (X unless max_over_time(X[1h] offset 5m)) > 0`: a series with no sample in the hour before the last 5 minutes is new and its value is all new events.
- **Metric sources (ADR-0014).** `vendor_attempts_total` and `dlq_entries_total` are not exported yet (HUB-241). Auth and transform failures are dead-lettered at once on async paths, so the final counter sees them (except a transformer time-budget overrun that retries and later succeeds). `DLQSurge` is the net growth of unreplayed dead letters per account; replays inside the window hide as many new entries, and a missing baseline sample at t−10m (leader change) suppresses rather than pages.
- **Scheduler-leader gauges.** `queue_oldest_age_s` and `dlq_count` are cleared when leadership moves; `parked_*` are not yet (HUB-242), hence the leader filter.
- `breaker_state` is reported by every node; `max` gives the most pessimistic view.
- All names and labels are checked against the registry by `TestAlertRulesUseExportedMetrics` (ADR-0015).

### 11.2 Additional rules
| Alert | Expression | For | Severity |
| --- | --- | --- | --- |
| `ZeroLossViolation` | `max(hub_accepted_unaccounted) > 0` | 0m | page |
| `AvailabilityBurnFast` | 1 h and 5 m burn rate > 14.4 × (1 − 0.999) on S3 ratio | 2m | page |
| `AvailabilityBurnSlow` | 6 h and 30 m burn rate > 6 × budget | 15m | warn |
| `AcceptP99High` | `histogram_quantile(0.99, sum by (le) (rate(hub_accept_ms_bucket[5m]))) > 10` | 10m | warn |
| `SchedulerLeaderMissing` | `sum(scheduler_leader) != 1` | 2m | page |
| `SnapshotApplyFailing` | `increase(snapshot_apply_failures_total[10m]) > 0` | 0m | warn |
| `CredsCacheMiss` | `increase(creds_cache_miss_total[5m]) > 0` | 0m | warn |
| `HeapNearLimit` | `go_memstats_heap_inuse_bytes / go_gc_gomemlimit_bytes > 0.8` | 15m | warn |
| `GoroutinesGrowing` | `go_goroutines > 2 * quantile_over_time(0.5, go_goroutines[7d]) and deriv(go_goroutines[1h]) > 0` | 1h | warn |
| `AuditBufferFilling` | `max(audit_buffer_fill_ratio) > 0.7` | 5m | warn |
| `CallbackFailuresHigh` | `sum by (tenant) (rate(callback_deliveries_total{outcome!="delivered"}[10m])) / sum by (tenant) (rate(callback_deliveries_total[10m])) > 0.2` | 10m | warn |
| `MCPInvokeErrorsHigh` | see §14 | 10m | warn |

Not shipped yet (HUB-244); several need metrics that are not exported (`hub_accepted_unaccounted`, `hub_accept_ms`). Rules are unit-tested with `promtool test rules` in CI (TESTING_STRATEGY §11, CI job `monitoring`).

---

## 12. Load and soak tests (`deploy/load/`)

Tool: **`hubload`** (`backend/cmd/hubload`, ADR-0016), driven by `deploy/load/run.sh` (`make load`, `make soak`). It is an open-loop generator (arrivals at a constant rate, latency measured from the intended send time, so a stalled Hub shows up as latency rather than as fewer requests). Each step also scrapes `/metrics` of every role before and after (`hub_overhead_ms`, `hub_stage_duration_ms`, `vendor_latency_ms`, `hub_requests_total` deltas; native histogram buckets when present), samples the outbox every second (same queries as `queue_depth` / `queue_oldest_age_s`), reads the audit ledger's `hub.requests.hub_overhead_ms` for the measured window, and reports CPU per process. k6 and vegeta are not used (ADR-0016). The target is laptop scale (DESIGN §19 M8) with mockvendor at a configured latency. Default sizes fit a 2-CPU machine; `deploy/load/README.md` gives the full-length settings. Results: `docs/tech/LOAD_TEST_REPORT.md`.

Until HUB-405 is fixed, the exported `hub_overhead_ms` histogram is wrong: it is computed with a timestamp taken before the vendor call, so it is mostly negative. Use the audit ledger value (`hub.requests.hub_overhead_ms`) or the stage breakdown instead. The S1 SLO and the `HubOverheadHigh` alert read the broken metric until then.

| Scenario | Shape | Pass criteria |
| --- | --- | --- |
| L1 sync steady | 500 RPS sync, 4 KB body, mockvendor 20 ms, 10 min | server-side `hub_overhead_ms` p99 ≤ 5 ms; 0 Hub-caused errors |
| L2 async steady | 2,000 msg/s `async`, 5 min | `hub_accept_ms` p99 ≤ 10 ms; queue oldest age < 5 s; outbox claim p99 < 20 ms |
| L3 mixed | L1 + L2 together, 30 min | both sets of criteria; Postgres CPU < 70 % |
| L4 wait-mode | 300 RPS wait, mockvendor latency 50 % at 100 ms / 50 % at 4 s | ~50 % 200, ~50 % 202; all eventually `succeeded`; S4 = 0 |
| L5 outage chaos | L2 load; mockvendor hard-down for 3 min at random every 15 min | breaker opens; parked grows; drain starts ≤ 60 s after recovery; ramp 20 % → 100 %; S4 = 0 |
| L6 soak | L3 at 50 % load for 24 h (staging) with random L5 outages | S4 = 0; flat goroutines and heap after warm-up; no audit drops; p99 stable |

Accepted request ids (202 bodies) are recorded by `hubload soak` and reconciled against `hub.requests` at the end (zero-loss proof independent of Hub metrics). Its callback receiver counts terminal events per request id, so duplicates (two outcomes for one request) and redeliveries (the same event twice) are told apart. Worker restarts (`SOAK_RESTARTS`) stay off until HUB-406: a worker restarted while a backlog exists panics on a nil snapshot.

Also new for the load runs: `net/http/pprof` is mounted on the ops port of every role (§8.1, HUB-403). The harness takes a 20 s CPU profile of the gateway during the sync run and of the worker and gateway during the async run.

---

## 13. Frontend observability (admin console)
- **Web vitals**: `web-vitals` (LCP, INP, CLS, TTFB) reported to a console-side endpoint `POST /internal/rum` handled by the SvelteKit server and exported as metrics (`console_web_vital{name, route}` histogram). Not part of `/admin/v1`; it is the console's own server route.
- **Error reporting**: `handleError` hooks (server and client) log structured errors with route, `request_id` (from the API error envelope), user role (not user identity), build version. Optional Sentry-compatible sink, configured with PII scrubbing; never sends request/response bodies.
- **API error rates**: the `openapi-fetch` middleware records `console_api_requests_total{operation_id, status}` and duration server-side (SSR loads and form actions), giving per-operation error rates.
- **Logs**: SvelteKit server uses the same JSON field names as §4.1 (`role=console`).
- **Dashboards**: console panel on the platform dashboard (vitals p75, API error rate by operation, SSR latency).

---

## 14. MCP server observability (`hubmcp`)
> **Status (2026-11-07):** HUB-300–307 ship the logs of §4.6 and the `User-Agent`; the metrics, span and alert below are planned in HUB-311.

- **Metrics** (on its own ops port in HTTP mode; stdio mode exports via OTLP push or not at all):
  `hubmcp_tool_calls_total{tool, outcome}` (outcome = `ok`, `queued`, `hub_error:<code>`, `invalid_input`, `transport_error`), `hubmcp_tool_duration_seconds{tool}`, `hubmcp_sessions_active{transport}`, `hubmcp_hub_http_requests_total{operation, status}`.
- **Logs**: §4.1 schema with `role=hubmcp`, `component=tool`, plus `mcp_tool`, `mcp_session` (random id, not user identity). Tool arguments and results are **never logged** (they contain canonical bodies); log `request_id` from Hub responses instead. In stdio mode logs go to **stderr** only (stdout is the protocol channel).
- **Traces**: span `hubmcp.tool` (server) → child client spans for each Hub HTTP call with `traceparent` propagation, so a tool call links to `hub.edge`.
- **Alert**: `MCPInvokeErrorsHigh`: `sum(rate(hubmcp_tool_calls_total{tool="hub_invoke", outcome=~"hub_error:.*|transport_error"}[10m])) / sum(rate(hubmcp_tool_calls_total{tool="hub_invoke"}[10m])) > 0.2` for 10 m (warn).
