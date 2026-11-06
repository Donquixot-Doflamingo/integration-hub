# Integration Hub: use cases

**Audience:** product owners, their seniors, and any team that wants to know "can the Hub do X for me, and how would my service call it?"
**State described:** `main` on 2026-11-06. Milestones M0–M7 are done (gateway, transformers, audit, durable delivery, resilience, versioning, control plane). M8 (observability and load tests) is in progress. Release 2 (EP-12) is planned, not built.
**Ticket:** HUB-342. Runnable example: [`examples/otp-account-flow/`](../../examples/otp-account-flow/README.md).

Every use case below has a status line:

| Status | Meaning |
| --- | --- |
| **Works today** | Built and proven by an automated test that runs in CI. The test is named, so anyone can check. |
| **Partly** | The Hub mechanism exists and is tested, but something a production rollout needs is missing. The gap has a ticket. |
| **Planned** | Designed (DESIGN.md) but not built. The ticket is named. Request shapes for planned items are not in the contract yet, so this page shows the flow, not field names. |

The HTTP examples use the real caller contract, [`contracts/openapi/hub-public.v1.yaml`](../../contracts/openapi/hub-public.v1.yaml), and real capability schemas. Callers never see a vendor name, an account name or a version, so neither do the requests and responses here. Vendor names appear only in the prose, to explain what happens behind the Hub.

## Catalogue

