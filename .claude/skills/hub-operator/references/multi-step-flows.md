# Multi-step flows: operations and `X-Hub-Session`

**Question teams ask:** "Aadhaar e-KYC is two steps: get an OTP, then verify it. Is that two calls on one integration?"

**Yes. One integration id, one capability with two operations, two calls.** The caller calls `POST /v1/integrations/{id}/invoke/send_otp`, gets an `X-Hub-Session` header back, and sends it on `POST /v1/integrations/{id}/invoke/verify_otp`. The Hub keeps the vendor's transaction id in that session for 10 minutes and routes step 2 to **the same account and integration version** that sent the OTP, even if you publish a new version in between (DESIGN §6.3, §14).

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

| Piece | Example (`examples/otp-account-flow/`) | Notes |
| --- | --- | --- |
| Capability with operations | `demo.mobile_otp@v1`: `send_otp` (`mobile` → `otp_sent`), `verify_otp` (`mobile`, `otp` → `verified`), flagged `interactive` | A request and a response schema per operation. Production capabilities live in `contracts/` and are added by the contract-steward (the Aadhaar one, `kyc.aadhaar_otp@v1`, is HUB-334) |
| One vendor endpoint per step | `demo-kyc.otp_send@1` (`POST /v1/otp/send`), `demo-kyc.otp_verify@1` (`POST /v1/otp/verify`) | [vendors-and-accounts.md](vendors-and-accounts.md#add-or-change-an-endpoint) |
| One transformer pair per operation | `DEMO-OTP-SEND-REQ/RES@1` (`operation: send_otp`), `DEMO-OTP-VERIFY-REQ/RES@1` (`operation: verify_otp`) | Step 1's response transformer has a `session:` section; step 2's request transformer reads `session.*` |
| Integration | `int_demo_otp`, `sync`, `on_circuit_open: fail_fast`, `moves_money_or_quota: true` | Interactive capabilities must be `sync` (a 202 is never an answer to a person waiting); an OTP costs quota, so callers must send `Idempotency-Key` |
| Audit | `masked` in the demo; **`sealed`** for Aadhaar, PAN, bureau (D6) | Compliance may require `metadata_only` for Aadhaar (open question in `docs/use-cases/README.md` §6) |

## The transformers

Step 1 response (`DEMO-OTP-SEND-RES@1`): map the answer and keep **only** the vendor txn id in the session.

```yaml
transformer: DEMO-OTP-SEND-RES
version: 1
kind: response
capability: demo.mobile_otp@v1
operation: send_otp
response:
  status:       '"otp_sent"'
  expires_in_s: res.body.expires_in
  vendor_ref:   res.body.ref
session:
  vendor_txn_id: res.body.txn_id      # opens the session; evaluated on success only
errors: [...]
```

Step 2 request (`DEMO-OTP-VERIFY-REQ@1`): read the txn id back from the session.

```yaml
transformer: DEMO-OTP-VERIFY-REQ
version: 1
kind: request
capability: demo.mobile_otp@v1
operation: verify_otp
vendor_endpoint: demo-kyc.otp_verify@1
request:
  method: POST
  path: /v1/otp/verify
  headers:
    Content-Type: '"application/json"'
    X-Client-Id:  account.config.client_id
  body:
    mobile: req.mobile
    txn_id: session.vendor_txn_id
    otp:    req.otp
```

Step 2 response (`DEMO-OTP-VERIFY-RES@1`) maps wrong and expired OTPs to `UPSTREAM_REJECTED` with fixed, vendor-neutral messages (`"The OTP does not match"`, `"The OTP has expired; request a new one"`); it has no `session:` section (later steps never open a session). A transformer error rule cannot set the machine-readable `reason` yet (HUB-346), so callers tell wrong from expired only by `message`.

The `session:` section may read `res`, `req` and `account`, never `creds`. The session store refuses anything that looks like an Aadhaar number (12 digits, with or without spaces or hyphens): such a call fails with `TRANSFORM_FAILED` and opens no session. Keep the session to the vendor txn id.

## The integration version

Each target has a **default pair** (`transformer`, used by every operation not listed) and a pair per listed operation (`operations`, admin contract 1.2.0):

```json
"targets": [{
  "vendor_id": "demo-kyc", "account_id": "demo-kyc-a",
  "transformer": { "request": "DEMO-OTP-SEND-REQ@1",   "response": "DEMO-OTP-SEND-RES@1" },
  "operations":  { "verify_otp": { "request": "DEMO-OTP-VERIFY-REQ@1", "response": "DEMO-OTP-VERIFY-RES@1" } },
  "weight": 100
}],
"delivery": { "mode": "sync", "on_circuit_open": "fail_fast" },
"audit": { "payloads": "sealed" }
```

The same selection applies to failover targets, shadow, dry-run and queued work. Publish refuses a version where an operation would run a pair compiled for another operation (check `operations_have_transformers`), so step 2 can never silently run step 1's mapping. Create, validate and publish it with the loop in [cutover.md](cutover.md#draft-validate-diff-publish) (console, hubctl, or `hub_admin_create_integration` + `hub_admin_create_integration_draft` + `hub_admin_validate_integration_version` + `hub_admin_publish_integration_version`).

## What the caller does

Two calls, the session id carried between them (full caller guide: the consumer skill `clients/claude-skill/integration-hub/`, reference `examples.md`):

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

- Goldens per transformer: step 2 request fixtures set `"_context": {"session": {"vendor_txn_id": "..."}}`; step 1 response fixtures expect `{"response": ..., "session": {...}}`.
- Transformer dry-run with a session: `hub_admin_dry_run_transformer {"ref": "DEMO-OTP-VERIFY-REQ@1", "account_id": "demo-kyc-a", "input": {"mobile": "+919812345678", "otp": "482913"}, "session": {"vendor_txn_id": "dk_txn_7f3a"}}`.
- Caller dry-run of one operation: `POST /v1/integrations/int_demo_otp/dry-run?operation=verify_otp` with `X-Hub-Session` shows the request the pinned account would get (MCP: `hub_dry_run` with `operation` and `session`).
- Never test with a real Aadhaar number or phone; use `mockvendor` or the provider's sandbox.

## Runnable examples

- `examples/otp-account-flow/`: OTP send → verify → bank account check, through the real gateway against `mockvendor`, with every failure case (test `TestExampleOTPAccountFlow`).
- `examples/aadhaar-okyc/`: Aadhaar OTP e-KYC (`int_aadhaar_okyc`, operations `send_otp` and `verify_otp`) against a sandbox provider played by `mockvendor`.
- `docs/use-cases/README.md` §6: the UX journey, sequence diagram and failure table.
