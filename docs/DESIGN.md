# Integration Hub — Claude Code Kickoff

> **Capabilities were removed on 2026-11-07 ([ADR-0023](tech/adr/0023-remove-capabilities-integration-owns-its-request-schema.md)).** Where this document talks about capabilities, the integration's per-operation request schema and the vendor endpoint's request schema now apply. EP-14 updates the code and docs.

> Paste this whole file as the first prompt in Claude Code (or save it in the repo and say "read KICKOFF.md and start").
> It contains the working instructions, the complete design, and a milestone build plan.

---

## 0. Instructions for Claude Code

**You are the lead engineer building the Integration Hub described below.** The design has been discussed and agreed with me; your job is to implement it, milestone by milestone, with tests.

### Language rule
Always respond in **English**, even when I write in Hindi or Hinglish.

### First actions (in this order)
1. Read this entire file before writing any code.
2. Save it into the repo unchanged as `docs/DESIGN.md`.
3. Create `CLAUDE.md` at the repo root containing: the language rule, the working rules below, and the decisions table from §2 (short form). Keep it under 150 lines.
4. Ask me **one message** with these three confirmations, each with the default stated, then proceed with defaults if I just say "go":
   - Implementation language: **Go** (default) or Java.
   - Repo: new repo (default) or an existing one (give path / module name). Default Go module: `github.com/<org>/integration-hub` (ask for `<org>`).
   - Local Docker available for Postgres + Redis (default: yes).
5. Start **Milestone M0** (§19). Work through milestones in order.

### Working rules
- **Callers never see vendor names, account names or versions.** The caller contract is: integration ID + canonical body → canonical response. If a change would leak vendor identity into the caller contract, stop and tell me.
- **Hot path discipline** (§18.5): no DB or registry reads on the sync path; only the in-memory snapshot. Every middleware publishes its own duration metric.
- **Never call real vendors in tests.** Use the programmable mock vendor (`cmd/mockvendor`, built in M0). Real vendor payload shapes in this doc are *illustrative*: before writing a production transformer for a vendor, verify field names, endpoints and auth headers against that vendor's current API docs and note the doc URL in the transformer file.
- **Every transformer ships with golden fixtures** (canonical input → expected vendor request; vendor response → expected canonical response). Publishing a transformer without passing goldens must be impossible.
- **Never log secrets.** Credentials only enter via `creds.*` in header/query/auth targets; the logger redacts by value.
- Table-driven tests; `go test -race` in CI; integration tests use real Postgres/Redis via docker-compose (or testcontainers).
- At the end of each milestone: all tests green, a short summary of what was built and what was deferred, then commit with a descriptive message and continue to the next milestone unless I said to stop.
- When something in this design is ambiguous or wrong in practice, pick the simplest option consistent with §2, implement it, and list it under "Deviations" in your milestone summary.

---

## 1. Product summary

The Integration Hub is one vendor-agnostic API through which every internal service talks to every third party: CleverTap and other engagement tools, SMS/WhatsApp, credit bureaus, UIDAI/PAN/KYC providers, Account Aggregators, payment and mandate rails, and LLM APIs.

Callers send a **stable integration ID** plus a **canonical request body**. The Hub resolves which **vendor**, which **account** (credentials) and which **transformer** to use, enforces delivery policy, rate limits and circuit breakers, transforms the request into the vendor's shape, calls the vendor (now, or later via a durable queue), transforms the response back, and records a **lossless audit trail** of every step.

**Why:** today each team integrates vendors directly: N copies of keys, N half-built retry loops, and a vendor change touches every caller. With the Hub, swapping CleverTap for another vendor, or OpenAI account A for account B, or OpenAI for Anthropic, is a Hub-side config publish. Callers change nothing and deploy nothing.

**What it is:** a thin, config-driven proxy with a schema registry, an expression-based transformation layer, versioned integrations, durable async delivery with retries and automatic recovery after vendor outages, webhooks in both directions, a full audit trail and a control plane.

**What it is not (v1):** not a multi-step workflow/orchestration engine across vendors, not ETL/data sync, not a gateway for our own public APIs, not a self-serve builder for business users.

### Success metrics
| Metric | Target |
| --- | --- |
| Hub-added latency, sync path, p99 (vendor time excluded) | ≤ 5 ms |
| Async accept latency, p99 (202 returned) | ≤ 10 ms |
| Accepted async requests lost | 0 |
| Vendor or account swap, config only, incl. canary | ≤ 1 working day |
| New vendor for an existing capability | ≤ 2 engineer-days (transformer + goldens) |
| Gateway availability | 99.9 % phase 1, 99.95 % from phase 2 |
| Config propagation to all nodes | ≤ 10 s |
| Parked backlog starts draining after vendor recovery | ≤ 60 s |

---

## 2. Decisions (proposed defaults; proceed with these unless I override)

| # | Decision | Default |
| --- | --- | --- |
| D1 | Core entity name | **Integration** |
| D2 | Where the ID travels | Path: `POST /v1/integrations/{id}/invoke` |
| D3 | Default delivery mode for new integrations | **wait**, 3 s window |
| D4 | Sync behaviour when the circuit is open | `fail_fast` (503 + `retry_after_ms`) |
| D5 | Breaker scope | `(account, endpoint)`, shared across nodes; per-integration pause and threshold overrides on top |
| D6 | Audit payload policy for bureau / Aadhaar / PAN | `sealed`, 90 days, unseal is an audited action |
| D7 | Who authors transformers | Platform team, plus PRs from other teams gated by golden tests |
| D8 | Language + expression engine | **Go + `expr-lang/expr`** (CEL if mappings must also run in Java) |
| D9 | Phase-1 queue | Postgres outbox (`SKIP LOCKED`) behind a `Queue` interface; NATS JetStream or RabbitMQ later |
| D10 | Pending requests at a cutover | **Re-resolve at send time**; `pinned` is opt-in per integration |
| D11 | Breaking change to a canonical schema | **New integration ID**; the old one keeps working ≥ 6 months |
| D12 | Callers selecting versions | **Never possible.** Versions are Hub-only. |

---

## 3. Domain model

### 3.1 Entities
| Entity | What it is | Example |
| --- | --- | --- |
| **Tenant** | An internal service/team that calls the Hub. Owns API keys, scopes, quotas, callback endpoints. | `orders-service` |
| **Capability** | A vendor-agnostic operation with a versioned canonical request/response JSON Schema. Can have multiple operations (multi-step). | `events.track@v1`, `llm.chat@v1`, `kyc.aadhaar_otp@v1` (ops `generate`, `verify`) |
| **Vendor** | Vendor definition: base URLs per region, endpoints, auth *scheme shape*, default timeouts, limits, breaker defaults, error classification hints. Same for every account of that vendor. | `clevertap`, `openai`, `anthropic` |
| **Account** | One credentialed instance of a vendor: encrypted secrets (versioned `active`/`next`), non-secret config (project id, region, brand code), quotas, lifecycle state. | `ct-brandA-in`, `ct-brandB-us`, `openai-acct-A` |
| **Transformer** | An immutable, versioned mapping. Request transformer: canonical → vendor request. Response transformer: vendor response → canonical response (full or minimal). Includes error mapping. Reusable across integrations. | `T1@7`, `T2@3`, `R1@2`, `R2@1` |
| **Integration** | **The core entity.** A stable ID owned by a tenant, bound to one capability. Its content lives in immutable **versions**; one version is active. A version = targets (vendor + account + transformers, with weights) + failover chain + delivery policy + breaker overrides + webhook subscriptions + audit policy. | `int_evt_brandA`, `int_chat_support` |
| **Registrar endpoint** | A tenant-level webhook destination (URL + signing secret), registered once and reused. | `reg_orders_cb` |
| **Webhook subscription** | Part of an integration version: which event types go to which registrar endpoint. | `delivery.status → reg_orders_cb` |
| **Vendor session** | Hub-side state for multi-step vendor flows (vendor txn id, pinned account/version, TTL). | Aadhaar OTP generate → verify |
| **Request** | One invocation: request id, status, attempts, audit events. | `req_01J9…` |

