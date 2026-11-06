# Example: OTP → verify → bank account verification

A runnable example of the UX flow "send an OTP, the user types it, then the user types a bank account", through the real Hub gateway, against `mockvendor`. It backs use case 6 of the [use-case catalogue](../../docs/use-cases/README.md#6-two-step-flows-otp-then-verify-then-bank-account). Ticket: HUB-342.

**Everything here is an example.** The vendor `demo-kyc` is fictional; mockvendor plays it. Nothing in this folder is production config (`backend/config/`) or a frozen contract (`contracts/`): the test merges this folder over both, in memory, and refuses to overwrite any existing file.

## What is in this folder

| Path | What |
| --- | --- |
| `contracts/schemas/capabilities/demo.mobile_otp.v1.*.json` | Example capability `demo.mobile_otp@v1` (interactive): `mobile`, optional `otp` → `otp_sent` / `verified` |
| `contracts/schemas/capabilities/demo.bank_account_verify.v1.*.json` | Example capability `demo.bank_account_verify@v1` (interactive): `account_number`, `ifsc`, `name` → `valid` / `invalid`, `name_at_bank`, `name_match` |
| `config/capabilities/` | Registry entries pointing at those schemas |
| `config/vendors/demo-kyc.yaml` | Fictional vendor, bearer auth, endpoints `otp@1` and `bank_verify@1` |
| `config/accounts/demo-kyc-a.yaml`, `demo-kyc-b.yaml` | Two accounts of that vendor (`client_id` DEMO-CLIENT-A / B) |
| `config/tenants/loan-onboarding.yaml` | The calling service; local test key `hk_test_example_loan_onboarding_local_only` (stored as its sha256) |
| `config/integrations/int_demo_otp/` | Step 1 + 2. `sync`, `fail_fast`, `moves_money_or_quota: true`. v1 → account A, v2 → account B |
| `config/integrations/int_demo_bank_verify/` | Step 3. `sync`, `fail_fast`, `moves_money_or_quota: true` (a penny drop moves money) |
| `config/transformers/DEMO-OTP-REQ`, `DEMO-OTP-RES` | OTP request/response mapping. The response transformer's `session:` section keeps only the vendor txn id |
| `config/transformers/DEMO-BANK-REQ`, `DEMO-BANK-RES` | Bank verification mapping; the caller's `Idempotency-Key` becomes the vendor reference |
| `config/transformers/*/fixtures/` | Golden fixtures. The registry runs them while building the snapshot: a failing golden refuses the whole config |

## Run it

From the repository root, with Postgres and Redis running (`docker compose -f deploy/docker-compose.yml up -d`, or local servers):

```bash
cd backend
# Goldens + config validation only (no database needed):
go test -race -count=1 -run TestExampleOTPConfigBuilds ./internal/edge/

# The full flow through the gateway (needs Postgres and Redis):
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub?sslmode=disable' HUB_REDIS_ADDR=localhost:6379
go test -race -count=1 -tags integration -run TestExampleOTPAccountFlow -v ./internal/edge/
```

The test lives in `backend/internal/edge/example_otp_flow_integration_test.go` (the overlay builder is in `example_otp_flow_test.go`). Every request and response is validated against `contracts/openapi/hub-public.v1.yaml`, and every response is checked for vendor or account names (`demo-kyc`, `DEMO-CLIENT`, the fake tokens): none may appear.

| Subtest | What it proves |
| --- | --- |
| happy path | Step 1 opens a session holding only `{"vendor_txn_id": "dk_txn_7f3a"}` (no mobile, no OTP) on v1 / account A. v2 (account B) is published. Step 2 with `X-Hub-Session` still goes to account A with the stored txn id. Step 3 verifies the bank account. A new user after the publish is served by account B. |
| wrong OTP | 422 `UPSTREAM_REJECTED` "The OTP does not match"; the same session then verifies with the right OTP |
| OTP expired at the vendor | 422 `UPSTREAM_REJECTED` "The OTP has expired; request a new one" |
| expired Hub session | After 10 minutes: 404 `SESSION_NOT_FOUND`, no vendor call; step 1 again opens a new session |
| double tap on Send OTP | Same `Idempotency-Key`: one vendor call (one SMS), the same `X-Hub-Session`, `X-Hub-Idempotent-Replay: true` |
| vendor down during verify | 503 `UPSTREAM_UNAVAILABLE`, retryable; the session survives; the same key retries successfully |
| caller mistakes | No `Idempotency-Key` → 400 `IDEMPOTENCY_KEY_REQUIRED`; bad IFSC or mobile → 400 `VALIDATION_FAILED` with the JSON pointer; unknown operation → 404 `OPERATION_NOT_FOUND`; no vendor call for any of them |
| invalid bank account | 200 `{"account_status": "invalid"}`: a business answer, not an error |

## The requests

Captured from the test run (ids differ per run).

**Step 1: send the OTP.**

```http
POST /v1/integrations/int_demo_otp/invoke
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: otp-send-app-77

{"mobile": "+919812345678"}
```

```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJ7KK3N8BBET4GQDSFD
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED
X-Hub-Served-By: primary

{"expires_in_s":300,"status":"otp_sent","vendor_ref":"dk_ref_0001"}
```

What the vendor received (Hub-side only, never shown to the caller): `POST /v1/otp`, `Authorization: Bearer <account A token>`, `X-Client-Id: DEMO-CLIENT-A`, body `{"action": "send", "mobile": "+919812345678"}`.

**Step 2: verify the OTP.** Send the session id back.

```http
POST /v1/integrations/int_demo_otp/invoke/default
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: otp-verify-app-77-2
X-Hub-Session: ses_01M6S0SYJAPG6N5XSV02NPW0ED

{"mobile": "+919812345678", "otp": "482913"}
```

```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJP9NX9AC2VV8C1EBJZ
X-Hub-Served-By: primary

{"status":"verified","vendor_ref":"dk_ref_0002"}
```

The vendor received `{"action": "verify", "mobile": "+919812345678", "txn_id": "dk_txn_7f3a", "otp": "482913"}` on account A, even though v2 (account B) was published between the two steps.

Wrong OTP:

```http
HTTP/1.1 422 Unprocessable Entity

{"error":{"code":"UPSTREAM_REJECTED","message":"The OTP does not match","retryable":false,"upstream":{"status":422,"code":null},"request_id":"req_01M6S0SYJKD148RAC5SRS01X1Q"}}
```

Expired session:

```http
HTTP/1.1 404 Not Found

{"error":{"code":"SESSION_NOT_FOUND","message":"Unknown or expired session","retryable":false,"request_id":"req_01M6S0SYK6BAD11FCVX319A0K9"}}
```

**Step 3: verify the bank account.** A separate integration; no session.

```http
POST /v1/integrations/int_demo_bank_verify/invoke
Authorization: Bearer hk_test_example_loan_onboarding_local_only
Content-Type: application/json
Idempotency-Key: bank-verify-app-77

{"account_number": "50100123456789", "ifsc": "DEMO0001234", "name": "Asha Verma"}
```

```http
HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6S0SYJX3A6VSHD1T6CWSNZR
X-Hub-Served-By: primary

{"account_status":"valid","name_at_bank":"ASHA VERMA","name_match":true,"vendor_ref":"dk_ref_0003"}
```

Bad IFSC (refused before any vendor call):

```http
HTTP/1.1 400 Bad Request

{"error":{"code":"VALIDATION_FAILED","message":"Body does not match the capability schema","retryable":false,"reason":"schema","details":[{"pointer":"/ifsc","keyword":"pattern","message":"'demo123' does not match pattern '^[A-Z]{4}0[A-Z0-9]{6}$'"}],"request_id":"req_01M6S0SYK9WM441C67G649VT2W"}}
```

## Why steps 1 and 2 share one operation

The natural shape is one integration with two operations, `send_otp` and `verify_otp`, each on its own vendor endpoint. The Hub cannot express that today: an integration version has **one** request transformer (so one vendor endpoint) for every operation of its capability (HUB-343, which first needs the admin contract change HUB-344). Such a config is even accepted today and would misroute the second operation (HUB-345).

So this example uses one operation (`default`) and a fictional vendor with one OTP endpoint: the request transformer sends `action: "send"` when there is no session and `action: "verify"` when `session.vendor_txn_id` is set. That is a stand-in to show the session mechanism end to end. **It is not the shape to ship to callers**: once HUB-343 lands, this example moves to `invoke/send_otp` and `invoke/verify_otp` with a schema per step.

Two more limits worth knowing:

- Capability schemas are read only from `contracts/` (ADR-0004). The example's schemas live in `contracts/` *inside this folder*, merged by the test. A real OTP or bank verification capability is added to the frozen contracts by the contract-steward.
- The OTP screen can tell "wrong" from "expired" only by the `message` text: transformer error rules cannot set the canonical `reason` yet (HUB-346).

## Running it against a live gateway

The `hub` binary reads one config directory (`HUB_CONFIG_DIR`) and one contracts directory (`HUB_CONTRACTS_DIR`). To try the example by hand, copy `backend/config` and `config/` from this folder into one temporary directory, `contracts/` and this folder's `contracts/` into another, point the two variables at them, give the two `demo-kyc` accounts local credentials (a `token` value each), start `mockvendor` and program the `/v1/otp` and `/v1/bank-accounts/verify` routes through its control API (`contracts/openapi/mockvendor.v1.yaml`). The integration test does all of this in a few lines and is the supported way to run the example.
