---
name: integration-hub
description: Call third-party capabilities (engagement events, SMS/WhatsApp, credit bureau, KYC/PAN/Aadhaar, Account Aggregator, payments/mandates, LLMs) through the company Integration Hub instead of integrating vendors directly. Covers HUB_BASE_URL/HUB_API_KEY setup, discovering integrations and capability schemas, building canonical bodies, dry-run first, invoke with Idempotency-Key, 200 vs 202 Queued handling and polling, error codes and retry rules, verifying X-Hub-Signature callbacks (Go/Python/Node), the Go hubclient SDK, the hubmcp MCP tools, and requesting a new integration. Use whenever the user wants to send an event or message, pull a bureau report, verify PAN/Aadhaar, call an LLM or any external vendor from an internal service, mentions the Hub, an integration id like int_..., hubclient, hubmcp, or a vendor SDK/API (CleverTap, MoEngage, OpenAI, etc.) for a company service.
---

# Using the Integration Hub

The Hub is **one vendor-agnostic API** for every third party. You send a **stable integration ID** +
a **canonical request body**; you get a **canonical response**. The Hub picks vendor, account
(credentials) and version, handles retries, rate limits, circuit breakers and audit.

**Callers never choose a vendor, account or version.** There is no header or field for it. If the
user asks "send this via CleverTap account B", explain that the platform team configures that
behind the integration ID; the code stays the same. Never suggest calling the vendor directly or
adding a vendor SDK when an integration exists.

## Safety rules (always)
1. **Never invoke a money- or quota-moving integration without the user's explicit confirmation**
   in this conversation (bureau pulls, payments, mandates, penny drop, OTP sends, paid LLM bulk
   jobs). Dry-run first, show what will be sent, ask, then invoke. Same for any irreversible side
   effect (messages to real users).
2. **Never paste, print or commit API keys.** Read `HUB_API_KEY` from the environment or a secret
   manager. Do not echo it, do not put it in code, tests, `.mcp.json`, chat or logs. If the user
   pastes a key into chat, tell them to rotate it.
3. Use fake test data (no real PAN/Aadhaar/phone numbers) unless the user explicitly runs a real call.
4. `202` is **not** success. Never report a queued request as done.

## Setup
```bash
export HUB_BASE_URL="https://<hub host for your environment>"   # ask the platform team
export HUB_API_KEY="$(<read from your secret manager>)"          # your tenant key; never hard-code
```
All calls: `Authorization: Bearer $HUB_API_KEY`. Keys are per tenant (service), shown once at
creation, rotatable. Use a non-production environment for experiments.

Prefer the MCP server if it is configured (tools `hub_list_integrations`, `hub_describe_integration`,
`hub_dry_run`, `hub_invoke`, `hub_get_request`): see `references/mcp-setup.md`.

