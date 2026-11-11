# ADR-0028: Callers authenticate as ISFC backend clients, checked in memory; no OPA on the sync path

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-11 |
| Deciders | lead, product owner (decision D2 in `docs/tech/AUTH_PLAN.md` §19) |
| Tickets | HUB-467 (this ADR); implementation HUB-483, HUB-486 to HUB-490 |
| DESIGN refs | §0, §5.1, §16.5, §18.5, §18.6 |

Status of the behaviour described here: **planned (EP-15)**.

## Context
Calling teams use `hk_live_` tenant keys today. The company wants callers to be ISFC `backend_services` clients that use the ISFC auth lib (it fetches the API key from auth-service, caches it and refreshes once on 401/403). Two earlier requests conflict: "no Rego on the client API" and "OPA for callers too". DESIGN §18.5 and §18.6 allow about 0.1 ms in total for auth, grant check and tenant limit on the sync path, with **no network hop**. An OPA sidecar is an HTTP call to `localhost:8181` per request (typically 0.3 to 1 ms) and adds a failure mode: with fail-closed, `/v1/invoke` would go down whenever OPA does.

## Options considered
1. **OPA sidecar for `/v1`** (the ISFC default). Reuses ISFC Rego, but breaks the §18.5 budget and couples invoke availability to OPA.
2. **Embedded OPA** (`open-policy-agent/opa/v1/rego`, prepared query, in process). No hop; cost unknown until measured.
3. **In-memory check in Go of the same data OPA would use** (key exists, ACTIVE, issued by the Hub backend client to this caller, permission, tenant). Cheapest; Hub-only code.

## Decision
Option 3 (D2), with embedded OPA only if HUB-490 proves it fits.

1. **Credential.** Callers send `x-isfc-client-id` + `x-isfc-api-key` (and `X-Tenant-Id` when the key covers more than one tenant). `Authorization: Bearer hk_...` stays the legacy path; both present is 400. Switch: `HUB_EDGE_ISFC_AUTH=off|on` (default off); `on` accepts ISFC credentials **in addition to** `hk_live_` keys.
2. **Data source.** The reconciler reads `GET /api/v1/client/configuration/<Hub backend client>` and hashes each key with `registry.HashKey` the moment it is read into `hub.isfc_caller_keys`; **plaintext is never stored or logged** (gap G4). Gateway nodes load the hashes into an in-memory map via a Redis notification, without touching the config snapshot.
3. **Edge rules.** SHA-256 of the key, map lookup, constant-time compare. Unknown, not ACTIVE or `caller_client_id` different from `x-isfc-client-id`: 401. One covered tenant: that tenant; more: `X-Tenant-Id` (ISFC UUID, mapped to the Hub id by the snapshot copy) is required and must be covered. Tenant `setup: pending`, `disabled` or ISFC INACTIVE: 401; `suspended`: 403 `tenant_suspended` as today. `HUB:INVOKE`, `HUB:DRY_RUN`, `HUB:REQUESTS_READ` map to the existing scopes; integration-level control stays in Hub grants. Everything after (grants, tenant limiter, body limit, validation, idempotency keyed by the Hub tenant) is unchanged. The `request.received` audit record carries `caller_client_id`, `isfc_key_id` and the key hash, never the key; the logger redacts `x-isfc-api-key` by value.
4. **Latency argument.** The path is one SHA-256, one map lookup and a small slice scan, inside the `auth` stage budget of §18.6, with no network hop and no new dependency on `/v1`. The benchmark in HUB-490 (10k keys, p99) proves it and is recorded in `docs/tech/LATENCY.md`.
5. **OPA latency numbers: pending.** The local OPA sidecar round-trip p50/p99 comes from HUB-463, which is blocked on a running ISFC local stack. This ADR does not depend on the numbers: even at the optimistic 0.3 ms a sidecar hop exceeds the whole 0.1 ms sync auth budget, and the availability coupling remains. The ADR will be amended with the measured figures; embedded OPA is considered only if HUB-490 shows its p99 inside the budget.
6. **Revocation is slower** than with `hk_` keys: up to one reconcile interval (5 min) without ISFC events, seconds with ISFC-CR-2, plus the "Sync now" action (gap G15). Documented in SECURITY.md.
7. **Legacy cut-off rule.** `hk_live_` keys keep working while callers move team by team (`hub_edge_auth_total{method}` shows progress). Only after every caller has moved, and at least 6 months (by analogy with D11), set `HUB_EDGE_LEGACY_KEYS=off`; admin key-minting routes then return 410 in ISFC mode.
8. **Interim linking (D5).** Until ISFC-CR-1, one caller client per tenant with a Hub-side list (ADR-0027).
9. **Caller contract unchanged.** Integration id + operation + body; no vendor, account or version is exposed (DESIGN §0, D12). The new credential is identity only.

## Consequences
- Positive: callers get the ISFC model (own backend client, auth lib, access granted and revoked in ISFC); the sync path keeps its budget and its independence from OPA and ISFC; existing callers are untouched.
- Negative / costs: the Hub re-implements the key check ISFC would do in Rego, so the two can drift (tested against `mockisfc`); revocation delay; until CR-1 a key cannot express a tenant in ISFC; ISFC keys are plaintext in ISFC (gap G4) until ISFC-CR-4 adds `key_sha256`.
- Follow-ups: contract HUB-470 (`isfcS2S` scheme, optional `X-Tenant-Id`); HUB-415 (enforce `requests_read`) before HUB-487; SDK `WithISFC` and consumer skill (HUB-489).

## Deviation from DESIGN.md
None to §18.5 and §18.6: the budget is kept by not adding a hop. DESIGN §5.1 describes tenant bearer keys; ISFC credentials are an additional method.