### 3.2 Where each thing lives
| Thing | Lives on | Why |
| --- | --- | --- |
| Endpoints, base URL per region, auth shape, default limits | Vendor | Same for every account |
| Secrets + non-secret config (project id, region, brand code) | Account | Stored once, rotated once, shared by every integration using the account |
| Request/response transformers | Transformer (versioned), referenced by integration version | Two accounts may need different payloads; two integrations may share one |
| Delivery mode, wait window, TTL, retry schedule, on_circuit_open | Integration version | This is the contract the caller sees |
| Circuit breaker state | Account + endpoint (shared) | An outage belongs to a credential set and host, not to one caller |
| Breaker overrides, pause/kill switch | Integration | Per-caller sensitivity on top of the shared breaker |
| Rate limits | Account (vendor quota) and Tenant (fairness) | Two different budgets |
| Webhook endpoints (URL + secret) | Tenant Registrar | No duplicated URLs/secrets |
| Webhook subscriptions | Integration version | Same vendor + account can need different webhooks per transformer |
| Audit trail | Request | Lossless, per call |

### 3.3 Same vendor, different data per account
- **Only values differ** → one transformer; values come from `account.config.*` (project id, custom field names, region, brand code). Brand A and B share `T1`.
- **Structure differs** → a second transformer (brand B must send `brand` and `campaign` inside the event → `T2`).
- Rule: prefer config over a fork; fork only when the payload *shape* changes.

### 3.4 Integration version record (example)
```yaml
integration: int_evt_brandB_min
owner_tenant: orders-service
grants: []                       # other tenants allowed to invoke
capability: events.track@v1
version: 3                       # immutable once published
targets:
  - vendor: clevertap
    account: ct-brandB-us        # credentials live on the account
    transformer: { request: T2@3, response: R2@1 }
    weight: 100
failover:
  - vendor: moengage
    account: mo-brandB
    transformer: { request: T4@1, response: R2@1 }
delivery:
  mode: wait                     # sync | wait | async | async_callback
  wait_ms: 3000
  ttl: 6h
  on_circuit_open: queue_if_allowed   # fail_fast | failover | queue_if_allowed
  pending: re_resolve            # re_resolve | pinned
breaker:
  override: { open_after_consecutive_timeouts: 3 }
webhooks:
  - event: delivery.status
    endpoint: reg_orders_cb
audit:
  payloads: masked               # full | masked | sealed | metadata_only
  retention: 30d
publish:
  author: platform-team
  reason: "brand B needs campaign field"
```

---

## 4. Caller API contract

### 4.1 Endpoints
| Endpoint | Purpose |
| --- | --- |
| `POST /v1/integrations/{id}/invoke` | Invoke a single-operation capability |
| `POST /v1/integrations/{id}/invoke/{op}` | Invoke one operation of a multi-operation capability (e.g. `generate`, `verify`) |
| `GET /v1/requests/{request_id}` | Status and final canonical result (for wait/async) |
| `POST /v1/integrations/{id}/dry-run` | Return the vendor request the Hub would send, without sending it |
| `POST /webhooks/in/{vendor}/{account}` | Inbound vendor webhooks |
| `GET /handoff/return/{session_id}` | Return URL for redirect flows (DigiLocker, eSign, eNACH) |
| `/admin/v1/*` | Control plane (§16) |

### 4.2 Request headers
| Header | Required | Meaning |
| --- | --- | --- |
| `Authorization: Bearer <tenant key>` | yes (or mTLS identity) | Identifies the tenant |
| `Idempotency-Key` | recommended; **mandatory** where money or vendor quota moves | Same key within 24 h returns the stored result |
| `Prefer: respond-async` | no | Caller accepts a 202 if the vendor circuit is open (used by `queue_if_allowed`) |
| `X-Request-Deadline` | no | Caps the sync timeout (ms) |
| `X-Hub-Session` | for step ≥ 2 of multi-step ops | Hub session id returned by step 1 |
| `traceparent` | no | Continues the caller's trace |

There is deliberately **no** header to choose a vendor, account or version.

### 4.3 Responses
- **200**: canonical response body + `X-Hub-Request-Id` (+ debug-only `X-Hub-Integration-Version`, which SDKs must not act on).
- **202**: `{"request_id":"req_…","status":"accepted|queued|in_flight","reason":"…","poll":"/v1/requests/req_…"}`.
- **Errors**: one envelope, always with `request_id`:
```json
{ "error": { "code": "UPSTREAM_UNAVAILABLE", "message": "Vendor circuit is open",
             "retryable": true, "retry_after_ms": 30000, "circuit": "open",
             "upstream": { "status": null, "code": null },
             "request_id": "req_01J9ABCD" } }
```

### 4.4 Canonical error codes
| Code | HTTP | Retryable | When |
| --- | --- | --- | --- |
| `VALIDATION_FAILED` | 400 | no | Body fails capability schema; details list JSON pointers |
| `UNAUTHORIZED` / `FORBIDDEN` | 401 / 403 | no | Bad key / tenant not owner or grantee of the integration |
| `INTEGRATION_NOT_FOUND` | 404 | no | Unknown integration ID |
| `INTEGRATION_PAUSED` | 503 | yes | Admin kill switch on this integration |
| `SESSION_NOT_FOUND` | 404 | no | Unknown or expired `X-Hub-Session` |
| `IDEMPOTENT_REPLAY` | 200/202 | – | Stored result returned; header `X-Hub-Idempotent-Replay: true` |
| `TRANSFORM_FAILED` | 500 | no | Transformer threw; always a Hub bug; alerts platform |
| `UPSTREAM_TIMEOUT` | 504 | yes | Vendor exceeded endpoint timeout |
| `UPSTREAM_UNAVAILABLE` | 503 | yes | Circuit open or vendor 5xx; carries `retry_after_ms` |
| `UPSTREAM_RATE_LIMITED` | 429 | yes | Vendor 429, account quota or tenant budget exhausted |
| `UPSTREAM_REJECTED` | 422 | no | Vendor 4xx (not auth/rate); vendor message passed through |
| `UPSTREAM_AUTH_FAILED` | 502 | no | Vendor rejected our credentials; pages on-call |
| `EXPIRED` | – | no | Async request passed its TTL before delivery (status/callback only) |

### 4.5 SDK rule
The Go client SDK (`pkg/hubclient`) returns a 202 as a distinct `Queued` result type, never as success, so callers cannot accidentally treat "accepted" as "done".

---

## 5. Request pipeline and delivery modes

