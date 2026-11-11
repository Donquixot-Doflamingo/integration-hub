# F-11 — Outbound callbacks & registrar (signed webhooks, SSRF guard)

## Summary
Tenants register callback endpoints once (registrar); integration versions subscribe event types to them; the Hub delivers HMAC-signed callbacks with its own retry schedule and DLQ, behind an SSRF guard (DESIGN §12.1, §3.1).

## Status
`in-progress` — 2026-10-31. HUB-119 (SSRF guard, registrar secrets) and HUB-120 (signed payloads, transactional enqueue, dispatcher with retries and DLQ) are done in `internal/callbacks`; wiring into the M4 state machine and the worker role (lead) and `async_callback` end to end (HUB-121) remain.

## Owner
`backend-engineer`; `contract-steward` for event schemas.

## Milestone / Epic
M4 · EP-05 · HUB-110–139.

## Problem
`async_callback` callers need results pushed to them, securely, without duplicating URLs/secrets per integration and without letting the Hub be used to reach internal hosts.

## Goals / Non-goals
**Goals**
- Registrar: `endpoint_id`, URL (allowlisted host), signing secret (stored by ref).
- Subscriptions in integration versions: `event → endpoint_id`.
- Events: `request.completed`, `request.failed`, `request.expired`, `delivery.status`.
- `X-Hub-Signature: t=<unix>,v1=HMAC-SHA256(secret, t + "." + body)`.
- Independent retries and callback DLQ; payload carries `request_id` and `idempotency_key`.
- SSRF guard: resolve host, refuse private/link-local, no redirects, 5 s timeout.

**Non-goals**
- Inbound vendor webhooks and canonical inbound events (F-22).

## User stories
- [US-1101](../product/USER_STORIES.md) — Register an endpoint once
- [US-1102](../product/USER_STORIES.md) — Signed callbacks I can verify
- [US-1103](../product/USER_STORIES.md) — Independent callback retries

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | Registrar CRUD, rotate secret | `contracts/openapi/hub-admin.v1.yaml#listRegistrarEndpoints`, `#createRegistrarEndpoint`, `#getRegistrarEndpoint`, `#updateRegistrarEndpoint`, `#deleteRegistrarEndpoint`, `#rotateRegistrarSecret` |
| Callback payload | `request.completed`, `request.failed`, `request.expired`, `delivery.status` | `contracts/schemas/events/envelope.json`, `request.completed.json`, `request.failed.json`, `request.expired.json`, `delivery.status.json`; webhooks `contracts/openapi/hub-public.v1.yaml#onRequestCompleted`, `#onRequestFailed`, `#onRequestExpired`, `#onDeliveryStatus` |
| Callback headers | `X-Hub-Signature`, `X-Hub-Event`, `X-Hub-Delivery-Id` | `contracts/openapi/hub-public.v1.yaml#/components/parameters/CallbackSignature`, `#/components/parameters/CallbackEvent`, `#/components/parameters/CallbackDeliveryId` |

## Behaviour & rules
1. Callbacks are enqueued only on terminal transitions (or `delivery.status`) and only if the version has a subscription (DESIGN §18.4 worker loop).
2. Signature over `t + "." + raw body`; tenants reject `t` older than 5 min (documented in F-21 skill + SDK).
3. SSRF guard runs at registration and at every delivery (DNS rebinding): resolve, refuse RFC1918, loopback, link-local, CGNAT, metadata IPs; pin the resolved IP for the connection.
4. No redirects followed; 5 s total timeout; 2xx = delivered.
5. Callback retry schedule is independent of vendor delivery (proposed: same backoff shape, 24 h max); exhausted → callback DLQ, replayable.
6. Guarded transitions on `callback_deliveries` prevent double delivery from the Hub's side; receivers dedupe on `X-Hub-Delivery-Id`.
7. Payload contains canonical result/error, never vendor/account/version.
8. Secret rotation supports overlap: several `v1=` values during the window; receivers accept if any one matches (contract `CallbackSignature`).

