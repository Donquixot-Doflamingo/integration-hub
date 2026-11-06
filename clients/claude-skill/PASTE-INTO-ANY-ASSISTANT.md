# Integration Hub: rules for AI assistants

Paste this into any assistant's custom instructions, a repo `AGENTS.md`, or a Cursor rule. It is the
one-file version of the `integration-hub` skill (`clients/claude-skill/integration-hub/` in the Hub
repo); the skill has the full error table, SDK examples and callback verifiers.

## What the Hub is
Our services never call third-party vendors (engagement, SMS/WhatsApp, bureau, KYC/PAN/Aadhaar,
Account Aggregator, payments, LLMs) directly. They call the **Integration Hub** with a stable
**integration id** (`int_...`) and a **canonical body**, and get a canonical response. The Hub picks
the vendor, account and version; callers can never choose them and must never mention them in code.
Never add a vendor SDK or vendor URL when an integration exists.

## Safety rules
1. Never invoke a money- or quota-moving integration (OTP send, bureau pull, penny drop, payment,
   mandate, paid bulk LLM job, messages to real users) without the user's explicit "yes" in this
   conversation. Dry-run first, show what will be sent, ask, then invoke.
2. Never print, paste, hard-code or commit `HUB_API_KEY` or any secret. Read keys from the
   environment or a secret manager. If a key appears in chat, tell the user to rotate it.
3. Use fake test data (no real PAN, Aadhaar, phone numbers, OTPs). Never put them in idempotency keys.
4. A `202` is **not** success. Never report a queued request as done.

## Calling it
Environment: `HUB_BASE_URL`, `HUB_API_KEY` (tenant key). Header on every call:
`Authorization: Bearer $HUB_API_KEY`. If MCP tools `hub_list_integrations`,
`hub_describe_integration`, `hub_dry_run`, `hub_invoke`, `hub_get_request` exist, prefer them.

1. **Discover**: `GET /v1/integrations` (what this tenant may call: operations, delivery mode,
   `moves_money_or_quota`), `GET /v1/integrations/{id}`, `GET /v1/capabilities/{capability}`
   (canonical JSON Schemas). Build bodies from the schema, never from vendor docs. Never invent ids.
2. **Dry-run** (developer keys with scope `dry_run`): `POST /v1/integrations/{id}/dry-run` shows the
   upstream request with secrets `***`, sends nothing. Use it to check the mapping only; never copy
   vendor hosts, headers or shapes from it into code.
3. **Invoke**: `POST /v1/integrations/{id}/invoke` (or `/invoke/{operation}`), with
   `Idempotency-Key` derived from the business event (`order-8821-created`), mandatory where money or
   quota moves. Same key within 24 h replays the stored result (`X-Hub-Idempotent-Replay: true`).
   Log `X-Hub-Request-Id`.
4. **Handle**:
   - `200`: canonical response.
   - `202 {"request_id", "status": "accepted|queued|in_flight", "poll"}`: not done; poll
     `GET /v1/requests/{request_id}` with backoff until `succeeded | failed | dead_lettered | expired`,
     or wait for the signed callback.
   - Error `{"error": {"code", "message", "retryable", "retry_after_ms", "request_id", ...}}`: branch
     on `code`, never on `message` (except where noted below). Retry only if `retryable: true`, after
     `retry_after_ms`, with backoff, the **same** `Idempotency-Key`, and a cap. Not retryable:
     `VALIDATION_FAILED` (fix fields listed as JSON pointers), `IDEMPOTENCY_KEY_REQUIRED`,
     `UNAUTHORIZED`/`FORBIDDEN`, `*_NOT_FOUND`, `IDEMPOTENCY_CONFLICT`, `UPSTREAM_REJECTED` (fix input),
     `UPSTREAM_AUTH_FAILED`/`TRANSFORM_FAILED` (Hub side: report the request id). Retryable:
     `UPSTREAM_TIMEOUT`, `UPSTREAM_UNAVAILABLE`, `UPSTREAM_RATE_LIMITED`, `INTEGRATION_PAUSED`,
     `INTERNAL_ERROR`.

## Two-step flows (OTP send, then verify; Aadhaar e-KYC)
One integration, two operations, two calls:
1. `POST /v1/integrations/{id}/invoke/send_otp` + `Idempotency-Key` → `200` with header
   `X-Hub-Session: ses_...`. Store it server side with the user's attempt.
2. `POST /v1/integrations/{id}/invoke/verify_otp` + `X-Hub-Session` + a new `Idempotency-Key` + the OTP.

The session lives 10 minutes and pins step 2 to the account that sent the OTP. Wrong OTP → 422
`UPSTREAM_REJECTED` (retry with the same session); expired OTP → 422 (resend = step 1 again, new
session); expired session → 404 `SESSION_NOT_FOUND` (restart); double tap with the same key → one SMS,
same session; vendor down → 503 retryable (same session, same key). Wrong vs expired is only in
`message` today. OTP integrations are `sync`: never a 202. Read operation names from discovery.

## Callbacks
For `async_callback` integrations the Hub POSTs to the team's registered URL with
`X-Hub-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, t + "." + raw_body)>`. Verify over the raw
bytes before parsing, constant-time compare, reject `|now - t| > 5 min`, accept any `v1`, dedupe by
`X-Hub-Delivery-Id` / `request_id`, answer 2xx within 5 s.

## Go
Use `github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`: `hubclient.New(baseURL,
key)`, `Invoke(ctx, id, body, hubclient.WithIdempotencyKey(k), hubclient.WithOperation(op),
hubclient.WithSession(s))` returns `*hubclient.Result` (200; `.Session` after step 1) or
`*hubclient.Queued` (202) and errors as `*hubclient.Error` (`Code`, `Retryable`, `RetryAfter`).
Handle both outcomes; never treat `Queued` as success.

## No integration fits?
Do not work around it. The platform team creates integrations: send the tenant, capability and
operations, delivery needs (sync / wait / async / callback), volume, whether money or quota moves,
data classes (PII, Aadhaar, PAN, bureau) and example bodies with fake data.
