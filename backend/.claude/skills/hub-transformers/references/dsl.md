# Transformer DSL reference

The authoritative grammar is the parser in `internal/transform`; if this file and the parser disagree, the parser wins and this file gets fixed.

## Top-level keys
| Key | Kinds | Meaning |
| --- | --- | --- |
| `transformer`, `version` | all | id + immutable integer version |
| `kind` | all | `request`, `response`, `inbound`, `stream` |
| `capability` | all | `name@vN` it serves |
| `vendor_endpoint` | request | `vendor.endpoint@N` from the vendor definition |
| `docs` | all | vendor doc URLs (required) |
| `request.method/path/headers/query/body` | request | targets ← expressions; `path` may interpolate expressions |
| `auth` | request | auth-scheme parameters (creds allowed) |
| `allow_creds_in_body` | request | default false |
| `batch` | request | `{ path, max }` |
| `response` | response | canonical output fields ← expressions |
| `errors` | response | ordered rules `{ when, code, retryable, retry_after_ms?, message? }` |
| `stream` | stream | per-chunk mapping to canonical chunk (`delta.text`, `finish_reason`, `usage`) |
| `supports` | request (LLM) | `[tools, vision, json_mode]` capability flags checked at publish |

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
Minimal response (R2):
```yaml
response:
  status:     len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"
  vendor_ref: header(res, "X-Request-Id")
```
Multi-step (session):
```yaml
body:
  txn: session.vendor_txn_id
  otp: req.otp
```

## Fixture formats
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
`*.expected_response.json` for an error case
```json
{ "error": { "code": "UPSTREAM_RATE_LIMITED", "retryable": true, "retry_after_ms": 3000 } }
```
Optional `_context` in `*.input.json` sets `hdr`, `account.config`, `session` and a fixed `now` (RFC 3339) for that case.

## Common compile/publish errors
| Error | Fix |
| --- | --- |
| unknown field `req.user.uid` | field not in the capability schema; fix the path or request a schema change |
| creds used in body target | move to header/auth, or justify `allow_creds_in_body` |
| expression too deep / output too large | simplify; split into stdlib call; check batch max |
| response output does not conform | output field names/types must match the capability response schema |
| goldens failed | read the diff; update the transformer or (after doc check) the golden |
