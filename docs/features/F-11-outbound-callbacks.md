# F-11 — Outbound callbacks & registrar (signed webhooks, SSRF guard)

## Summary
Tenants register callback endpoints once (registrar); integration versions subscribe event types to them; the Hub delivers HMAC-signed callbacks with its own retry schedule and DLQ, behind an SSRF guard (DESIGN §12.1, §3.1).

## Status
`in-progress` — 2026-10-31 (HUB-119 SSRF guard and registrar secrets).

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
- `NewGuard(GuardOptions)` builds the only HTTP client callbacks use. `Guard.Do(req)` checks the URL form (absolute, no userinfo), the scheme (`https`; see the dev allowance below) and the optional host allowlist (`AllowedHosts`: exact names or `*.suffix`), then sends. The **dialer** resolves the host itself and dials the vetted IP literal; every address in the answer must pass, otherwise the host is refused (one private address in a round-robin answer refuses it). `net.Dialer.ControlContext` re-checks the socket address right before `connect(2)`. So the address that is checked is the address that is connected to, and a DNS answer that changes after a registration-time check (DNS rebinding) cannot reach a private address.
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

## Data model impact
`registrar_endpoints(endpoint_id, tenant_id, url, secret_ref)`, `callback_deliveries(delivery_id, request_id, endpoint_id, status, attempts, next_attempt_at)` (DESIGN §18.2).

## Config impact
Integration version `webhooks: [{event, endpoint}]`; tenant YAML registrar entries; host allowlist.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `callback_deliveries_total{tenant,endpoint,result}` | |
| Metric | `callback_queue_oldest_age_s`, `callback_dlq_count` | |
| Log | `ssrf_blocked` | endpoint_id, resolved IP class |
| Trace | span `callback` | |
| Audit | `callback.delivered` event | |

## Security & PII
Signing secret never logged or returned after creation. SSRF guard is mandatory. Payload follows the integration's audit policy for what is retained, but the callback itself carries the full canonical result to the tenant.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Tenant endpoint down | Retries, then callback DLQ | Late callback or none; `GET /v1/requests/{id}` still works |
| URL resolves to private IP | Refused | Registration 400 / delivery fails |
| Redirect | Not followed, counted as failure | — |

## Testing
- **Integration**: test receiver verifies signature (DESIGN §19 M4 accept); stale timestamp rejected.
- **Unit**: SSRF table (IPv4/IPv6 ranges, DNS rebinding).
- **Contract**: callback payloads validated against event schemas; signature test vectors are not in `contracts/` yet (to be added via CCR).

## Rollout & migration
Tenants register endpoints before publishing `async_callback` versions (publish validates).

## Open questions
- Callback retry schedule and max age are not specified in DESIGN.
- Host allowlist ownership: per tenant or global?

## Tickets
HUB-110 – HUB-139 (EP-05, filter `feature: F-11`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-10-31 | HUB-119: SSRF guard, registrar signing secrets via creds, dev recipe | backend-engineer |