### 5.1 Common steps (all modes, synchronous at the edge)
1. **Receive**: authenticate tenant key (in-memory hash set), check tenant owns/is granted the integration, tenant rate limit, body size limit, mint request id. Audit `request.received`.
2. **Validate** against the capability schema (pre-compiled). Fail → 400 now, never later in a queue. Audit `request.validated`.
3. **Resolve** the integration's active version from the snapshot → target (weighted choice hashed on idempotency key or request id), failover chain, policies. Audit `integration.resolved` (records version + snapshot version).
4. **Idempotency** check (Redis) if a key was sent.
5. **Transform** with the request transformer; inject credentials from the decrypted account cache. Audit `request.transformed` (secrets redacted).

### 5.2 Modes
| Mode | Vendor healthy | Circuit open | Caller must have |
| --- | --- | --- | --- |
| **sync** | Call vendor now → 200 with canonical result | Decided by `on_circuit_open` (§5.3) | Nothing |
| **wait** (default) | Persist envelope to outbox first, then attempt immediately in the gateway. 200 if done within `wait_ms`; else 202 `in_flight`, worker finishes | 202 at once, `status: queued, reason: circuit_open`; parked; drains on recovery | Polling or a callback subscription |
| **async** | Persist → 202 → worker delivers | 202 → parked → drained after recovery | Nothing (fire-and-forget) or polling |
| **async_callback** | Persist → 202 → worker → signed POST of result to subscribed endpoint | 202 → callback arrives after the drain | Registrar endpoint + subscription |

Onboarding an integration with `wait`, `async` or `async_callback` requires a declared polling path or callback subscription.

### 5.3 `on_circuit_open` (sync mode)
- **`fail_fast`** (default): 503 `UPSTREAM_UNAVAILABLE` with `retry_after_ms` from the breaker's open window, `circuit: "open"`. Nothing is sent or queued; the caller's picture is exact.
- **`failover`**: resolve the next failover target and repeat from Transform. Only for idempotent endpoints or requests carrying an `Idempotency-Key`. Chain exhausted → `fail_fast`. Response header `X-Hub-Served-By: failover:1`.
- **`queue_if_allowed`**: downgrade to async **only** if the caller sent `Prefer: respond-async` or the integration has a callback subscription; otherwise `fail_fast`. Fine for analytics events; never for OTP, payments, chat.

### 5.4 Interactive capabilities
Capabilities flagged `interactive: true` (e.g. `llm.chat@v1`, OTP flows) may only be used in `sync` mode; the publish pipeline rejects other modes. **A 202 is never a valid answer to a person waiting.** For LLM streams, failover happens only before the first byte; after it, a terminal error event.

---

## 6. Cutover and versioning

### 6.1 Principle
**The integration ID never changes. You publish a new version behind it.** Account swap (OpenAI acct A → B), vendor swap (OpenAI → Anthropic), transformer change (T9@2 → T9@3): all are a new integration version. Callers change nothing and deploy nothing.

### 6.2 Version axes
| Axis | Example | Who sees it |
| --- | --- | --- |
| Integration version | v7 → v8 | Hub only. The cutover lever. |
| Transformer version, credential version | T9@3, cred v4 | Hub only; referenced by an integration version |
| Capability schema version | `llm.chat@v1` | **Caller-facing.** Additive changes stay in v1. A breaking change = a new integration ID callers adopt when they choose. The only case needing a caller deployment. |

### 6.3 What happens to each request when v8 is published
| Request | Lands on | Rule |
| --- | --- | --- |
| Completed before publish | v7 | Audit keeps v7, its account and payloads forever; a publish rewrites nothing |
| In flight (e.g. live stream) | v7 | A request is pinned to the version it resolved at for the current attempt; nothing switches mid-flight |
| Parked/queued in outbox | **v8** (default `re_resolve`) / v7 if `pending: pinned` | Worker re-resolves the active version at send time; works because the envelope holds the canonical body, not the vendor payload |
| Retry of a failed v7 request (same idempotency key) | v8 | A retry is a new attempt and resolves fresh; idempotency only short-circuits *completed* requests |
| First new request after publish | v8 | Same URL, body, response schema |
| Step 2 of a vendor session opened on v7 | v7 | Sessions are pinned to the version + account that opened them |

Every attempt records the integration version it used.

### 6.4 Publishing rules
- Versions are immutable. Publish = validate (schemas compile, expressions type-check, goldens pass, response transformer output conforms to the capability response schema, referenced accounts/transformers exist and are not retired, interactive capabilities only use sync) → new config snapshot → nodes swap within 10 s.
- During propagation one node may serve v7 while another serves v8; per-request version recording keeps the audit exact. A node that cannot fetch keeps the last good snapshot.
- **Rollback** = publish a new version with the old content (v9 = v7). History stays linear; nothing is deleted.
- **Gradual cutover**: weights across versions (v8: A 90/B 10, v9: 50/50, v10: B 100) or **shadow** (serve from A, copy to B; result logged, never returned; dry-run-only shadow for vendors with side effects).

### 6.5 Account lifecycle
`active → draining → retired`. An account enters `draining` when no active integration version targets it. It stays there while pinned requests or open vendor sessions reference it; **retire is refused until both are zero**. Credentials are deleted only at retire.

