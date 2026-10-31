# Capability schemas

Each capability (DESIGN §3.1, §8.1) is a vendor-agnostic operation with a versioned canonical request and response, written as **JSON Schema 2020-12**. These files are the caller-facing source of truth; the Hub serves them at `GET /v1/capabilities/{capability}` and the admin API stores them at `/admin/v1/capabilities`.

## File naming

| Capability shape | Files |
| --- | --- |
| Single operation (op name `default`) | `<name>.v<major>.request.json`, `<name>.v<major>.response.json` |
| Multi-operation (e.g. `kyc.aadhaar_otp@v1` with `generate`, `verify`) | `<name>.v<major>.<op>.request.json`, `<name>.v<major>.<op>.response.json` |

`$id` is `urn:hub:capability:<name>@v<major>:<op>:<request|response>`. The audit event `request.validated` records this id.

## Versioning

- The **major** is part of the capability key (`events.track@v1`) and is caller-facing. A breaking change means a new major, which callers adopt through a **new integration ID** (D11).
- The **minor** (`x-capability-minor`) increases for additive changes only: new optional request fields, new response fields, new enum values in responses. Removing, renaming or tightening a field is breaking.
- Response schemas never set `additionalProperties: false`, so additive response changes stay compatible. Callers must ignore unknown response fields.
- Request schemas set `additionalProperties: false`, so typos fail fast with `VALIDATION_FAILED` (400) instead of being dropped silently.

## Extensions

These keywords are not standard JSON Schema. Validators ignore them as unknown keywords (annotations). The Hub registry reads them.

### `x-pii` (on any property)
This tag marks a value as personal data. It drives audit masking and sealing (DESIGN §13.2) and the deletion job (§16.5). Values:

| Value | Meaning | `masked` policy does |
| --- | --- | --- |
| `identifier` | Subject identifier (user id, customer id). The deletion job keys on it. | HMAC-SHA256 with a per-tenant key |
| `email` | Email address | hash local part, keep domain |
| `phone` | Phone number | keep last 4 digits |
| `name` | Person name | replace with `***` |
| `address` | Postal address | replace with `***` |
| `government_id` | PAN, Aadhaar, passport and similar | `maskPAN` / `maskAadhaar` style, last 4 |
| `financial` | Account or card number, balances | keep last 4 / replace |
| `free_text` | Unstructured object or string that may contain PII | replace the whole value with its hash and size |
| `ip_address`, `device_id` | Network and device identifiers | truncate or hash |

A tag on an object applies to the whole subtree. Under `sealed` policy, every tagged value is field-level encrypted instead of masked.

### `interactive` (capability level, boolean)
`true` means a person is waiting (LLM chat, OTP). Integrations bound to an interactive capability may only use `sync` delivery, and the publish pipeline rejects other modes (DESIGN §5.4). A 202 is never a valid answer to an interactive call. It defaults to `false`. Put it on both request and response files with the same value.

### `operations` (capability level, object; in the request file of the `default` op, or in each op's request file)
Maps operation name to `{ description, idempotent, request_schema, response_schema }`. File names are relative to this folder. Single-operation capabilities have exactly one key, `default`. The operation name is the `{operation}` path segment in `POST /v1/integrations/{id}/invoke/{operation}`. Pattern: `^[a-z][a-z0-9_]{0,31}$`.

`idempotent: true` declares that repeating the operation has no extra side effect. The Hub uses it to allow an immediate sync retry on timeout and `failover` without an `Idempotency-Key` (DESIGN §5.3, §10.2).

### `x-capability`, `x-capability-minor`
These hold the capability key and the current minor version, for tooling.

## Current capabilities

| Capability | Ops | Interactive | Files |
| --- | --- | --- | --- |
| `events.track@v1` | `default` | no | `events.track.v1.request.json`, `events.track.v1.response.json` |
