# F-06 — Sync invoke path & canonical error envelope

## Summary
The end-to-end sync pipeline: resolve the active integration version from the snapshot, transform, inject credentials, call the vendor through the per-account upstream client, map the response, and answer with a canonical body or the single error envelope. Includes `GET /v1/requests/{id}` and caller dry-run (DESIGN §4, §5.1, §10.1–10.2, §18.4).

## Status
`in-progress` — 2026-10-31. Sync path shipped in M3 (`docs/milestones/M3.md`); failover and the 202 paths arrive with M4/M5.

## Owner
`backend-engineer`.

## Milestone / Epic
M3 · EP-04 · HUB-080–109.

## Problem
Callers need one contract, one error vocabulary and predictable timeouts across every vendor, with Hub overhead ≤ 5 ms p99.

## Goals / Non-goals
**Goals**
- `POST /v1/integrations/{id}/invoke` and `/invoke/{op}` in `sync` mode.
- Resolver: active version, weighted target (hash of idempotency key or request id), failover chain recorded (failover behaviour itself is F-12).
- Upstream client per account: HTTP pool, `connect_timeout_ms` (500), `timeout_ms` (3000), `X-Request-Deadline` cap.
- Error envelope and all codes of the caller error catalogue in `contracts/README.md` (DESIGN §4.4 plus the contract additions `IDEMPOTENCY_KEY_REQUIRED`, `OPERATION_NOT_FOUND`, `CAPABILITY_NOT_FOUND`, `REQUEST_NOT_FOUND`, `IDEMPOTENCY_CONFLICT`, `PAYLOAD_TOO_LARGE`, `INTERNAL_ERROR`, `NOT_READY`); outcome classification per DESIGN §10.2.
- One immediate retry on timeout only if the endpoint is idempotent or a key is present.
- `X-Hub-Request-Id` on every response; debug-only `X-Hub-Integration-Version`.
- `GET /v1/requests/{request_id}`; caller dry-run (`dryRunIntegration`), a documented, scope-gated exception to the no-vendor-leak rule (PRD DV-6).

**Non-goals**
- Breaker and `on_circuit_open` (F-12), outbox modes (F-09), streaming (F-22).

## User stories
- [US-0601](../product/USER_STORIES.md) — Canonical call, canonical answer
- [US-0602](../product/USER_STORIES.md) — One error envelope, one code per outcome
- [US-0603](../product/USER_STORIES.md) — Deadlines and traces
- [US-0604](../product/USER_STORIES.md) — Single safe retry on timeout
- [US-0605](../product/USER_STORIES.md) — Dry-run without side effects

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | `POST /v1/integrations/{id}/invoke` | `contracts/openapi/hub-public.v1.yaml#invokeIntegration` |
| Caller API | `POST /v1/integrations/{id}/invoke/{op}` | `#invokeIntegrationOperation` |
| Caller API | `GET /v1/requests/{request_id}` | `#getRequestStatus` |
| Caller API | `POST /v1/integrations/{id}/dry-run` (key scope `dry_run`) | `#dryRunIntegration` |
| Schema | `ErrorEnvelope`, `CanonicalError`, `Accepted`, `RequestStatus`, `DryRunResult` | `contracts/openapi/hub-public.v1.yaml#/components/schemas`; `contracts/schemas/events/canonical-error.json` |

## Behaviour & rules
1. Pipeline order DESIGN §18.4; no DB or registry reads, only the snapshot pointer (DESIGN §18.5.1).
2. One JSON parse in, one marshal out; transforms operate on the parsed tree (§18.5.2).
3. Classification (DESIGN §10.2): 2xx → 200; timeout/reset → `UPSTREAM_TIMEOUT` 504; 429 → `UPSTREAM_RATE_LIMITED` 429 with `retry_after_ms`; 5xx → `UPSTREAM_UNAVAILABLE` 503; 401/403 → `UPSTREAM_AUTH_FAILED` 502 (pages); other 4xx → `UPSTREAM_REJECTED` 422 (vendor message passed through); transformer error → `TRANSFORM_FAILED` 500; any other unexpected Hub failure → `INTERNAL_ERROR` 500 (retryable).
4. Every error envelope carries `request_id`, `retryable`, and `upstream.{status,code}` where known; `circuit` only when relevant.
5. `X-Hub-Integration-Version` is debug-only; SDKs must not act on it (DESIGN §4.3).
6. Dry-run (`dryRunIntegration`, PRD DV-6): full pipeline up to transform; nothing sent, no queue entry, no idempotency record. The response is the **exact** upstream request (method, URL, headers, body) with credentials and any value marked secret replaced by `***`. This is a documented exception to the no-vendor-leak rule: it requires API-key scope `dry_run` (not granted by default; for integration developers during onboarding and debugging; otherwise 403 `FORBIDDEN`, reason `missing_scope`), it is audited with `dry_run: true`, and callers must never build logic on it because it changes at every Hub-side cutover.
7. `GET /v1/requests/{id}` is visible only to the owning tenant (others get 404 `REQUEST_NOT_FOUND`); returns status (DESIGN §10.9 states plus `failed`, the terminal state of a sync request that returned an error) and canonical result or error.

## Upstream client (HUB-086)
Package `internal/upstream`: `Client.Do(ctx, Target, core.VendorRequest) (core.VendorResponse, error)` and `Client.Attempt` (same call plus `Latency`, `Truncated`, `MalformedBody`). `TargetFor(snap, accountID, "clevertap.upload@1")` builds the `Target` from the snapshot only.

