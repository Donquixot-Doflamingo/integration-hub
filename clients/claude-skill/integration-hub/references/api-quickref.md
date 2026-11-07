# Integration Hub caller API: quick reference

Base: `$HUB_BASE_URL`. Auth: `Authorization: Bearer $HUB_API_KEY` (tenant key) on every call.
The authoritative spec is `contracts/openapi/hub-public.v1.yaml` in the Hub repo.

## Endpoints
| Method + path | Purpose |
| --- | --- |
| `GET /v1/integrations` | Integrations your tenant owns or is granted (`integration_id`, `operations` with each `request_schema`, delivery mode, interactive, paused, `moves_money_or_quota`, `idempotency_key_required`, description) |
| `GET /v1/integrations/{id}` | One integration's caller-visible details: operations (`name`, `description`, `idempotent`, `request_schema {body, headers?}`), wait window, TTL, `on_circuit_open`, callback events |
| `POST /v1/integrations/{id}/dry-run[?operation=op]` | Validate + show the exact upstream request the Hub would send (secrets `***`); sends nothing. Needs key scope `dry_run`. Add `X-Hub-Session` for a later step. Never build logic on it |
| `POST /v1/integrations/{id}/invoke` | Invoke the `default` operation of a single-operation integration |
| `POST /v1/integrations/{id}/invoke/{op}` | Invoke one operation of a multi-operation integration (e.g. `send_otp`, `verify_otp`) |
| `GET /v1/requests/{request_id}` | Status and final result (wait/async modes) |

There is no response schema: the result is the integration's output, or (pass-through) the third party's own body.

Discovery endpoints never return vendor, account or version. There is no way to pick them.

## Request headers
| Header | Required | Meaning |
| --- | --- | --- |
| `Authorization: Bearer <tenant key>` | yes | Identifies your tenant |
| `Content-Type: application/json` | for bodies | |
| `Idempotency-Key` | recommended; **mandatory** where money or vendor quota moves | Same key within 24 h returns the stored result |
| `Prefer: respond-async` | no | You accept a 202 if the vendor circuit is open (only where the integration allows queueing) |
| `X-Request-Deadline` | no | Caps the sync wait, in ms |
| `X-Hub-Session` | step >= 2 of multi-step ops | Session id returned by step 1 |
| `traceparent` | no | Continue your distributed trace |
| Headers the operation declares in `request_schema.headers` | as the schema says | Passed to the integration; undeclared caller headers are not. The Hub's own headers above can never be declared |

## Response headers
| Header | Meaning |
| --- | --- |
| `X-Hub-Request-Id` | Always present. Log it; quote it to support |
| `X-Hub-Idempotent-Replay: true` | Stored result returned for a repeated Idempotency-Key |
| `X-Hub-Session` | Returned by step 1 of a multi-step operation; send it on later steps (valid 10 min) |
| `X-Hub-Served-By: failover:N` | Served by a failover target (informational) |
| `X-Hub-Pass-Through: true` | The body and status are the third party's own, unchanged (the integration has no response transformer). Never the error envelope, even on 4xx/5xx: check it before parsing `{"error": ...}`. Only the third party's `Content-Type` and `Retry-After` headers are kept |
| `X-Hub-Integration-Version` | Debug only. Do not branch on it |

## Status codes
- **200**: the integration's output (any JSON). With `X-Hub-Pass-Through: true`, the third party's own 2xx body (JSON or not).
- **Other 2xx/4xx/5xx with `X-Hub-Pass-Through: true`**: the third party's own status and body after the Hub's retries (timeouts, 429, 5xx are retried). Timeouts, an open circuit and Hub-side failures still use the envelope below.
- **202**: `{"request_id":"req_…","status":"accepted|queued|in_flight","reason":"…","poll":"/v1/requests/req_…"}`. Not done yet.
- **4xx / 5xx**: error envelope:
```json
{ "error": { "code": "UPSTREAM_UNAVAILABLE", "message": "Vendor circuit is open",
             "retryable": true, "retry_after_ms": 30000, "circuit": "open",
             "upstream": { "status": null, "code": null },
             "request_id": "req_01J9ABCD" } }
```

