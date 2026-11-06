---
name: hub-transformers
description: "How to author, change and test an Integration Hub transformer - the YAML DSL (DESIGN 7), expression context (req, hdr, account, creds, session, res), stdlib functions, creds restrictions, error mapping rules, batch section, golden fixture layout, verifying real vendor docs and citing the URL, hubctl transform test and dry-run, and preferring account config over forking (DESIGN 3.3). Use whenever you add a vendor or capability mapping, edit anything in config/transformers, write or update golden fixtures, map vendor errors to canonical codes, or debug TRANSFORM_FAILED or a wrong vendor payload."
---

# Authoring Hub transformers

A transformer is an immutable, versioned YAML mapping (DESIGN §3.1, §7). Request transformers: canonical → vendor request. Response transformers: vendor response → canonical response + error mapping. They are config, not Go code: a new vendor for an existing capability should need **no Go change** (target ≤ 2 engineer-days, transformer + goldens). DSL reference: [references/dsl.md](references/dsl.md).

## Before you write one
1. **Do you need a new transformer at all?** (§3.3) If only *values* differ between accounts (project id, brand code, region, custom field names) → reuse the transformer and read them from `account.config.*`. Fork (new transformer id) only when the payload **shape** changes. Changing an existing one = a new **version** (`T1@8`), never an edit of `T1@7`.
2. **Read the vendor's current API docs.** Shapes in DESIGN are illustrative. Confirm endpoint path, method, auth headers, field names and types, required fields, limits (batch max, payload size), error format, rate-limit headers, idempotency support. Put the doc URL and the date you checked in the file header (`docs_url:` field + comment). No URL → not mergeable.
3. **Check the capability schema** in `contracts/schemas/capabilities/` (canonical request/response). The response transformer's output must conform to the capability response schema (publish validation, §6.4). If the canonical schema lacks something the vendor needs, the answer is `account.config`, a default, or a capability change request to the contract-steward — never a vendor-specific field in the canonical schema.

## File skeleton
```yaml
# Vendor docs: https://<vendor docs url>  (checked 2026-10-31)
transformer: T1
version: 7
kind: request                       # request | response | inbound | stream
capability: events.track@v1
vendor_endpoint: clevertap.upload@1
docs_url: https://<vendor docs url>
request:
  method: POST
  path: /1/upload
  headers:
    X-CleverTap-Account-Id: creds.account_id     # creds only in header/query/auth targets
    X-CleverTap-Passcode:   creds.passcode
    Content-Type:           '"application/json"' # string literal = quoted expression
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())
        evtData:  req.event.properties
batch: { path: d, max: 1000 }
```
Left side = target path; right side = an `expr-lang/expr` expression. Literal strings must be quoted *inside* YAML (`'"event"'`). ~80 % of lines should be plain paths.