| # | Use case | Delivery mode | Status |
| --- | --- | --- | --- |
| 0 | [How a call flows through the Hub](#0-how-a-call-flows-through-the-hub) | all | Works today |
| 1 | [Track an event on CleverTap (sync, wait, async), one account per brand, minimal response](#1-track-an-event-on-clevertap) | sync, wait, async_callback | Works today |
| 2 | [Swap vendor or account with zero caller change](#2-swap-vendor-or-account-with-zero-caller-change) | all | Works today |
| 3 | [Vendor down: fail fast, wait, or park and drain](#3-vendor-down-fail-fast-wait-or-park-and-drain) | sync, wait, async | Works today |
| 4 | [Async with a signed callback](#4-async-with-a-signed-callback) | async_callback | Works today |
| 5 | [Idempotent retry by the caller](#5-idempotent-retry-by-the-caller) | all | Works today |
| 6 | [Two-step flows: OTP, then verify, then bank account](#6-two-step-flows-otp-then-verify-then-bank-account) | sync | **Partly** |
| 7 | [Lending catalogue patterns (DESIGN §14)](#7-lending-catalogue-patterns) | various | Partly / Planned |
| — | [Gaps found while writing this page](#gaps-found-while-writing-this-page) | | tickets HUB-343 … HUB-348 |

---

## 0. How a call flows through the Hub

**In one sentence:** your service calls one stable URL per business integration with a vendor-neutral body; the Hub picks the vendor, account and payload format, calls the vendor, and gives you back a vendor-neutral answer.

```text
POST /v1/integrations/{integration_id}/invoke              single-step capabilities
POST /v1/integrations/{integration_id}/invoke/{operation}  one operation of a multi-operation capability
GET  /v1/requests/{request_id}                             status and result of wait/async requests
```

There is deliberately **no header or field to choose a vendor, account or version** (decision D12). Changing any of those is a Hub-side publish; callers deploy nothing.

### The pipeline

```mermaid
flowchart LR
  A[Your service] -->|HTTPS + API key| B[Authenticate key,<br/>check grant,<br/>tenant rate limit]
  B --> C[Body size limit,<br/>validate against the<br/>capability schema]
  C --> D[Resolve: active version,<br/>vendor + account<br/>from the in-memory snapshot]
  D --> E[Idempotency check<br/>Redis, only with a key]
  E --> F[Transform canonical body<br/>to the vendor format,<br/>inject credentials]
  F --> G[Circuit breaker,<br/>account rate limit,<br/>bulkhead]
  G --> H[(Vendor)]
  H --> I[Map vendor response<br/>to the canonical response<br/>or a canonical error]
  I --> J[Audit, buffered<br/>and non-blocking]
  J --> A
```

Every request gets an `X-Hub-Request-Id` (`req_` + ULID). Every stage is audited (DESIGN §13.1) and times itself into `hub_stage_duration_ms`.

### Where latency is added

DESIGN §18.6 budgets the Hub's own overhead on a synchronous call (p99, 4 KB body, warm), on top of the vendor's own time:

| Stage | Budget (p99) |
| --- | --- |
| TLS + HTTP parse | 0.3 ms |
| Auth, grant, tenant limit | 0.1 ms |
| Schema validation | 0.8 ms |
| Resolve (in-memory snapshot, no database) | 0.05 ms |
| Idempotency (Redis, only if an `Idempotency-Key` is sent) | 1.0 ms |
| Transform | 0.5 ms |
| Credential injection | 0.02 ms |
| Breaker, account limiter, bulkhead | 0.1 ms |
| Response map + serialise | 0.4 ms |
| Audit + metrics | 0.1 ms |
| **Total Hub overhead** | **≈ 3.4 ms (target ≤ 5 ms)** |

Measured so far: `BenchmarkInvokeOverhead` (M3, 4 KB body, mock vendor answering in 0 ms) gave **p99 0.42–0.51 ms** ([M3 summary](../milestones/M3.md)). The full-load check (500 sync RPS + 2,000 async msg/s) is HUB-235 in M8.

The sync path never reads the database: configuration comes from an in-memory snapshot, and audit writes are buffered (`TestE2ENoDatabaseOnTheRequestPath` keeps answering at full speed with a stalled database). The one documented exception is step 2 of a vendor session (use case 6): one primary-key read, only when the request carries `X-Hub-Session`.

### Delivery modes in plain words

The mode belongs to the integration, not to the call (DESIGN §5.2).

| Mode | What the caller gets | Good for |
| --- | --- | --- |
| `sync` | The answer now (200), or a canonical error. Nothing is queued. | A person is waiting: OTP, bank check, chat |
| `wait` (default) | 200 if the vendor answers within the wait window (default 3 s), else 202 and the Hub finishes in the background | Most server-to-server calls |
| `async` | 202 at once; the Hub delivers in the background; poll for the result | Fire-and-forget events |
| `async_callback` | 202 at once; the result is POSTed, signed, to your callback URL | Events where you want to know the outcome |

A 202 always means "accepted, not done". The Go SDK returns it as a separate `Queued` type, so it cannot be mistaken for success (DESIGN §4.5).

### What the caller gets back

| HTTP | Body |
| --- | --- |
| 200 | The canonical response of the capability |
| 202 | `{"request_id", "status": "accepted" \| "queued" \| "in_flight", "reason", "poll"}` |
| 4xx / 5xx | One envelope: `{"error": {"code", "message", "retryable", "retry_after_ms", "circuit", "reason", "upstream", "details", "request_id"}}` (unused members are null or absent) |

The error codes and when they happen are in DESIGN §4.4 and `contracts/README.md`. `retryable` tells the caller whether trying again (with the same `Idempotency-Key`) can help.

---

## 1. Track an event on CleverTap

**Scenario.** The orders service records "order placed" for analytics. Brand A and brand B each have their own CleverTap account (different region, different payload shape), and one team only wants a minimal "ok" back.

**Hub features.** Capability `events.track@v1`; one integration per brand, each bound to its own account; transformers T1 (brand A) and T2 (brand B, adds brand and campaign fields) share the vendor definition; R1 is a full response, R2 a minimal one (DESIGN §3.3, §7.4).

| Integration | Account behind it (Hub-side only) | Mode | Response |
| --- | --- | --- | --- |
| `int_evt_brandA` | `ct-brandA-in` | `wait` (3 s), polling | full (R1) |
| `int_evt_brandB` | `ct-brandB-us` | `async_callback` | full (R1), via callback |
| `int_evt_brandB_min` | `ct-brandB-us` | `wait`, `on_circuit_open: queue_if_allowed` | minimal (R2) |

A pure `sync` events integration is a one-line config choice (`delivery.mode: sync`); the dev config does not have one, but the sync path is what `TestE2EErrorClasses` exercises.

### Request (same for every brand)

```http
POST /v1/integrations/int_evt_brandA/invoke
Authorization: Bearer hk_live_...
Content-Type: application/json
Idempotency-Key: order-8821-created

{
  "user": { "id": "u_48213" },
  "event": {
    "name": "order_placed",
    "timestamp": "2026-10-31T09:15:00Z",
    "properties": { "order_id": "ord_8821", "amount_minor": 129900, "currency": "INR" }
  },
  "context": { "campaign": "diwali_2026" }
}
```

### Responses

Sync, or wait within the window (full response, R1):

```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01J9Z3K8X2V7Q4M6N8P0R2S4T6
X-Hub-Served-By: primary

{ "status": "ok", "accepted": 1, "rejected": [], "vendor_ref": "a1b2c3d4e5" }
```

Minimal response (`int_evt_brandB_min`, R2): `{ "status": "ok", "vendor_ref": "a1b2c3d4e5" }`.

Async (`int_evt_brandB`), or wait when the window elapses:

```http
HTTP/1.1 202 Accepted
Location: /v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6

{ "request_id": "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6", "status": "accepted", "reason": "async_mode",
  "poll": "/v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6" }
```

Polling (`GET /v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6`) later returns `"status": "succeeded"` with the same canonical `result`.

```mermaid
sequenceDiagram
  participant S as Orders service
  participant H as Integration Hub
  participant V as CleverTap (brand A account)
  S->>H: POST /v1/integrations/int_evt_brandA/invoke (canonical event)
  H->>H: validate, resolve version, transform with T1
  H->>V: POST /1/upload (vendor format, brand A credentials)
  alt vendor answers within 3 s
    V-->>H: 200
    H-->>S: 200 {"status":"ok", ...}
  else window elapses
    H-->>S: 202 {"status":"in_flight", "poll": ...}
    V-->>H: 200 (later)
    S->>H: GET /v1/requests/{id}
    H-->>S: 200 {"status":"succeeded", "result": {...}}
  end
```

**On failure.** Vendor 5xx → 503 `UPSTREAM_UNAVAILABLE` (retryable); 429 → 429 `UPSTREAM_RATE_LIMITED` with `retry_after_ms`; bad credentials → 502 `UPSTREAM_AUTH_FAILED` (pages on-call, not your problem to retry); a 4xx about the data → 422 `UPSTREAM_REJECTED` with the vendor's message; timeout → 504 `UPSTREAM_TIMEOUT` (one immediate retry first, because the endpoint is idempotent). Async requests retry on a backoff schedule and expire after their TTL (6 h here) with an `EXPIRED` status. Circuit open: see use case 3.

**Status: Works today.** Sync path and every error class: `TestE2EErrorClasses` (`backend/internal/edge/e2e_integration_test.go`, includes the minimal-response integration). Brand A on its own account with full audit: `TestInvokeHappyPath`. Wait mode: `TestWaitModeWithinWindow`, `TestWaitModeWindowElapses`. Async + polling: `TestAsyncAcceptThenWorkerDelivers` (all in `backend/internal/edge/`). Golden fixtures for T1, T2, R1, R2: `backend/config/transformers/*/fixtures/`. Vendor delivery-status webhooks (CleverTap telling us later what happened) need inbound webhooks: **Planned, HUB-330**.

---

## 2. Swap vendor or account with zero caller change

**Scenario.** Brand A moves to a new CleverTap account, or from one engagement vendor to another, or the payload mapping changes. The orders service must not change a line or redeploy.

**Hub features.** Immutable integration versions behind a stable integration ID (DESIGN §6). The platform team publishes v8; every node swaps within 10 s. Optional gradual cutover by weights, or shadow traffic to the new target first. Rollback = publish a new version with the old content.

What the caller sends and receives is **byte-for-byte the same** before and after: same URL, same body, same response schema. What happens to each request during a cutover (DESIGN §6.3):

| Request | Served by |
| --- | --- |
| Completed before the publish | v7, and its audit keeps v7 forever |
| In flight during the publish | v7 (nothing switches mid-call) |
| Queued/parked in the outbox | v8 by default (re-resolved at send time), or v7 if the integration is `pinned` |
| Retry of a failed v7 request with the same `Idempotency-Key` | v8 (a retry is a new attempt) |
| First new request | v8 |
| Step 2 of a vendor session opened on v7 | v7 (sessions are pinned, use case 6) |

```mermaid
sequenceDiagram
  participant S as Orders service
  participant H as Integration Hub
  participant A as Old account / vendor
  participant B as New account / vendor
  S->>H: POST /v1/integrations/int_evt_brandA/invoke
  H->>A: served by v7
  H-->>S: 200 {"status":"ok", ...}
  Note over H: platform team publishes v8 (new account or vendor)
  S->>H: POST /v1/integrations/int_evt_brandA/invoke (identical)
  H->>B: served by v8
  H-->>S: 200 {"status":"ok", ...} (same schema)
```

**On failure.** A publish that would break callers is refused before it goes live: schemas must compile, expressions type-check, every transformer's golden fixtures must pass, and the response transformer's output must match the capability response schema (DESIGN §6.4). A node that cannot fetch the new snapshot keeps serving the last good one.

**Status: Works today.** All six DESIGN §6.3 rows, for an account change, a vendor change (to a second, test-only vendor) and a transformer change: `TestCutoverCompletedAndNewRequests`, `TestCutoverInFlightStaysOnV7`, `TestCutoverQueuedReResolveOrPinned`, `TestCutoverRetryWithSameKeyResolvesFresh`, `TestMixedVersionPropagation` (`backend/internal/edge/cutover_integration_test.go`) and `TestSessionStepTwoStaysOnOpeningVersion` (`cutover_session_integration_test.go`). Shadow traffic: `TestShadow`. The publish step is driven from the admin API and `hubctl apply` (M7).

---

## 3. Vendor down: fail fast, wait, or park and drain

**Scenario.** CleverTap has an outage. What does each caller experience, and does anything get lost?

**Hub features.** A circuit breaker per (account, endpoint), shared by all Hub nodes through Redis (DESIGN §10.4). It opens after > 50 % failures over 30 s (≥ 20 calls) or 5 consecutive timeouts. While open, nothing is sent to the vendor. A synthetic probe detects recovery; parked requests then drain at 20 % of the account rate, doubling every 30 s.

| Mode | While the circuit is open the caller gets |
| --- | --- |
| `sync`, `on_circuit_open: fail_fast` (default) | 503 at once, no vendor call, no queueing |
| `sync`, `failover` | Served by the next target in the failover chain (`X-Hub-Served-By: failover:1`), only for idempotent endpoints or calls with an `Idempotency-Key` |
| `sync`, `queue_if_allowed` | 202 `queued` only if the caller sent `Prefer: respond-async` or has a callback subscription; otherwise 503. Never for OTP, payments or chat |
| `wait` | 202 `{"status": "queued", "reason": "circuit_open"}` at once; delivered after recovery |
| `async` / `async_callback` | 202 as usual; the request is parked and drained after recovery |

Sync fail-fast:

```http
HTTP/1.1 503 Service Unavailable
Retry-After: 30

{ "error": { "code": "UPSTREAM_UNAVAILABLE", "message": "Vendor circuit is open", "retryable": true,
             "retry_after_ms": 30000, "circuit": "open", "reason": "circuit_open",
             "upstream": { "status": null, "code": null }, "request_id": "req_01J9ABCDEFGHJKMNPQRSTVWXYZ" } }
```

Wait mode while open:

```http
HTTP/1.1 202 Accepted

{ "request_id": "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6", "status": "queued", "reason": "circuit_open",
  "poll": "/v1/requests/req_01J9Z3K8X2V7Q4M6N8P0R2S4T6" }
```

```mermaid
sequenceDiagram
  participant S as Caller
  participant H as Integration Hub
  participant Q as Outbox (Postgres)
  participant V as Vendor
  V--xH: failures, the breaker opens
  S->>H: invoke (sync integration)
  H-->>S: 503 UPSTREAM_UNAVAILABLE, circuit open, retry_after_ms
  S->>H: invoke (wait or async integration)
  H->>Q: persist and park
  H-->>S: 202 queued / accepted
  loop every 30 s while open
    H->>V: synthetic health probe
  end
  V-->>H: probe succeeds, the breaker closes
  H->>Q: release parked requests at 20 % of the rate, doubling every 30 s
  H->>V: deliver
  S->>H: GET /v1/requests/{id}
  H-->>S: 200 {"status":"succeeded", ...}
```

**On failure.** The vendor fails again during the drain → the breaker re-opens (window doubles) and the rest stay parked; nothing is dead-lettered or lost. A parked request that outlives its TTL becomes `expired` (and `async_callback` callers get a signed `request.expired` callback). An admin can also pause one integration (kill switch): new calls get 503 `INTEGRATION_PAUSED`, requests already queued are held without being sent, and other integrations on the same account are unaffected (`TestChaos4SharedAccountAndPause`, `TestPausedIntegrationIsHeldWithoutAnAttempt`).

**Status: Works today.** `TestChaos1VendorDown` (breaker opens; sync 503 with `retry_after_ms` and no vendor call; wait 202 queued; async parks), `TestChaos2RecoveryAndRampedDrain` (drain starts within 60 s and ramps 20 → 40 → 80 → 100 %), `TestChaos3ReopenMidDrain`, `TestChaos4SharedAccountAndPause`, `TestChaos5Failover` (`backend/internal/edge/m5_integration_test.go`); expiry: `TestExpiryCallback`. The zero-loss soak test with random outages is HUB-236 (M8).

---

## 4. Async with a signed callback

**Scenario.** Brand B's events are fire-and-forget, but the orders service wants to be told when each one is delivered or finally fails, without polling.

**Hub features.** Mode `async_callback`; a registrar endpoint (the tenant's callback URL + signing secret, registered once) and subscriptions per integration version (DESIGN §12.1). Callbacks have their own retry schedule and dead-letter queue, independent of vendor delivery. The callback URL is protected against SSRF (no private ranges, no redirects, 5 s timeout).

Request: as in use case 1, to `int_evt_brandB`. Immediate answer: 202 `accepted`. Later, the Hub POSTs to the callback URL:

```http
POST /hub/callbacks
X-Hub-Signature: t=1793438102,v1=5d41402abc4b2a76b9719d911017c5925d41402abc4b2a76b9719d911017c592
X-Hub-Event: request.completed
X-Hub-Delivery-Id: dlv_01J9Z5D4E5F6G7H8J9K0M1N2PQ

{
  "event_id": "evt_01J9Z5C3D4E5F6G7H8J9K0M1NP",
  "type": "request.completed",
  "spec_version": "1",
  "occurred_at": "2026-10-31T09:15:02Z",
  "tenant_id": "orders-service",
  "integration_id": "int_evt_brandB",
  "operation": "default",
  "request_id": "req_01J9Z3K8X2V7Q4M6N8P0R2S4T6",
  "idempotency_key": "order-8821-created",
  "data": {
    "status": "succeeded",
    "result": { "status": "ok", "accepted": 1, "rejected": [], "vendor_ref": "a1b2c3d4e5" },
    "attempts": 1,
    "completed_at": "2026-10-31T09:15:02Z",
    "timings": { "total_ms": 1840, "hub_overhead_ms": 3, "vendor_ms": 212, "queue_wait_ms": 1625 }
  }
}
```

The receiver verifies `HMAC-SHA256(secret, t + "." + raw_body)` against `v1`, rejects timestamps older than 5 minutes, and answers any 2xx within 5 s. Other events: `request.failed` (dead-lettered), `request.expired` (TTL passed), `delivery.status` (non-terminal changes such as parked).

```mermaid
sequenceDiagram
  participant S as Orders service
  participant H as Integration Hub
  participant W as Hub worker
  participant V as Vendor
  S->>H: POST /v1/integrations/int_evt_brandB/invoke
  H-->>S: 202 accepted
  W->>V: deliver (retries with backoff if needed)
  V-->>W: 200
  W->>S: POST callback request.completed (X-Hub-Signature)
  S-->>W: 2xx
```

**On failure.** The callback URL is down → the Hub retries on its callback schedule and finally dead-letters the callback (the request itself stays `succeeded`). A worker crashes mid-delivery → another worker reclaims the request; the vendor may see it twice (at-least-once), the caller sees one terminal state and one callback.

**Status: Works today.** `TestAsyncCallbackEndToEnd` (202, then a signed `request.completed`; a tampered body or an old timestamp fails verification), `TestExpiryCallback`, `TestWorkerCrashDeliveredOnceToTheCaller` (`backend/internal/edge/`); `TestDispatchSignedCallbackEndToEnd`, `TestDispatchRetriesThenDeadLetters`, `TestGuardRefusesDestinations` (`backend/internal/callbacks/`).

---

## 5. Idempotent retry by the caller

**Scenario.** The caller's network drops after sending, or the user double-taps "Pay". The caller retries. The vendor must be called once, and the caller must get the original answer.

**Hub features.** `Idempotency-Key` header (DESIGN §4.2, §5.1 step 4), stored in Redis for 24 h, scoped to tenant + integration. **Mandatory** on integrations marked `moves_money_or_quota` (otherwise 400 `IDEMPOTENCY_KEY_REQUIRED`).

| Situation | Answer |
| --- | --- |
| Same key + same body, first call completed (200 or 202) | The stored answer, with `X-Hub-Idempotent-Replay: true`. No vendor call. |
| Same key + same body, first call still running synchronously | 409 `IDEMPOTENCY_CONFLICT` (retry shortly) |
| Same key + different body | 409 `IDEMPOTENCY_CONFLICT` |
| First call ended in an error | Errors are never stored: the key is released and the retry runs fresh. Use the same key for a `retryable: true` error. |

```http
POST /v1/integrations/int_evt_brandA/invoke
Idempotency-Key: order-8821-created
...same body...

HTTP/1.1 200 OK
X-Hub-Idempotent-Replay: true
X-Hub-Request-Id: req_01J9Z3K8X2V7Q4M6N8P0R2S4T6

{ "status": "ok", "accepted": 1, "rejected": [], "vendor_ref": "a1b2c3d4e5" }
```

```mermaid
sequenceDiagram
  participant S as Caller
  participant H as Integration Hub
  participant R as Redis
  participant V as Vendor
  S->>H: invoke, Idempotency-Key K
  H->>R: claim K
  H->>V: call
  V-->>H: 200
  H->>R: store the answer for K
  H--xS: 200 (lost on the network)
  S->>H: same invoke, same key K
  H->>R: K completed?
  R-->>H: stored answer
  H-->>S: 200 + X-Hub-Idempotent-Replay: true (no vendor call)
```

**On failure.** Redis unavailable: integrations that move money or quota **fail closed** (500 `INTERNAL_ERROR`, reason `idempotency_unavailable`, no vendor call), other integrations pass through without deduplication (`TestIdempotencyStoreDown`). A key reused after 24 h runs fresh.

**Status: Works today.** `TestIdempotentReplayAndConflict`, `TestIdempotencyStoreDown` (`backend/internal/edge/queued_integration_test.go`), `TestDuplicateSubmissionsOneVendorCall` (concurrent duplicates reach the vendor once, in sync, wait and async; `m4_integration_test.go`), `TestCutoverRetryWithSameKeyResolvesFresh`, and the double-tap case of `TestExampleOTPAccountFlow`.

---

## 6. Two-step flows: OTP, then verify, then bank account

This is the product owner's question: *"In a UX flow, first an OTP comes, then the user gives the account number. How does the Hub handle that today?"*

### Short answer

- **The Hub handles each call; your backend owns the journey.** The Hub is not a workflow engine (PRD non-goal: "not a multi-step workflow/orchestration engine across vendors"). Your onboarding backend knows which screen the user is on and decides what to call next. The Hub makes each call vendor-neutral, safe to retry, audited, and resilient.
- **Where the vendor needs continuity between two calls** (an OTP is generated and must be verified against the same vendor transaction), the Hub keeps a **vendor session**: step 1 returns an `X-Hub-Session` header; your backend sends it back on step 2. The Hub stores only the vendor's transaction id, for 10 minutes, pinned to the exact account and version that sent the OTP. That works today.
- **The bank account check is a separate integration** (a sync lookup, often a different vendor). It needs no session. Your backend calls it after it has seen "OTP verified".
- **What is not possible yet:** one integration whose steps go to *different vendor endpoints* (e.g. `send_otp` and `verify_otp` as two operations). Today an integration version has one request transformer, so one vendor endpoint, for all operations (HUB-343). The runnable example works around this with a demo vendor that has one OTP endpoint for both steps. The production Aadhaar OTP capability, with a licensed KUA/ASA, is Release 2 (HUB-334).

### The UX journey, step by step

| UX screen | Your backend does | The Hub does | Your backend gets |
| --- | --- | --- | --- |
| "Enter mobile number" → **Send OTP** | `POST /v1/integrations/int_demo_otp/invoke` with `{"mobile"}` and an `Idempotency-Key` | Validates, resolves the active version (say v1, account A), asks the vendor to send the OTP, keeps the vendor txn id in a new session (10 min) | 200 `{"status": "otp_sent", "expires_in_s": 300}` + header `X-Hub-Session: ses_…`. Store it with the user's onboarding attempt. |
| "Enter the OTP" → **Verify** | `POST /v1/integrations/int_demo_otp/invoke/default` with `X-Hub-Session` and `{"mobile", "otp"}` | Loads the session, sends the call to **the same account and version** with the stored txn id, even if the platform team published v2 meanwhile | 200 `{"status": "verified"}`, or 422 `UPSTREAM_REJECTED` "The OTP does not match" |
| "Enter bank account" → **Continue** | Checks its own state ("OTP verified"), then `POST /v1/integrations/int_demo_bank_verify/invoke` with `{"account_number", "ifsc", "name"}` | A plain sync lookup (penny-drop style) | 200 `{"account_status": "valid", "name_at_bank", "name_match"}` |

```mermaid
sequenceDiagram
  actor U as User (app)
  participant B as Onboarding backend (tenant)
  participant H as Integration Hub
  participant DB as Hub session store
  participant V as KYC vendor (account A)
  U->>B: mobile number, tap Send OTP
  B->>H: POST /v1/integrations/int_demo_otp/invoke {"mobile"} + Idempotency-Key
  H->>V: send OTP (vendor format, account A credentials)
  V-->>H: 200 OTP_SENT, txn_id
  H->>DB: session = {vendor_txn_id}, version 1, account A, expires in 10 min
  H-->>B: 200 {"status":"otp_sent"} + X-Hub-Session
  B-->>U: show OTP screen
  Note over H: platform team may publish v2 (account B) here, the session stays on v1 + account A
  U->>B: types OTP
  B->>H: POST .../invoke/default + X-Hub-Session {"mobile","otp"}
  H->>DB: load session (one primary-key read)
  H->>V: verify OTP with txn_id (account A)
  V-->>H: 200 VERIFIED
  H-->>B: 200 {"status":"verified"}
  B-->>U: show bank account screen
  U->>B: account number + IFSC
  B->>H: POST /v1/integrations/int_demo_bank_verify/invoke + Idempotency-Key
  H->>V: verify bank account
  V-->>H: 200 VALID, name at bank
  H-->>B: 200 {"account_status":"valid","name_match":true}
```

### The exact calls (from the runnable example)

Captured from `TestExampleOTPAccountFlow` (request ids and session ids differ per run). The API key is the example's local test key.

Step 1, send the OTP:

```http
POST /v1/integrations/int_demo_otp/invoke
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: otp-send-app-77

{"mobile": "+919812345678"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJ7KK3N8BBET4GQDSFD
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED
X-Hub-Served-By: primary

{"expires_in_s":300,"status":"otp_sent","vendor_ref":"dk_ref_0001"}
```

Step 2, verify the OTP inside the session:

```http
POST /v1/integrations/int_demo_otp/invoke/default
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: otp-verify-app-77-2
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED

{"mobile": "+919812345678", "otp": "482913"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJP9NX9AC2VV8C1EBJZ
X-Hub-Served-By: primary

{"status":"verified","vendor_ref":"dk_ref_0002"}
```

Step 3, verify the bank account (separate integration, no session):

```http
POST /v1/integrations/int_demo_bank_verify/invoke
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: bank-verify-app-77

{"account_number": "50100123456789", "ifsc": "DEMO0001234", "name": "Asha Verma"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJX3A6VSHD1T6CWSNZR
X-Hub-Served-By: primary

{"account_status":"valid","name_at_bank":"ASHA VERMA","name_match":true,"vendor_ref":"dk_ref_0003"}
```

### What happens when things go wrong

| What happens | What your backend gets | What to do in the UX |
| --- | --- | --- |
| User types a wrong OTP | 422 `{"error": {"code": "UPSTREAM_REJECTED", "message": "The OTP does not match", "retryable": false, "upstream": {"status": 422, "code": null}, ...}}`. The session stays open. | Show "wrong OTP"; let the user try again **with the same `X-Hub-Session`** and a new `Idempotency-Key`. The vendor counts attempts; when it refuses more, that comes back as another rejection or a 429. |
| The OTP expired at the vendor (e.g. after its 5 minutes) | 422 `UPSTREAM_REJECTED`, "The OTP has expired; request a new one" | Offer **Resend OTP** = call step 1 again. It opens a new session; the old one simply expires. |
| The Hub session expired (more than 10 minutes since step 1), or the session id is wrong, or belongs to another tenant or integration | 404 `{"error": {"code": "SESSION_NOT_FOUND", "message": "Unknown or expired session", "retryable": false, ...}}`. Nothing is sent to the vendor. | Restart from step 1. |
| User double-taps **Send OTP** | Both taps get the same 200 and the same `X-Hub-Session`; the second has `X-Hub-Idempotent-Replay: true`. One SMS. | Nothing; send the same `Idempotency-Key` for the same attempt. |
| Vendor down or 5xx during verify | 503 `UPSTREAM_UNAVAILABLE`, `retryable: true`. The session survives. | Show "try again"; retry with the same session **and the same `Idempotency-Key`**. |
| Vendor circuit open | 503 `UPSTREAM_UNAVAILABLE` with `circuit: "open"` and `retry_after_ms`, at once. OTP integrations are `sync` + `fail_fast`: a person is waiting, so the Hub never queues an OTP (DESIGN §5.3, §5.4). | Show "service busy, retry in N seconds". |
| Vendor rate limit (OTP quota) | 429 `UPSTREAM_RATE_LIMITED` with `retry_after_ms` | Show the wait time. |
| Platform team switches account or vendor between step 1 and step 2 | Nothing visible: step 2 goes to the account and version that sent the OTP. Users who start after the publish use the new one. | Nothing. |
| Missing `Idempotency-Key` (OTP and penny drop move quota or money) | 400 `IDEMPOTENCY_KEY_REQUIRED`, no vendor call | Fix the caller. |
| Malformed input (bad IFSC, bad mobile) | 400 `VALIDATION_FAILED` with a JSON pointer, e.g. `{"pointer": "/ifsc", "keyword": "pattern", ...}`; no vendor call | Show a field error. |
| Bank account does not exist | 200 `{"account_status": "invalid", ...}`: a business answer, not an error | Ask the user to check the number. |

Today the app can tell "wrong OTP" from "expired OTP" only by the `message` text, because a transformer cannot set the machine-readable `reason` yet (HUB-346). Do not branch on `upstream.code`: the contract says it is opaque.

### How sessions work inside the Hub

- **Opened by step 1** when the response transformer has a `session:` section and the vendor answered successfully. Any call without `X-Hub-Session` is a step 1. Later steps never open another session.
- **Contents:** only what the transformer maps, normally the vendor transaction id (`session.vendor_txn_id`). Never credentials (a transformer that tries fails to compile), never more than 16 KB.
- **Pinned** to the tenant, the integration, the integration version and the account that served step 1 (DESIGN §6.3 row 6). A step 2 to another integration, or from another tenant, gets `SESSION_NOT_FOUND`.
- **Lifetime:** 10 minutes (DESIGN §14; fixed today, not configurable per integration). The scheduler deletes expired sessions. An account cannot be retired while sessions are open on it (DESIGN §6.5).
- **Cost:** one Postgres primary-key read, only on requests that carry `X-Hub-Session` (the documented exception to "no database on the sync path"; timed in the `resolve` stage).
- **Dry-run** with `X-Hub-Session` shows the request the pinned account would get. Shadow traffic never copies session steps.

### Why the Hub never stores the Aadhaar number

For Aadhaar OTP eKYC, step 1 carries the Aadhaar number in the canonical body. The Hub forwards it to the licensed KUA/ASA and keeps only the vendor transaction id:

1. **Session store refuses it, twice.** `sessions.CheckData` rejects any session data that contains a 12-digit Aadhaar-like number (with or without spaces or hyphens), in keys or values; the `vendor_sessions` table has the same check as a database constraint. A transformer that tries anyway fails the call with `TRANSFORM_FAILED` and opens no session.
2. **Logs never contain bodies** (redacting logger; codes and counts only).
3. **Audit trail:** bureau, Aadhaar and PAN integrations use the `sealed` payload policy (decision D6): payloads are stored encrypted under a per-tenant key and are readable only through an audited "unseal" admin action (works today). The 90-day retention and KMS-managed keys are HUB-337. The capability schema tags the field `x-pii: government_id`. **Open question for compliance:** UIDAI rules restrict storing Aadhaar numbers outside an Aadhaar Data Vault. If even encrypted audit copies are not acceptable, the Aadhaar integration should use the `metadata_only` policy (hashes and sizes only, no bodies). Decide this before HUB-334 goes live.

### Status: Partly

| Piece | Status | Proof or ticket |
| --- | --- | --- |
| Vendor session: open on step 1, `X-Hub-Session`, pinned to version + account, txn id injected into step 2, 10-min expiry → `SESSION_NOT_FOUND`, survives a publish | **Works today** | `TestExampleOTPAccountFlow` (`backend/internal/edge/example_otp_flow_integration_test.go`), `TestSessionStepTwoStaysOnOpeningVersion`, `TestSessionNotFound`, `TestSessionDryRunUsesTheSession`, `TestQueuedSessionStepIsPinned`, `TestSchedulerSweepsExpiredSessions`, `TestRetireGuardCountsPinnedWorkAndSessions` |
| Aadhaar-like numbers never stored in a session | **Works today** | `TestSessionRefusesAadhaarLikeData`, `TestCheckDataRefusesAadhaar`, `TestSchemaHasNoRawIdentifierColumn` |
| Sealed audit (encrypted payloads, audited unseal) | **Works today**, production key management planned | `TestSealedPolicyIsEncryptedAtTheEdge`; KMS-backed keys: HUB-337 |
| Wrong OTP / expired OTP / vendor down / double tap / edge refusals | **Works today** | `TestExampleOTPAccountFlow` subtests |
| Bank account verification as a sync lookup | **Works today** (pattern) | `TestExampleOTPAccountFlow`, demo vendor only |
| Steps on **different vendor endpoints** (`send_otp`, `verify_otp` as operations of one integration) | **Not possible today** | HUB-343 (backend) after HUB-344 (admin contract). Until then the registry should refuse such a config instead of misrouting it: HUB-345 |
| Machine-readable reason for wrong vs expired OTP | **Partly** (message text only) | HUB-346 |
| Production Aadhaar OTP capability (`kyc.aadhaar_otp@v1`) with a licensed KUA/ASA, UIDAI OTP limits as account quota | **Planned** | HUB-334; the capability schema goes through the contract-steward |

---

## 7. Lending catalogue patterns

DESIGN §14 groups lending integrations into seven patterns. No production lending vendor is configured today; the table says what the Hub can already do for each pattern and what is missing. For **planned** patterns the request bodies are not in the contract yet (each needs a capability schema from the contract-steward), so the flows below name endpoints, not fields.

| Pattern | Examples | Status | What exists / what is missing |
| --- | --- | --- | --- |
| Sync lookup | Bureau pull, PAN verify, PAN–Aadhaar link, GST/Udyam/MCA, penny drop, CKYC search | **Partly** | Works: sync mode, transformers with goldens, mandatory `Idempotency-Key` for money/quota, sealed audit (example: `int_demo_bank_verify`). Missing: capability schemas (CCR each), auth schemes many of these vendors use (OAuth2, HMAC signing, mTLS: HUB-332), the optional 24 h result cache (HUB-347), KMS-managed seal keys (HUB-337) |
| Two-step with vendor txn id | Aadhaar OTP eKYC | **Partly** | Use case 6. HUB-343, HUB-344, HUB-334 |
| Consent + async fetch | Account Aggregator | **Planned** | HUB-336 (long-running requests, consent store), HUB-330 (inbound notifications) |
| Redirect + callback | DigiLocker, eSign, eNACH, reverse penny drop | **Planned** | HUB-335; the return endpoint is reserved in the contract (`GET /handoff/return/{session_id}`, `x-status: planned`, answers 404 today) |
| Upload + job | Bank statement analysis, bulk CKYC | **Planned** | HUB-348 (needs object storage for large payloads, HUB-098) |
| Fire and forget | CleverTap, MoEngage, SMS, WhatsApp | **Works today** (delivery-status webhooks planned) | Use cases 1, 3, 4. Vendor delivery receipts: HUB-330 |
| Stream | LLM chat | **Planned** | HUB-333 (`llm.chat@v1`, SSE relay, failover before the first byte only, token accounting) |

### 7.1 Sync lookup (bureau pull, PAN, penny drop)

**Intended caller flow.** One sync call with an `Idempotency-Key` (a bureau pull costs money and may leave a footprint on the credit file), the answer in the same response. Same shape as step 3 of use case 6.

```mermaid
sequenceDiagram
  participant B as Lending backend
  participant H as Integration Hub
  participant V as Bureau / PAN / bank verification vendor
  B->>H: POST /v1/integrations/{id}/invoke + Idempotency-Key
  H->>V: vendor request (sealed audit)
  V-->>H: report
  H-->>B: 200 canonical result
```

**On failure.** Vendor down → 503 at once (`fail_fast`) or the next vendor (`failover`, safe because the call carries a key). A retry with the same key after success replays the stored answer: the bureau is not hit twice.
**Status: Partly**, see the table above.

### 7.2 Consent + async fetch (Account Aggregator)

**Intended caller flow** (DESIGN §14, HUB-336). One long-running request covers consent request → user approves in the AA app → consent notification → FI request → FI-ready notification → FI fetch. The caller starts it once, gets a 202, and receives the canonical financial data in one `request.completed` callback, days later if needed (TTL in days).

```mermaid
sequenceDiagram
  participant B as Lending backend
  participant H as Integration Hub
  participant AA as Account Aggregator
  actor U as User (AA app)
  B->>H: invoke (consent request), async_callback
  H-->>B: 202 accepted
  H->>AA: create consent
  U->>AA: approves consent
  AA->>H: consent notification (inbound webhook)
  H->>AA: FI request
  AA->>H: FI-ready notification
  H->>AA: fetch FI data
  H->>B: callback request.completed (canonical data, signed)
```

**Status: Planned**, HUB-336 and HUB-330.

### 7.3 Redirect + callback (DigiLocker, eSign, eNACH)

**Intended caller flow** (DESIGN §4.1, §14, HUB-335). The caller invokes; the Hub returns a hand-off URL; the app opens it; the user finishes on the vendor's page; the vendor redirects the browser to the Hub's return URL (`GET /handoff/return/{session_id}`); the Hub records the result and redirects the browser to the tenant's return URL with `hub_request_id` and `hub_status`; the final status also arrives as a callback.

```mermaid
sequenceDiagram
  actor U as User (browser)
  participant B as Lending backend
  participant H as Integration Hub
  participant V as DigiLocker / eSign / eNACH
  B->>H: invoke (start hand-off)
  H-->>B: hand-off URL
  B-->>U: open hand-off URL
  U->>V: authenticate / sign / mandate
  V-->>U: redirect to the Hub return URL
  U->>H: GET /handoff/return/{session_id}
  H-->>U: 302 to the tenant return URL (hub_request_id, hub_status)
  H->>B: callback with the final status
```

**Status: Planned**, HUB-335.

### 7.4 Upload + job (bank statement analysis)

**Intended caller flow** (DESIGN §14, HUB-348). The caller submits the statement (large payload kept in object storage, the ledger keeps a pointer); the vendor processes it as a job; the Hub polls the vendor or takes its webhook and returns the canonical analysis by polling (`GET /v1/requests/{id}`) or callback.

```mermaid
sequenceDiagram
  participant B as Lending backend
  participant H as Integration Hub
  participant V as Statement analysis vendor
  B->>H: invoke (upload statement), async_callback
  H-->>B: 202 accepted
  H->>V: upload, create job
  loop until done
    H->>V: poll job (or receive the vendor webhook)
  end
  H->>B: callback request.completed (canonical analysis)
```

**Status: Planned**, HUB-348.

### 7.5 LLM streaming

**Intended caller flow** (DESIGN §15, HUB-333). A sync streaming call: the Hub relays the vendor's chunks as canonical chunks without buffering. Failover to another model or provider happens only before the first byte; a failure mid-stream ends with a terminal error event. Model choice is Hub config, so a model swap is a cutover (use case 2).

```mermaid
sequenceDiagram
  participant B as Support service
  participant H as Integration Hub
  participant L as LLM provider
  B->>H: invoke llm.chat (stream)
  H->>L: vendor request
  L-->>H: chunk 1..n
  H-->>B: canonical chunks (SSE)
  H-->>B: final chunk with usage
```

**Status: Planned**, HUB-333.

---

## Gaps found while writing this page

| Ticket | Gap | Why it matters |
| --- | --- | --- |
| HUB-343 | An integration version has one transformer pair for every operation, so multi-operation capabilities with different vendor endpoints (`generate` / `verify`) cannot be configured | Blocks the clean OTP shape and Aadhaar OTP (HUB-334) |
| HUB-344 | Admin contract change (contract-steward) for the per-operation transformer map | HUB-343 needs it first |
| HUB-345 | Today such a config is *accepted* and the second operation silently runs the first operation's transformers | A misconfiguration should fail at publish, not at request time |
| HUB-346 | Error rules cannot set a machine-readable `reason` (e.g. wrong vs expired OTP) | UX branching on free text is fragile |
| HUB-347 | The 24 h result cache for sync lookups (DESIGN §14) has no implementation and had no ticket | Cost of repeated PAN/bureau lookups |
| HUB-348 | The upload + job pattern (bank statements) had no Release 2 item | Lending catalogue coverage |

Capability schemas are read only from `contracts/` by design (ADR-0004: they are the caller-facing contract). The runnable example therefore keeps its schemas in an example overlay that the test merges in memory; a production capability always goes through the contract-steward.
