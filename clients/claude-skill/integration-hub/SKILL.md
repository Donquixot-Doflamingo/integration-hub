---
name: integration-hub
description: Call third-party APIs (engagement events, SMS/WhatsApp, credit bureau, KYC/PAN/Aadhaar, Account Aggregator, payments, LLMs, public lookups) through the company Integration Hub instead of integrating vendors directly. Covers HUB_BASE_URL/HUB_API_KEY setup, discovering integrations with their operations and request schemas, building the body, dry-run first, invoke with Idempotency-Key, 200 vs 202 Queued and polling, pass-through answers (X-Hub-Pass-Through), two-step flows such as OTP send then verify with X-Hub-Session, error codes and retries, verifying X-Hub-Signature callbacks, the Go hubclient SDK, the hubmcp MCP tools, and requesting a new integration. Use whenever the user wants to send an event or message, pull a bureau report, verify PAN/Aadhaar or an OTP, call an LLM or any external API from an internal service, mentions the Hub, an integration id like int_..., X-Hub-Session, hubclient, hubmcp, or a vendor SDK/API for a company service.
---

# Using the Integration Hub

> This skill is plain Markdown and works in any agent that reads `SKILL.md` folders (Claude Code,
> claude.ai, OpenAI Codex, Cursor) or as pasted instructions. Where it says "MCP tools", use them only
> if your client has the `hubmcp` server configured; otherwise use the HTTP calls shown.

The Hub is **one vendor-agnostic API** for every third party. You send a **stable integration ID**,
an **operation** and a **request body that matches the operation's request schema**; you get back
what the integration returns. The Hub picks vendor, account (credentials) and version, handles
retries, rate limits, circuit breakers and audit.

Each integration publishes, per operation, a **request schema**: the JSON Schema of the body (and of
any caller headers it reads). Fields in `required` must be sent; the others are optional. There is
**no response schema**: an integration either returns the output its platform team configured, or
(pass-through) the third party's own answer unchanged, marked `X-Hub-Pass-Through: true`.

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
`hub_dry_run`, `hub_invoke`, `hub_get_request`): see `references/mcp-setup.md`. `hub_invoke` takes
`integration_id`, `body`, and optionally `operation`, `headers` (only header names the operation
declares in `request_schema.headers`), `idempotency_key`, `session`, `prefer_async`, `deadline_ms`,
`confirm`; a 202 comes back as `outcome: "queued"`, never success. `pass_through: true` means
`result` is the third party's own body (on `outcome: "error"` too, with `error` null). It refuses
money/quota integrations until you pass `idempotency_key` and `confirm: true`, and you set `confirm`
only after the user said yes in this conversation.

## Workflow
### 1. Discover
```bash
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations"
curl -s -H "Authorization: Bearer $HUB_API_KEY" "$HUB_BASE_URL/v1/integrations/int_evt_brandA"
```
- `/v1/integrations` lists only what **your tenant** may invoke: id, `operations` (each with `name`,
  `description`, `idempotent` and `request_schema`), delivery mode, interactive, paused,
  `moves_money_or_quota`, `idempotency_key_required`, description.
- `/v1/integrations/{id}` adds the delivery details (wait window, TTL, what happens when the
  upstream is down, callback events). Its `operations[].request_schema.body` is the JSON Schema of
  the body; `request_schema.headers` (when present) lists the caller headers the integration reads.
  Build bodies from this schema, not from vendor docs.
- No suitable integration? See "Requesting a new integration" below. Do not work around it.