## Implementation (HUB-119)
Package `internal/callbacks` (layer L5), `guard.go`:
- `NewGuard(GuardOptions)` builds the only HTTP client callbacks use. `Guard.Do(req)` checks the URL form (absolute, no userinfo), the scheme (`https`; see the dev allowance below) and the optional host allowlist (`AllowedHosts`: exact names or `*.suffix`), then sends. The **dialer** resolves the host itself and dials the vetted IP literal; every address in the answer must pass, otherwise the host is refused (one private address in a round-robin answer refuses it). The A and AAAA lookups run in parallel (`ip4` and `ip6`); the first family that answers is used and the other is waited for at most 150 ms (so its addresses are vetted too) and otherwise kept as a fallback, so a resolver that never answers AAAA, or a black-holed AAAA address, no longer uses the whole 5 s (HUB-452). Every address of every answer received is vetted (private, loopback, link-local, metadata, mapped, NAT64 stay refused; one refused address refuses the host), and each address is dialed with an equal share of the time left. A dial that ends in timeouts reports as `timeout`. `CheckURL` at registration uses the same resolution. `net.Dialer.ControlContext` re-checks the socket address right before `connect(2)`. So the address that is checked is the address that is connected to, and a DNS answer that changes after a registration-time check (DNS rebinding) cannot reach a private address.
- Refused (`ClassifyIP`, IPv4-mapped IPv6 unmapped first): `0.0.0.0/8`, `10/8`, `100.64/10` (CGNAT), `127/8`, `169.254/16` (incl. metadata `169.254.169.254`), `172.16/12`, `192.0.0/24`, `192.0.2/24`, `192.88.99/24`, `192.168/16`, `198.18/15`, `198.51.100/24`, `203.0.113/24`, `224/4`, `240/4`; `::`, `::1`, `::/96`, `::ffff:0:0:0/96`, `64:ff9b::/96`, `64:ff9b:1::/48` (NAT64), `100::/64`, `2001::/23` (incl. Teredo), `2001:db8::/32`, `2002::/16` (6to4), `fc00::/7`, `fe80::/10`, `fec0::/10`, `ff00::/8`, and any IPv6 outside `2000::/3`. Refusals are `*BlockedError{Host, Class, Detail}` (`errors.Is ErrBlocked`); `Class` is one of `unspecified, loopback, private, cgnat, link_local, multicast, documentation, translation, reserved, policy`.
- Redirects are never followed (`CheckRedirect` returns the 3xx response, which delivery counts as a failed attempt). Proxies from the environment are ignored (`Proxy: nil`). Total budget 5 s per request (`Timeout`, also TLS handshake and response-header timeouts).
- `Guard.CheckURL(ctx, url)` is the same check for registration time (admin registrar CRUD, HUB-201); delivery never trusts it and re-checks at dial time.
- **Dev/test allowance** `GuardOptions.AllowLoopback`: loopback receivers (`127/8`, `::1`) are allowed, and plain `http` is allowed only when every address is loopback; every other private range stays refused. Production wiring must not set it; the dev registrar `config/registrar/reg_orders_cb.yaml` (`http://127.0.0.1:8097/hub/callbacks`) needs it.

Registrar signing secrets reuse the credential machinery (`internal/creds`, F-07), not a second store:
- `secret_ref: localfile://<id>` names an entry of the same AES-256-GCM dev file as vendor credentials, value `signing_secret` (≥ 16 bytes, `creds.MinSigningSecretLen`), slots `active` and optional `next`.
- `creds.SnapshotSecretIDs(snap)` (= `AccountIDs` ∪ `RegistrarSecretIDs`) is what a role that signs callbacks passes to `Cache.Warm`; loading registers both slots with `telemetry.SecretSet`, so they are redacted from logs, and nothing decrypts on the delivery path.
- `creds.RegistrarSecrets{Cache}` implements the `callbacks.SigningSecrets` port (`SigningSecrets(ctx, ref) ([][]byte, error)`: active first, then next; identical values once) through the new `Cache.Slots(ctx, id)`. callbacks never imports creds (TECH_SPEC §4.1); the role wiring passes the adapter in.

Dev setup (from `backend/`, fake values only; key setup as in F-07):
```sh
go run ./cmd/hubctl creds set reg_orders_cb signing_secret=dev-callback-secret-not-real-0001
# rotation overlap: both secrets sign (two v1= values) until the next slot is promoted
go run ./cmd/hubctl creds set -slot next reg_orders_cb signing_secret=dev-callback-secret-not-real-0002
# promote: set the active slot to the new value (the duplicate next value is signed once)
go run ./cmd/hubctl creds set reg_orders_cb signing_secret=dev-callback-secret-not-real-0002
go run ./cmd/hubctl creds list   # keys only, never values
```

