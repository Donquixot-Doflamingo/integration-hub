# Transformers: mapping the caller's input to the vendor and back

A transformer is an immutable, versioned YAML mapping (DESIGN §7, ADR-0023). A **request** transformer turns the caller's input (body as `req.*`, declared headers as `hdr.*`) into the vendor's HTTP request. It is always required. A **response** transformer turns the vendor's response into what the caller gets, or into a canonical error. It is optional: without one the call is **pass-through**. An integration version picks one pair per target (and per operation, for multi-step flows). Both kinds are proven by **samples** (golden fixtures). The full DSL reference for platform engineers is `backend/.claude/skills/hub-transformers/` (and its `references/dsl.md`); this page is the walk-through. For a short first example, see [onboard-an-api.md](onboard-an-api.md).

## Contents
- [What checks what](#what-checks-what)
- [The round trip, step by step](#the-round-trip-step-by-step)
- [Expression context](#expression-context)
- [Pass-through (no response transformer)](#pass-through-no-response-transformer)
- [Error mapping](#error-mapping)
- [Samples (golden fixtures)](#samples-golden-fixtures)
- [Create, test and dry-run (console, hubctl, MCP)](#create-test-and-dry-run)
- [Checklist](#checklist)

## What checks what

| Check | Against | When | Name in reports |
| --- | --- | --- | --- |
| A request sample produces exactly its expected vendor request | the sample | transformer create, integration validate/publish, `hubctl transform test` | `goldens_pass` |
| That produced request fits the endpoint's request schema (query, headers, body, path; credentials left out) | `vendor_endpoint`'s `request_schema` | same | `endpoint_schema` |
| Every `req.*` / `hdr.*` the transformer reads is declared | the operation's `request_schema.body` / `.headers` | integration validate/publish | `schemas_compile` |
| Each request sample's input (body, `input_headers`) fits the operation | the operation's `request_schema` | integration validate/publish | `input_schema` |
| A response sample produces exactly its expected output or error | the sample | transformer create, validate/publish | `goldens_pass` |
| The caller's live input fits the required fields | the operation's `request_schema` | every live call, when `validate_input` is on (default) | 400 `VALIDATION_FAILED` |

The transformer itself knows no caller schema, so one transformer can serve several integrations whose operations declare the fields it reads. The vendor request is never re-checked on live calls (no hot-path cost); a call the vendor rejects fails with the vendor's error. A response transformer's output is never checked against a schema.

## The round trip, step by step

Real files from the dev registry: `backend/config/transformers/T1/7.yaml` (request) and `R1/2.yaml` (response), integration `int_evt_brandA`, vendor endpoint `clevertap.upload@1`.

**1. The caller sends a body** that matches the operation's request schema (`backend/config/integrations/int_evt_brandA/versions/2.yaml`, `operations.default.request_schema.body`; no vendor field anywhere):

```json
POST /v1/integrations/int_evt_brandA/invoke
{ "user": { "id": "u_48213" }, "event": { "name": "app_opened" } }
```

**2. The request transformer maps it.** Left side = path in the vendor request; right side = an `expr-lang/expr` expression over `req` (the caller's body), `hdr` (declared caller headers), `account` (non-secret account config), `creds` (secrets, headers/query only), `session`:

```yaml
transformer: T1
version: 7
kind: request
docs_url: https://developer.clevertap.com/docs/upload-events-api   # required for real vendors
vendor_endpoint: clevertap.upload@1
request:
  method: POST
  path: /1/upload
  headers:
    X-CleverTap-Account-Id: creds.account_id        # secrets only in headers/query/auth
    X-CleverTap-Passcode:   creds.passcode
    Content-Type:           '"application/json; charset=utf-8"'   # a literal string is a quoted expression
  body:
    d:
      - identity: req.user.id
        type:     '"event"'
        evtName:  req.event.name
        ts:       epochSeconds(req.event.timestamp ?? now())      # ?? = default when absent
        evtData:  req.event.properties                            # absent → field left out, no null sent
batch: { path: d, max: 1000 }
```

There is no `capability` or `operation` key: the file names only its `vendor_endpoint`. Which operation it serves is decided by the integration version that references it.

**3. The vendor receives** (credentials injected from the account at send time; audit and dry-run show them as `***`). The samples check this request against `clevertap.upload@1`'s `request_schema` (required header `Content-Type`, body `d[]` with `identity`, `type`, `evtName`); the credential headers are not part of the check:

```json
{ "method": "POST", "path": "/1/upload",
  "headers": { "X-CleverTap-Account-Id": "***", "X-CleverTap-Passcode": "***",
               "Content-Type": "application/json; charset=utf-8" },
  "body": { "d": [ { "identity": "u_48213", "type": "event", "evtName": "app_opened", "ts": 1793438100 } ] } }
```

**4. The vendor answers** `200 {"status": "success", "processed": 1, "unprocessed": []}`.

**5. The response transformer (optional) maps it back.** `res.status`, `res.body` (parsed JSON), `res.text` (non-JSON body) and `header(res, "X")` are available. A response transformer has no `vendor_endpoint`:

```yaml
transformer: R1
version: 2
kind: response
docs_url: https://developer.clevertap.com/docs/upload-events-api
response:
  status:     'len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"'   # quote anything with ": "
  accepted:   res.body.processed
  rejected:   'map(res.body.unprocessed ?? [], ({reason: string(#.error ?? "rejected")}))'
  vendor_ref: header(res, "X-Request-Id")
  raw:        res.body
errors: [...]   # see Error mapping
```

**6. The caller receives** `200 {"status": "ok", "accepted": 1, "rejected": [], ...}`: the response transformer's output, unchanged. Without `R1` the caller would get CleverTap's own body with `X-Hub-Pass-Through: true`.

Rules of thumb:
- **Config over fork** (DESIGN §3.3): if two accounts differ only in values (project id, brand code, field names), read them from `account.config.*` in one transformer. Fork only when the payload *shape* differs (brand B adds fields → `T2`).
- **Changing a transformer = a new version** (`T1@8`). It carries traffic only after an integration version references it and is published.
- **Switching vendor keeps the caller's input, not the output.** The new request transformer must read the same operation schema. Callers keep receiving the same response only if the new response transformer reproduces it; there is no shared response schema to enforce that. Compare response samples side by side before publishing ([cutover.md](cutover.md#switch-to-another-vendor)).
- If the vendor needs something the caller does not send: `account.config`, a default (`??`), or a new optional field in the operation's request schema (a new integration version). Never ask callers for vendor-specific fields.

## Expression context

| Var | Where | Contents |
| --- | --- | --- |
| `req` | request; response; `session:` | The caller's body. Every field read must be declared in the operation's `request_schema.body` (checked at validate/publish) |
| `hdr` | request | Caller headers declared in the operation's `request_schema.headers` (case-insensitive); no other caller header is visible. `hdr.idempotency_key` is always available |
| `account` | request, response | `account.id`, `account.region`, `account.config.*` (non-secret) |
| `creds` | request headers, query, auth only | Secret values (compile error elsewhere unless `allow_creds_in_body: true` after platform review). An account of an auth `none` vendor has none |
| `session` | later steps of a multi-step flow | What step 1's `session:` section stored (e.g. `session.vendor_txn_id`) |
| `res` | response and errors | `res.status`, `res.body`, `res.text`, `header(res, "X")` |
| `now()` | all | Current time (pinned in samples) |

Standard library only (DESIGN §7.3): `lower upper trim split join replace format truncate`, `now epochSeconds epochMillis parseTime formatTime addDuration`, `?? merge pick omit flatten map filter len toJSON fromJSON`, `uuid sha256 hmacSHA256 base64 urlEncode`, `phoneE164 emailNormalize currencyMinor maskPAN maskAadhaar`. No loops beyond `map`/`filter`, no network, no disk; expressions compile once at snapshot build.

YAML traps: quote any expression containing `: ` or `#`; wrap an object literal right after `map(x,` in parentheses.

## Pass-through (no response transformer)

Leave `response` out (or `null`) in the target's pair. Then, on a **sync** answer (also a `wait` answer inside its window):
- a vendor 2xx returns the vendor's status and body unchanged; JSON stays JSON, anything else keeps its `Content-Type`;
- a vendor non-2xx is still classified for retries and the breaker (timeouts, 429, 5xx retryable; other 4xx not); once retries are exhausted the caller gets the vendor's status and body unchanged;
- only the vendor's `Content-Type` and `Retry-After` headers pass, plus the Hub's own (`X-Hub-Request-Id`, ...) and `X-Hub-Pass-Through: true`. Every other vendor header is dropped;
- timeouts, an open circuit and every Hub-side failure still use the canonical error envelope.

Requests completed later (202, `async`, `async_callback`) store the vendor's body as the result (non-JSON as a JSON string) and report failures with the usual `UPSTREAM_*` codes.

Use pass-through when callers want the vendor's answer as it is. Warn the owner: the body is the vendor's, so it may name the vendor (DESIGN §0 is kept for headers only). Add a response transformer when callers need a stable shape across vendors, error mapping, or fewer fields.

## Error mapping

Response transformers end with ordered rules; the first match wins:

```yaml
errors:
  - when: res.status == 401 || res.status == 403
    code: UPSTREAM_AUTH_FAILED            # pages on-call
    retryable: false
  - when: res.status == 429
    code: UPSTREAM_RATE_LIMITED
    retryable: true
    retry_after_ms: int(header(res, "Retry-After") ?? "1") * 1000
  - when: 'res.status == 503 && string(res.body.error ?? "") contains "account ID"'   # vendor quirk above generic rules
    code: UPSTREAM_AUTH_FAILED
    retryable: false
  - when: res.status >= 500
    code: UPSTREAM_UNAVAILABLE
    retryable: true
  - when: res.status >= 400               # always end with a catch-all
    code: UPSTREAM_REJECTED
    retryable: false
    message: res.body.error               # shown to the caller: must not name the vendor or account
```

- Codes come only from the `UPSTREAM_*` set (DESIGN §4.4). `TRANSFORM_FAILED` is never mapped: the engine raises it when an expression throws (always a Hub bug, it pages).
- Timeouts and connection errors are classified by the upstream client, not here.
- A business "no" is often a 200, not an error (e.g. bank account `invalid`): map it into the response.
- A fixed, vendor-neutral `message` (`'"The OTP does not match"'`) is better than passing vendor text through.

## Samples (golden fixtures)

Mandatory: at least one sample per transformer, and a transformer whose samples fail cannot be stored or published (AGENTS §3.4).

Through the admin API, MCP and console a sample is an object (`GoldenFixture` in the admin contract):
- request: `{name, input, input_headers?, account_config?, expected_request}`. `input` is the caller's body, `input_headers` the caller headers (read as `hdr.*`), `expected_request` is `{method, path, query?, headers?, body?}` without credential headers;
- response: `{name, vendor_response: {status, headers, body}, expected_response | expected_error}`.

In a registry tree (local dev, examples):

```text
transformers/<ID>/<version>.yaml
transformers/<ID>/fixtures/<case>.input.json              # request: caller body + optional "_context"
transformers/<ID>/fixtures/<case>.expected_request.json   # request: {method, path, query, headers, body}
transformers/<ID>/fixtures/<case>.vendor_response.json    # response: {status, headers, body} + optional "_context"
transformers/<ID>/fixtures/<case>.expected_response.json  # response: {"response": ..., "session": ...} or {"error": ...}
```

`fixtures/v<N>/` holds samples for version N only, when a version needs its own. `_context` supplies `hdr` (the sample's caller headers), `creds` (obviously fake, e.g. `fixture-passcode`), `account` (`id`, `region`, `config`), `session`, `now` and, for response fixtures, `req`. Cover: minimal input, all optional fields, each `??` default, every error rule (401, 403, 429 with and without `Retry-After`, 5xx, generic 4xx, vendor quirks), a non-JSON error page, and partial success.

Check expected outputs against the vendor's docs by hand; never just regenerate them.

## Create, test and dry-run

Role: `editor`.

**Console:** Transformers → **New transformer** (or *id* → **New version**, which starts from the latest version's source and samples) → id, Kind, Vendor endpoint (request only), vendor documentation URL, **Source (transformer YAML)**, samples (request: **Input body**, **Expected vendor request**; response: **Vendor response**, **Expected output**; **Add sample**) → **Run samples on this draft**. Each failing sample shows why, with the field (for example `/query/name`). **Create <ref>** unlocks only when the exact draft passes → reason → confirm. On a stored version: **Run samples for <ref>** (runs every sample now, nothing saved) and **Dry-run** (start from a sample, pick an optional account, edit the input or vendor response, run; nothing is sent or saved).

**MCP** (iterate on a draft, then store):
1. `hub_admin_get_transformer {"transformer_id": "T1"}` → read the latest version (and its `latest_version`) if you are changing one.
2. `hub_admin_dry_run_transformer {"draft": {"kind": "request", "vendor_endpoint": "clevertap.upload@1", "yaml": "..."}, "account_id": "ct-brandA-in", "input": {...caller body...}, "input_headers": {...}}` → the exact vendor request, secrets `***`, and `schema_errors` against the endpoint's request schema. For a response transformer pass `vendor_response` instead of `input`; for a later step pass `session`.
3. `hub_admin_run_transformer_goldens {"draft": {...}, "fixtures": [...]}` → per-sample pass/fail with `diff` and `schema_errors`.
4. `hub_admin_create_transformer_version {"transformer_id": "T1", "kind": "request", "vendor_endpoint": "clevertap.upload@1", "expected_latest_version": 7, "yaml": "...", "fixtures": [...], "docs_url": "https://...", "reason": "..."}` → stores `T1@8` only if it compiles and every sample passes both `goldens_pass` and `endpoint_schema`; otherwise 422 with `details[]` (sample, field), nothing stored. Omit `expected_latest_version` for a new transformer id. Omit `vendor_endpoint` for `kind: response`.
5. Reference `T1@8` from a new integration version ([cutover.md](cutover.md)); validate checks its field reads and sample inputs against the operation.

**hubctl** (files in the repo, reviewed in a PR; D7):
```bash
cd backend
bin/hubctl transform test            # every transformer's samples, including endpoint_schema (same runner as CI and create)
bin/hubctl transform test -v T1@8    # one version, printing passing cases too
bin/hubctl dry-run int_evt_brandA body.json   # through the live active version, nothing sent
```
`hubctl apply` does not upload transformers; store them with the console, MCP or `createTransformerVersion`, or (local dev) keep them in `backend/config/transformers/` with `-config-watch`.

**Caller-side dry-run** (a developer with a `dry_run`-scoped key): `POST /v1/integrations/{id}/dry-run` or MCP `hub_dry_run` shows what the active version would send for a body. Same request, seen from the caller.

**Admin API:** `dryRunTransformer`, `runTransformerGoldens`, `createTransformerVersion`.

## Checklist

- [ ] Vendor doc URL in `docs_url` and the file header; payload checked against it.
- [ ] No `capability` or `operation` key; request transformers name `vendor_endpoint`, response transformers do not.
- [ ] Config over fork considered; a new version, not an edit.
- [ ] `creds.*` only in headers, query or auth.
- [ ] Every `req.*` / `hdr.*` read is declared in the operation's request schema of each integration that uses it.
- [ ] Request samples fit the endpoint's request schema (`endpoint_schema`) and the operation's (`input_schema`).
- [ ] Response transformer: present only if needed; error rules ordered, catch-all last, no vendor identity in `message`. Pass-through: the owner accepted the vendor's body as it is.
- [ ] Samples for every case; `hubctl transform test` or `hub_admin_run_transformer_goldens` green.
- [ ] Multi-step: one pair per operation in the target ([multi-step-flows.md](multi-step-flows.md)).
- [ ] Exercised against `mockvendor`, never a real vendor, before production.
