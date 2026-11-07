# Vendors, endpoints and accounts

A **vendor** (some teams say "client") is the third-party API: base URL per region, endpoints, auth scheme *shape*, defaults (rate limit, breaker, health probe). Each **endpoint** version declares the request the vendor accepts (its **request schema**, ADR-0023). An **account** is one instance of the vendor: encrypted secrets (none for a public API on an auth `none` vendor), non-secret `config` (project id, brand code, client id), quota, lifecycle state (DESIGN §3.2, §8.3). Two brands on the same vendor are one vendor and two accounts.

For a complete first onboarding, follow [onboard-an-api.md](onboard-an-api.md).

## Contents
- [Before you start](#before-you-start)
- [Onboard a vendor](#onboard-a-vendor)
- [Add or change an endpoint](#add-or-change-an-endpoint)
- [Create an account](#create-an-account)
- [Change vendor config safely](#change-vendor-config-safely)
- [Change account config or quota safely](#change-account-config-or-quota-safely)
- [Rotate credentials, drain and retire](#rotate-credentials-drain-and-retire)

## Before you start

1. **Read the vendor's current API docs.** Note base URLs per region, endpoint paths and methods, auth headers, limits, error format, idempotency support. Keep the doc URL: vendors take `docs_url`, transformers must cite it.
2. **Check the auth scheme is built.** The admin API accepts `none, static_headers, bearer, basic, query_param, hmac_signing, oauth2_client_credentials, mtls`, but the runtime implements **`none`, `static_headers`, `bearer` and `basic`** today; OAuth2, HMAC signing and mTLS are HUB-332. Credential keys per scheme: `none` → no credentials at all (public APIs); `bearer` → `token`; `basic` → `username`, `password`; `static_headers` → any keys, which the request transformer maps into headers as `creds.<key>`.
3. **Write down what each endpoint accepts**: query parameters, non-credential headers, body, path parameters, and which fields are required. A sample request from the docs is enough: the console turns it into fields.
4. **Prefer config over a fork** (DESIGN §3.3): values that differ per brand go in `account.config`; only a different payload *shape* needs another transformer.

## Onboard a vendor

Role: `admin`.

- **Console:** Vendors → **Create vendor** → Vendor id, Display name, Auth scheme (**None (public API, no credentials)** for public APIs), Region code + Base URL per region, reason → **Create vendor**. Change it later with **Edit vendor**.
- **hubctl:** not supported (`apply` refuses new vendors).
- **MCP:**
  ```json
  hub_admin_create_vendor {
    "vendor_id": "clevertap", "display_name": "CleverTap", "auth_scheme": "static_headers",
    "regions": {"in": "https://in1.api.clevertap.com", "us": "https://us1.api.clevertap.com"},
    "defaults": {"rate_limit": {"per_account_rps": 50}},
    "docs_url": "https://developer.clevertap.com/docs/upload-events-api",
    "reason": "Onboard CleverTap for brand A engagement events"
  }
  ```
- **Admin API:** `POST $HUB_ADMIN_URL/admin/v1/vendors` with the same body (`createVendor`).

The Hub only lets traffic out to hosts declared in `regions` (egress allowlist). `defaults.breaker` keys are in ms in the admin API (`window_ms`, `min_calls`, `failure_ratio`, `consecutive_timeouts`, `open_ms`, `open_max_ms`, `half_open_probes`); omit them to take the platform defaults (DESIGN §10.4).

## Add or change an endpoint

Endpoint versions are immutable: posting an existing `name` creates `<name>@<n+1>`. Transformers reference an exact version (`vendor_endpoint: clevertap.upload@1`), so a new endpoint version reaches traffic only through a new transformer version and a new integration version.

Every endpoint version carries a **`request_schema`** (required): one JSON Schema 2020-12 per part, `path`, `query`, `headers` (non-credential) and `body`. Fields listed in `required` are required, the rest optional; leave out a part the endpoint does not take (`{}` means nothing besides method and path). Request transformer samples are checked against it when the transformer is created and when an integration is validated or published (`endpoint_schema`), never on live calls. Do not declare credential headers or query values set from `creds.*`. `response_schema` is optional and informational only.

- **Console:** Vendors → *vendor* → **Add endpoint** (or **New version** on an existing endpoint) → Name, Method, Path → in **Path parameters**, **Query parameters**, **Headers** and **Body**: **Paste a sample** → **Generate fields** → set each field's type and tick **required** where needed → reason → **Add endpoint**. The endpoint page shows each part as a field table and as JSON Schema.
- **MCP:**
  ```json
  hub_admin_create_vendor_endpoint {
    "vendor_id": "clevertap", "name": "upload", "method": "POST", "path": "/1/upload",
    "timeout_ms": 3000, "connect_timeout_ms": 500, "idempotent": true, "batch_max": 1000,
    "request_schema": {
      "headers": {"type": "object", "required": ["Content-Type"],
                  "properties": {"Content-Type": {"type": "string", "pattern": "^application/json"}}},
      "body": {"type": "object", "required": ["d"],
               "properties": {"d": {"type": "array", "minItems": 1, "maxItems": 1000,
                 "items": {"type": "object", "required": ["identity", "type", "evtName"],
                           "properties": {"identity": {"type": "string"}, "type": {"enum": ["event", "profile"]},
                                          "evtName": {"type": "string"}, "ts": {"type": "integer"}, "evtData": {"type": "object"}}}}}}
    },
    "reason": "Upload events endpoint"
  }
  ```
  (the dev registry's `backend/config/vendors/clevertap.yaml` holds the same schema).
- **Admin API:** `POST /admin/v1/vendors/{vendor_id}/endpoints` (`createVendorEndpoint`).
- **hubctl:** not available.

Set `idempotent: true` only if the vendor documents it: it allows one immediate retry on timeout and failover without an idempotency key. Multi-step flows need one endpoint per step (e.g. `otp_send`, `otp_verify`): see [multi-step-flows.md](multi-step-flows.md).

## Create an account

Role: `admin`.

**Public API (auth `none` vendor).** The account has no credentials. Console: Accounts → **Create account** → Account id, Vendor, Region (the form says the account has no credentials and shows no secret field) → reason → **Create account**. Admin API: `POST /admin/v1/accounts` with `{"account_id", "vendor_id", "region", "config": {}, "reason"}` and no `credentials` (or `{}`). Sending credentials to a `none` vendor is refused (400 at `/credentials`); credential rotation answers 409. MCP still has no tool (`createAccount` is forbidden there for every scheme), so hand the step to the user.

**Any other scheme: a human does this, never an AI.** The body carries secret values. MCP has no tool for it on purpose (`createAccount` is in the contract's `forbidden_operations`).

- **Console:** Accounts → **Create account** → Account id, Vendor, Region, config, quota, the credential fields of the vendor's scheme, expiry → reason → **Create account**. The values are write-only and never shown again.

- **Admin API, run by a person.** Put the body in a file only you can read, send it, delete it:
  ```bash
  umask 077 && cat > /tmp/acct.json <<'EOF'
  { "account_id": "ct-brandA-in", "vendor_id": "clevertap", "region": "in",
    "config": { "project_id": "4RW-XXX-XXXX", "brand_code": "A" },
    "quota": { "rps": 50, "daily": 5000000 },
    "credentials": { "account_id": "<from the vendor>", "passcode": "<from the vendor>" },
    "credential_expires_at": "2027-11-01T00:00:00Z",
    "reason": "Brand A India workspace" }
  EOF
  curl -sS -X POST "$HUB_ADMIN_URL/admin/v1/accounts" -H "Authorization: Bearer $HUB_ADMIN_TOKEN" \
    -H "Content-Type: application/json" -d @/tmp/acct.json && rm /tmp/acct.json
  ```
  Every credential value must be at least 6 bytes (shorter values would escape log redaction). Set `credential_expires_at` so the `CredentialExpiringSoon` alert can warn you.
- **Local dev:** add `backend/config/accounts/<id>.yaml` (`credentials_ref: localfile://<id>`, no secrets in it) and put the values in the encrypted dev file: `hubctl creds set <id> passcode=<value> account_id=<value>` (`hubctl creds init-key` once first).

Then test the credentials against the vendor: console Accounts → *account* → **Test connection**, or `testAccountConnection` (role `editor`) `POST /admin/v1/accounts/{id}/test-connection` with `{"slot": "active"}` → continue only on `"ok": true`. This makes a real vendor call, so it is not an MCP tool.

An AI assistant may prepare everything *except* the secret values (vendor, endpoints with their request schemas, transformers with samples, the account body with placeholders) and then hand the last step to the user.

## Change vendor config safely

`updateVendor` replaces regions, auth scheme and defaults (it bumps `revision`; endpoints are not edited here). It affects **every account and integration** of that vendor on the next snapshot.

1. Read it: `hub_admin_get_vendor` (or `GET /admin/v1/vendors/{id}`); note the `etag`.
2. List who is affected: `hub_admin_list_accounts {"vendor_id": "..."}` and `hub_admin_list_integrations {"vendor_id": "..."}`.
3. Show the user current vs new values and the affected integrations; wait for approval.
4. **Console:** Vendors → *vendor* → **Edit vendor**. **MCP:** `hub_admin_update_vendor` with the full replacement (`vendor_id`, `display_name`, `auth_scheme`, `regions`, optional `defaults`, `webhook_verification`, `docs_url`), `reason`, `if_match`, `confirm: true`. **Admin API:** `PUT /admin/v1/vendors/{id}`. hubctl: not available.
5. Watch the **Health** board and the request explorer for errors on that vendor.

A risky change (new base URL, new auth scheme) is safer as a **new account or vendor plus a cutover with weights** ([cutover.md](cutover.md)) than as an in-place update, because a version can be rolled back and an in-place update cannot.

## Change account config or quota safely

`config` is merge-patched (a `null` value removes a key); `vendor`, `region` and credentials are immutable here. The change hits live traffic of every integration using the account.

- **Console:** Accounts → *account* → **Edit config and quota**.
- **hubctl:** `export`, edit `accounts/<id>.yaml` (`config`, `quota` only), `plan`, `apply -reason "..." -yes`.
- **MCP:** `hub_admin_get_account` → show current and new values → after approval:
  ```json
  hub_admin_update_account {"account_id": "ct-brandA-in", "quota": {"rps": 80},
    "reason": "Vendor raised our quota to 80 rps (VND-221)", "if_match": "<etag>", "confirm": true}
  ```
- **Admin API:** `PATCH /admin/v1/accounts/{id}`.

Never put a secret in `config`: it is shown to every viewer and is not encrypted.

## Rotate credentials, drain and retire

Not MCP tools, by design. In the console: Accounts → *account* → Credentials (**Add next credential**, **Test connection**, **Promote**, delete the previous one) and Lifecycle. Follow the runbook [`docs/runbooks/credential-rotation.md`](../../../../docs/runbooks/credential-rotation.md): add `next` → test-connection on `next` → promote → watch `UPSTREAM_AUTH_FAILED` for 15 minutes → delete old. It is not a new integration version (DESIGN §6.6).

Lifecycle `active → draining → retired` (`transitionAccountLifecycle`, role `admin`): an account drains when no active integration version targets it; **retire is refused while pinned requests or open vendor sessions still reference it** (`pinned_requests`, `open_sessions` on `getAccount`). Credentials are deleted only at retire. To move traffic off an account, publish a version that no longer targets it ([cutover.md](cutover.md)) first.