## Implementation (HUB-120)
**Payloads** (`events.go`): `callbacks.Event{EventID, OccurredAt, TenantID, IntegrationID, IntegrationVersion, Operation, RequestID, IdempotencyKey, Data}` with `Data` one of `*Completed{Result, Attempts, CompletedAt, Timings}`, `*Failed{Error, Attempts, FailedAt}`, `*Expired{Error, Attempts, ExpiresAt, LastState}` (code `EXPIRED`, reason `ttl_elapsed` by default), `*DeliveryStatus{State, PreviousState, Sequence, Attempt, NextAttemptAt, Reason, LastError}`. `Event.Marshal` produces the exact body: the `envelope.json` members (`spec_version` `"1"`, `operation` default `default`, `idempotency_key` always present, `null` when absent) plus the per-type `data`; errors are `core.CanonicalError` with `request_id` filled in. Ids: `evt_` + ULID (stable across retries, receivers dedupe on it), `dlv_` + ULID per delivery (header `X-Hub-Delivery-Id`). `SchemaValidator` compiles `contracts/schemas/events/*.json` (draft 2020-12, formats asserted); every payload shape is tested against it.

**Signing** (`sign.go`): `Sign(secrets, t, body)` → `t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`, one `v1=` per secret (active first, then next during rotation). `Verify(header, body, secrets, now, tolerance)` is the receiver side (and the test receiver's): timestamp within ±5 min (`DefaultTolerance`), any `v1=` matching any secret (constant time), unknown `vN=` ignored; errors `ErrSignatureMalformed`, `ErrSignatureExpired`, `ErrSignatureMismatch`.

**Enqueue** (`enqueue.go`): `NewEnqueuer(snapshots, metrics, EnqueueOptions{Validator, Log, Now})`, then `(*Enqueuer).Enqueue(ctx, tx pgx.Tx, ev *Event) (int, error)`:
- Called by the request state machine **inside its own transaction**, after the winning guarded transition (DESIGN §10.9): the deliveries exist if and only if the transition commits, once.
- Subscriptions come from the snapshot: `Integrations[ev.IntegrationID].Versions[ev.IntegrationVersion].Webhooks` entries whose `event` equals the type, one row per distinct registrar endpoint. Only endpoints of `ev.TenantID` are used: a granted tenant's result never goes to the owner tenant's endpoint (counted as `tenant_mismatch`).
- No matching subscription → returns 0 without touching `tx` (so `delivery.status` can be offered on every transition cheaply). Unknown version/endpoint → skipped with a WARN and a counter. No snapshot → `ErrNoSnapshot`; invalid event, contract violation (when a `Validator` is set) or a database error → error, and the caller rolls back.
- `ev.EventID`/`OccurredAt` are filled when empty; pass a stable `EventID` if the same transition may be enqueued twice: `(event_id, endpoint_id)` is unique and the insert is `ON CONFLICT DO NOTHING`.

**Table** `hub.callback_deliveries` (`migrations/00005_callbacks.sql`, owned by callbacks): pk `delivery_id`; `event_id`, `event_type`, `request_id`, `tenant_id`, `integration_id`, `integration_version`, `endpoint_id`, `subscription` (index in the version's webhooks list), `payload bytea` (the exact signed bytes, identical on every attempt), `status` (`pending → in_flight → delivered | retry_scheduled | dead_lettered`), `attempts`, `next_attempt_at`, `claimed_by`, `claimed_until`, `last_http_status`, `last_error` (error class only), timestamps; unique `(event_id, endpoint_id)`. Indexes: partial `(next_attempt_at) WHERE status IN ('pending','retry_scheduled')` (claim), partial `(claimed_until) WHERE status='in_flight'` (reclaim), `(request_id)`, partial `(created_at) WHERE status='dead_lettered'` (the callback DLQ). `fillfactor=80`, `autovacuum_vacuum_scale_factor=0.01`.

**Dispatcher** (`dispatcher.go`, `store.go`): `NewDispatcher(DispatcherDeps{Pool, Snapshots, Secrets, Audit, Metrics, Log, TracerProvider}, DispatcherOptions{ID, Senders, ClaimBatch, PollInterval, Visibility, Schedule, SampleEvery, Guard, Now})`, then `Run(ctx) error`:
- One claim loop plus at most `Senders` (32) concurrent attempts. It claims only as many rows as there are free senders (`FOR UPDATE SKIP LOCKED`, due rows by `next_attempt_at` plus `in_flight` rows whose `claimed_until` passed), sets `in_flight`, `claimed_by`, `claimed_until = now() + Visibility` (30 s, must exceed the 5 s send timeout) and counts the attempt. No claimed row waits in memory. One `LIMIT n` applies over the union of due and abandoned rows (abandoned first), so a claim never returns more than the free senders (HUB-452); the dispatcher's sender tokens do not depend on the row count, and a store that returned more anyway would make the surplus rows wait for a token (`callback.claim_overflow` WARN) instead of exceeding `Senders`.
- Each attempt reads the registrar endpoint from the **current** snapshot (URL and `secret_ref`; latest config wins), gets the signing secrets from the warm cache, signs the stored body with a fresh `t`, and POSTs it through the SSRF guard with `Content-Type: application/json`, `X-Hub-Signature`, `X-Hub-Event`, `X-Hub-Delivery-Id`. 2xx is delivered; anything else (3xx included) is a failed attempt. The response body is drained (≤ 4 KB) and discarded.
- The outcome is a guarded transition (`WHERE status='in_flight' AND claimed_by=<me>`); losing it (the row was reclaimed) has no side effects: no metric, no audit, no log beyond `callback.claim_lost`.
- **Retry schedule** (own, independent of vendor delivery; `DefaultSchedule`): after failed attempt n wait 5 s, 30 s, 2 min, 10 min, 30 min, 1 h, 2 h, 4 h, 6 h, 6 h, each ±20 % jitter. 11 attempts over ≈ 19.7 h (15.8–23.7 h); the 11th failure dead-letters (`status='dead_lettered'`, the callback DLQ; replay is `UPDATE … SET status='pending', attempts=0, next_attempt_at=now()`, admin action pending F-16).
- Dead-letter at once (retrying cannot help): endpoint no longer in the snapshot (`endpoint_unknown`), endpoint of another tenant (`tenant_mismatch`), URL refused by policy (`url_refused`: not https, not on the allowlist, userinfo). **Held, not retried** (HUB-452): a delivery for a disabled endpoint (`endpoint_disabled`) is not sent and does not use an attempt: the attempt counted at claim is given back, the row waits 5 minutes (`disabledHold`) and is looked at again, indefinitely, so it can never dead-letter while the endpoint is disabled and is delivered as a normal attempt once re-enabled. A hold emits no `callback.delivered` audit event (nothing was sent; its sequence number would repeat); `callback_deliveries_total{outcome="retry_scheduled"}` and the DEBUG `callback.retry_scheduled` log still record it. Retried: `http_4xx`, `http_5xx`, `redirect`, `timeout`, `connect`, `ssrf_blocked` (DNS may change back), `secret_unavailable` (cache not warm yet).
- `Run` stops claiming when ctx ends, lets started attempts finish (≤ 5 s each) and record, then returns; a row whose outcome could not be recorded is reclaimed after the visibility timeout (at-least-once; receivers dedupe on `event_id` / `X-Hub-Delivery-Id`).
- Audit: every recorded attempt emits `callback.delivered` through `audit.Sink` (DESIGN §13.1): `seq = 1 000 000 + subscription×100 + attempt` (never collides with the request trail's 1…n), payload sha256 and size only (`payload_state=omitted`), meta `delivery_id, event_id, event, endpoint_id, attempt, outcome, http_status, error, duration_ms, retry_in_ms`. Never the body, the signature, a header or the secret.

## Data model impact
`registrar_endpoints(endpoint_id, tenant_id, url, secret_ref)`, `callback_deliveries(delivery_id, request_id, endpoint_id, status, attempts, next_attempt_at)` (DESIGN §18.2).

## Config impact
Integration version `webhooks: [{event, endpoint}]`; tenant YAML registrar entries; host allowlist.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `callback_deliveries_total{tenant,outcome}` | per recorded attempt; outcome `delivered`, `retry_scheduled`, `dead_lettered` |
| Metric | `callback_latency_seconds{tenant}` | duration of attempts that sent a request |
| Metric | `callback_ssrf_blocked_total{class}` | attempts refused by the guard by address class |
| Metric | `callback_enqueued_total{event}` | rows written by Enqueue (counted at write; a rolled-back transaction still counts) |
| Metric | `callback_enqueue_skipped_total{reason}` | `unknown_version`, `unknown_endpoint`, `tenant_mismatch`, `endpoint_disabled`, `invalid_payload` (a terminal event whose own payload is unusable: logged by request id and event type, never the payload; the request's terminal transition is not affected, HUB-449) |
| Log | `callback.enqueue_skipped` (WARN) | reason, request_id, integration_id, integration_version, endpoint_id, event |
| Metric | `callback_queue_oldest_age_s`, `callback_dlq_count` | gauges sampled by every dispatcher every 15 s |
| Log | `callback.ssrf_blocked` (WARN) | endpoint_id, tenant, class, delivery_id (never the resolved IP or URL path) |
| Log | `callback.dead_lettered` (WARN), `callback.retry_scheduled` (DEBUG) | delivery_id, request_id, tenant, endpoint_id, event, attempt, http_status, error (+ retry_in) |
| Log | `callback.claim_lost` (INFO), `callback.record_failed`, `callback.claim_failed`, `callback.sample_failed`, `callback.claim_overflow` (WARN; asked, got: the store returned more rows than asked, should never happen) | |
| Trace | span `hub.callback.deliver` (client) | `hub.endpoint_id`, `hub.callback.attempt`, `hub.callback.event`, `hub.request_id`, `http.response.status_code`, `hub.outcome`; error status on failure |
| Audit | `callback.delivered` event | one per recorded attempt, hash and size only |

## Security & PII
Signing secret never logged or returned after creation. SSRF guard is mandatory. Payload follows the integration's audit policy for what is retained, but the callback itself carries the full canonical result to the tenant.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Tenant endpoint down | Retries, then callback DLQ | Late callback or none; `GET /v1/requests/{id}` still works |
| URL resolves to private IP | Refused | Registration 400 / delivery fails |
| Redirect | Not followed, counted as failure | — |

## Testing
- **Integration** (`//go:build integration`, real Postgres): enqueue inside the caller's transaction (rollback leaves nothing), every event type dispatched to a test receiver that verifies the signature, retry then dead-letter and recovery, three concurrent dispatchers never double-send (SKIP LOCKED, `-race`), abandoned claims reclaimed, claim plan uses the partial indexes. Stale timestamps rejected by `Verify` (unit).
- **Unit**: SSRF table (IPv4/IPv6 ranges, DNS rebinding).
- **Contract**: callback payloads validated against event schemas; signature test vectors are not in `contracts/` yet (to be added via CCR).

## Rollout & migration
Tenants register endpoints before publishing `async_callback` versions (publish validates).

## Open questions
- Callback retry schedule and max age are not specified in DESIGN. **Decision (HUB-120):** `DefaultSchedule` above (≈ 19.7 h, 11 attempts); revisit with the lead.
- Host allowlist ownership: per tenant or global? `GuardOptions.AllowedHosts` is one process-wide list today (empty = any public host).
- Granted tenants: subscriptions point at owner-tenant endpoints (registry validation), and Enqueue only delivers to endpoints of the requesting tenant, so a grantee's requests get no callback. A per-grantee subscription model needs a design decision.
- Registration-time check: `Guard.CheckURL` exists; the admin registrar CRUD (HUB-201) must call it. Registry validation still accepts `http://` URLs (dev registrar).
- `payload` is stored in clear in `callback_deliveries` for retries (it is the canonical result the tenant receives). A purge job for delivered rows (and DLQ retention, 14 days like `dead_letters`) belongs to the scheduler.

## Tickets
HUB-110 – HUB-139 (EP-05, filter `feature: F-11`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | HUB-119: SSRF guard, registrar signing secrets via creds, dev recipe | backend-engineer |
| 2026-10-31 | HUB-120: event payloads, signing, transactional enqueue, callback_deliveries | backend-engineer |
| 2026-10-31 | HUB-120: dispatcher, retry schedule, callback DLQ, metrics, audit | backend-engineer |
