# F-11 — Outbound callbacks & registrar (signed webhooks, SSRF guard)

## Summary
Tenants register callback endpoints once (registrar); integration versions subscribe event types to them; the Hub delivers HMAC-signed callbacks with its own retry schedule and DLQ, behind an SSRF guard (DESIGN §12.1, §3.1).

## Status
`proposed` — 2026-10-31.

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