### 2. Build the body
Follow the operation's `request_schema.body` exactly: send every field in `required`, add optional
fields only when you have real values, and respect formats (E.164 phones, RFC 3339 timestamps, minor
currency units). Most operations check live calls against the schema (`validate_input`); a missing
required field or unknown field is rejected with 400 `VALIDATION_FAILED`, `details` naming each
field. If the operation declares `request_schema.headers`, send those headers too (HTTP headers, or
`headers` in `hub_invoke`); undeclared headers are not passed on.

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
- Multi-operation integrations: `POST /v1/integrations/{id}/invoke/{op}` (e.g. `send_otp`, then
  `verify_otp` with header `X-Hub-Session: <session id from step 1>`). See "Two-step flows" below.
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
| `200` | Done. The integration's output (no response schema; any JSON). | Use it. Ignore `X-Hub-Integration-Version` (debug only). |
| `202` | Accepted, **not done**: `{"request_id","status":"accepted\|queued\|in_flight","reason","poll"}` | Poll `GET /v1/requests/{request_id}` (back off, e.g. 1 s, 2 s, 5 s, ... capped) or rely on your callback subscription. |
| `4xx/5xx` | Error envelope `{ "error": { "code", "message", "retryable", "retry_after_ms", "request_id", ... } }` | Branch on `error.code` (table below), never on `message`. |
| any, with `X-Hub-Pass-Through: true` | **Pass-through**: the integration has no response transformer, so this is the third party's own status and body, unchanged (JSON or not; check `Content-Type`). Never the error envelope, even on 4xx/5xx. | Check this header **before** parsing `{"error": ...}`. A 2xx is success; a non-2xx is the third party's refusal after the Hub's retries. Only `Content-Type` and `Retry-After` come from the third party. |

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
| `PAYLOAD_TOO_LARGE` | 413 | no | Body over 1 MB; shrink it |
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
`Idempotency-Key`. A pass-through error has no `retryable`: the Hub already retried timeouts, 429
and 5xx; honour the third party's `Retry-After` if it sent one. Requests completed after a 202 report
failures with the usual `UPSTREAM_*` codes, also for pass-through integrations. Full table and
headers: `references/api-quickref.md`.

## Two-step flows (OTP send, then verify)
Some integrations have several **operations** on **one integration id**: for Aadhaar or mobile OTP,
`send_otp` then `verify_otp`. That is **two calls on the same integration**, linked by a session:

1. `POST /v1/integrations/{id}/invoke/send_otp` with an `Idempotency-Key` (an OTP costs quota, so
   the key is mandatory). The 200 response carries header **`X-Hub-Session: ses_...`**. Store it
   with the user's attempt (server side, not in the app).
2. `POST /v1/integrations/{id}/invoke/verify_otp` with header `X-Hub-Session: <that id>`, a new
   `Idempotency-Key`, and the OTP the user typed.

The Hub keeps only the vendor's transaction id in the session, for **10 minutes**, and sends step 2
to the same vendor account that sent the OTP, even if the platform switches vendors in between.
Your backend owns the journey (which screen, when to offer Resend); the Hub is not a workflow engine.

| Situation | You get | Do |
| --- | --- | --- |
| Wrong OTP | 422 `UPSTREAM_REJECTED` ("The OTP does not match"); session stays open | Let the user retry: same `X-Hub-Session`, new `Idempotency-Key` |
| OTP expired at the vendor | 422 `UPSTREAM_REJECTED` ("... request a new one") | Resend = call `send_otp` again (new session) |
| Session expired (> 10 min), wrong, or from another integration | 404 `SESSION_NOT_FOUND`, nothing sent | Restart from `send_otp` |
| Double tap on Send | Same 200 + same `X-Hub-Session`, `X-Hub-Idempotent-Replay: true`; one SMS | Reuse the key for the same attempt |
| Vendor down / circuit open | 503 `UPSTREAM_UNAVAILABLE`, `retryable: true`, maybe `retry_after_ms`; session survives | "Try again in N s"; same session, same key |
| No `Idempotency-Key` | 400 `IDEMPOTENCY_KEY_REQUIRED` | Fix the code |