## Workflow
### 1. Discover
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations"
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations/int_evt_brandA"
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/capabilities/events.track@v1"
```
- `/v1/integrations` lists only what **your tenant** may invoke: id, capability, operations,
  delivery mode, paused flag, description.
- `/v1/capabilities/{capability}` returns the canonical request/response JSON Schemas per operation.
  Build bodies from this schema, not from vendor docs.
- No suitable integration? See "Requesting a new integration" below. Do not work around it.

### 2. Build the canonical body
Follow the capability schema exactly (required fields, formats such as E.164 phones, RFC 3339
timestamps, minor currency units). Unknown fields may be rejected with `VALIDATION_FAILED`.

### 3. Dry-run first
```bash
curl -s -X POST "$HUB_BASE_URL/v1/integrations/int_evt_brandA/dry-run" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" -d @body.json
```
Validates the body and returns the exact upstream request the Hub *would* send (method, URL,
headers, body), with credentials and secrets shown as `***`. Nothing is sent to the vendor. Fix every
validation error here. Your key needs scope `dry_run` (not granted by default; ask the platform team
for a developer key, otherwise you get 403 `FORBIDDEN`, reason `missing_scope`). This is the one
place the Hub shows what is behind an integration: use it only to check the mapping, and **never
put vendor hosts, headers or shapes from it into code** (they change whenever the Hub cuts over).

### 4. Invoke
```bash
curl -s -i -X POST "$HUB_BASE_URL/v1/integrations/int_evt_brandA/invoke" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -H "Idempotency-Key: order-8821-created" -d @body.json
```
- Multi-operation capabilities: `POST /v1/integrations/{id}/invoke/{op}` (e.g. `generate`, then
  `verify` with header `X-Hub-Session: <session id from step 1>`).
- **`Idempotency-Key`**: recommended always, **mandatory where money or vendor quota moves**
  (discovery shows `moves_money_or_quota` / `idempotency_key_required`; missing key → 400 `IDEMPOTENCY_KEY_REQUIRED`).
  Derive it from the business event (`loan-123-bureau-pull`), not a random value per retry. Same
  key within 24 h returns the stored result with `X-Hub-Idempotent-Replay: true`.
- Optional: `Prefer: respond-async` (you accept a 202 if the vendor is down, for integrations
  configured to queue), `X-Request-Deadline: <ms>` (caps sync wait), `traceparent`.
- Always log the `X-Hub-Request-Id` response header (or `request_id` in bodies); support uses it.

### 5. Handle the response
| Status | Meaning | Do |
| --- | --- | --- |
| `200` | Done. Canonical response body. | Use it. Ignore `X-Hub-Integration-Version` (debug only). |
| `202` | Accepted, **not done**: `{"request_id","status":"accepted\|queued\|in_flight","reason","poll"}` | Poll `GET /v1/requests/{request_id}` (back off, e.g. 1 s, 2 s, 5 s, ... capped) or rely on your callback subscription. |
| `4xx/5xx` | Error envelope `{ "error": { "code", "message", "retryable", "retry_after_ms", "request_id", ... } }` | Branch on `error.code` (table below), never on `message`. |

Final states from `GET /v1/requests/{id}`: `succeeded`, `failed` (sync error), `dead_lettered`, `expired`; intermediate:
`accepted`, `in_flight`, `retry_scheduled`, `parked` (vendor down, will drain automatically).

### 6. Errors and retries (DESIGN §4.4)
| Code | HTTP | Retry? | Caller action |
| --- | --- | --- | --- |
| `VALIDATION_FAILED` | 400 | no | Fix body; `details` lists JSON pointers |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | no | Integration moves money/quota; send an `Idempotency-Key` |
| `UNAUTHORIZED` / `FORBIDDEN` | 401/403 | no | Check key / ask owner for a grant |
| `INTEGRATION_NOT_FOUND` | 404 | no | Check id via `/v1/integrations` |
| `SESSION_NOT_FOUND` | 404 | no | Restart the multi-step flow from step 1 |
| `IDEMPOTENCY_CONFLICT` | 409 | no | Same key with a different body, or original still running; do not change the body under a key |
| `PAYLOAD_TOO_LARGE` | 413 | no | Body over 1 MB (4 MB LLM); shrink it |
| `INTEGRATION_PAUSED` | 503 | yes | Admin kill switch; retry later with backoff |
| `TRANSFORM_FAILED` | 500 | no | Hub bug; report with request id |
| `INTERNAL_ERROR` | 500 | yes | Unexpected Hub failure; retry with backoff, same key |
| `UPSTREAM_TIMEOUT` | 504 | yes | Retry with the **same** Idempotency-Key |
| `UPSTREAM_UNAVAILABLE` | 503 | yes | Wait `retry_after_ms` (circuit open), then retry same key |
| `UPSTREAM_RATE_LIMITED` | 429 | yes | Wait `retry_after_ms`; reduce rate |
| `UPSTREAM_REJECTED` | 422 | no | Vendor refused the content; fix input |
| `UPSTREAM_AUTH_FAILED` | 502 | no | Hub credential problem; platform is paged; report |
| `EXPIRED` | - | no | Async request passed TTL; decide whether to resubmit |
Retry only when `retryable: true`, with exponential backoff + jitter, bounded attempts, same
`Idempotency-Key`. Full table and headers: `references/api-quickref.md`.

## Callbacks: verify `X-Hub-Signature`
For `async_callback` integrations the Hub POSTs results to your registered endpoint with
`X-Hub-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, t + "." + raw_body)>`.
Rules: compute over the **raw body bytes** (before JSON parsing), constant-time compare, reject
if `|now - t| > 5 min`, dedupe by `request_id` / `idempotency_key` (delivery is at-least-once), return
2xx quickly. (Hex is the expected encoding; if your webhook contract says otherwise, decode per it.)

```go
func VerifyHubSignature(secret, body []byte, header string, now time.Time) error {
	var ts string
	var sigs []string
	for _, p := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok { continue }
		if k == "t" { ts = v } else if k == "v1" { sigs = append(sigs, v) }
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 { return errors.New("malformed X-Hub-Signature") }
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute { return errors.New("stale signature") }
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, s := range sigs {
		if got, err := hex.DecodeString(s); err == nil && hmac.Equal(got, want) { return nil }
	}
	return errors.New("signature mismatch")
}
```
```python
import hmac, hashlib, time
def verify_hub_signature(secret: bytes, body: bytes, header: str, now: float | None = None) -> bool:
    parts = dict(p.strip().split("=", 1) for p in header.split(",") if "=" in p)
    ts, sig = parts.get("t"), parts.get("v1")
    if not ts or not sig or not ts.isdigit(): return False
    if abs((now or time.time()) - int(ts)) > 300: return False
    want = hmac.new(secret, ts.encode() + b"." + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, sig)
