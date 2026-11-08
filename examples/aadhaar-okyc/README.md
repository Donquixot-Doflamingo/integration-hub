# Example: Aadhaar OTP e-KYC through the Hub (two steps, one integration ID)

You get someone's Aadhaar details for verification in two calls to **one integration**, `int_aadhaar_okyc`:

1. **`send_otp`**: your backend sends the Aadhaar number (and the person's consent). UIDAI sends an OTP by SMS to the mobile number linked to that Aadhaar.
2. **`verify_otp`**: the person types the OTP into your app. Your backend sends it with the `X-Hub-Session` from step 1 and gets back the minimal e-KYC data (name, date of birth, gender, state, district, pincode).

You can run it in two ways: against **mockvendor** on your machine (no account needed, every error case on demand), or against a **real KYC provider's sandbox** (Cashfree Secure ID test mode). Ticket: HUB-351. PAN verification, the single-step sibling, is in [`../pan-verify/`](../pan-verify/README.md). Background on sessions: use case 6 of the [use-case catalogue](../../docs/use-cases/README.md#6-two-step-flows-otp-then-verify-then-bank-account).

**Everything here is an example.** Nothing in this folder is production config (`backend/config/`). The tests and `run.sh` merge this folder over it and refuse to overwrite any existing file. Every schema lives inline in the config, as in `backend/config` (ADR-0023 in `docs/tech/adr/`): the caller's request schema per operation in the integration version, the request the provider accepts on each vendor endpoint.

## What is possible, and what is not

| Question | Answer |
| --- | --- |
| Can we call UIDAI's own authentication or e-KYC APIs? | **No, not without a licence.** UIDAI serves its authentication and e-KYC APIs only to *requesting entities* (AUA / KUA): legal entities registered in India that meet UIDAI's eligibility criteria, apply, are approved, sign an agreement, pay fees, connect through an ASA and pass audits ([UIDAI: requesting entities](https://www.uidai.gov.in/en/ecosystem/authentication-ecosystem/authentication-requesting-agency.html), [eligibility criteria](https://www.uidai.gov.in/images/resource/eligibility_criteria_for_aua_kua_17122016.pdf), [onboarding and audit checklists](https://uidai.gov.in/images/AUA_KUA_Onboarding_Process_Application_Form_and_Audit_Compliance_Checklists.pdf)). There is no public UIDAI sandbox for us to call. |
| Then how do lenders do it? | Through a **licensed KYC provider** that offers **Aadhaar OTP-based offline e-KYC** (OKYC): the provider triggers UIDAI's OTP and fetches the signed offline e-KYC data for you. That is what this example does. Whether your use case may rely on a provider's OKYC is a compliance decision for your organisation; this example only shows the integration. |
| Can we test it for real without going live? | **Yes**: the provider's sandbox answers with deterministic test data (a fixed test Aadhaar and OTP `111000`, no SMS is sent). Real Aadhaar numbers do not work in the sandbox. |
| Does the Hub store the Aadhaar number? | No. The session keeps only the provider's reference id (the session store refuses any 12-digit number). The audit trail is **sealed** (D6): encrypted with a per-tenant key, readable only through an audited unseal. Logs never contain bodies. |

## The provider: Cashfree Secure ID (test mode)

Candidates compared on 2026-11-07 (public docs only):

| Provider | Why not / why |
| --- | --- |
| Setu OKYC | Four calls (create, captcha, OTP, complete) with a captcha image the person must solve; sandbox credentials come from Setu on request ([docs](https://docs.setu.co/data/okyc/api-integration)). |
| Sandbox.co.in (Quicko) | Needs a JWT from an extra authenticate call (refreshed daily); the Hub has no token-exchange auth scheme yet (HUB-332). Its reference pages did not load for verification. |
| Surepass, Decentro, IDfy, Signzy | No public, versioned API reference with error catalogue and sandbox test data that could be checked. |
| **Cashfree Secure ID** | **Picked.** Two calls (generate OTP, submit OTP), static header auth (`x-client-id`, `x-client-secret`) that the Hub supports today, a public OpenAPI reference with every error body, a documented sandbox host, and documented deterministic test data for Aadhaar and PAN. |

Docs every mapping here was checked against (cited in each transformer file):

- Generate OTP: <https://www.cashfree.com/docs/api-reference/vrs/v2/aadhaar/generate-otp-to-verify-aadhaar>
- Submit OTP: <https://www.cashfree.com/docs/api-reference/vrs/v2/aadhaar/submit-otp-to-verify-aadhaar>
- Test data: <https://www.cashfree.com/docs/api-reference/vrs/data-to-test-integration>
- Keys and hosts: <https://www.cashfree.com/docs/api-reference/vrs/getting-started>

### Getting sandbox keys

1. Sign up for a Cashfree merchant account and open the Secure ID dashboard: <https://merchant.cashfree.com/verificationsuite/home>.
2. Switch to **Test** mode (top right). Cashfree's test-data page says test mode must be enabled by Cashfree first: if it is not available, ask through your account manager or the support form.
3. **Developers → API Keys → Generate API Keys**, download the file: it holds the *client id* and *client secret*. Test keys work only with the sandbox host.
4. **Developers → Two-Factor Authentication**: whitelist your public IP (`curl -s https://ifconfig.me`). Cashfree requires a second factor on every environment; the other option, a signed `x-cf-signature` header, cannot be produced by the Hub today.
5. Run the demo with `HUB_DEMO_TARGET=sandbox` (below).

Sandbox test data ([source](https://www.cashfree.com/docs/api-reference/vrs/data-to-test-integration)):

| Aadhaar | OTP | Result |
| --- | --- | --- |
| `655675523712` | `111000` | verified |
| `655675523712` | `000111` | wrong OTP → 422 "The OTP does not match" |
| `655675523712` | `000222` | verification failed → 422 "The Aadhaar could not be verified" |
| `655675523710` | any | invalid Aadhaar → 422 "The Aadhaar number is not valid" |

## The flow

```mermaid
sequenceDiagram
  actor P as Person (phone)
  participant A as App
  participant B as Your backend (tenant)
  participant H as Integration Hub
  participant K as KYC provider (licensed)
  participant U as UIDAI
  P->>A: types Aadhaar number, ticks consent
  A->>B: start KYC
  B->>H: POST /v1/integrations/int_aadhaar_okyc/invoke/send_otp<br/>{"aadhaar_number","consent":true,"purpose"} + Idempotency-Key
  H->>K: generate OTP (provider format, provider credentials)
  K->>U: OTP request
  U-->>P: SMS with the OTP, to the mobile linked to the Aadhaar
  K-->>H: 200 SUCCESS, ref_id
  H->>H: session = {vendor_txn_id: ref_id}, 10 min, pinned to version + account
  H-->>B: 200 {"status":"otp_sent"} + X-Hub-Session
  B-->>A: show the OTP screen
  P->>A: types the OTP from the SMS
  A->>B: OTP
  B->>H: POST .../invoke/verify_otp {"otp"} + X-Hub-Session + Idempotency-Key
  H->>K: submit OTP with the stored ref_id (same account)
  K->>U: OTP verify + offline e-KYC
  U-->>K: signed e-KYC data
  K-->>H: 200 VALID, name, dob, gender, address, photo, XML link, ...
  H-->>B: 200 {"status":"verified","name","date_of_birth","gender","address":{state,district,pincode}}
```

**Where the OTP comes from:** UIDAI sends it by SMS to the mobile number registered with that Aadhaar, which is usually the person's phone, not the device running your app. Neither your backend nor the Hub ever sees it until the person types it in. (In the sandbox no SMS is sent; the OTP is always `111000`.)

## What the app shows between the steps, and timing

| Moment | The app shows | Rules |
| --- | --- | --- |
| Before step 1 | Aadhaar field, a consent checkbox with the purpose in plain words | `consent` must be `true` and `purpose` present, or the Hub answers 400 without calling anyone. Validate the 12 digits locally too. |
| Step 1 in flight | Spinner on **Send OTP**, button disabled | A double tap with the same `Idempotency-Key` is one SMS and returns the same session. The provider can take a few seconds (it waits for UIDAI); the Hub's timeout for these endpoints is 10 s. |
| After 200 `otp_sent` | "Enter the 6-digit OTP sent to the mobile number linked to your Aadhaar", a 6-digit input, **Resend OTP** disabled for 30 s | Store `X-Hub-Session` with the onboarding attempt. Do not show the Aadhaar number again; mask it (`XXXX XXXX 3712`) if you show it. |
| Wrong OTP (422 "The OTP does not match") | "Wrong OTP, try again", keep the input | Retry `verify_otp` in the **same session** with a **new** `Idempotency-Key`. The provider and UIDAI limit attempts; when they refuse more, the answer is another rejection or a 429. |
| OTP expired (422 "The OTP has expired; request a new one") | "OTP expired", enable **Resend OTP** | Resend = step 1 again with a new key: a new SMS and a new session. |
| Resend too soon (429, `retry_after_ms: 30000`) | "Wait 30 s before asking again" | The provider refuses a second OTP for the same Aadhaar within 30 s. |
| Hub session expired (404 `SESSION_NOT_FOUND`) | "This took too long, start again" | The Hub keeps a session 10 minutes; after that, nothing is sent upstream. Restart from step 1. |
| Provider down (503 `UPSTREAM_UNAVAILABLE`, `retryable: true`) | "Service busy, try again" | Retry with the same session **and the same key**. The circuit breaker answers at once (fail fast) when the provider keeps failing; an OTP is never queued. |
| 200 `verified` | Prefill name and date of birth, ask the person to confirm | The response is minimal on purpose; the photo and full address never reach your backend. |

The OTP's own validity is decided by UIDAI and is not returned by the provider. Treat the Hub session (10 minutes) as the upper bound and let the person resend after 30 seconds.

## Run it

Prerequisites: Go, curl, python3, and Postgres + Redis (`docker compose -f deploy/docker-compose.yml up -d`). From the repository root:

```bash
# 1. Against mockvendor (default): no account, no network. Shows step 1, the OTP screen, step 2.
examples/aadhaar-okyc/run.sh

# Other scenarios: one file each in mockvendor/ (aadhaar-<scenario>.json)
examples/aadhaar-okyc/run.sh wrong-otp       # 422 The OTP does not match
examples/aadhaar-okyc/run.sh otp-expired     # 422 The OTP has expired; request a new one
examples/aadhaar-okyc/run.sh invalid         # 422 The Aadhaar number is not valid (no session)
examples/aadhaar-okyc/run.sh rate-limited    # 429 retry_after_ms 30000
examples/aadhaar-okyc/run.sh provider-down   # 503 during verify, retryable

# 2. Against the provider's sandbox: switch with one environment variable.
HUB_DEMO_TARGET=sandbox CASHFREE_CLIENT_ID=... CASHFREE_CLIENT_SECRET=... examples/aadhaar-okyc/run.sh
#    It asks for the OTP: type 111000 (or 000111 to see a wrong OTP). AADHAAR=655675523710 for an invalid one.

# 3. Keep the Hub running and call it yourself (curl or the Go client below).
examples/aadhaar-okyc/run.sh serve                        # add HUB_DEMO_TARGET=sandbox ... for the sandbox
```

`run.sh` builds the binaries, merges this folder over `backend/config` into `.demo/aadhaar-okyc/`, stores the credentials encrypted there (`hubctl creds set`), starts mockvendor on `127.0.0.1:18099` (mock target only; not the dev stack's 8099, which the examples never touch) and a gateway on `127.0.0.1:18080`, then makes the calls. `HUB_DEMO_TARGET=sandbox` changes only the account's region from `mock` to `sandbox`, which picks the vendor's sandbox base URL; nothing else changes, and nothing the caller sends or receives changes either. Override `HUB_PG_DSN`, `HUB_REDIS_ADDR`, `HUB_DEMO_HTTP`, `MOCKVENDOR_ADDR` if your ports differ. If a port is already in use, `run.sh` stops with a message naming it instead of starting; a gateway or mockvendor left by a killed run is stopped by the next run. (`hubctl creds set` takes values as arguments, so the test secret is briefly visible in your process list: use test keys only.)

### With curl

```bash
# Step 1
curl -i http://127.0.0.1:18080/v1/integrations/int_aadhaar_okyc/invoke/send_otp \
  -H 'Authorization: Bearer hk_test_example_kyc_onboarding_local_only' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: onboarding-77-send' \
  -d '{"aadhaar_number": "655675523712", "consent": true, "purpose": "KYC for a loan application"}'

# Step 2: X-Hub-Session from step 1's response headers
curl -i http://127.0.0.1:18080/v1/integrations/int_aadhaar_okyc/invoke/verify_otp \
  -H 'Authorization: Bearer hk_test_example_kyc_onboarding_local_only' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: onboarding-77-verify-1' \
  -H 'X-Hub-Session: ses_...' \
  -d '{"otp": "111000"}'
```

### With the Go SDK

[`client/main.go`](client/main.go) makes the same two calls with `backend/pkg/hubclient` (`WithOperation`, `WithIdempotencyKey`, `WithSession`, typed `*hubclient.Error`):

```bash
examples/aadhaar-okyc/run.sh serve                             # terminal 1
cd backend && go run ../examples/aadhaar-okyc/client/main.go   # terminal 2 (OTP=111000 to skip the prompt)
```

```go
out, err := hub.Invoke(ctx, "int_aadhaar_okyc",
    map[string]any{"aadhaar_number": aadhaar, "consent": true, "purpose": "KYC for a loan application"},
    hubclient.WithOperation("send_otp"), hubclient.WithIdempotencyKey(attempt+"-send"))
session := out.(*hubclient.Result).Session                     // keep it with the onboarding attempt
// ... the person types the OTP ...
out, err = hub.Invoke(ctx, "int_aadhaar_okyc", map[string]any{"otp": otp},
    hubclient.WithOperation("verify_otp"), hubclient.WithSession(session),
    hubclient.WithIdempotencyKey(attempt+"-verify-1"))
var he *hubclient.Error
if errors.As(err, &he) && he.Code == hubclient.CodeUpstreamRejected { /* show he.Message */ }
```

## The exchange (captured from `run.sh` against mockvendor)

```http
POST /v1/integrations/int_aadhaar_okyc/invoke/send_otp
Authorization: Bearer hk_test_example_kyc_onboarding_local_only
Idempotency-Key: okyc-send-1794003165-18953

{"aadhaar_number": "655675523712", "consent": true, "purpose": "KYC for a loan application (demo)"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6SEQRDYMN83WG81JWMHXJC4
X-Hub-Session: ses_01M6SEQRDZNDQ9W9CBSVWZ6R3X

{"status": "otp_sent", "vendor_ref": "21637861"}
```

The provider received `POST /verification/offline-aadhaar/otp` with headers `x-client-id`, `x-client-secret` and the body `{"aadhaar_number": "655675523712"}` only: consent and purpose stay in the Hub's sealed audit (the provider's API has no field for them). The session row holds `{"vendor_txn_id": "21637861"}` and nothing else.

```http
POST /v1/integrations/int_aadhaar_okyc/invoke/verify_otp
Idempotency-Key: okyc-verify-1794003165-18953
X-Hub-Session: ses_01M6SEQRDZNDQ9W9CBSVWZ6R3X

{"otp": "111000"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6SEQRFJGBHN7NZBM4EFWEMD

{"status": "verified", "name": "Mallesh Fakkirappa Dollin", "date_of_birth": "1995-02-02", "year_of_birth": 1995,
 "gender": "male", "address": {"state": "Karnataka", "district": "Haveri", "pincode": "581115"}, "vendor_ref": "21637861"}
```

The provider received `{"otp": "111000", "ref_id": "21637861"}`: the reference came from the session, the caller never handled it. The provider's answer also had the photo, street address, care-of, e-mail, mobile hash, an XML download link and a share code; the response transformer drops all of them.

## Errors, mapped to canonical codes

| Provider answer (documented) | Caller gets |
| --- | --- |
| 200 `INVALID` "Invalid Aadhaar Card"; 400 `aadhaar_invalid` / `aadhaar_empty`; 400 "Invalid Aadhaar Card" on verify | 422 `UPSTREAM_REJECTED` "The Aadhaar number is not valid" |
| 200 `SUCCESS` "Aadhaar not linked to mobile number" (no OTP was sent!); same text as a 400 on verify | 422 `UPSTREAM_REJECTED` "No mobile number is linked to this Aadhaar…" |
| 400 "OTP entered is invalid", 400 `otp_invalid` | 422 `UPSTREAM_REJECTED` "The OTP does not match" |
| 400 "Otp expired", 400 "Session expired, please generate a new OTP" | 422 `UPSTREAM_REJECTED` "The OTP has expired; request a new one" |
| 409 `verification_pending` (second OTP within 30 s, or still processing) | 429 `UPSTREAM_RATE_LIMITED`, `retry_after_ms: 30000` |
| 429 `too_many_requests_per_operation` / `_per_ip` | 429 `UPSTREAM_RATE_LIMITED`, `retry_after_ms` from `Retry-After`, else 5000 |
| 401 `authentication_failed`, 403 `ip_validation_failed`, 400 `x-client-id_missing` / `x-client-secret_value_invalid` | 502 `UPSTREAM_AUTH_FAILED` (our configuration; pages on-call) |
| 422 `insufficient_balance` (prepaid balance ran out) | 503 `UPSTREAM_UNAVAILABLE`, retryable |
| 500 `verification_failed` / `api_error`, 502 source down, a non-JSON 200 | 503 `UPSTREAM_UNAVAILABLE`, retryable |
| Anything else 4xx | 422 `UPSTREAM_REJECTED` "The OTP could not be sent" / "The Aadhaar could not be verified" |
| Provider slower than 10 s | 504 `UPSTREAM_TIMEOUT` |

Messages are fixed, vendor-neutral texts: the provider's own messages can name it, echo the caller's IP or link its docs. Every row has a golden fixture in `config/transformers/*/fixtures/`. Branch on `code`; the text tells wrong from expired today, and a machine-readable `reason` for it is HUB-346.

## What is in this folder

| Path | What |
| --- | --- |
| `config/vendors/cashfree-sid.yaml` | The provider: regions `mock` and `sandbox` (production is set by the control plane), static header auth, endpoints `okyc_otp@1` (body `aadhaar_number`), `okyc_verify@1` (body `otp`, `ref_id`; 10 s timeouts) with the `request_schema` every request sample must fit (the credential headers are never part of it), breaker, a health probe that sends `{}` without credentials (a 400, so no OTP and no charge) |
| `config/accounts/cf-sid-test.yaml` | The test-mode account (region `mock`; `run.sh` switches it to `sandbox`) |
| `config/tenants/kyc-onboarding.yaml` | The calling service; local test key `hk_test_example_kyc_onboarding_local_only` (stored as its sha256) |
| `config/integrations/int_aadhaar_okyc/` | `interactive`, `sync`, `fail_fast`, `moves_money_or_quota: true`, audit `sealed` for `90d` (D6). Version 1 declares the operations and their request schemas: `send_otp` (`aadhaar_number` tagged `x-pii: government_id`, `consent: true`, `purpose`) and `verify_otp` (`otp`). `send_otp` uses the default pair, `verify_otp` its own |
| `config/transformers/CF-OKYC-*` | Four transformers with goldens for success and every documented error (42 cases) |
| `mockvendor/aadhaar-*.json` | Scenario files: the provider's two endpoints as mockvendor routes. `run.sh` and the integration test load the same files |
| `config/integrations/int_pan_verify/`, `config/transformers/CF-PAN-*`, `mockvendor/pan-*.json` | PAN verification on the same vendor and account: see [`../pan-verify/`](../pan-verify/README.md) (HUB-352) |
| `run.sh`, `client/main.go` | The demo (curl) and the Go SDK client |

## Tests

```bash
cd backend
go test -race -count=1 -run 'TestExampleKYC' ./internal/edge/            # config + all goldens + scenario files
export HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub?sslmode=disable' HUB_REDIS_ADDR=localhost:6379
go test -race -count=1 -tags integration -run TestExampleAadhaarOKYCFlow -v ./internal/edge/
```

`TestExampleAadhaarOKYCFlow` (`backend/internal/edge/example_kyc_integration_test.go`) runs the gateway against mockvendor (every vendor region, the sandbox one included, points at mockvendor: tests never call the real provider). It proves: the happy path with the exact provider requests, the session holding only the reference, the minimal response, sealed audit (no Aadhaar number or OTP stored in clear, sealed copies open with the tenant key, no credential inside); wrong OTP then right OTP in the same session; provider-expired OTP; invalid Aadhaar (200 `INVALID`) opens no session; rate limit; provider down then retry; double tap (one provider call); expired Hub session; edge refusals (no consent, malformed Aadhaar, 5-digit OTP, extra fields, no key) with no provider call. Every response is validated against `contracts/openapi/hub-public.v1.yaml` and checked for the provider's name, the account and the credentials.

## Going to production

- A production account of the same vendor (production keys, region set by the control plane), with its own integration (same operations and request schemas, its own audit policy review).
- Compliance sign-off for the audit policy: `sealed` keeps an encrypted copy of the request, which contains the Aadhaar number. If even that is not acceptable, use `metadata_only` (open question in the use-case catalogue, HUB-337 for KMS-backed keys).
- `verify_otp` without `X-Hub-Session` is treated as a first step and reaches the provider without a reference (it rejects it). Always send the session. For the same reason `CF-OKYC-VERIFY-REQ` has no sample without a session: `okyc_verify@1` requires `ref_id`, and the Hub refuses a sample that breaks its endpoint's `request_schema`.
