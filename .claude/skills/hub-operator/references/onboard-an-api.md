# Onboard a third-party API end to end (worked example: agify age estimate)

An operator can onboard a new API from the console alone: no code, no PR, no contract change (ADR-0023). This page walks through every step with a real public API, the age estimate of [agify.io](https://agify.io/documentation), and shows the console clicks, the hubmcp tool calls and the admin API for each. The runnable version is `examples/agify-age/`; the backend proof is `TestAgifyOnboardedWithoutCapabilities` (`backend/cmd/hub/internal/roles/agify_integration_test.go`) and the console proof is `frontend/e2e/onboarding.test.ts`.

**The goal.** Callers send `{"firstName": "Mich", "lastName": "ael"}`. The Hub calls `GET https://api.agify.io/?name=Michael`. Agify answers `{"count": 21, "name": "Michael", "age": 57}`. Callers get back only `{"age": 57}`.

## Contents
- [The pieces and the order](#the-pieces-and-the-order)
- [1. Vendor](#1-vendor)
- [2. Endpoint with its request schema](#2-endpoint-with-its-request-schema)
- [3. Account (none needed for a public API)](#3-account)
- [4. Request transformer with samples](#4-request-transformer-with-samples)
- [5. Response transformer with samples (optional)](#5-response-transformer-with-samples-optional)
- [6. Integration with its operation and input schema](#6-integration-with-its-operation-and-input-schema)
- [7. Validate and publish](#7-validate-and-publish)
- [8. Try it and hand it over](#8-try-it-and-hand-it-over)
- [hubctl and local files](#hubctl-and-local-files)
- [When something is refused](#when-something-is-refused)

## The pieces and the order

| Step | Object | Agify value | Role |
| --- | --- | --- | --- |
| 1 | Vendor | `agify`, auth scheme `none`, region `global` → `https://api.agify.io` | `admin` |
| 2 | Endpoint | `agify.estimate@1`: `GET /`, query `name` required | `admin` |
| 3 | Account | `agify-public`, no credentials | `admin` |
| 4 | Request transformer | `AGE-REQ@1`: `query.name = req.firstName + req.lastName`, one sample | `editor` |
| 5 | Response transformer | `AGE-RES@1`: `age = res.body.age`, one sample (optional) | `editor` |
| 6 | Integration | `int_age_estimate`, operation `default` with required `firstName` and `lastName` | `editor` |
| 7 | Publish | v1, `sync` | `editor` |

Read the vendor's current API docs first and keep the URL: vendors take `docs_url`, and every transformer must cite it.

## 1. Vendor

- **Console:** Vendors → **Create vendor** → Vendor id `agify`, Display name `Agify`, Auth scheme **None (public API, no credentials)**, Region code `global`, Base URL `https://api.agify.io`, reason → **Create vendor**.
- **MCP:**
  ```json
  hub_admin_create_vendor {
    "vendor_id": "agify", "display_name": "Agify", "auth_scheme": "none",
    "regions": {"global": "https://api.agify.io"},
    "docs_url": "https://agify.io/documentation",
    "reason": "Onboard the agify age estimate"
  }
  ```
- **Admin API:** `POST /admin/v1/vendors` (`createVendor`), same body.

Use `none` only for APIs that need no credentials at all. Any other scheme needs an account with secrets (see [vendors-and-accounts.md](vendors-and-accounts.md)).

## 2. Endpoint with its request schema

The endpoint declares what the vendor accepts: `query`, `headers`, `body` and `path` parameters, each a JSON Schema whose `required` list marks required fields. Every other field is optional. Leave out a part the endpoint does not take. Never declare credential headers or query values set from `creds.*`: they are not checked.

- **Console:** Vendors → `agify` → **Add endpoint** → Name `estimate`, Method `GET`, Path `/` → in **Query parameters**, **Paste a sample** `{ "name": "Michael" }` → **Generate fields** → tick **name required** → reason → **Add endpoint**. The endpoint page then lists `name  string  Required`.
- **MCP:**
  ```json
  hub_admin_create_vendor_endpoint {
    "vendor_id": "agify", "name": "estimate", "method": "GET", "path": "/", "idempotent": true,
    "timeout_ms": 3000,
    "request_schema": {
      "query": {"type": "object", "required": ["name"],
                "properties": {"name": {"type": "string", "minLength": 1}}}
    },
    "response_schema": {"type": "object", "properties": {"count": {"type": "integer"}, "name": {"type": "string"}, "age": {"type": ["integer", "null"]}}},
    "reason": "Age estimate endpoint"
  }
  ```
- **Admin API:** `POST /admin/v1/vendors/agify/endpoints` (`createVendorEndpoint`). `request_schema` is required; `{}` means "nothing besides method and path".

`response_schema` is optional and informational: the Hub stores and shows it, never enforces it. Endpoint versions are immutable: posting `estimate` again creates `estimate@2`.

## 3. Account

On an auth `none` vendor the account has no credentials, so anyone with role `admin` can create it, and an AI may prepare it.

- **Console:** Accounts → **Create account** → Account id `agify-public`, Vendor `agify`, Region `global`. The form says this account has no credentials and shows no secret fields. Reason → **Create account**.
- **Admin API:** `POST /admin/v1/accounts` with `{"account_id": "agify-public", "vendor_id": "agify", "region": "global", "config": {}, "reason": "..."}`. Omit `credentials` (or send `{}`); sending credentials to a `none` vendor answers 400 at `/credentials`.
- **MCP:** no tool. `createAccount` is in the MCP contract's `forbidden_operations` because other schemes carry secrets. Hand this step to the user (console or admin API).

Credential operations (add next, promote, delete old) answer 409 `CONFLICT` on a `none` account.

## 4. Request transformer with samples

Always required. It reads the caller's body as `req.*` (and declared caller headers as `hdr.*`) and produces the vendor request. A **sample** is an input and the exact vendor request it must produce. The Hub runs every sample and refuses the transformer, naming the sample and the field, when the output differs from the expected request (`goldens_pass`) or breaks the endpoint's request schema (`endpoint_schema`).

```yaml
transformer: AGE-REQ
kind: request
vendor_endpoint: agify.estimate@1
docs_url: https://agify.io/documentation
request:
  method: GET
  path: /
  query:
    name: req.firstName + req.lastName
```

Sample `full_name`:
```json
{ "name": "full_name",
  "input": { "firstName": "Mich", "lastName": "ael" },
  "expected_request": { "method": "GET", "path": "/", "query": { "name": "Michael" } } }
```

- **Console:** Transformers → **New transformer** → Transformer id `AGE-REQ`, Kind `request`, Vendor endpoint `agify.estimate@1`, vendor documentation URL, paste the YAML into **Source (transformer YAML)** → Sample 1: Name `full_name`, **Input body**, **Expected vendor request** (and **Add sample** for more) → **Run samples on this draft**. **Create AGE-REQ@1** stays disabled until the exact draft passes; a failing sample shows "Why sample … fails" with the field (for example `/query/name: must NOT have fewer than 1 characters`). Then **Create AGE-REQ@1** → reason → confirm.
- **MCP:**
  1. `hub_admin_run_transformer_goldens {"draft": {"kind": "request", "vendor_endpoint": "agify.estimate@1", "yaml": "..."}, "fixtures": [<sample>]}` → per sample `passed`, `diff` and `schema_errors` (`field`, `message`).
  2. Optional: `hub_admin_dry_run_transformer {"draft": {...}, "account_id": "agify-public", "input": {"firstName": "Mich", "lastName": "ael"}}` → the vendor request and `schema_errors`.
  3. `hub_admin_create_transformer_version {"transformer_id": "AGE-REQ", "kind": "request", "vendor_endpoint": "agify.estimate@1", "yaml": "...", "fixtures": [<sample>], "docs_url": "https://agify.io/documentation", "reason": "Name from first and last name"}` → `AGE-REQ@1`, or 422 with `details[]` (`keyword` `goldens_pass` or `endpoint_schema`, `field` such as `/query/name`, the sample name). Nothing is stored on a refusal.
- **Admin API:** `runTransformerGoldens`, `dryRunTransformer`, `createTransformerVersion` (`POST /admin/v1/transformers/AGE-REQ/versions`).

Samples check the transformer, never live callers: on live calls the vendor request is not re-checked.

## 5. Response transformer with samples (optional)

Decide what callers get back:

- **With a response transformer**, its output is returned unchanged (no schema check). Use it to keep only what callers need, rename fields, map vendor errors to canonical `UPSTREAM_*` codes, or hide a body that names the vendor.
- **Without one (pass-through)**, a sync call returns the vendor's status and body unchanged, with only the vendor's `Content-Type` and `Retry-After` headers plus the Hub's own and `X-Hub-Pass-Through: true`. Retries and the breaker still classify timeouts, 429 and 5xx as retryable. Requests completed later (202) report the vendor's body as the result and keep canonical `UPSTREAM_*` codes for failures. Warn the owner: the vendor body may name the vendor.

For agify, keep only `age`:

```yaml
transformer: AGE-RES
kind: response
docs_url: https://agify.io/documentation
response:
  age: res.body.age
```

Sample `known_name`:
```json
{ "name": "known_name",
  "vendor_response": { "status": 200, "headers": { "Content-Type": "application/json" },
                       "body": { "count": 21, "name": "Michael", "age": 57 } },
  "expected_response": { "age": 57 } }
```

- **Console:** Transformers → **New transformer** → id `AGE-RES`, Kind `response` (no vendor endpoint field), source, Sample 1 with **Vendor response** and **Expected output** → **Run samples on this draft** → **Create AGE-RES@1**.
- **MCP:** `hub_admin_create_transformer_version {"transformer_id": "AGE-RES", "kind": "response", "yaml": "...", "fixtures": [<sample>], "docs_url": "...", "reason": "Return only the age"}`. A response transformer stands alone: sending `vendor_endpoint` answers 400 at `/vendor_endpoint`.

## 6. Integration with its operation and input schema

Create the id first, then its first draft. The draft holds the **operations** (the caller's input, per operation) and the targets.

**Create the id.**
- **Console:** Integrations → **Create integration** → Integration id `int_age_estimate`, Owner tenant, Description, leave **Interactive** unticked (it is fixed at creation; tick it only for multi-step flows a person waits on), reason → **Create integration**. The console opens the draft form.
- **MCP:**
  ```json
  hub_admin_create_integration {
    "integration_id": "int_age_estimate", "owner_tenant": "orders-service",
    "description": "Estimate the age for a first and last name.",
    "interactive": false, "moves_money_or_quota": false, "reason": "Onboard the age estimate"
  }
  ```
- **Admin API:** `createIntegration`.

**Draft v1.** The operation `default` takes a body with `firstName` and `lastName`, both required. `validate_input: true` (the default) makes the Hub check live calls against the required fields and answer 400 `VALIDATION_FAILED` with one `details[]` entry per violation. Switch it off only when callers must reach the transformer with input the schema does not describe; samples are still checked at publish.

```json
{
  "operations": {
    "default": {
      "description": "Estimate the age for a first and last name.",
      "idempotent": true,
      "validate_input": true,
      "request_schema": {
        "body": {
          "type": "object", "required": ["firstName", "lastName"],
          "properties": {
            "firstName": {"type": "string", "minLength": 1},
            "lastName":  {"type": "string", "minLength": 1}
          }
        }
      }
    }
  },
  "targets": [
    {"vendor_id": "agify", "account_id": "agify-public",
     "transformer": {"request": "AGE-REQ@1", "response": "AGE-RES@1"}, "weight": 100}
  ],
  "delivery": {"mode": "sync", "on_circuit_open": "fail_fast"},
  "audit": {"payloads": "masked"}
}
```

Leave `response` out (or `null`) for pass-through.

- **Console:** on the draft form, **Operation default** → Description (shown to callers), tick **Idempotent**, keep the **Check live calls against the request schema** switch on → in **Request body**, **Paste a sample** `{ "firstName": "Mich", "lastName": "ael" }` → **Generate fields** (generated fields start required; untick the optional ones). Add **Request headers** only if the transformer reads `hdr.*`. **Add operation** for multi-step flows. Then **Add target** → Vendor `agify`, Account `agify-public`, Weight 100, Request transformer `AGE-REQ@1`, Response transformer (optional) `AGE-RES@1` → Mode `sync` → Why this version → **Create draft**.
- **MCP:** `hub_admin_create_integration_draft {"integration_id": "int_age_estimate", "spec": {...above...}, "reason": "First version"}` → version 1. Fix it later with `hub_admin_update_integration_draft` (full spec).
- **Admin API:** `createIntegrationDraft`, `updateIntegrationDraft`.

`x-pii` tags on body fields drive audit masking, sealing and deletion (values in `contracts/README.md`). Here both fields could carry `"x-pii": "name"`.

## 7. Validate and publish

Validation runs every check, including:
- `schemas_compile`: the operation schemas compile, and every `req.*` / `hdr.*` the transformers read is declared in the operation's body / headers schema;
- `input_schema`: each request sample's input fits the operation's request schema;
- `endpoint_schema` and `goldens_pass`: the request samples still produce the expected request, and it fits the endpoint's schema;
- references exist, accounts not retired, interactive only `sync`, weights sum to 100, a delivery path is declared, `operations_have_transformers`.

Failures carry `pointer`, `sample` and `field`.

- **Console:** after **Create draft** the page shows "Saved draft v1" and the checks (**Run publish checks on v1** reruns them) → **Publish v1** → reason → **Publish v1**.
- **MCP:** `hub_admin_validate_integration_version {"integration_id": "int_age_estimate", "version": 1}` → show the checks → after a yes, `hub_admin_publish_integration_version {"integration_id": "int_age_estimate", "version": 1, "expected_active_version": null, "reason": "Go live", "confirm": true}`.
- **Admin API:** `validateIntegrationVersion`, `publishIntegrationVersion`.

Nodes pick the new snapshot up within about 10 s.

## 8. Try it and hand it over

```bash
curl -s -i -X POST "$HUB_BASE_URL/v1/integrations/int_age_estimate/invoke" \
  -H "Authorization: Bearer $HUB_API_KEY" -H "Content-Type: application/json" \
  -d '{"firstName": "Mich", "lastName": "ael"}'
# 200 {"age": 57}
# without lastName: 400 VALIDATION_FAILED (details[] names /lastName)
# with v2 published without AGE-RES: 200 {"count": 21, "name": "Michael", "age": 57}, X-Hub-Pass-Through: true
```

- `hubctl dry-run int_age_estimate body.json` (or MCP `hub_dry_run`, key scope `dry_run`) shows the request the active version would send. Nothing is sent.
- Discovery (`GET /v1/integrations/int_age_estimate`, MCP `hub_describe_integration`) shows the operation and its request schema, never the vendor.
- Give the calling team the integration id and the consumer skill `clients/claude-skill/integration-hub/`.

## hubctl and local files

hubctl does not create vendors, endpoints, accounts, transformers or integrations in a shared environment: `apply` refuses them (use the console, MCP or the admin API). It helps around them:

- `hubctl transform test [AGE-REQ]` runs samples from files, including the `endpoint_schema` check, exactly as transformer creation does.
- `hubctl dry-run int_age_estimate body.json` after publish.
- `hubctl export` / `plan` / `apply` for later versions of the integration (a new `versions/<n>.yaml` plus `active_version`).

**Local dev only:** the registry can be loaded from files with `-config-watch` (`backend/config/`, or an example's `config/`). The same objects as files:

```text
vendors/agify.yaml                          auth: none, endpoints: estimate@1 with request_schema
accounts/agify-public.yaml                  no credentials_ref for a none vendor
transformers/AGE-REQ/1.yaml                 kind: request, vendor_endpoint: agify.estimate@1
transformers/AGE-REQ/fixtures/full_name.input.json, full_name.expected_request.json
transformers/AGE-RES/1.yaml                 kind: response
transformers/AGE-RES/fixtures/known_name.vendor_response.json, known_name.expected_response.json
integrations/int_age_estimate/integration.yaml, versions/1.yaml (operations.default.request_schema)
```

Registry files use `vendor:` / `account:` in targets and `auth:` on the vendor; see `backend/config/README.md`. `examples/agify-age/` has the complete tree.

## When something is refused

| Message | Meaning | Fix |
| --- | --- | --- |
| 422 `endpoint_schema`, field `/query/name` | A sample's vendor request misses or breaks a field the endpoint requires | Fix the transformer (or the endpoint schema, with a new endpoint version) |
| 422 `goldens_pass`, a diff | The output differs from the expected request or response | Fix the transformer, or the expected sample after checking the vendor docs |
| `schemas_compile`, field `/body/firstName` | The transformer reads a field the operation does not declare | Declare it in the operation's request schema, or fix the expression |
| `input_schema`, sample `full_name` | A sample input breaks the operation's request schema | Make the sample a valid caller input, or relax the field |
| 400 at `/credentials` | Credentials sent for a `none` vendor, or missing for another scheme | Omit them for `none`; a human enters them otherwise |
| 400 at `/vendor_endpoint` | A response transformer named an endpoint | Remove `vendor_endpoint` from response transformers |
| Live 400 `VALIDATION_FAILED` | The caller's body misses a required field (`validate_input` on) | The caller fixes the body; or make the field optional in a new version |