```
```js
import crypto from 'node:crypto';
export function verifyHubSignature(secret, rawBody /* Buffer */, header, nowMs = Date.now()) {
  const parts = Object.fromEntries(header.split(',').map(p => p.trim().split('=', 2)));
  const ts = Number(parts.t); const sig = parts.v1;
  if (!Number.isInteger(ts) || !sig) return false;
  if (Math.abs(nowMs / 1000 - ts) > 300) return false;
  const want = crypto.createHmac('sha256', secret).update(`${parts.t}.`).update(rawBody).digest();
  const got = Buffer.from(sig, 'hex');
  return got.length === want.length && crypto.timingSafeEqual(got, want);
}
```
(Python dict keeps only the last `v1`; if the header carries several during secret rotation, check each.)
In Express use `express.raw({ type: 'application/json' })` on the callback route so you get the raw bytes.

## Go SDK: `pkg/hubclient`
Module `github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`. It returns a **202
as a distinct `Queued` result type, never as success**, plus an idempotency helper and an SSE reader.
Check exact names with `go doc github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`;
the shape is:
```go
out, err := hc.Invoke(ctx, "int_evt_brandA", body /* idempotency key option */)
// err: *hubclient.Error with Code (canonical) and Retryable
// out: either a Result (200, canonical body) or Queued (202, RequestID + Poll) - handle both branches
```
Do not collapse `Queued` into success; persist its request id and poll or await the callback.

## Requesting a new integration
If no integration fits, the platform team creates one (callers never configure vendors). Send:
- Your tenant (service name) and owning team; which environment(s).
- The capability (existing like `events.track@v1`, or a description of the new operation) and
  operations needed.
- Delivery needs: must the user wait for the answer (`sync`), or is `wait`/`async`/`async_callback`
  fine? If callbacks: your registered callback endpoint.
- Volume (peak RPS / msgs per day), latency needs, TTL for queued work.
- Whether money or vendor quota moves (idempotency becomes mandatory).
- Data classes (PII, bureau, Aadhaar, PAN) and residency constraints.
- Example canonical bodies with fake data, and any vendor/contract preference (platform decides).
Use the platform team's normal intake channel; ask the user which one their org uses.

## References
- `references/api-quickref.md` - endpoints, headers, status codes, error codes, request states.
- `references/mcp-setup.md` - add `hubmcp` to Claude Code (`claude mcp add`, `.mcp.json`), tool list.
- `references/examples.md` - `events.track@v1` end to end with curl and Go.
