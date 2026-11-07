# Integrations, versions and cutover

**The integration id never changes; you publish a new version behind it** (DESIGN §6.1). Switching account (OpenAI account A → B), vendor (CleverTap → MoEngage) or transformer (`T1@7` → `T1@8`), and changing an operation's request schema, are all a new integration version. Callers change nothing and deploy nothing, unless you change what they must send or what they get back.

## Contents
- [The version spec](#the-version-spec)
- [Create an integration and publish v1](#create-an-integration-and-publish-v1)
- [Draft, validate, diff, publish (the loop for every change)](#draft-validate-diff-publish)
- [Switch to another account](#switch-to-another-account)
- [Switch to another vendor](#switch-to-another-vendor)
- [Canary with weights](#canary-with-weights)
- [Shadow traffic and failover](#shadow-traffic-and-failover)
- [Roll back](#roll-back)
- [Pending work: re-resolve vs pinned (D10)](#pending-work-re-resolve-vs-pinned)
- [After every publish](#after-every-publish)

## The version spec

Admin API / MCP / console form (JSON or YAML in the console editor; durations in ms):

```json
{
  "operations": {
    "default": {
      "description": "Track a single event for a user.",
      "idempotent": true,
      "validate_input": true,
      "request_schema": {
        "body": { "type": "object", "required": ["user", "event"],
                  "properties": { "user":  { "type": "object", "required": ["id"],
                                             "properties": { "id": { "type": "string", "x-pii": "identifier" } } },
                                  "event": { "type": "object", "required": ["name"],
                                             "properties": { "name": { "type": "string" },
                                                             "timestamp": { "type": "string", "format": "date-time" },
                                                             "properties": { "type": "object", "x-pii": "free_text" } } } } }
      }
    }
  },
  "targets": [
    { "vendor_id": "clevertap", "account_id": "ct-brandA-in",
      "transformer": { "request": "T1@7", "response": "R1@2" }, "weight": 100 }
  ],
  "failover": [],
  "delivery": { "mode": "wait", "wait_ms": 3000, "ttl_ms": 21600000,
                "on_circuit_open": "fail_fast", "pending": "re_resolve" },
  "webhooks": [ { "event": "request.completed", "endpoint_id": "reg_orders_cb" } ],
  "audit": { "payloads": "masked", "retention_ms": 2592000000 },
  "shadow": null
}
```

- **`operations`** (required, at least one): what callers invoke. Single-operation integrations use `default`. Each has `request_schema.body` (required; `{"type": "object"}` when a call has no fields), optional `request_schema.headers` (the caller headers the transformer may read as `hdr.*`), `idempotent`, a caller-facing `description` (no vendor, account or version in it) and `validate_input` (default `true`: live calls are checked against the schema, 400 `VALIDATION_FAILED`). Fields in `required` are required, the rest optional. `x-pii` tags drive audit masking.
- **`targets[].transformer`**: `request` is required; `response` is optional (omit or `null` = pass-through, see [transformers.md](transformers.md#pass-through-no-response-transformer)).

Registry YAML files (what `hubctl export` writes and `apply` reads) use slightly different keys: `vendor:` / `account:` instead of `vendor_id` / `account_id`, `endpoint:` in webhooks, durations like `ttl: 6h` and `retention: 30d`, and `polling: true` under `delivery` to declare polling. Example: `backend/config/integrations/int_evt_brandA/versions/2.yaml`.

Rules the publish check enforces (DESIGN §6.4, ADR-0023; check names from `validateIntegrationVersion`): the operation schemas compile and every `req.*` / `hdr.*` a transformer reads is declared in them (`schemas_compile`), expressions type-check, request and response samples pass (`goldens_pass`), request samples' inputs fit the operation's schema (`input_schema`) and their vendor requests fit the endpoint's schema (`endpoint_schema`), referenced accounts and transformers exist and are not retired, interactive integrations (OTP, chat) only use `sync`, target weights sum to 100, a delivery path is declared (`wait`/`async` need polling or a webhook; `async_callback` needs a webhook), and every operation has a transformer pair (`operations_have_transformers`). Failures carry `pointer`, `sample` and `field`. There is no response schema check.

Choosing `delivery.mode`: `sync` when a person is waiting (OTP, bank check, chat); `wait` (default, 3 s) for most server-to-server calls; `async` for fire-and-forget; `async_callback` when the tenant wants a signed callback. `on_circuit_open` (sync only): `fail_fast` (default), `failover` (needs a failover chain and idempotent calls), `queue_if_allowed` (never for OTP, payments or chat). Audit `payloads`: `sealed` for bureau, Aadhaar and PAN (D6), `metadata_only` for LLM prompts, `masked` default.

## Create an integration and publish v1

Role: `editor`. The owner tenant, the account and the transformers must exist. The operations and their request schemas go into the first draft, not into the integration itself. For a first onboarding from nothing, follow [onboard-an-api.md](onboard-an-api.md).

1. **Create the id** (hubctl cannot create integrations):
   - **Console:** Integrations → **Create integration** → Integration id, Owner tenant, Description, grants, money flag, **Interactive** (only for steps of a session a person waits on; fixed at creation) → reason → **Create integration**. The console opens the first draft.
   - **MCP:**
     ```json
     hub_admin_create_integration {
       "integration_id": "int_evt_brandA", "owner_tenant": "orders-service",
       "description": "Brand A engagement events", "grants": [],
       "interactive": false, "moves_money_or_quota": false, "reason": "Onboard brand A events, OPS-123"
     }
     ```
     Set `moves_money_or_quota: true` for OTPs, bureau pulls, penny drops, payments: callers then must send an `Idempotency-Key`. Owner and `interactive` are permanent. A request schema change that breaks callers (a new required field, a removed field, a narrower type) should use a new integration id (D11, now guidance); publishing shows the schema diff and the change log records it.
   - **Admin API:** `POST /admin/v1/integrations` (`createIntegration`).
   Until v1 is published, invocations answer 503 `INTEGRATION_PAUSED` (reason `no_active_version`).
2. **Draft v1** with the spec above (operations with their request schemas, targets, delivery, audit), **validate**, then **publish** with `expected_active_version: null` (see the loop below). In the console's draft form, build each operation's **Request body** (and optional **Request headers**) with **Paste a sample** → **Generate fields**, tick or untick **required** per field, and keep **Check live calls against the request schema** on unless callers must reach the transformer with input the schema does not describe.

## Draft, validate, diff, publish

The same loop for every change. Publishing changes live traffic within about 10 s.

**Console:** Integrations → *id* → **Edit a new draft** (or **Review draft vN and publish**) → edit the operations, targets and the rest (form or YAML/JSON) → reason → **Create draft** / **Save draft vN** → **Run publish checks on vN** (each check pass/fail with a pointer to the line) → read **Changes against the active version** → **Publish vN** → confirm with a reason.

**hubctl (config as code):**
```bash
bin/hubctl export -out /tmp/live
cp /tmp/live/integrations/int_evt_brandA/versions/7.yaml /tmp/live/integrations/int_evt_brandA/versions/8.yaml
# edit 8.yaml (version: 8, the change) and set active_version: 8 in integration.yaml
bin/hubctl plan  -config-dir /tmp/live           # shows "+ integration int_evt_brandA v8" and the paths
bin/hubctl apply -config-dir /tmp/live -reason "Move brand A to the new workspace, OPS-140"        # prints the actions
bin/hubctl apply -config-dir /tmp/live -reason "Move brand A to the new workspace, OPS-140" -yes   # drafts + publishes v8
```
The new file must be exactly the next version number, and `apply` publishes with `expected_active_version` = the live active version, so a concurrent publish makes it fail rather than overwrite.

**MCP:**
1. `hub_admin_get_integration {"integration_id": "int_evt_brandA"}` → note `active_version` (say 7).
2. `hub_admin_create_integration_draft {"integration_id": "int_evt_brandA", "based_on": 7, "spec": {...}, "reason": "..."}` → returns the draft version (8). Only one draft may exist per integration (`CONFLICT` otherwise: edit the existing one). Without `spec` it copies `based_on`; then change it with `hub_admin_update_integration_draft {"integration_id", "version": 8, "spec": {...full spec...}, "reason"}`.
3. `hub_admin_validate_integration_version {"integration_id": "int_evt_brandA", "version": 8}` → fix every `fail`.
4. `hub_admin_diff_integration_versions {"integration_id": "int_evt_brandA", "from": 7, "to": 8}`.
5. Show the user the diff and the checks; wait for an explicit yes.
6. `hub_admin_publish_integration_version {"integration_id": "int_evt_brandA", "version": 8, "expected_active_version": 7, "reason": "...", "confirm": true}`.

**Admin API:** `createIntegrationDraft` → `updateIntegrationDraft` → `validateIntegrationVersion` → `diffIntegrationVersions?from=7&to=8` → `publishIntegrationVersion` with `{"expected_active_version": 7, "reason": "..."}`.

## Switch to another account

Same vendor, same payload shape, different credentials (another brand, region or workspace). Only `account_id` changes, so the transformers are reused and values come from the new account's `config`.

1. Make sure the new account exists, is `active`, and passed `testAccountConnection` ([vendors-and-accounts.md](vendors-and-accounts.md)).
2. Draft from the active version, change `targets[0].account_id` (and any `failover` / `shadow` entries that should move).
3. Validate → diff → publish (or canary first, below).
4. The old account goes `draining` once no active version targets it; retire it later, after pinned work and sessions are gone.

## Switch to another vendor

The operations and their request schemas stay as they are: callers keep sending the same body. Only the target changes.

1. **Vendor + endpoints (with their request schemas) + account** for the new vendor ([vendors-and-accounts.md](vendors-and-accounts.md)).
2. **Request transformer** for the new vendor, written against the **same operation schema**: it may read only the `req.*` / `hdr.*` fields the operation declares, and its samples use real caller inputs of that operation (validate checks both). For multi-operation integrations, one pair per operation.
3. **Response transformer**: decide what callers get.
   - **Warn the owner first: there is no canonical output any more.** Without a response transformer that reproduces the old output, callers receive a different body after the switch (in pass-through, the new vendor's own body).
   - To keep callers unchanged, write a new response transformer whose samples produce exactly the outputs the old one produced (reuse the old response samples' `expected_response` values as the new samples' expected outputs).
   - If the old target was pass-through, callers were getting the old vendor's body; the new vendor's body will differ unless a response transformer rebuilds it.
4. **Prove the mapping** before any traffic: `hub_admin_dry_run_transformer {"ref": "MO1@1", "account_id": "mo-brandA", "input": {...caller body...}}` (or `hubctl transform test MO1`), and compare with the vendor's documented example request.
5. **Draft** with the new target (`vendor_id`, `account_id`, `transformer`), validate (watch `goldens_pass`, `endpoint_schema`, `input_schema`, `schemas_compile`), diff (compare the response samples of the old and new response transformers side by side), publish. Safer: canary with weights or shadow first, and tell the calling team if their output changes.

## Canary with weights

Put both targets in one version and move traffic in steps (DESIGN §6.4: v8 A 90 / B 10, v9 50/50, v10 B 100). The weighted choice is hashed on the `Idempotency-Key` (else the request id), so retries of one business event stay on the same target.

1. Draft and publish a version with two targets:
   ```json
   "targets": [
     {"vendor_id": "clevertap", "account_id": "ct-brandA-in",  "transformer": {"request": "T1@7", "response": "R1@2"}, "weight": 90},
     {"vendor_id": "clevertap", "account_id": "ct-brandA-in2", "transformer": {"request": "T1@7", "response": "R1@2"}, "weight": 10}
   ]
   ```
2. Watch errors and latency per account (Health board, `hub_admin_search_requests {"account_id": "ct-brandA-in2"}`).
3. Move the weights. Each change creates (and by default publishes) version n+1 with the same targets:
   - **Console:** Integration → **Set weights** (shown when the active version has more than one target).
   - **MCP:** show current next to new weights, then
     ```json
     hub_admin_set_integration_weights {"integration_id": "int_evt_brandA", "expected_active_version": 8,
       "weights": [{"vendor_id": "clevertap", "account_id": "ct-brandA-in", "weight": 50},
                   {"vendor_id": "clevertap", "account_id": "ct-brandA-in2", "weight": 50}],
       "publish": true, "reason": "Canary step 2", "confirm": true}
     ```
     `publish: false` leaves it as a draft to publish later.
   - **hubctl:** a new version file with the new weights + `active_version`, then `apply`.
   - **Admin API:** `updateIntegrationWeights`.
   The set of targets must equal the active version's; weights sum to 100.
4. At 100 %, publish a final version with only the new target, so the old account can drain.

## Shadow traffic and failover

- **Shadow** (`spec.shadow`): `{"target": {...TargetRef...}, "mode": "send" | "dry_run", "sample_ratio": 0.1}`. Requests are served by the normal target and copied to the shadow; the shadow result is logged, never returned. Use `dry_run` (transform only) for vendors with side effects (SMS, OTP, payments). Session steps are never shadowed.
- **Failover** (`spec.failover`): an ordered list of TargetRefs used for `on_circuit_open: failover` or a vendor 5xx in sync mode. Only safe for idempotent endpoints or calls with an `Idempotency-Key`; the response carries `X-Hub-Served-By: failover:1`.

## Roll back

Rollback = publish a **new** version with an older version's content (v9 = v7); history stays linear and nothing is deleted. It re-runs full validation (an account may have been retired since).

- **Console:** Integration → version history → **Roll back to vN** → confirm with a reason.
- **MCP:** show `hub_admin_diff_integration_versions` from the active version to the target, then `hub_admin_rollback_integration {"integration_id": "int_evt_brandA", "to_version": 7, "expected_active_version": 8, "reason": "Errors on the new account", "confirm": true}`.
- **hubctl:** add version n+1 with the old content and `active_version: n+1`, then `apply` (`apply` refuses to just move `active_version` back).
- **Admin API:** `rollbackIntegration` `{"to_version", "expected_active_version", "reason"}`.

An emergency stop for one integration (not a rollback) is **Pause integration** in the console: callers get 503 `INTEGRATION_PAUSED`, queued work is held, other integrations on the account are unaffected. MCP cannot pause.

## Pending work: re-resolve vs pinned

`delivery.pending` decides what happens to work already queued or parked when you publish (D10, DESIGN §6.3):

| Request at publish time | Served by |
| --- | --- |
| Completed before | Old version (its audit keeps it forever) |
| In flight | Old version (nothing switches mid-call) |
| Queued / parked in the outbox | **New version** with `re_resolve` (default: the envelope holds the caller's body, so it is re-transformed for the new target); **old version** with `pinned` |
| Retry of a failed request with the same key | New version (a retry is a new attempt) |
| First new request | New version |
| Step 2 of a vendor session opened before | **Old version + old account, always** (sessions are pinned) |

Keep `re_resolve` unless the queued payload must reach the exact account that accepted it (for example a contractual per-account quota or data residency). `pinned` keeps the old account `draining` until that work finishes, and retire is refused until it is zero.

## After every publish

- Nodes switch within about 10 s; **Snapshots** shows the new snapshot, and a node that cannot fetch keeps the last good one.
- Watch the **Health** board, **Requests** filtered by integration, and alerts (`docs/runbooks/`).
- Tell the calling team what changed for them: nothing, if the operations' request schemas and the response output are the same (`hub_describe_integration` shows the request schemas; the response samples show the output). If the output changed, they must hear it before the publish.
