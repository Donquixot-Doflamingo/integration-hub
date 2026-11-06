# Transformers: mapping the caller's input to the vendor and back

A transformer is an immutable, versioned YAML mapping (DESIGN §7). A **request** transformer turns the caller's canonical body into the vendor's HTTP request. A **response** transformer turns the vendor's response into the capability's canonical response, or into a canonical error. An integration version picks one pair per target (and per operation, for multi-step capabilities). The full DSL reference for platform engineers is `backend/.claude/skills/hub-transformers/` (and its `references/dsl.md`); this page is the walk-through.

## Contents
- [The round trip, step by step](#the-round-trip-step-by-step)
- [Expression context](#expression-context)
- [Error mapping](#error-mapping)
- [Golden fixtures](#golden-fixtures)
- [Create, test and dry-run (console, hubctl, MCP)](#create-test-and-dry-run)
- [Checklist](#checklist)

## The round trip, step by step

Real files from the dev registry: `backend/config/transformers/T1/7.yaml` (request) and `R1/2.yaml` (response), capability `events.track@v1`, vendor CleverTap.

**1. The caller sends the canonical body** (shape from `contracts/schemas/capabilities/events.track.v1.request.json`; no vendor field anywhere):

```json
POST /v1/integrations/int_evt_brandA/invoke
{ "user": { "id": "u_48213" }, "event": { "name": "app_opened" } }
```

**2. The request transformer maps it.** Left side = path in the vendor request; right side = an `expr-lang/expr` expression over `req` (the validated canonical body), `account` (non-secret account config), `creds` (secrets, headers only), `session`, `hdr`:

```yaml
transformer: T1
version: 7
kind: request
capability: events.track@v1
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

**3. The vendor receives** (credentials injected from the account at send time; audit and dry-run show them as `***`):

```json
{ "method": "POST", "path": "/1/upload",
  "headers": { "X-CleverTap-Account-Id": "***", "X-CleverTap-Passcode": "***",
               "Content-Type": "application/json; charset=utf-8" },
  "body": { "d": [ { "identity": "u_48213", "type": "event", "evtName": "app_opened", "ts": 1793438100 } ] } }
```

**4. The vendor answers** `200 {"status": "success", "processed": 1, "unprocessed": []}`.

**5. The response transformer maps it back** to the canonical response (`events.track.v1.response.json`). `res.status`, `res.body` (parsed JSON), `res.text` (non-JSON body) and `header(res, "X")` are available:

```yaml
transformer: R1
version: 2
kind: response
capability: events.track@v1
response:
  status:     'len(res.body.unprocessed ?? []) == 0 ? "ok" : "partial"'   # quote anything with ": "
  accepted:   res.body.processed
  rejected:   'map(res.body.unprocessed ?? [], ({reason: string(#.error ?? "rejected")}))'
  vendor_ref: header(res, "X-Request-Id")
  raw:        res.body
errors: [...]   # see Error mapping
```

**6. The caller receives** `200 {"status": "ok", "accepted": 1, "rejected": []}` — the same schema whichever vendor or account served it. That is what makes a vendor switch invisible: a new vendor needs its own pair that produces this same canonical response.

Rules of thumb:
- **Config over fork** (DESIGN §3.3): if two accounts differ only in values (project id, brand code, field names), read them from `account.config.*` in one transformer. Fork only when the payload *shape* differs (brand B adds fields → `T2`).
- **Changing a transformer = a new version** (`T1@8`). It carries traffic only after an integration version references it and is published.
- If the vendor needs something the canonical schema lacks: `account.config`, a default, or a capability change through the contract-steward. Never a vendor-specific field in the canonical schema.

## Expression context

| Var | Where | Contents |
| --- | --- | --- |
| `req` | request | Validated canonical body |
| `hdr` | request | Allowlisted caller headers |
| `account` | request, response | `account.id`, `account.region`, `account.config.*` (non-secret) |
| `creds` | request headers, query, auth only | Secret values (compile error elsewhere unless `allow_creds_in_body: true` after platform review) |
| `session` | later steps of a multi-step flow | What step 1's `session:` section stored (e.g. `session.vendor_txn_id`) |
| `res` | response and errors | `res.status`, `res.body`, `res.text`, `header(res, "X")` |
| `now()` | all | Current time (pinned in goldens) |

Standard library only (DESIGN §7.3): `lower upper trim split join replace format truncate`, `now epochSeconds epochMillis parseTime formatTime addDuration`, `?? merge pick omit flatten map filter len toJSON fromJSON`, `uuid sha256 hmacSHA256 base64 urlEncode`, `phoneE164 emailNormalize currencyMinor maskPAN maskAadhaar`. No loops beyond `map`/`filter`, no network, no disk; expressions compile once at snapshot build.

YAML traps: quote any expression containing `: ` or `#`; wrap an object literal right after `map(x,` in parentheses.

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

## Golden fixtures

Mandatory: a transformer whose goldens fail cannot be stored or published (AGENTS §3.4). In the registry tree:

```text
transformers/<ID>/<version>.yaml
transformers/<ID>/fixtures/<case>.input.json              # request: canonical input + optional "_context"
transformers/<ID>/fixtures/<case>.expected_request.json   # request: {method, path, headers, body}
transformers/<ID>/fixtures/<case>.vendor_response.json    # response: {status, headers, body} + optional "_context"
transformers/<ID>/fixtures/<case>.expected_response.json  # response: {"response": ..., "session": ...} or {"error": ...}
```

`fixtures/v<N>/` holds goldens for version N only, when a version needs its own. `_context` supplies `creds` (obviously fake, e.g. `fixture-passcode`), `account` (`id`, `region`, `config`), `session` and, for response fixtures, `req`. Cover: minimal input, all optional fields, each `??` default, every error rule (401, 403, 429 with and without `Retry-After`, 5xx, generic 4xx, vendor quirks), a non-JSON error page, and partial success. Through the admin API and MCP, fixtures are objects `{name, input, account_config, expected_request}` or `{name, vendor_response, expected_response | expected_error}` (`GoldenFixture` in the admin contract).

Check expected outputs against the vendor's docs by hand; never just regenerate them.

## Create, test and dry-run

Role: `editor`.

**MCP** (iterate on a draft, then store):
1. `hub_admin_get_transformer {"transformer_id": "T1"}` → read the latest version (and its `latest_version`) if you are changing one.
2. `hub_admin_dry_run_transformer {"draft": {"kind": "request", "capability": "events.track@v1", "vendor_endpoint": "clevertap.upload@1", "yaml": "..."}, "account_id": "ct-brandA-in", "input": {...canonical body...}}` → the exact vendor request, secrets `***`. For a response transformer pass `vendor_response` instead of `input`; for a later step pass `session`.
3. `hub_admin_run_transformer_goldens {"draft": {...}, "fixtures": [...]}` → per-fixture pass/fail with diffs.
4. `hub_admin_create_transformer_version {"transformer_id": "T1", "kind": "request", "capability": "events.track@v1", "vendor_endpoint": "clevertap.upload@1", "expected_latest_version": 7, "yaml": "...", "fixtures": [...], "docs_url": "https://...", "reason": "..."}` → stores `T1@8` only if it compiles, type-checks and every golden passes (else `PUBLISH_VALIDATION_FAILED`, nothing stored). Omit `expected_latest_version` for a new transformer id.
5. Reference `T1@8` from a new integration version ([cutover.md](cutover.md)).

**hubctl** (files in the repo, reviewed in a PR; D7):
```bash
cd backend
bin/hubctl transform test            # every transformer's goldens (same runner as CI and publish)
bin/hubctl transform test -v T1@8    # one version, printing passing cases too
bin/hubctl dry-run int_evt_brandA body.json   # through the live active version, nothing sent
```
`hubctl apply` does not upload transformers; store them with MCP or `createTransformerVersion`, or (local dev) keep them in `backend/config/transformers/` with `-config-watch`.

**Console:** Transformers → *id* → version: the YAML, the fixtures, **Run goldens for <ref>** (runs every fixture now, nothing saved) and **Dry-run** (start from a fixture, pick an optional account, edit the input or vendor response, run; nothing is sent or saved). Creating a version is not in the console yet.

**Caller-side dry-run** (a developer with a `dry_run`-scoped key): `POST /v1/integrations/{id}/dry-run` or MCP `hub_dry_run` shows what the active version would send for a canonical body. Same request, seen from the caller.

**Admin API:** `dryRunTransformer`, `runTransformerGoldens`, `createTransformerVersion`.

## Checklist

- [ ] Vendor doc URL in `docs_url` and the file header; payload checked against it.
- [ ] Config over fork considered; a new version, not an edit.
- [ ] `creds.*` only in headers, query or auth.
- [ ] Response output matches the capability response schema exactly (publish checks `response_conforms`).
- [ ] Error rules ordered, catch-all last, no vendor identity in `message`.
- [ ] Goldens for every case; `hubctl transform test` or `hub_admin_run_transformer_goldens` green.
- [ ] Multi-step: `operation:` set and one pair per operation ([multi-step-flows.md](multi-step-flows.md)).
- [ ] Exercised against `mockvendor`, never a real vendor, before production.