- **Pools**: one `http.Transport` per account (`MaxConnsPerHost` = bulkhead, default 64; 32 idle; 90 s idle timeout; HTTP/2 when the vendor negotiates it; no proxy). `Prune(snap)` closes pools of removed accounts; `Close()` at shutdown.
- **Timeouts**: the attempt context is `min(endpoint timeout_ms (default 3000), caller deadline)`; the caller sets its `X-Request-Deadline` as the context deadline. `connect_timeout_ms` (default 500) bounds the TCP connect plus the TLS handshake.
- **TLS**: 1.2 minimum, verification always on; the client dials TLS itself so the connect timeout covers the handshake.
- **Egress**: the base URL's scheme and host:port must equal one of the vendor's `regions`, checked before any dial; every dial re-checks host:port (and refuses a dial without an allowlist). Plain `http` is allowed only to loopback hosts (dev mockvendor). Redirects are returned, never followed.
- **Request**: body JSON-encoded with `Content-Length`; `Content-Type: application/json` unless the transformer set one; query from `VendorRequest.Query`. `traceparent`/`tracestate` from the transformer are dropped; the Hub's `traceparent` is sent only to vendors listed in `Options.ForwardTraceparent` (default none), `tracestate` never.
- **Response**: a vendor answer of any status is a response, not an error. A JSON (or untyped) body is parsed into `Body`; anything else stays a string; a body declared JSON that does not parse sets `MalformedBody`. The read is bounded at 4 MiB (`Truncated`).
- **Transport errors**: `*TransportError{Kind, Phase, CallerDeadline}`, matching `ErrTimeout`, `ErrConnect`, `ErrReset`, `ErrEgressDenied`, `ErrCanceled`, `ErrProtocol` with `errors.Is`. Classifier mapping: timeout (connect, endpoint or caller deadline) → `UPSTREAM_TIMEOUT`; connect (refused, DNS, TLS), reset, protocol, egress → `UPSTREAM_UNAVAILABLE`; canceled → the caller left. Error text never includes the URL query or header values.
- **Telemetry**: `vendor_latency_ms{vendor, account, endpoint, outcome}` (outcome = status class `2xx`… or the transport kind), `upstream_egress_denied_total{vendor}`, client span `upstream {vendor}.{endpoint}` with `hub.vendor`, `hub.account`, `hub.endpoint`, `server.address`, `http.request.method`, `http.response.status_code` (never header values or bodies).
- Not here yet: breaker, account limiter, bulkhead semaphore and `bulkhead_inflight` (later EP-04 tickets).

## Implementation notes (M3)
- `internal/edge/invoke.go`: prepare (auth → body → validate → resolve → credentials) → deliver (paused, Idempotency-Key requirement, `X-Request-Deadline`, transform, `request.transformed` audit, auth scheme, attempt + one sync retry, response map, enrichment) → 200 with `X-Hub-Served-By: primary`.
- `internal/edge/outcome.go`: transport errors → 504 `UPSTREAM_TIMEOUT` (reason `request_deadline` / `caller_canceled`) or 503 `UPSTREAM_UNAVAILABLE`; retry once after timeout/reset if the endpoint is idempotent or keyed; Retry-After and vendor message/code enrichment.
- Credential cache miss → 503 `UPSTREAM_UNAVAILABLE`, reason `credentials_unavailable`, `retry_after_ms` 5000.
- M3 delivers every mode synchronously (deviation 1 in `docs/milestones/M3.md`).
- Benchmarks: `BenchmarkInvokeOverhead` (p99 0.42–0.51 ms), e2e table `TestE2EErrorClasses`.

## Data model impact
`requests` row per request (status, `final_version`, timings) — written via the audit path (F-08).

## Config impact
Vendor endpoint `timeout_ms`, `connect_timeout_ms`, `idempotent` (DESIGN §8.3).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_requests_total{mode,code}` | labels tenant, integration, vendor, account, version |
| Metric | `hub_overhead_ms{stage}` | every stage |
| Metric | `vendor_latency_ms` | |
| Trace | spans `edge`, `transform`, `upstream` | forward `traceparent` to vendors that accept it |
| Alert | `hub_overhead_ms` p99 > 5 ms for 10 min | warn |
| Alert | any `UPSTREAM_AUTH_FAILED` / `TRANSFORM_FAILED` | page |

## Security & PII
Vendor-passed messages in `UPSTREAM_REJECTED` may contain vendor names; transformers should map messages where needed (see open questions). Egress only to declared vendor hosts; TLS 1.2+.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Vendor slow beyond deadline | Cancel | 504 `UPSTREAM_TIMEOUT` |
| Vendor connection refused | Classified as unavailable | 503 `UPSTREAM_UNAVAILABLE` |
| Malformed vendor body | Response map fails | 500 `TRANSFORM_FAILED` |

## Testing
- **Integration (e2e)**: caller → hub → mockvendor for each error class (DESIGN §19 M3 accept).
- **Contract**: every response validated against `hub-public.v1.yaml`.
- **Benchmark**: overhead ≤ 5 ms p99 with mockvendor at 0 ms.

## Rollout & migration
First caller-visible feature; CleverTap pilot integrations use it in dev.

## Open questions
- `UPSTREAM_REJECTED` "vendor message passed through" can leak vendor identity (DESIGN §0 vs §4.4). Proposed: pass through only fields mapped by the error rule.
- ~~`EXPIRED` has no HTTP status; confirm it only appears in status/callbacks.~~ Resolved by the contract: `EXPIRED` appears only in request status and `request.expired` callbacks.

## Tickets
HUB-080 – HUB-109 (EP-04, filter `feature: F-06`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | HUB-086: upstream client section | backend-engineer |