### 6.6 Credential rotation (not a version)
Accounts hold `active` and `next` credentials. In-flight calls keep what was already injected; every later attempt (including a parked request's drain) uses `active` at send time. Runbook: add `next` → connection test → promote → watch `UPSTREAM_AUTH_FAILED` 15 min → delete old.

---

## 7. Transformation layer

### 7.1 Principles
- Declarative YAML: target paths on the left, expressions on the right. ~80 % of lines are plain paths.
- **Compile once**: every expression parsed and type-checked against the capability schema at snapshot build, never per request.
- **Pure and bounded**: no network, no disk, no loops (only `map`/`filter` builtins), max expression depth, max output size, runtime time budget.
- Both directions + error mapping.

### 7.2 Expression context
| Var | Contents |
| --- | --- |
| `req` | Canonical request body |
| `hdr` | Caller headers (allowlisted) |
| `account` | Non-secret account config (region, project id, brand code) |
| `creds` | Secrets; allowed only in header/query/auth targets unless the transformer sets `allow_creds_in_body: true` |
| `session` | Vendor session data (multi-step ops) |
| `res` | Vendor response (status, headers via `header(res, "X")`, body) — response/error mapping only |
| `now()` | Current time |

### 7.3 Standard library (extended only by platform code releases)
- Strings: `lower upper trim split join replace format truncate`
- Time: `now epochSeconds epochMillis parseTime formatTime addDuration`
- Data: `??` (coalesce) `merge pick omit flatten map filter len toJSON fromJSON`
- Encoding/identity: `uuid sha256 hmacSHA256 base64 urlEncode`
- Domain: `phoneE164 emailNormalize currencyMinor maskPAN maskAadhaar`

### 7.4 Examples (CleverTap shapes are illustrative; verify against current CleverTap upload API docs)

**T1 — `events.track@v1` → CleverTap upload (brand A)**
```yaml
transformer: T1
version: 7
kind: request
capability: events.track@v1
vendor_endpoint: clevertap.upload@1
request:
  method: POST
  path: /1/upload
  headers:
    X-CleverTap-Account-Id: creds.account_id
    X-CleverTap-Passcode:   creds.passcode
    Content-Type:           '"application/json"'
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())
        evtData:  req.event.properties
batch: { path: d, max: 1000 }     # enables worker micro-batching
```

**T2 — brand B needs two extra fields (shape change → new transformer)**
```yaml
    evtData: merge(req.event.properties, {
               brand:    account.config.brand_code,
               campaign: req.context.campaign ?? "organic" })
```

**R1 — full response**
```yaml
transformer: R1
version: 2
kind: response
response:
  status:     len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"
  accepted:   res.body.processed
  rejected:   res.body.unprocessed
  vendor_ref: header(res, "X-Request-Id")
  raw:        res.body
errors:
  - when: res.status == 401 || res.status == 403
    code: UPSTREAM_AUTH_FAILED
    retryable: false
  - when: res.status == 429
    code: UPSTREAM_RATE_LIMITED
    retryable: true
    retry_after_ms: int(header(res, "Retry-After") ?? "1") * 1000
  - when: res.status >= 500
    code: UPSTREAM_UNAVAILABLE
    retryable: true
  - when: res.status >= 400
    code: UPSTREAM_REJECTED
    retryable: false
```

**R2 — minimal response (caller only wants two fields)**
```yaml
response:
  status:     len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"
  vendor_ref: header(res, "X-Request-Id")
```

### 7.5 Micro-batching
For vendors whose endpoint accepts arrays (`batch:` declared), the async worker groups envelopes for the same account + endpoint within a 50–200 ms window into one vendor call and splits per-record results back onto individual requests.

### 7.6 Testing and safety rails
1. Golden fixtures next to each transformer: `fixtures/<name>.input.json`, `.expected_request.json`, `.vendor_response.json`, `.expected_response.json`.
2. `hub transform test` runs all goldens; CI and publish both run it.
3. Dry-run endpoint returns the exact vendor request.
4. Type-check against the capability schema (and vendor schema where available); unknown fields fail publish.
5. Benchmark: transform ≤ 0.5 ms p99 for a 4 KB payload.

---

## 8. Config registry and snapshots

### 8.1 Objects and formats
| Object | Format | Versioning |
| --- | --- | --- |
| Capability schema | JSON Schema 2020-12 (+ `x-pii`, `interactive`, `operations`) | Major per breaking change; minor additive |
| Vendor | YAML | Versioned; endpoints carry their own version (`clevertap.upload@1`) |
| Account | YAML + encrypted credential blob | Live object; credential versions `active`/`next` |
| Transformer | YAML + fixtures | Immutable per version |
| Integration | YAML | Immutable versions + active pointer |
| Tenant, API keys, Registrar endpoints | YAML (keys hashed) | Live |

### 8.2 Publish pipeline
1. Change arrives via admin API, UI or `hubctl apply` (Git).
2. Validation (§6.4).
3. New `config_snapshots` row (version, checksum, author, diff) + blob in object storage.
4. Nodes long-poll `/admin/v1/snapshots/latest` (or a Redis pub/sub ping), download, compile in the background, atomically swap the pointer. Target ≤ 10 s.
5. Rollback = republish previous content. Snapshots retained 90 days.

### 8.3 Vendor + account example
```yaml
vendor: clevertap
auth: static_headers
regions:
  in: https://in1.api.clevertap.com
  us: https://us1.api.clevertap.com
endpoints:
  upload@1: { method: POST, path: /1/upload, timeout_ms: 3000, connect_timeout_ms: 500, idempotent: true, batch_max: 1000 }
defaults:
  rate_limit: { per_account_rps: 50 }
  breaker:    { window_s: 30, min_calls: 20, failure_ratio: 0.5, consecutive_timeouts: 5, open_s: 30, open_max_s: 600, half_open_probes: 3 }
  health_probe: { endpoint: upload@1, dry: true, every_s: 30 }
---
account: ct-brandA-in
vendor: clevertap
region: in
state: active
credentials_ref: kms://hub/ct-brandA-in     # versions: active=v3, next=null
config: { project_id: "4RW-...", brand_code: "A" }
quota: { rps: 50, daily: 5000000 }
```

---

## 9. Credentials and vendor auth

- **Storage**: envelope encryption — per-account data key wrapped by KMS / Vault transit. DB sees only ciphertext. Every decrypt and change audited.
- **Runtime**: decrypted cache keyed `account + cred_version`, TTL 5 min, invalidated on snapshot ping, warmed on snapshot load. **No decryption on the request path.**
- **Local dev**: a `CredentialSource` backed by an AES-GCM-encrypted local file with a dev key; KMS/Vault implementations behind the same interface.
- **Redaction**: logger holds current secret values and replaces any occurrence with `***`.
- **Auth schemes** (plugins in `internal/creds`): static headers (CleverTap), bearer (OpenAI, Anthropic), basic (Twilio), query param (discouraged, never logged), HMAC request signing (payment partners), OAuth2 client credentials (token cache per account, refresh 60 s before expiry, single-flight), mTLS client cert (per-account TLS config).

---

## 10. Resilience

### 10.1 Timeouts
Per vendor endpoint: `connect_timeout_ms` (default 500), `timeout_ms` (default 3000; LLM 60 000–120 000). `X-Request-Deadline` caps sync further.

### 10.2 Retry policy by outcome
| Vendor outcome | Code | Sync | Async / wait-tail |
| --- | --- | --- | --- |
| 2xx | – | return | `succeeded` |
| Timeout / reset | `UPSTREAM_TIMEOUT` | one immediate retry only if endpoint idempotent or idempotency key present | `retry_scheduled` |
| 429 | `UPSTREAM_RATE_LIMITED` | return with `retry_after_ms` | retry at max(backoff, Retry-After) |
| 5xx | `UPSTREAM_UNAVAILABLE` | failover if configured, else return | `retry_scheduled`; counts toward breaker |
| 401/403 | `UPSTREAM_AUTH_FAILED` | return; page | `dead_lettered` now; page |
| other 4xx | `UPSTREAM_REJECTED` | return | `dead_lettered` |
| transformer exception | `TRANSFORM_FAILED` | 500 | `dead_lettered`; alert |

### 10.3 Backoff (async default)
+1 s, +5 s, +30 s, +2 min, +10 min, +30 min, +1 h, +1 h → dead-letter after 8 attempts or at TTL. Full jitter. Overridable per integration. **Retry budget**: retries ≤ 20 % of the account's live traffic.

### 10.4 Circuit breaker
- Key `(account, endpoint)`, state shared via Redis (all nodes agree within ~1 s; local cache of state, Redis sync off the request path).
- Closed → Open: > 50 % failures over a 30 s window with ≥ 20 calls (5xx, timeouts, connection errors; 429 at half weight), or 5 consecutive timeouts (integration override may tighten, e.g. 3).
- Open 30 s, doubling per re-open, capped 10 min → Half-open: 3 probes; all succeed → Closed, any fail → Open.
- Synthetic health probe every 30 s while Open, so recovery is detected without live traffic.
- While Open: sync follows `on_circuit_open`; wait returns 202 queued; async parks.
- Integration **pause** (kill switch): sync/wait → `INTEGRATION_PAUSED` 503; nothing parked; other integrations on the same account unaffected.

### 10.5 Parking and automatic drain
Parked envelopes are tagged with account + endpoint, kept in arrival order. On breaker close the scheduler releases them at **20 % of the account rate limit for 30 s, doubling every 30 s** to normal. Live traffic has priority on the shared account limiter. Re-open mid-drain → the rest stay parked. Parked count/age are metrics with paged alerts.

### 10.6 DLQ
Keeps canonical payload, last vendor request/response, attempt history, reason. 14 days. Replay by filter (tenant, integration, account, reason, time range); replay re-resolves and re-transforms, so a transformer fix applies. Poison detector: an envelope that crashes a worker twice is dead-lettered.

### 10.7 TTL
Every envelope has `expires_at`; checked before each attempt; past it → `expired` (callback says so). Defaults per capability, overridable per integration.

### 10.8 Bulkheads and rate limits
- Per-account HTTP pool + concurrency limit (default 64 in flight). LLM endpoints run in a separate worker pool.
- Tenant quotas at the edge (token bucket per tenant + integration). Account quotas in the upstream client (shared Redis bucket); workers throttle to the account limit instead of collecting 429s.

### 10.9 Request state machine (async/wait)
States: `accepted → in_flight → succeeded | retry_scheduled | parked | dead_lettered | expired`.
- `in_flight → retry_scheduled` (retryable error) → `in_flight` (next attempt due)
- `in_flight → parked` (circuit open) → `retry_scheduled` (breaker closes, drain)
- `in_flight → dead_lettered` (non-retryable or exhausted) → `accepted` (manual replay)
- `retry_scheduled | parked → expired` (TTL passed)
Transitions are guarded (compare-and-set on status) so duplicates cannot double-complete or double-callback.

---

## 11. Queue

### 11.1 Choice
Phase 1: **Postgres outbox** — transactional with the ledger ("accepted" literally means "committed"), native delayed retry (`next_attempt_at`), native park/release (`status`, `account_id`), ~2–5k msg/s. Phase 3: NATS JetStream or RabbitMQ behind the same interface via transactional-outbox relay. Kafka only if volume or replay genuinely demands it (retry-heavy workloads fit it poorly).

### 11.2 Interface
```go
type Queue interface {
    Enqueue(ctx context.Context, env Envelope) error                      // durable before return
    Claim(ctx context.Context, worker string, n int) ([]Envelope, error)  // visibility timeout applies
    Ack(ctx context.Context, id string) error
    Nack(ctx context.Context, id string, retryAt time.Time, reason string) error
    Park(ctx context.Context, id string, account, endpoint string) error
    Release(ctx context.Context, account, endpoint string, limit int) (int, error) // drain scheduler
    DeadLetter(ctx context.Context, id string, reason string) error
}
```

### 11.3 Envelope
```json
{
  "request_id": "req_01J9...",
  "tenant": "orders-service",
  "integration_id": "int_evt_brandA",
  "resolved_version": 7,
  "pinned": false,
  "session_id": null,
  "capability": "events.track@v1",
  "operation": "default",
  "canonical": { "...": "validated request body" },
  "mode": "async_callback",
  "idempotency_key": "order-8821-created",
  "ordering_key": null,
  "attempt": 0,
  "next_attempt_at": "2026-10-29T17:30:00Z",
  "expires_at": "2026-10-29T23:30:00Z",
  "trace": { "traceparent": "00-..." },
  "created_at": "2026-10-29T17:29:58Z"
}
```
- Holds the **canonical body + integration ID**, not the vendor payload → re-resolve and re-transform at send time (cutover, transformer fixes, failover).
- If `pinned` or `session_id` is set, the worker uses `resolved_version`; otherwise the active version.
- At-least-once delivery; vendor-side idempotency keys where supported; guarded status transitions.
- `ordering_key` (opt-in per capability): per-key in-order delivery.

---

## 12. Webhooks

### 12.1 Outbound (Hub → tenant)
- **Registrar** (tenant level): `endpoint_id`, URL (allowlisted host), signing secret. SSRF guard: resolve host, refuse private/link-local ranges, no redirects, 5 s timeout.
- **Subscriptions** (integration version): event types → registrar endpoint.
- Events: `request.completed`, `request.failed`, `request.expired`, `delivery.status`, plus canonical inbound events.
- Signature: `X-Hub-Signature: t=<unix>,v1=HMAC-SHA256(secret, t + "." + body)`; tenants reject timestamps > 5 min old.
- Own retry schedule and DLQ, independent of vendor delivery. Payload carries `request_id` and `idempotency_key`.

### 12.2 Inbound (vendor → Hub → tenant)
1. Vendor posts to `/webhooks/in/{vendor}/{account}`; verify with the account's webhook scheme (HMAC / basic / IP allowlist); reject replays > 5 min.
2. Correlate to request/integration by the request id embedded in the outbound call, or by `vendor_ref` from the audit.
3. Inbound transformer maps it to a canonical event (`message.delivered@v1`, `aa.fi_ready@v1`).
4. 200 to the vendor immediately; fan out via subscriptions of every integration on that account subscribed to the event type, through the signed outbound path.

---

## 13. Audit trail

### 13.1 Events per request (each stores the exact bytes it saw, subject to policy)
| Event | Contents |
| --- | --- |
| `request.received` | Raw body + headers as received; tenant key replaced by its hash |
| `request.validated` | Canonical JSON after schema check, schema id, warnings |
| `integration.resolved` | Integration id + version, vendor, account, transformers, policy, snapshot version, breaker state |
| `request.transformed` | Exact vendor request: method, URL, headers (secrets redacted), body |
| `vendor.sent` | Attempt n, host, TLS, conn reuse, rate-limit and bulkhead waits |
| `vendor.responded` | Status, headers, raw body verbatim (truncated at 64 KB, rest to object storage), vendor_ref |
| `response.transformed` / `response.transform_failed` | Transformer version, canonical result, or the failing expression + input |
| `request.completed` / `request.failed` / `request.expired` | Final record: status, attempts, hub_overhead_ms, vendor_ms, queue_wait_ms, pointers to all events, errors |
Plus `request.persisted`, `request.parked`, `request.accepted`, `policy.applied`, `callback.delivered` where relevant.

### 13.2 Payload policies (per integration)
- `full`: raw bytes at every stage (engagement, messaging).
- `masked` (default): PII masked/hashed via `x-pii` tags.
- `sealed`: full raw bytes, field-level encrypted with a per-tenant key; readable only via an audited "unseal" admin action. **Default for bureau, Aadhaar, PAN.**
- `metadata_only`: no bodies (LLM prompts by default).

### 13.3 Always kept
SHA-256 + byte size of every payload (so "lossless" is verifiable even when sealed), versions (integration, transformer, snapshot), account and host, every attempt (timestamp, duration, status, error class), breaker state, waits, callback deliveries, one final record.

### 13.4 Storage
`requests` + `request_events` + `request_attempts`, partitioned by day; hot 30 days (sealed: 90), then object storage (metadata 1 year). Event writes are buffered and non-blocking on the hot path (bounded channel + drop counter + alert); terminal records are written synchronously for async paths since they are already in a transaction.

---

## 14. Lending integration catalog and interaction patterns

| Pattern | Examples | Hub feature |
| --- | --- | --- |
| **Sync lookup** | Bureau pull (CIBIL, Experian, Equifax, CRIF High Mark), PAN verify, PAN–Aadhaar link, GST/Udyam/MCA, penny drop, CKYC search, fraud/device checks | sync/wait, response transformers, idempotency keys (mandatory where money/quota moves), optional per-integration result cache (e.g. PAN–Aadhaar link, 24 h) |
| **Two-step with vendor txn id** | Aadhaar OTP eKYC (generate → verify) via licensed KUA/ASA | **Vendor session store**: step 1 returns `X-Hub-Session`; vendor txn id stored 10 min, pinned to account + version; step 2 routed there; UIDAI OTP limits as account quota; Aadhaar number never stored |
| **Consent + async fetch** | Account Aggregator (Finvu, OneMoney, Setu, Sahamati router): consent → consent notification → FI request → FI notification → FI fetch | **Long-running request** (TTL in days), consent artefact store, inbound notifications as first-class steps of one request state machine, final data via callback |
| **Redirect + callback** | DigiLocker / Aadhaar offline XML, eSign, eNACH / eMandate, reverse penny drop | **Hosted hand-off**: Hub returns `handoff_url`; Hub owns the return URL; status webhook to tenant |
| **Upload + job** | Bank statement analysis, bulk CKYC | Large payloads in object storage, ledger keeps pointers; job poll/webhook adapters |
| **Fire and forget** | CleverTap, MoEngage, SMS, WhatsApp BSPs | async, TTL, delivery-status webhooks, multi-account per brand |
| **Stream** | LLM chat | sync streaming, interactive flag, pre-first-byte failover, token accounting |

Bureau/Aadhaar/PAN/AA data: `sealed` audit, strict retention, consent reference stored on the request.

---

## 15. LLM providers

- Capabilities: `llm.chat@v1` (messages, tools, temperature, max_tokens, stream), `llm.embed@v1`.
- Model choice belongs in the integration version's target config (e.g. `account.config.model` or target-level `params.model`), so swapping models or providers is a cutover, not caller code.
- Streaming: SSE relay without buffering; transformer `stream:` section maps each vendor chunk to a canonical chunk (`delta.text`, `finish_reason`, `usage`); `max_stream_s` ceiling; mid-stream failure → terminal error event.
- Failover only on breaker-open or 429/5xx/timeout **before the first byte**. Transformer `supports: [tools, vision, json_mode]` prevents routing to an incapable target (checked at publish).
- Token accounting: response transformer extracts usage; per-model price table on the vendor → cost metrics per tenant/integration/account; optional daily token budgets per tenant → `UPSTREAM_RATE_LIMITED` with reason `budget`.
- Audit: `metadata_only` by default.

---

## 16. Control plane, observability, security

### 16.1 Admin API (`/admin/v1`, OIDC, RBAC `admin | editor | viewer | tenant_viewer`)
`vendors`, `accounts` (test-connection, rotate, lifecycle), `capabilities`, `transformers` (dry-run, run-goldens), `integrations` (versions, publish, rollback, diff, pause, weights), `tenants`, `api-keys`, `registrar-endpoints`, `snapshots`, `requests` (search, timeline, unseal), `dlq` (list, replay, purge), `breakers` (state, force-open/close). Every change in a `change_log` table (actor, action, object, before, after, reason).

**Config as code**: registry (minus secrets) round-trips to Git as YAML. `hubctl plan` diffs against live; `hubctl apply` publishes through the same pipeline. UI changes also land as commits (phase 2).

### 16.2 Metrics (labels: tenant, integration, vendor, account, version)
`hub_requests_total{mode,code}`, `hub_overhead_ms`, `vendor_latency_ms`, `breaker_state`, `breaker_transitions_total`, `queue_depth`, `queue_oldest_age_s`, `parked_count`, `parked_oldest_age_s`, `dlq_count`, `ratelimit_rejections_total{layer}`, `retry_budget_exhausted_total`, `snapshot_version`, `credential_expiry_days`, `llm_tokens_total`, `llm_cost_usd_total`, `audit_events_dropped_total`.

### 16.3 Alerts
Breaker open > 5 min (page) · `parked_oldest_age_s` > 10 min or `queue_oldest_age_s` > 2 min (page) · > 50 DLQ entries / 10 min / account (page) · any `UPSTREAM_AUTH_FAILED` (page) · any `TRANSFORM_FAILED` (page) · node > 60 s behind snapshot (warn) · credential expiring < 7 days (warn) · `hub_overhead_ms` p99 > 5 ms for 10 min (warn) · any audit events dropped (warn).

### 16.4 Tracing
OTel spans: edge, transform, upstream, callback; accept caller `traceparent`; forward to vendors that accept it.

### 16.5 Security
Tenant keys hashed, shown once, rotatable with overlap; scopes per integration. KMS envelope encryption. PII tags drive masking/sealing; deletion job purges by subject identifier. Egress restricted to hosts declared in vendor definitions; TLS 1.2+ with verification; optional pinning for regulated partners. Body limits (1 MB default, 4 MB LLM). Regional accounts enforce data residency.

---

## 17. Architecture

| Component | Responsibility | Scaling |
| --- | --- | --- |
| **Gateway** | Edge auth, validation, resolution, transform, sync calls, wait-mode immediate attempts, outbox writes, inbound webhooks, hand-off return URLs | Stateless, HPA on CPU + p99 |
| **Worker** | Claims envelopes, re-resolves, re-transforms, sends, retry/park/DLQ transitions, callbacks | Stateless, scale on queue depth; separate pools for messaging vs LLM |
| **Scheduler** | Promotes due retries, drains parked envelopes on breaker close, expires TTLs, runs health probes, expires vendor sessions | Leader-elected singleton + hot standby |
| **Control plane** | Admin API/UI, validation + publish pipeline, snapshot distribution, change log | 2 replicas, off the request path |
| **Postgres** | Registry, snapshots, ledger/audit, outbox, DLQ, sessions, change log | Managed primary + read replica |
| **Redis** | Breaker state, rate-limit buckets, idempotency cache, snapshot ping | Managed |
| **KMS / Vault** | Wraps per-account data keys | Cloud-managed |
| **Object storage** | Snapshot blobs, large payloads, cold audit | Managed |

One binary, entrypoints `hub gateway | worker | scheduler | admin`, so all roles run identical transform/upstream code. Kubernetes; one region in phase 1, warm standby later. Nodes can boot from the last snapshot in object storage if the control plane is down.

---

## 18. Low-level design (Go)

### 18.1 Module layout
```text
cmd/
  hub/                 # hub gateway | worker | scheduler | admin
  hubctl/              # plan, apply, transform test, dry-run, replay
  mockvendor/          # programmable fake vendor for tests and local dev
internal/
  edge/                # HTTP server, auth, tenant rate limit, schema validation, error envelope
  registry/            # snapshot build/load, in-memory indexes, compiled schemas + transformers
  transform/           # DSL parser, expr env, stdlib, request/response/error/stream mapping, dry-run, goldens
  resolve/             # integration version resolution, weights, failover, session pinning
  creds/               # CredentialSource (localfile, kms, vault), cache, auth-scheme plugins
  upstream/            # per-account HTTP pools, breaker, account limiter, bulkhead, retry
  queue/               # Queue interface; postgres impl (phase 1)
  delivery/            # worker loop, wait mode, state machine, drain scheduler, TTL expiry
  sessions/            # vendor session store
  audit/               # request events, attempts, masking/sealing, buffered writer
  callbacks/           # registrar, subscriptions, signed delivery, callback retries
  webhooks/            # inbound verification, correlation, inbound transformers, fan-out
  control/             # admin API, publish pipeline, change log, account lifecycle
  telemetry/           # OTel, metrics, redacting slog handler
pkg/
  hubclient/           # Go SDK: Result / Queued types, idempotency helper, SSE reader
config/                # dev registry: capabilities/, vendors/, accounts/, transformers/ (+fixtures), integrations/, tenants/
deploy/                # docker-compose.yml, k8s manifests (later)
migrations/
docs/DESIGN.md
```

### 18.2 Data model (Postgres)
| Table | Key columns |
| --- | --- |
| `tenants`, `api_keys` | `tenant_id`, `key_hash`, `scopes[]`, `rate_limit` |
| `vendors`, `vendor_endpoints` | `vendor_id`, `endpoint_id`, `version`, `timeouts`, `breaker_cfg`, `idempotent` |
| `accounts`, `account_credentials` | `account_id`, `vendor_id`, `region`, `state` (active/draining/retired), `config`; `cred_version`, `slot` (active/next), `ciphertext`, `wrapped_key` |
| `capabilities` | `capability`, `major`, `minor`, `request_schema`, `response_schema`, `operations`, `interactive` |
| `transformers` | `transformer_id`, `version`, `kind`, `capability`, `endpoint_id`, `yaml`, `fixtures`, immutable |
| `integrations` | `integration_id`, `owner_tenant`, `capability`, `active_version`, `paused` |
| `integration_versions` | `integration_id`, `version`, `spec` (JSONB), `author`, `reason`, `published_at`, immutable |
| `integration_grants` | `integration_id`, `tenant_id` |
| `registrar_endpoints` | `endpoint_id`, `tenant_id`, `url`, `secret_ref` |
| `config_snapshots` | `version`, `checksum`, `blob_ref`, `author`, `diff` |
| `requests` (partitioned by day) | `request_id`, `tenant_id`, `integration_id`, `status`, `final_version`, `timings`, `policy` |
| `request_events` (partitioned) | `request_id`, `seq`, `type`, `at`, `payload` / `payload_ref`, `sha256`, `bytes` |
| `request_attempts` | `request_id`, `attempt_no`, `integration_version`, `account_id`, `vendor_status`, `duration_ms`, `error_code` |
| `outbox` | `request_id`, `status`, `account_id`, `endpoint_id`, `next_attempt_at`, `expires_at`, `ordering_key`, `claimed_by`, `claimed_until`, `envelope` — indexes `(status, next_attempt_at)`, `(status, account_id, endpoint_id)` |
| `dead_letters` | `request_id`, `reason`, `last_attempt`, `replayed_at` |
| `vendor_sessions` | `session_id`, `integration_id`, `version`, `account_id`, `vendor_txn_id`, `data`, `expires_at` |
| `callback_deliveries` | `delivery_id`, `request_id`, `endpoint_id`, `status`, `attempts`, `next_attempt_at` |
| `change_log` | `actor`, `action`, `object`, `before`, `after`, `reason`, `at` |

### 18.3 Key interfaces
```go
type Registry interface{ Snapshot() *Snapshot } // immutable, atomically swapped

type Snapshot struct {
    Version      uint64
    Tenants      map[string]*Tenant
    Capabilities map[CapKey]*Capability             // compiled JSON Schemas
    Vendors      map[string]*Vendor
    Accounts     map[string]*Account
    Transformers map[TransformerKey]*CompiledTransformer
    Integrations map[string]*Integration            // includes all versions + active pointer
}

type Resolver interface {
    // version == 0 → active version; otherwise that exact (pinned/session) version
    Resolve(snap *Snapshot, tenant, integrationID string, version int, routeKey string) (Resolution, error)
}

type Transformer interface {
    BuildRequest(ctx context.Context, t *CompiledTransformer, in TransformInput) (VendorRequest, error)
    MapResponse(ctx context.Context, t *CompiledTransformer, resp VendorResponse) (CanonicalResponse, *CanonicalError)
}

type CredentialSource interface {
    Get(ctx context.Context, accountID string) (Credentials, error) // cached, decrypted, active slot
}

type Upstream interface {
    Do(ctx context.Context, acct *Account, ep *Endpoint, req VendorRequest) (VendorResponse, error) // breaker+limiter+bulkhead inside
}

type Breakers interface {
    State(account, endpoint string) BreakerState
    Record(account, endpoint string, outcome Outcome)
}

type AuditSink interface {
    Emit(ev RequestEvent) // non-blocking
}
```

### 18.4 Pipelines
**Gateway sync**: recover → request id + trace → auth → grant check → tenant rate limit → body limit → schema validate → resolve → idempotency → transform → breaker/policy → upstream → response map → audit (buffered) → respond. Each stage records its duration into the `hub_overhead_ms` breakdown.

**Worker loop**
```text
loop:
  envs := queue.Claim(worker, 32)                     // SKIP LOCKED, visibility 30 s
  for env in envs:
    if expired(env): audit expired; ack; enqueue callback; continue
    res := resolver.Resolve(snap, env.tenant, env.integration_id, pinnedOrSessionVersion(env), env.idem_or_request_id)
    if integration paused: nack(retry later); continue
    if breaker(res.account, res.endpoint).Open(): queue.Park(env); continue
  group by (account, endpoint)                         // micro-batch if transformer.batch
  for each group:
    req := transform.BuildRequest(...)
    wait for account rate-limit tokens
    resp := upstream.Do(...)
    classify → Ack + succeed | Nack(retryAt) | DeadLetter
    record attempt with integration_version
    if terminal and subscribed: callbacks.Enqueue(...)
```

### 18.5 Hot-path rules
1. No DB/registry read on the sync path; only the snapshot pointer.
2. One JSON parse in, one marshal out; transforms work on the parsed tree.
3. Pooled buffers; audit writes are non-blocking channel sends with a bounded buffer and a drop counter.
4. No credential decryption on the request path.
5. A new middleware must publish its own duration metric and stay < 100 µs p99.
6. The only network call besides the vendor on the sync path is the Redis idempotency check (skipped without a key). Breaker state is read from local cache.

### 18.6 Latency budget (sync, p99, 4 KB, warm)
| Stage | Budget |
| --- | --- |
| TLS + HTTP parse | 0.3 ms |
| Auth, grant, tenant limit | 0.1 ms |
| Schema validation | 0.8 ms |
| Resolve | 0.05 ms |
| Idempotency (Redis, if key) | 1.0 ms |
| Transform | 0.5 ms |
| Credential injection | 0.02 ms |
| Breaker, account limiter, bulkhead | 0.1 ms |
| Response map + serialise | 0.4 ms |
| Audit + metrics | 0.1 ms |
| **Total Hub overhead** | **≈ 3.4 ms (target ≤ 5 ms)** |

### 18.7 Libraries (Go; confirm current versions at kickoff)
HTTP `net/http` + `chi` · Postgres `pgx` + `sqlc`, migrations `goose` · Redis `go-redis` · expressions `expr-lang/expr` · JSON Schema `santhosh-tekuri/jsonschema` · breaker: small custom implementation with Redis-shared state (or `sony/gobreaker` as the local state machine) · rate limiting `golang.org/x/time/rate` locally + Redis token bucket for shared quotas · telemetry OpenTelemetry Go SDK + `log/slog` with a redacting handler · tests `testify` (or std), testcontainers-go or docker-compose.
Java fallback (only if I choose Java): Quarkus or Spring WebFlux, `resilience4j`, `cel-java` or sandboxed JEXL (never raw SpEL), `networknt/json-schema-validator`, jOOQ + Flyway, Lettuce.

---

## 19. Build plan

Each milestone ends with: tests green (`make test`, `make itest`), summary, commit.

### M0 — Scaffold
- Repo layout (§18.1), `go.mod`, `Makefile` (`build test itest lint bench run`), `golangci-lint`, CI workflow.
- `deploy/docker-compose.yml`: Postgres 16, Redis 7.
- `hub` binary with role entrypoints, config loading (env + flags), `/healthz`, `/readyz`, redacting `slog`, OTel/metrics skeleton.
- **`cmd/mockvendor`**: programmable fake vendor. Control API to set per-route behaviour: latency (fixed/random), status codes, error ratio, 429 with `Retry-After`, hard down (connection refused), slow drip, malformed body; records received requests for assertions; can send inbound webhooks.
- **Accept**: `docker compose up` + `hub gateway` serves `/healthz`; mockvendor behaviours covered by tests.

### M1 — Config model and registry
- YAML/JSON loaders for capability, vendor, account, transformer, integration (with versions), tenant, registrar endpoint.
- Validation with clear, path-qualified errors; snapshot build; atomic pointer swap; dev file watcher.
- Dev config: `events.track@v1` schema; vendor `clevertap` (pointing at mockvendor in dev); accounts `ct-brandA-in`, `ct-brandB-us`; tenant `orders-service`; integrations `int_evt_brandA`, `int_evt_brandB`, `int_evt_brandB_min`.
- **Accept**: invalid configs rejected with precise errors (table tests); snapshot swap under concurrent reads is race-free (`-race`).

### M2 — Transformer engine
- DSL parse → compile with typed env (§7.2) → stdlib (§7.3) → request, response, error mapping → dry-run.
- `hubctl transform test` golden runner.
- Transformers T1, T2, R1, R2 with fixtures (§7.4).
- **Accept**: goldens pass; type errors caught at compile (test: reference to a missing field fails); budget limits enforced; `BenchmarkTransform4KB` ≤ 0.5 ms p99.

### M3 — Gateway sync path + audit
- Tenant API keys (hashed), grants, schema validation, resolve (active version), transform, `CredentialSource` (local encrypted file), auth schemes: static headers, bearer, basic.
- Upstream client per account (pool, timeouts), response map, error envelope + codes (§4.4), `X-Hub-Request-Id`.
- Audit: all §13.1 events for sync; `masked` policy via `x-pii`; buffered writer to Postgres; payload sha256/bytes.
- `pkg/hubclient` with `Result`/`Queued` types.
- **Accept**: e2e test caller → hub → mockvendor for each error class maps to the right canonical code; audit timeline complete and secrets never present (grep test on stored events); overhead benchmark ≤ 5 ms p99 with mockvendor at 0 ms.

### M4 — Outbox, worker, wait/async/callback, idempotency
- `Queue` Postgres impl (`SKIP LOCKED`, visibility timeout), worker loop, state machine with guarded transitions, backoff with jitter, retry budget, DLQ, TTL expiry.
- `wait` mode (persist → immediate attempt → 200 within window or 202), `async`, `async_callback`.
- Registrar + subscriptions + signed callbacks with their own retries/DLQ; SSRF guard.
- Idempotency (Redis): replay stored results for completed requests.
- **Accept**: kill a worker mid-flight → envelope reclaimed, delivered once from the caller's view; duplicate submissions with the same key → one vendor call; callback signature verified by a test receiver; TTL expiry produces `expired` + callback.

### M5 — Resilience
- Breaker `(account, endpoint)` with Redis-shared state, local cache, half-open probes, synthetic health probe.
- `on_circuit_open`: `fail_fast`, `failover`, `queue_if_allowed`; integration pause.
- Park + ramped drain scheduler; account rate limiter (Redis token bucket, live traffic priority); tenant limiter; bulkheads.
- **Accept** (chaos tests with mockvendor):
  1. Vendor down → breaker opens within the configured window; sync gets 503 with `retry_after_ms` and `circuit: open`; wait gets 202 `queued`; async parks.
  2. Vendor recovers → probes close the breaker; drain starts ≤ 60 s; observed send rate ramps 20 % → 100 %.
  3. Re-open mid-drain → remaining envelopes stay parked.
  4. Two integrations on one account trip and recover together; pausing one does not affect the other.
  5. Failover policy serves from the failover target and sets `X-Hub-Served-By`.

### M6 — Versioning and cutover
- Immutable integration versions, publish (full validation §6.4), rollback-as-new-version, weights (hashed on idempotency key / request id), shadow (logged only).
- Re-resolve vs pinned; version recorded per attempt; vendor session store pinning; account lifecycle (draining/retire guard).
- Credential `active`/`next` rotation.
- **Accept**: the six scenarios of §6.3 as tests, for each change type (account, vendor, transformer); retire refused while pinned requests or sessions exist; rollback restores behaviour and leaves history linear.

### M7 — Control plane and hubctl
- Admin API (§16.1) with OIDC stub in dev, RBAC, change log.
- `hubctl plan|apply|transform test|dry-run|replay`.
- Request explorer endpoints (timeline), DLQ replay by filter, breaker force, pause, unseal (audited).
- **Accept**: `hubctl apply` of the dev config is idempotent; `plan` shows exact diffs; every admin mutation appears in `change_log`.

### M8 — Observability and hardening
- Metrics (§16.2), traces, alert rules as code (§16.3), dashboards JSON (tenant, vendor, platform).
- Load test (k6 or vegeta): 500 sync RPS + 2,000 async msg/s on a laptop-scale setup with mockvendor; verify budgets.
- **Accept**: overhead p99 ≤ 5 ms at target load; zero lost async requests in a soak test with random vendor outages.

### Phase 2 (after M8)
Inbound vendor webhooks + correlation; micro-batching; OAuth2 + HMAC + mTLS auth schemes; `llm.chat` with streaming, failover, token accounting; vendor session flows (Aadhaar OTP), redirect hand-offs (DigiLocker, eSign, eNACH); long-running consent requests (Account Aggregator); `sealed` audit with unseal; admin UI (vendor health board, transformer editor with dry-run, integration version diff, request explorer, DLQ console); tenant self-service; KMS/Vault credential sources.

### Phase 3
Broker-backed queue (NATS JetStream or RabbitMQ) via transactional outbox relay; per-key ordering at scale; warm-standby region; SDKs for Java and Node; cost dashboards; transformer template library.

---

## 20. Open questions (ask me when you reach them; don't block earlier milestones)
1. Callers: internal services only, or external customers too?
2. Expected volume at launch and in 12 months (sync RPS, async msg/s).
3. First production capabilities/vendors after the CleverTap pilot (bureau? PAN? AA? LLM?), and which second engagement vendor proves the swap.
4. How many CleverTap accounts exist today and what distinguishes them (brand, region, environment).
5. Do any callers need CleverTap's synchronous response, or can all engagement integrations default to async?
6. PII/retention: confirm 30-day masked default and 90-day sealed for bureau/KYC.
7. Secrets backend: cloud KMS or HashiCorp Vault.
8. Hosting: which Kubernetes cluster/region; is a warm standby needed in phase 1?

---

## 21. References (for humans; not readable by Claude Code)
- Detailed written design (original PRD/HLD/LLD; note it predates the Integration-entity and cutover decisions, which this file supersedes): https://claude.ai/artifact/Bo7GEuJx9XB6EBJMbTsoJY
- Interactive design brief (Integration entity, delivery modes, circuit-open behaviour, cutover simulator, audit, lending catalog): https://claude.ai/artifact/MZAAfREsnDvqHKGeTsgmva
- Account Aggregator FI request workflow (Sahamati): https://developer.sahamati.org.in/buildaathon-2024/network-scenarios/fi-request-workflow
- AA FI request is asynchronous (MoneyOne): https://developer.moneyone.in/api-reference/data/fi-request.md
- Setu AA quickstart (consent, fetch, notification flows): https://docs.setu.co/data/account-aggregator/v1/quickstart-v1
- India verification API catalogs: https://emudhra.com/en/idbroker/feature-india-apis · https://documentation.hyperverge.co/tags/india-apis
