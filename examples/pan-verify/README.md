# Example: PAN verification through the Hub (one sync call)

Check that a PAN exists and is valid at the Income Tax Department, and optionally that a name matches it, with one call to `int_pan_verify`. Same provider, account and tooling as the [Aadhaar OTP e-KYC example](../aadhaar-okyc/README.md) (Cashfree Secure ID, mockvendor or its sandbox); the overlay lives in [`../aadhaar-okyc/`](../aadhaar-okyc/) because both integrations share the vendor and the account. Ticket: HUB-352. Pattern: use case 7.1 of the [use-case catalogue](../../docs/use-cases/README.md#71-sync-lookup-bureau-pull-pan-penny-drop).

**Everything here is an example**, not production config.

## The call

```mermaid
sequenceDiagram
  participant B as Your backend (tenant)
  participant H as Integration Hub
  participant K as KYC provider
  participant I as Income Tax Department
  B->>H: POST /v1/integrations/int_pan_verify/invoke {"pan","name"?} + Idempotency-Key
  H->>K: verify PAN (provider format, provider credentials, sealed audit)
  K->>I: PAN lookup
  I-->>K: status, registered name, Aadhaar link
  K-->>H: 200 {valid, pan_status, registered_name, name_match_result, ...}
  H-->>B: 200 {"pan_status","pan_masked","name_match","aadhaar_linked",...}
```

```http
POST /v1/integrations/int_pan_verify/invoke
Authorization: Bearer hk_test_example_kyc_onboarding_local_only
Content-Type: application/json
Idempotency-Key: pan-1794003571-27690

{"pan": "ABCPV1234D", "name": "John Doe"}

HTTP/1.1 200 OK
X-Hub-Request-Id: req_01M6SF44S0EZRTB834B1666ESK

{"pan_status": "valid", "pan_masked": "XXXXXX234D", "pan_type": "individual", "registered_name": "JOHN DOE",
 "name_match": true, "aadhaar_linked": true, "vendor_ref": "161"}
```

The provider received `POST /verification/pan` with `x-client-id`, `x-client-secret`, `x-api-version: 2024-08-01` and `{"pan": "ABCPV1234D", "name": "John Doe"}`. Its answer also had the name score, ITD status codes and descriptions, the name printed on the card and the last update date: the response transformer keeps only what a verification needs, and masks the PAN.

An **invalid PAN is a business answer**, not an error: `200 {"pan_status": "invalid", "pan_masked": "XXXXXX126D", "vendor_ref": "17974435"}`. `pan_status` can also be `deleted` or `deactivated`. `name_match` is `true` for a direct or good partial match, `false` for a moderate or poor partial match or no match, and `null` when no name was sent or the provider has no match result (its name match needs activation by the provider). `aadhaar_linked` is `null` for business PANs.

A retry with the **same `Idempotency-Key`** returns the stored answer with `X-Hub-Idempotent-Replay: true`: the provider is not called, or paid, twice.

## Errors

| Provider answer (documented) | Caller gets |
| --- | --- |
| 400 `pan_length_short`, `pan_missing` (the Hub's schema refuses malformed PANs first: 400 `VALIDATION_FAILED`) | 422 `UPSTREAM_REJECTED` "The PAN is not valid" |
| 401 `authentication_failed`, 403 `ip_validation_failed` or "x-cf-signature missing", 400 `x-client-id_missing` / `x-client-secret_value_invalid` | 502 `UPSTREAM_AUTH_FAILED` |
| 422 `insufficient_balance` | 503 `UPSTREAM_UNAVAILABLE`, retryable |
| 429 `too_many_requests_per_operation` / `_per_ip` | 429 `UPSTREAM_RATE_LIMITED`, `retry_after_ms` from `Retry-After`, else 5000 |
| 500, 502, an empty 200 | 503 `UPSTREAM_UNAVAILABLE`, retryable |
| Other 4xx | 422 `UPSTREAM_REJECTED` "The PAN could not be verified" |
| Slower than 5 s | 504 `UPSTREAM_TIMEOUT` |

Every row has a golden in `../aadhaar-okyc/config/transformers/CF-PAN-RES/fixtures/` (17 cases, plus 3 request cases). Docs checked on 2026-11-07: <https://www.cashfree.com/docs/api-reference/vrs/v2/pan/verify-pan-sync>, <https://www.cashfree.com/docs/secure-id/kyc-stack/verify-pan>.

## Run it

Prerequisites and sandbox keys: as in the [Aadhaar example](../aadhaar-okyc/README.md#getting-sandbox-keys). From the repository root:

```bash
examples/pan-verify/run.sh                     # mockvendor: valid PAN with a name match, then a replay
examples/pan-verify/run.sh invalid             # 200 pan_status invalid
examples/pan-verify/run.sh provider-down       # 503 UPSTREAM_UNAVAILABLE, retryable

HUB_DEMO_TARGET=sandbox CASHFREE_CLIENT_ID=... CASHFREE_CLIENT_SECRET=... examples/pan-verify/run.sh
PAN=DEFPV0126D NAME= HUB_DEMO_TARGET=sandbox CASHFREE_CLIENT_ID=... CASHFREE_CLIENT_SECRET=... examples/pan-verify/run.sh
```

Sandbox test PANs ([source](https://www.cashfree.com/docs/api-reference/vrs/data-to-test-integration)): valid individual `ABCPV1234D`, `AZJPG7110R`; valid business `ABCCD8000T`, `AAAHU4383C`, `AMJCL2021N`; invalid `DEFPV0126D`, `LMNCD8010T`. Any other value fails in the sandbox.

To call it yourself, start the Hub with `examples/aadhaar-okyc/run.sh serve` (it serves both integrations) and:

```bash
curl -i http://127.0.0.1:18080/v1/integrations/int_pan_verify/invoke \
  -H 'Authorization: Bearer hk_test_example_kyc_onboarding_local_only' \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: pan-check-77' \
  -d '{"pan": "ABCPV1234D", "name": "John Doe"}'
```

With the Go SDK: `hub.Invoke(ctx, "int_pan_verify", map[string]any{"pan": pan, "name": name}, hubclient.WithIdempotencyKey(key))` (no operation, no session).

## What is where

| Path (under `../aadhaar-okyc/`) | What |
| --- | --- |
| `config/vendors/cashfree-sid.yaml` | Endpoint `pan_verify@1` (`POST /pan`, 5 s timeout); its `request_schema` requires `Content-Type` and a body with `pan` (`name` optional) |
| `config/integrations/int_pan_verify/` | `interactive` (never queued), `sync`, `fail_fast`, `moves_money_or_quota: true`, audit `sealed` for `90d` (D6). Operation `default`, request schema: `pan` (`x-pii: government_id`), optional `name` (`x-pii: name`) |
| `config/transformers/CF-PAN-REQ`, `CF-PAN-RES` | The mapping, with goldens; the response transformer returns `pan_status`, `pan_masked`, `pan_type`, `registered_name`, `name_match`, `aadhaar_linked`, `vendor_ref` |
| `mockvendor/pan-*.json` | Scenario files (valid, invalid, provider down), shared by `run.sh` and the tests |

## Tests

`TestExamplePANVerify` (`backend/internal/edge/example_kyc_integration_test.go`): valid PAN with the exact provider request, masked PAN, dropped score and ITD codes, sealed audit (no PAN or name in clear); invalid PAN as a 200 answer without a name field sent; a replay with the same key (one provider call); provider down; edge refusals (no key, lower-case or short PAN, unknown field) with no provider call. `TestExampleKYCConfigBuilds` runs the goldens.
