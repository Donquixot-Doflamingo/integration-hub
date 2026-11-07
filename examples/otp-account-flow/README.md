# Example: OTP → verify → bank account verification

A runnable example of the UX flow "send an OTP, the user types it, then the user types a bank account", through the real Hub gateway, against `mockvendor`. It backs use case 6 of the [use-case catalogue](../../docs/use-cases/README.md#6-two-step-flows-otp-then-verify-then-bank-account). Tickets: HUB-342 (the example), HUB-343 (one operation per vendor endpoint).

**Everything here is an example.** The vendor `demo-kyc` is fictional; mockvendor plays it. Nothing in this folder is production config (`backend/config/`): the test merges this folder over it, in memory, and refuses to overwrite any existing file. Every schema lives inline in the config, as in `backend/config` (ADR-0023 in `docs/tech/adr/`): the caller's request schema per operation in the integration versions, the request the vendor accepts on each vendor endpoint.

## What is in this folder

| Path | What |
| --- | --- |
| `config/vendors/demo-kyc.yaml` | Fictional vendor, bearer auth, endpoints `otp_send@1` (`POST /v1/otp/send`), `otp_verify@1` (`POST /v1/otp/verify`) and `bank_verify@1`, each with its `request_schema` (headers `Content-Type` and `X-Client-Id`, the JSON body; the bearer token is never part of it) |
| `config/accounts/demo-kyc-a.yaml`, `demo-kyc-b.yaml` | Two accounts of that vendor (`client_id` DEMO-CLIENT-A / B) |
| `config/tenants/loan-onboarding.yaml` | The calling service; local test key `hk_test_example_loan_onboarding_local_only` (stored as its sha256) |
| `config/integrations/int_demo_otp/` | Step 1 + 2. `interactive`, `sync`, `fail_fast`, `moves_money_or_quota: true`. Each version declares the operations `send_otp` (`mobile`) and `verify_otp` (`mobile`, `otp`) with their `request_schema.body` (`x-pii` tags drive audit masking). v1 → account A, v2 → account B. Each target maps `send_otp` to the default pair (`transformer`) and `verify_otp` to its own pair (`operations.verify_otp`) |
| `config/integrations/int_demo_bank_verify/` | Step 3. `interactive`, `sync`, `fail_fast`, `moves_money_or_quota: true` (a penny drop moves money). Operation `default`: `account_number`, `ifsc`, optional `name` |
| `config/transformers/DEMO-OTP-SEND-REQ`, `DEMO-OTP-SEND-RES` | `send_otp` → `/v1/otp/send`. The response transformer's `session:` section keeps only the vendor txn id |
| `config/transformers/DEMO-OTP-VERIFY-REQ`, `DEMO-OTP-VERIFY-RES` | `verify_otp` → `/v1/otp/verify`, reading the txn id from the session; maps wrong and expired OTPs |
| `config/transformers/DEMO-BANK-REQ`, `DEMO-BANK-RES` | Bank verification mapping; the caller's `Idempotency-Key` becomes the vendor reference |
| `config/transformers/*/fixtures/` | Golden fixtures (samples). The registry runs them while building the snapshot: a failing golden, a request sample that breaks its endpoint's `request_schema` or a sample input that breaks the operation's `request_schema` refuses the whole config |

## Run it

From the repository root, with Postgres and Redis running (`docker compose -f deploy/docker-compose.yml up -d`, or local servers):

```bash
cd backend
# Goldens + config validation only (no database needed):
go test -race -count=1 -run 'TestExampleOTPConfigBuilds|TestExampleOTPRefusesOnePairForBothOperations' ./internal/edge/

# The full flow through the gateway (needs Postgres and Redis):
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub?sslmode=disable' HUB_REDIS_ADDR=localhost:6379
go test -race -count=1 -tags integration -run TestExampleOTPAccountFlow -v ./internal/edge/
```

The test lives in `backend/internal/edge/example_otp_flow_integration_test.go` (the overlay builder is in `example_otp_flow_test.go`). Every request and response is validated against `contracts/openapi/hub-public.v1.yaml`, and every response is checked for vendor or account names (`demo-kyc`, `DEMO-CLIENT`, the fake tokens): none may appear.

| Subtest | What it proves |
| --- | --- |
| happy path | Both OTP endpoints are live on mockvendor. Step 1 (`send_otp`) calls `/v1/otp/send` and opens a session holding only `{"vendor_txn_id": "dk_txn_7f3a"}` (no mobile, no OTP) on v1 / account A. v2 (account B) is published. Step 2 (`verify_otp`) with `X-Hub-Session` calls `/v1/otp/verify`, still on account A, with the stored txn id. Step 3 verifies the bank account. A new user after the publish is served by account B. |
| wrong OTP | 422 `UPSTREAM_REJECTED` "The OTP does not match"; the same session then verifies with the right OTP |
| OTP expired at the vendor | 422 `UPSTREAM_REJECTED` "The OTP has expired; request a new one" |
| expired Hub session | After 10 minutes: 404 `SESSION_NOT_FOUND`, no vendor call; step 1 again opens a new session |
| double tap on Send OTP | Same `Idempotency-Key`: one vendor call (one SMS), the same `X-Hub-Session`, `X-Hub-Idempotent-Replay: true` |
| vendor down during verify | 503 `UPSTREAM_UNAVAILABLE`, retryable; the session survives; the same key retries successfully |
| caller mistakes | No `Idempotency-Key` → 400 `IDEMPOTENCY_KEY_REQUIRED`; bad IFSC or mobile, or `verify_otp` without `otp` → 400 `VALIDATION_FAILED` (with the JSON pointer); unknown operation, or no operation (`/invoke`) → 404 `OPERATION_NOT_FOUND`; no vendor call for any of them |
| invalid bank account | 200 `{"account_status": "invalid"}`: a business answer, not an error |

## The requests

Captured from the test run (ids differ per run).

**Step 1: send the OTP.**

```http
POST /v1/integrations/int_demo_otp/invoke/send_otp
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

What the vendor received (Hub-side only, never shown to the caller): `POST /v1/otp/send`, `Authorization: Bearer <account A token>`, `X-Client-Id: DEMO-CLIENT-A`, body `{"mobile": "+919812345678"}`.

**Step 2: verify the OTP.** Send the session id back.

```http
POST /v1/integrations/int_demo_otp/invoke/verify_otp
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

The vendor received `POST /v1/otp/verify` with `{"mobile": "+919812345678", "txn_id": "dk_txn_7f3a", "otp": "482913"}` on account A, even though v2 (account B) was published between the two steps.

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

{"error":{"code":"VALIDATION_FAILED","message":"Request does not match the operation's request_schema","retryable":false,"reason":"schema","details":[{"pointer":"/ifsc","keyword":"pattern","message":"'demo123' does not match pattern '^[A-Z]{4}0[A-Z0-9]{6}$'"}],"request_id":"req_01M6S0SYK9WM441C67G649VT2W"}}
```

## One operation per step, one vendor endpoint per operation

`send_otp` and `verify_otp` are two operations of one integration, and each goes to its own vendor endpoint. An integration version target has a default transformer pair and, for a multi-operation integration, a pair per operation (HUB-343, admin contract `TargetRef.operations`, hub-admin 1.2.0):

```yaml
targets:
  - vendor: demo-kyc
    account: demo-kyc-a
    transformer: { request: DEMO-OTP-SEND-REQ@1, response: DEMO-OTP-SEND-RES@1 }       # send_otp (not listed below)
    operations:
      verify_otp: { request: DEMO-OTP-VERIFY-REQ@1, response: DEMO-OTP-VERIFY-RES@1 } # verify_otp
    weight: 100
```

The invoked operation (`/invoke/{operation}`) picks the pair: its own when listed, else the default. The same holds for session steps, failover, wait, async (the worker re-resolves at send time) and dry-run (`?operation=verify_otp`). A transformer names no operation: the registry checks each pair against the operation that uses it. The request transformer's samples must fit that operation's `request_schema`, and every `req.*` it reads must be declared there. Remove the `operations` entry above and `verify_otp` would run `DEMO-OTP-SEND-REQ@1`, whose samples do not fit `verify_otp`'s schema (no `otp`): the build fails with an `input_schema` problem on `targets[0].transformer.request` naming `operation verify_otp` (`TestExampleOTPRefusesOnePairForBothOperations`, HUB-345), instead of misrouting step 2 at request time.

Two more limits worth knowing:

- `otp_verify@1` requires `txn_id`, so `DEMO-OTP-VERIFY-REQ@1` has no sample without a session: such a sample would produce a request the vendor refuses, and the Hub refuses the sample.
- The OTP screen can tell "wrong" from "expired" only by the `message` text: transformer error rules cannot set the canonical `reason` yet (HUB-346).

## Running it against a live gateway

The `hub` binary reads one config directory (`HUB_CONFIG_DIR`). To try the example by hand, copy `backend/config` and `config/` from this folder into one temporary directory, point the variable at it, give the two `demo-kyc` accounts local credentials (a `token` value each), start `mockvendor` and program the `/v1/otp/send`, `/v1/otp/verify` and `/v1/bank-accounts/verify` routes through its control API (`contracts/openapi/mockvendor.v1.yaml`). The integration test does all of this in a few lines and is the supported way to run the example.