Tell "wrong" from "expired" only by `message` today (a machine-readable reason is planned); never
branch on `upstream.code`. **Every `send_otp` sends a real SMS: confirm with the user before calling
it, and use fake numbers in tests.** OTP and other interactive integrations are `sync`: you never get
a 202 for them. Never log, store or put an Aadhaar number, OTP or phone number in code, tests or the
idempotency key. Full curl and Go example: `references/examples.md` ("Two-step OTP").

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
Module `github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`. `Invoke` returns an
`Outcome` that is **either `*hubclient.Result` (200, or a pass-through 2xx) or `*hubclient.Queued`
(202), never both**, and errors as `*hubclient.Error` (canonical `Code`, `Retryable`, `RetryAfter`,
`RequestID`, `Details`) or, for a pass-through non-2xx, `*hubclient.PassThroughError` (`Status`,
`ContentType`, `Body`, `RetryAfter`).
```go
hc, err := hubclient.New(os.Getenv("HUB_BASE_URL"), os.Getenv("HUB_API_KEY"))
if err != nil { return err }
out, err := hc.Invoke(ctx, "int_evt_brandA", body,
	hubclient.WithIdempotencyKey("order-"+orderID+"-created")) // also WithOperation, WithHeaders, WithSession, WithDeadline, WithPreferAsync
var hubErr *hubclient.Error
var ptErr *hubclient.PassThroughError
switch {
case errors.As(err, &ptErr):
	// pass-through: the third party refused (ptErr.Status, ptErr.Body); the Hub already retried
	return err
case errors.As(err, &hubErr):
	// branch on hubErr.Code; retry only if hubErr.Retryable, after hubErr.RetryAfter, same key
	return err
case err != nil:
	return err // transport error: retry only with the same idempotency key
}
switch o := out.(type) {
case *hubclient.Result:
	var resp MyResponse // o.PassThrough: the third party's own body; check o.ContentType first
	return o.Decode(&resp) // o.Session holds X-Hub-Session for step 1 of a two-step flow
case *hubclient.Queued:
	// NOT done: persist o.RequestID; poll hc.GetRequest(ctx, o.RequestID) until Status.Terminal(),
	// or wait for the callback
	return saveQueued(o.RequestID)
}
```
Also: `hc.GetRequest` (status; `Status.Terminal()`, `Result`, `Error`), `hc.DryRun`,
`hc.ListIntegrations` / `hc.AllIntegrations`, `hc.GetIntegration` (each with `Operations[].RequestSchema`),
`hubclient.NewIdempotencyKey()` (random; prefer a key derived from your business event),
`hubclient.IsRetryable(err)`. Check with `go doc github.com/Donquixot-Doflamingo/integration-hub/backend/pkg/hubclient`.
Do not collapse `Queued` into success. A callback verifier is not in the SDK yet: use the snippets above.

## Requesting a new integration
If no integration fits, the platform team creates one (callers never configure vendors). Send:
- Your tenant (service name) and owning team; which environment(s).
- The operations needed, each with an example body (fake data), which fields are required and
  which optional, and any caller headers the call must carry.
- What you need back: the third party's answer as it is (pass-through), or only some fields in a
  fixed shape (the platform team adds a response transformer).
- Delivery needs: must the user wait for the answer (`sync`), or is `wait`/`async`/`async_callback`
  fine? If callbacks: your registered callback endpoint.
- Volume (peak RPS / msgs per day), latency needs, TTL for queued work.
- Whether money or vendor quota moves (idempotency becomes mandatory).
- Data classes (PII, bureau, Aadhaar, PAN) and residency constraints.
- Any vendor preference (the platform decides) and a link to the third party's API docs if you have one.
The platform team can onboard a new API from its console without a code change.
Use the platform team's normal intake channel; ask the user which one their org uses.

## References
- `references/api-quickref.md` - endpoints, headers, status codes, error codes, request states.
- `references/mcp-setup.md` - add `hubmcp` to Claude Code, Claude Desktop, Codex CLI and Cursor; tenant key vs admin token; tool list.
- `references/examples.md` - event tracking end to end with curl and Go; a pass-through answer; the two-step OTP flow with curl and Go.