## Error codes
| Code | HTTP | Retryable | When | What to do |
| --- | --- | --- | --- | --- |
| `VALIDATION_FAILED` | 400 | no | Body or declared headers break the operation's request schema | Fix fields listed as JSON pointers in details |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | no | Integration has `moves_money_or_quota` and no key was sent | Send an `Idempotency-Key` |
| `UNAUTHORIZED` | 401 | no | Bad or revoked key | Check `HUB_API_KEY` |
| `FORBIDDEN` | 403 | no | Not owner or grantee (`reason: not_granted`), or key lacks a scope (`reason: missing_scope`) | Ask the integration owner / platform for a grant or scope |
| `INTEGRATION_NOT_FOUND` | 404 | no | Unknown id | List integrations |
| `OPERATION_NOT_FOUND` | 404 | no | The integration has no such `{op}` | Check operations via discovery |
| `REQUEST_NOT_FOUND` | 404 | no | Unknown request id (or another tenant's) | Check the id |
| `SESSION_NOT_FOUND` | 404 | no | Unknown/expired `X-Hub-Session` | Restart from step 1 |
| `IDEMPOTENCY_CONFLICT` | 409 | no | Same key with a different body, or original still in progress | Keep one body per key; wait and retry |
| `PAYLOAD_TOO_LARGE` | 413 | no | Body over 1 MB | Shrink the body |
| `INTEGRATION_PAUSED` | 503 | yes | Admin kill switch | Back off and retry later |
| `IDEMPOTENT_REPLAY` | 200/202 | - | Stored result returned | Treat as the original result |
| `TRANSFORM_FAILED` | 500 | no | Hub bug (platform alerted) | Report with request id |
| `INTERNAL_ERROR` | 500 | yes | Unexpected Hub failure | Retry with backoff, same key |
| `UPSTREAM_TIMEOUT` | 504 | yes | Vendor too slow | Retry, same Idempotency-Key |
| `UPSTREAM_UNAVAILABLE` | 503 | yes | Circuit open or vendor 5xx | Wait `retry_after_ms`, retry same key |
| `UPSTREAM_RATE_LIMITED` | 429 | yes | Vendor 429, account quota or tenant budget | Wait `retry_after_ms`, slow down |
| `UPSTREAM_REJECTED` | 422 | no | Vendor 4xx (content refused) | Fix input; vendor message passed through |
| `UPSTREAM_AUTH_FAILED` | 502 | no | Hub's vendor credentials rejected (on-call paged) | Report; retry later only if told |
| `EXPIRED` | - | no | Async request passed its TTL (status/callback only) | Decide whether to resubmit |

Retry rule: only when `retryable` is true; exponential backoff with jitter; honour `retry_after_ms`;
cap attempts; reuse the same `Idempotency-Key`. A pass-through answer has no `retryable`: honour its
`Retry-After` if present. In `GET /v1/requests/{id}` and callbacks, failures always carry an
`UPSTREAM_*` code, also for pass-through integrations, and `result` is the third party's body (a
non-JSON body as a JSON string).

## Request states (`GET /v1/requests/{id}`)
`accepted -> in_flight -> succeeded | retry_scheduled | parked | dead_lettered | expired`; sync requests that errored end `failed`
- `parked`: vendor circuit open; drains automatically after recovery.
- `retry_scheduled`: retryable failure, next attempt pending.
- Terminal: `succeeded`, `failed` (sync error), `dead_lettered` (platform can replay), `expired`.

## Delivery modes (set per integration by the platform; shown in discovery)
| Mode | Healthy vendor | Vendor down |
| --- | --- | --- |
| `sync` | 200 now | `fail_fast` 503 (default), or failover, or queue if you sent `Prefer: respond-async` and it is allowed |
| `wait` (default) | 200 if done within the wait window, else 202 `in_flight` | 202 `queued`, `reason: circuit_open` |
| `async` | 202, delivered by worker | 202, parked, drained later |
| `async_callback` | 202, then signed POST to your endpoint | 202, callback after the drain |
Interactive integrations (chat, OTP) are `sync` only: a 202 is never given to a person waiting.

## Callbacks
Events: `request.completed`, `request.failed`, `request.expired`, `delivery.status`, plus canonical
inbound events. Header `X-Hub-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + body)>`;
reject if older than 5 minutes. Payload carries `request_id` and `idempotency_key`; dedupe on them.