### YAML quoting (the §7.4 examples are not valid YAML as printed)
An expression is a YAML scalar, so YAML syntax wins:
- Quote any expression containing `: ` (the ternary `c ? "a" : "b"`, inline maps): `status: 'len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"'`.
- Write a multi-line expression as a block scalar (`>-`), e.g. `evtData: >-` followed by the `merge(..., { brand: ..., campaign: ... })` lines.
- String literals inside expressions keep their double quotes inside single-quoted YAML: `type: '"event"'`.
- In `map`/`filter`, an object literal right after the comma is read as a predicate block: write `map(xs, ({reason: #.error}))`, not `map(xs, {reason: #.error})`.
- Quote any expression that uses expr's `#` placeholder: YAML reads ` #` as a comment, so `tags: map(xs, # * 2)` silently becomes `map(xs,`. Write `tags: 'map(xs, # * 2)'`.

### Field access is nil-safe
The compiler makes every `a.b` behave like `a?.b`: if a parent object is absent, the access yields nil instead of failing, so `req.context.campaign ?? "organic"` works when `context` is omitted. Object fields whose value evaluates to nil are left out of the vendor body (no `null` is sent). Use `??` for defaults; a genuinely required value should be `required` in the capability schema, so validation rejects the request before the transformer runs.
The registry loader rejects invalid YAML with `file: line N: ...` (found in M1 on the dev config).

## Expression context (§7.2)
| Var | Available in | Notes |
| --- | --- | --- |
| `req` | request | validated canonical body |
| `hdr` | request | allowlisted caller headers only |
| `account` | request, response | non-secret config (`account.config.brand_code`, region) |
| `creds` | request **header/query/auth targets only** | secret values; body use requires `allow_creds_in_body: true` + review |
| `session` | multi-step ops | vendor session data (e.g. txn id from step 1) |
| `res` | response/error mapping | `res.status`, `res.body` (parsed JSON object/array, else nil), `res.text` (non-JSON body, e.g. an HTML error page), headers via `header(res, "X")` |
| `now()` | all | pinned in goldens for determinism |

Stdlib (§7.3) only — extended by platform code releases, not per transformer: strings `lower upper trim split join replace format truncate`; time `now epochSeconds epochMillis parseTime formatTime addDuration`; data `?? merge pick omit flatten map filter len toJSON fromJSON`; encoding `uuid sha256 hmacSHA256 base64 urlEncode`; domain `phoneE164 emailNormalize currencyMinor maskPAN maskAadhaar`. No loops beyond `map`/`filter`, no network, no disk; depth, output size and time budgets are enforced at compile/run.

## creds rules
- `creds.*` may appear only in `headers`, `query` or an `auth:` block. The compiler rejects it elsewhere unless `allow_creds_in_body: true` (needs platform review; some vendors require it).
- Never derive non-secret fields from creds; never `toJSON(creds)`.
- Query-param credentials are discouraged (§9) — prefer a header if the vendor offers one.
- Audit/dry-run show creds-sourced values as `***`.

## Error mapping (response transformers)
```yaml
errors:                                   # evaluated in order, first match wins
  - when: res.status == 401 || res.status == 403
    code: UPSTREAM_AUTH_FAILED
    retryable: false
  - when: res.status == 429
    code: UPSTREAM_RATE_LIMITED
    retryable: true
    retry_after_ms: int(header(res, "Retry-After") ?? "1") * 1000
  - when: res.status >= 500
    code: UPSTREAM_UNAVAILABLE
    retryable: true
  - when: res.status >= 400
    code: UPSTREAM_REJECTED
    retryable: false
    message: res.body.error.message ?? "rejected by upstream"
```
- Codes only from the `UPSTREAM_*` set of §4.4. `TRANSFORM_FAILED` is never mapped; it is what the engine raises when an expression throws.
- Always end with a `>= 400` catch-all. Specific vendor quirks (200 with an error body, auth errors as 400) go **above** the generic rules.
- `message` passes vendor text to callers for `UPSTREAM_REJECTED`: make sure it cannot contain the vendor's name/branding or account identifiers (rule: callers never see vendor identity). Prefer extracting the field-level reason over the whole vendor message.
- Timeouts/connection errors are classified by the upstream client, not here.

## Batch section (§7.5)
`batch: { path: <array in body>, max: <n> }` lets the worker merge several envelopes' single-record arrays into one call (≤ `max`, ≤ vendor's documented limit). Declare it only if the response lets the Hub attribute results **per record** (index or id); the response transformer must map per-record outcomes, otherwise one bad record fails the whole group. Sync/wait-immediate attempts never batch.

## Golden fixtures (mandatory, §7.6)
```text
config/transformers/<id>/<id>@<version>.yaml
config/transformers/<id>/fixtures/<case>.input.json             # request: canonical input (+ optional "_context": hdr, account, now)
config/transformers/<id>/fixtures/<case>.expected_request.json  # request: exact vendor request {method, path, query, headers, body}
config/transformers/<id>/fixtures/<case>.vendor_response.json   # response: {status, headers, body}
config/transformers/<id>/fixtures/<case>.expected_response.json # response: canonical result or canonical error
```
(Follow the existing layout if `config/transformers/` already differs; keep the four suffixes.) Required cases:
- Request: minimal valid input; all optional fields present; each `??` default path; unicode/large values; a 4 KB input for the benchmark.
- Response: success; partial success; every error rule (401, 403, 429 with/without `Retry-After`, 5xx, generic 4xx, vendor-specific quirks); malformed/empty body.
- Fixture creds are obviously fake (`fixture-…`) and come from the runner's fixture context, never real values.
- Expected outputs are **checked against the vendor docs by a human/agent**, not just regenerated. `-update` is for mechanical changes; review every diff.

## Run and verify
```bash
cd backend
hubctl transform test                         # all goldens (same runner as CI and publish)
hubctl transform test T1 T2@3                 # only these transformers (ID, or ID@version)
go test ./internal/transform -run TestGoldens
go test -run='^$' -bench=BenchmarkTransform4KB -benchmem ./internal/transform   # ≤ 0.5 ms p99
```
Dry-run end to end through the Hub (no vendor call) with `hubctl dry-run` or `POST /v1/integrations/{id}/dry-run`; compare with the vendor docs' example request. Then exercise it against **mockvendor** in an integration test — never a real vendor.

## Publishing
Publish validates: expressions compile and type-check against the capability schema, unknown fields fail, goldens pass, response output conforms to the response schema, referenced vendor endpoint exists, interactive capabilities only in sync (§6.4). A transformer change reaches callers only when an **integration version** references the new transformer version.

## PR checklist
- [ ] Vendor doc URL(s) + date in the file; payload verified against them.
- [ ] Config-over-fork considered; new version rather than edit.
- [ ] `creds.*` only in header/query/auth.
- [ ] Error rules ordered, catch-all present, no vendor identity in `message`.
- [ ] Goldens for every case above; `hubctl transform test` green; benchmark within budget.
- [ ] Integration test against mockvendor; feature doc updated.
