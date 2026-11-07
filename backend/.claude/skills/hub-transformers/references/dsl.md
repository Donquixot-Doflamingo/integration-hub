# Transformer DSL reference

The authoritative grammar is the parser in `internal/transform`; if this file and the parser disagree, the parser wins and this file gets fixed.

## Top-level keys
The registry decodes transformer files strictly: an unknown key (for example a leftover `capability:` or `operation:` from before ADR-0023) is a load error.

| Key | Kinds | Meaning |
| --- | --- | --- |
| `transformer`, `version` | all | id + immutable integer version (file `transformers/<id>/<version>.yaml`) |
| `kind` | all | `request` or `response` (`inbound` and `stream` are planned, Release 2) |
| `vendor_endpoint` | request only | `vendor.endpoint@N`; its `request_schema` checks every sample's produced request. A response transformer must not have one |
| `docs_url` | all | vendor doc URL the mapping was checked against (required for real vendors; add the date in a comment) |
| `request.method/path/headers/query/body` | request | targets ← expressions; `path` is a literal starting with `/` (the endpoint's `{name}` path parameters are matched against it for the `path` part of the endpoint schema) |
| `allow_creds_in_body` | request | default false |
| `batch` | request | `{ path, max }` |
| `response` | response | output fields ← expressions; returned to the caller unchanged, no schema check |
| `errors` | response | ordered rules `{ when, code, retryable, retry_after_ms?, message? }` |
| `session` | response | vendor session data of a multi-step operation ← expressions over `res`, `req`, `account` (never `creds`, `hdr`, `session`); evaluated on success only; opens a session (`X-Hub-Session`) when at least one field is non-nil; ≤ 16 KB; a key `vendor_txn_id` is also kept in its own column (HUB-175) |

Planned, not loaded today (F-22, Release 2): an `auth` block, `stream` chunk mapping, `supports` flags for LLM targets.

## What checks the fields you read and write
| Reference | Checked against | When |
| --- | --- | --- |
| `req.a.b` | the operation's `request_schema.body` (path must be declared) | integration validate/publish (`schemas_compile`, field `/body/a/b`) |
| `hdr.X-Name` / `hdr["X-Name"]` | the operation's `request_schema.headers` (case-insensitive); `hdr.idempotency_key` always allowed | integration validate/publish (`schemas_compile`, field `/headers/X-Name`) |
| produced `path` params, `query`, non-credential `headers`, `body` | the vendor endpoint's `request_schema` (`/path/...`, `/query/...`, `/headers/...`, `/body/...`) | every request sample, at create, validate/publish and `hubctl transform test` (`endpoint_schema`) |
| a sample's input body and `_context.hdr` | the operation's `request_schema` | integration validate/publish (`input_schema`) |
| `response` output | nothing | never |

## Expression language (expr-lang/expr)
- Member access `req.user.id`; missing optional fields need `??` (nil coalescing) or the expression throws → `TRANSFORM_FAILED`.
- Literals: strings in double quotes (inside a YAML single-quoted scalar: `'"ok"'`), numbers, `true/false`, `nil`, arrays `[1, 2]`, maps `{brand: account.config.brand_code}`.
- Operators: `== != < <= > >= && || !`, `+ - * / %`, ternary `cond ? a : b`, `in`, `matches` (regex literal, compiled once).
- Builtins used here: `len`, `map(arr, {...})`, `filter(arr, {...})` with `#` as the element, `int`, `float`, `string`.
- A YAML map/list on the right builds structure; an expression returning a map/list inserts it whole (`evtData: req.event.properties`).

## Examples
Merge account values into a nested object (shape change → own transformer T2):
```yaml
evtData: merge(req.event.properties, {
           brand:    account.config.brand_code,
           campaign: req.context.campaign ?? "organic" })
```
Map an array:
```yaml
items: map(req.items, { {sku: #.id, qty: #.quantity, amount: currencyMinor(#.price, req.currency)} })
```
Minimal response (R2). Without any response transformer the call is pass-through (the vendor's body unchanged):
```yaml
response:
  status:     len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"
  vendor_ref: header(res, "X-Request-Id")
```
Multi-step (session). Step 1's response transformer keeps what step 2 needs:
```yaml
session:
  vendor_txn_id: res.body.txn        # also stored in vendor_sessions.vendor_txn_id
  user: req.user.id                  # req is readable in session: (not in response:)
```
Step 2's request transformer reads it back:
```yaml
body:
  txn: session.vendor_txn_id
  otp: req.otp
```

## Fixture formats
`*.input.json` (the caller's body; `_context.hdr` holds the sample's caller headers, read as `hdr.*`)
```json
{ "user": { "id": "u-1" }, "event": { "name": "order_created" },
  "_context": { "hdr": { "X-Request-Source": "app" }, "creds": { "passcode": "fixture-passcode" },
                "account": { "id": "ct-brandA-in", "config": { "brand_code": "A" } } } }
```
`*.expected_request.json`
```json
{ "method": "POST", "path": "/1/upload", "query": {},
  "headers": { "X-CleverTap-Account-Id": "fixture-account", "X-CleverTap-Passcode": "fixture-passcode", "Content-Type": "application/json" },
  "body": { "d": [ { "identity": "u-1", "type": "event", "evtName": "order_created", "ts": 1793462400, "evtData": {} } ] } }
```
`*.vendor_response.json`
```json
{ "status": 429, "headers": { "Retry-After": "3" }, "body": { "status": "fail" } }
```
`*.expected_response.json` for a success case of a transformer with a `session:` section (the session must be pinned in every success case)
```json
{ "response": { "status": "ok", "accepted": 1, "rejected": [] }, "session": { "vendor_txn_id": "vr-1", "user": "u-1" } }
```
`*.expected_response.json` for an error case
```json
{ "error": { "code": "UPSTREAM_RATE_LIMITED", "retryable": true, "retry_after_ms": 3000 } }
```
Optional `_context` in `*.input.json` sets `hdr` (the sample's input headers), `account`, `creds` (obviously fake), `session`, a fixed `now` (RFC 3339) and `uuids` for that case. In `*.vendor_response.json` it may also set `req` (the caller's request a `session:` section reads). In files, `expected_request` holds the fixture credential headers too (the golden compares them); the `endpoint_schema` check leaves them out. Through the admin API (`GoldenFixture`), `input_headers` and `account_config` are separate fields and `expected_request` carries no credentials.

## Common compile/publish errors
| Error | Fix |
| --- | --- |
| `schemas_compile` at `/body/user/uid` | the operation's request schema does not declare the field; fix the path, or add the field to the operation (new integration version) |
| `schemas_compile` at `/headers/X-Source` | `hdr.X-Source` read but not declared in the operation's `request_schema.headers` |
| `endpoint_schema` at `/query/name` (sample `x`) | the sample's produced request misses or breaks a field the vendor endpoint requires; fix the mapping (or the endpoint schema, as a new endpoint version) |
| `input_schema` (sample `x`) | the sample's input is not a valid caller input of the operation; fix the sample |
| `field capability not found` / `field operation not found` | a pre-ADR-0023 file; delete the key (the integration decides which operation the transformer serves) |
| `vendor_endpoint: response transformers stand alone` | remove `vendor_endpoint` from a response transformer |
| creds used in body target | move to header/auth, or justify `allow_creds_in_body` |
| `creds.* is never allowed in session` | session data is stored in Postgres; keep only non-secret vendor state (txn id) |
| `/session: unexpected` in a golden | the transformer maps a session: pin it under `"session"` in `expected_response.json` |
| expression too deep / output too large | simplify; split into stdlib call; check batch max |
| goldens failed | read the diff; update the transformer or (after doc check) the golden |
