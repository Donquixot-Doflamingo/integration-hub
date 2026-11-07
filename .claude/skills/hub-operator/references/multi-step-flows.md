# Multi-step flows: operations and `X-Hub-Session`

**Question teams ask:** "Aadhaar e-KYC is two steps: get an OTP, then verify it. Is that two calls on one integration?"

**Yes. One integration id with two operations, two calls.** The caller calls `POST /v1/integrations/{id}/invoke/send_otp`, gets an `X-Hub-Session` header back, and sends it on `POST /v1/integrations/{id}/invoke/verify_otp`. The Hub keeps the vendor's transaction id in that session for 10 minutes and routes step 2 to **the same account and integration version** that sent the OTP, even if you publish a new version in between (DESIGN §6.3, §14).

The Hub handles each call; **the calling backend owns the journey** (which screen the user is on, when to resend). The Hub is not a workflow engine (PRD non-goal).

## Contents
- [What the operator configures](#what-the-operator-configures)
- [The transformers](#the-transformers)
- [The integration version](#the-integration-version)
- [What the caller does](#what-the-caller-does)
- [Session rules](#session-rules)
- [Testing and dry-run](#testing-and-dry-run)
- [Runnable examples](#runnable-examples)

## What the operator configures

| Piece | Example (`examples/aadhaar-okyc/`, `int_aadhaar_okyc`) | Notes |
| --- | --- | --- |
| Integration, created with `interactive: true` | `int_aadhaar_okyc`, `moves_money_or_quota: true` | `interactive` is fixed at creation (console checkbox **Interactive**, MCP `hub_admin_create_integration {"interactive": true}`). Interactive integrations must be `sync` (a 202 is never an answer to a person waiting); an OTP costs quota, so callers must send `Idempotency-Key` |
| Two operations on the integration version, each with its request schema | `send_otp` (`aadhaar_number`, `consent`, `purpose`, all required), `verify_otp` (`otp`, required) | Built in the draft form (**Add operation**, then **Paste a sample** → **Generate fields** per operation). Response schemas do not exist: each step returns what its response transformer produces |
| One vendor endpoint per step, each with its request schema | `cashfree-sid.okyc_otp@1` (`POST /offline-aadhaar/otp`, body `aadhaar_number` required), `cashfree-sid.okyc_verify@1` (`POST /offline-aadhaar/verify`, body `otp` and `ref_id` required) | [vendors-and-accounts.md](vendors-and-accounts.md#add-or-change-an-endpoint) |
| One transformer pair per operation | `CF-OKYC-OTP-REQ/RES@1` (step 1), `CF-OKYC-VERIFY-REQ/RES@1` (step 2) | Transformers name no operation: the target's pairs decide which runs where. Step 1's response transformer has a `session:` section; step 2's request transformer reads `session.*` |
| Audit | **`sealed`**, 90 days (D6: Aadhaar, PAN, bureau); `masked` in the `otp-account-flow` demo | Compliance may require `metadata_only` for Aadhaar (open question in `docs/use-cases/README.md` §6) |

## The transformers

Step 1 request (`CF-OKYC-OTP-REQ@1`): reads `req.aadhaar_number`, which `send_otp`'s request schema must declare. `consent` and `purpose` stay in the Hub (sealed audit): the provider has no field for them.

```yaml
transformer: CF-OKYC-OTP-REQ
version: 1
kind: request
docs_url: https://www.cashfree.com/docs/api-reference/vrs/v2/aadhaar/generate-otp-to-verify-aadhaar
vendor_endpoint: cashfree-sid.okyc_otp@1
request:
  method: POST
  path: /offline-aadhaar/otp
  headers:
    Content-Type:    '"application/json"'
    X-Client-Id:     creds.client_id
    X-Client-Secret: creds.client_secret
  body:
    aadhaar_number: req.aadhaar_number
```

Step 1 response (`CF-OKYC-OTP-RES@1`): map the answer and keep **only** the provider's reference id in the session.

```yaml
transformer: CF-OKYC-OTP-RES
version: 1
kind: response
response:
  status:     '"otp_sent"'
  vendor_ref: 'replace(toJSON(res.body.ref_id), "\"", "")'
session:
  vendor_txn_id: 'replace(toJSON(res.body.ref_id), "\"", "")'   # opens the session; evaluated on success only
errors: [...]
```

Step 2 request (`CF-OKYC-VERIFY-REQ@1`): reads `req.otp` (declared by `verify_otp`) and the reference id back from the session. The caller never sees or sends it.

```yaml
transformer: CF-OKYC-VERIFY-REQ
version: 1
kind: request
docs_url: https://www.cashfree.com/docs/api-reference/vrs/v2/aadhaar/submit-otp-to-verify-aadhaar
vendor_endpoint: cashfree-sid.okyc_verify@1
request:
  method: POST
  path: /offline-aadhaar/verify
  headers:
    Content-Type:    '"application/json"'
    X-Client-Id:     creds.client_id
    X-Client-Secret: creds.client_secret
  body:
    otp:    req.otp
    ref_id: session.vendor_txn_id
```

(The response transformers above are shortened; the example's files hold the full error rules.) Step 2's response transformer maps wrong and expired OTPs to `UPSTREAM_REJECTED` with fixed, vendor-neutral messages (`"The OTP does not match"`, `"The OTP has expired; request a new one"`); it has no `session:` section (later steps never open a session). A transformer error rule cannot set the machine-readable `reason` yet (HUB-346), so callers tell wrong from expired only by `message`.

The `session:` section may read `res`, `req` and `account`, never `creds`. The session store refuses anything that looks like an Aadhaar number (12 digits, with or without spaces or hyphens): such a call fails with `TRANSFORM_FAILED` and opens no session. Keep the session to the vendor txn id.

## The integration version

The version declares both **operations** with their request schemas. Each target has a **default pair** (`transformer`, used by every operation not listed) and a pair per listed operation (`operations`):

```json
"operations": {
  "send_otp": {
    "description": "Send an OTP to the mobile number linked to the Aadhaar number. Opens a session (X-Hub-Session).",
    "idempotent": false,
    "request_schema": { "body": {
      "type": "object", "required": ["aadhaar_number", "consent", "purpose"], "additionalProperties": false,
      "properties": {
        "aadhaar_number": { "type": "string", "pattern": "^[2-9][0-9]{11}$", "x-pii": "government_id" },
        "consent":        { "type": "boolean", "const": true },
        "purpose":        { "type": "string", "minLength": 3, "maxLength": 200 } } } }
  },
  "verify_otp": {
    "description": "Verify the OTP inside the session send_otp opened and return the minimal e-KYC data.",
    "idempotent": false,
    "request_schema": { "body": {
      "type": "object", "required": ["otp"], "additionalProperties": false,
      "properties": { "otp": { "type": "string", "pattern": "^[0-9]{6}$", "x-pii": "free_text" } } } }
  }
},
"targets": [{
  "vendor_id": "cashfree-sid", "account_id": "cf-sid-test",
  "transformer": { "request": "CF-OKYC-OTP-REQ@1",    "response": "CF-OKYC-OTP-RES@1" },
  "operations":  { "verify_otp": { "request": "CF-OKYC-VERIFY-REQ@1", "response": "CF-OKYC-VERIFY-RES@1" } },
  "weight": 100
}],
"delivery": { "mode": "sync", "on_circuit_open": "fail_fast" },
"audit": { "payloads": "sealed", "retention_ms": 7776000000 }
```

`validate_input` is left at its default (`true`), so a missing `consent` or a malformed Aadhaar number is refused with 400 `VALIDATION_FAILED` before any vendor call. The same pair selection applies to failover targets, shadow, dry-run and queued work. Validate checks each pair against its own operation: step 2's request transformer may read only fields `verify_otp` declares (`schemas_compile`), its samples' inputs must fit `verify_otp` (`input_schema`), and a target `operations` key that the version does not declare fails `operations_have_transformers`. A pair with no response transformer would pass the vendor's answer through unchanged; for OTP flows keep response transformers, so wrong and expired OTPs map to canonical errors. Create, validate and publish it with the loop in [cutover.md](cutover.md#draft-validate-diff-publish) (console, hubctl, or `hub_admin_create_integration` with `interactive: true` + `hub_admin_create_integration_draft` + `hub_admin_validate_integration_version` + `hub_admin_publish_integration_version`).

## What the caller does

Two calls, the session id carried between them (full caller guide: the consumer skill `clients/claude-skill/integration-hub/`, reference `examples.md`). The calls below use the demo flow of `examples/otp-account-flow/` (`int_demo_otp`, operations `send_otp` with `mobile` and `verify_otp` with `mobile` and `otp`); the Aadhaar flow is the same with its own bodies:

```http
POST /v1/integrations/int_demo_otp/invoke/send_otp
Idempotency-Key: otp-send-app-77
{"mobile": "+919812345678"}

200 OK
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED
{"status": "otp_sent", "expires_in_s": 300, "vendor_ref": "dk_ref_0001"}
```

```http
POST /v1/integrations/int_demo_otp/invoke/verify_otp
Idempotency-Key: otp-verify-app-77-1
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED
{"mobile": "+919812345678", "otp": "482913"}

200 OK
{"status": "verified", "vendor_ref": "dk_ref_0002"}
```

| Situation | Caller gets | Caller does |
| --- | --- | --- |
| Wrong OTP | 422 `UPSTREAM_REJECTED`; the session stays open | Retry verify with the same session and a new `Idempotency-Key` |
| OTP expired at the vendor | 422 `UPSTREAM_REJECTED` | Resend = call `send_otp` again (new session) |
| Hub session expired (> 10 min), wrong, or from another tenant or integration | 404 `SESSION_NOT_FOUND`, no vendor call | Restart from `send_otp` |
| Double tap on Send | Same 200 and same `X-Hub-Session`, `X-Hub-Idempotent-Replay: true`; one SMS | Reuse the same key for the same attempt |
| Vendor down | 503 `UPSTREAM_UNAVAILABLE` (retryable); the session survives | Retry with the same session and the same key |
| No `Idempotency-Key` | 400 `IDEMPOTENCY_KEY_REQUIRED` | Fix the caller |
| Unknown operation, or `/invoke` without one | 404 `OPERATION_NOT_FOUND` | Use the operation names from discovery |

## Session rules

- Opened by a step whose response transformer has a `session:` section, when the vendor answered successfully. A call without `X-Hub-Session` is always a step 1.
- Holds only what the transformer maps (≤ 16 KB, never credentials).
- Pinned to tenant, integration, integration version and account. Publishing or switching accounts does not move open sessions; users who start after the publish use the new version.
- Lifetime 10 minutes, fixed (not configurable per integration today). The scheduler deletes expired sessions.
- An account cannot be retired while sessions are open on it.
- Cost: one Postgres primary-key read, only on requests that carry `X-Hub-Session`.

## Testing and dry-run

- Samples per transformer: step 2 request samples set `"_context": {"session": {"vendor_txn_id": "..."}}`; step 1 response samples expect `{"response": ..., "session": {...}}`. Each request sample's input must be a valid input of the operation the transformer serves.
- Transformer dry-run with a session: `hub_admin_dry_run_transformer {"ref": "CF-OKYC-VERIFY-REQ@1", "account_id": "cf-sid-test", "input": {"otp": "111000"}, "session": {"vendor_txn_id": "1234567"}}`.
- Caller dry-run of one operation: `POST /v1/integrations/int_demo_otp/dry-run?operation=verify_otp` with `X-Hub-Session` shows the request the pinned account would get (MCP: `hub_dry_run` with `operation` and `session`).
- Never test with a real Aadhaar number or phone; use `mockvendor` or the provider's sandbox.

## Runnable examples

- `examples/otp-account-flow/`: OTP send → verify → bank account check, through the real gateway against `mockvendor`, with every failure case (test `TestExampleOTPAccountFlow`).
- `examples/aadhaar-okyc/`: Aadhaar OTP e-KYC (`int_aadhaar_okyc`, operations `send_otp` and `verify_otp` on the integration) against a sandbox provider played by `mockvendor`.
- `docs/use-cases/README.md` §6: the UX journey, sequence diagram and failure table.
