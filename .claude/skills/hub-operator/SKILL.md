---
name: hub-operator
description: "Operate the Integration Hub control plane: tenants and API keys; onboard a third-party API from the panel without code (vendor, endpoint with its request schema from a sample, account with credentials or none for a public API, request transformer proven by samples, optional response transformer or pass-through, integration with operations and their input schema, validate, publish); callback endpoints and X-Hub-Signature; safe vendor or account changes; switching vendor or account (draft, validate, publish, weights, shadow, rollback, re-resolve vs pinned); multi-step flows such as Aadhaar OTP with X-Hub-Session. Shows console clicks, hubctl and hubmcp calls for each task. Use whenever someone asks how to onboard, configure, cut over, map, test or troubleshoot anything behind an integration id, mentions vendor, endpoint, request schema, account, transformer, sample, golden, pass-through, validate_input, publish, rollback, OTP or session, or names hubctl, hub_admin_* tools or the admin console."
---

# Operating the Integration Hub

Words used here (DESIGN §3.1):

| Word | Means | Example |
| --- | --- | --- |
| **Tenant** | An internal service or team that **calls** the Hub. Owns API keys and callback endpoints. | `orders-service` |
| **Vendor** (people also say "client") | The third-party API the Hub **calls**: base URLs, endpoints, auth scheme. | `clevertap` |
| **Account** | One instance of a vendor: secrets (none on an auth `none` vendor), non-secret config, quota. | `ct-brandA-in` |
| **Endpoint** | One versioned vendor endpoint (`name@N`): method, path and the **request schema** it accepts (query, headers, body, path), each field required or optional. | `agify.estimate@1` |
| **Operation** | What a caller invokes on an integration, with its **request schema** (the caller's body and, optionally, headers; each field required or optional) and `validate_input`. Single-operation integrations use `default`. | `default`, `send_otp` |
| **Transformer** | Immutable, versioned mapping proven by **samples**: caller input → vendor request (always), or vendor response → what the caller gets (optional). | `T1@7`, `R1@2` |
| **Integration** | The stable id a tenant calls. Its content lives in immutable **versions** (operations, targets, delivery, webhooks, audit). | `int_evt_brandA` v7 |
| **Registrar endpoint** | A tenant's callback URL + signing secret, registered once. | `reg_orders_cb` |

The one rule behind everything: **callers send an integration id, an operation and a body that matches its request schema, and never see vendor, account or version** (D12). Every change on this page is Hub-side; the calling team deploys nothing.

The model (ADR-0023, in `docs/tech/adr/`): there are two schema registries, both built in the panel from a pasted sample. The **integration** owns the caller's input, per operation. The **endpoint** owns the vendor request. A request transformer sits between them and is proven by samples. The Hub refuses a transformer whose sample output differs from the expected request or breaks the endpoint's schema, and names the field. A response transformer is optional. With one, its output goes back unchanged. Without one, the call is **pass-through**: the vendor's status and body go back unchanged. There is no shared response schema, so switching vendor can change what callers receive.

## The ways to do each task

| Way | Who | Setup |
| --- | --- | --- |
| **Console** (SvelteKit admin UI) | Anyone with an OIDC login; buttons appear only for roles that may use them | Sign in at the console URL |
| **hubctl** (`backend/cmd/hubctl`) | Platform engineers; config as code (`export`, `plan`, `apply`), goldens, dry-run, DLQ replay | `HUB_ADMIN_URL`, `HUB_ADMIN_TOKEN` (dev: `hubctl login -dev`) |
| **MCP** (`hubmcp` with `HUB_ADMIN_TOKEN`) | Your AI assistant, acting **as you** with your own token, on stdio only | [references/access.md](references/access.md) |
| **Admin API** (`curl $HUB_ADMIN_URL/admin/v1/...`) | Anything the other three do not cover yet | Same token |

Not every way exists for every task yet. The full matrix, with the gaps, is in [references/access.md](references/access.md#what-each-way-can-do). Read it before promising a user a way to do something.

## Safety rules (always)

1. **MCP never deletes.** There is no delete tool at all. Deletes, purge, DLQ replay, unseal, pause/resume, disabling a tenant or callback endpoint, API-key revoke/rotate, signing-secret rotation, credential operations, breaker force and account drain/retire stay **manual in the console** (or hubctl/admin API) for people whose role allows them. If asked to do one through MCP, say so and give the console path.
2. **Secrets never pass through an AI.** Vendor credentials and signing secrets are entered by a human in the console or with hubctl / the admin API from a file only they can read. `hubmcp` refuses arguments that look like a Hub key or signing secret (`SECRET_IN_INPUT`). A minted API key or signing secret is shown **once**: tell the user to put it in their secret manager, never paste it back into chat, code or config.
3. **Show before you change live traffic.** For publish, weights, rollback and any update (tenant, vendor, account, callback URL): show the validation result and diff, or current vs new values, ask, and only then set `confirm: true`. For API keys and callback endpoints, agree owner, scopes or URL first. Every write needs a `reason`; it lands in the change log with your identity.
4. **Never edit a published version or transformer.** Versions are immutable: publish a new version (rollback is also a new version).
5. **Samples and dry-run before publish.** A transformer whose samples fail (output differs from the expected one, or the vendor request breaks the endpoint's request schema) cannot be created, and a version that fails validation cannot be published. Do not try to work around either: fix the transformer, the sample or the schema the message names.
6. **No real personal data in tests.** Use obviously fake PAN, Aadhaar and phone numbers; tests run against `mockvendor`, never a real vendor.
7. **Warn before dropping a response transformer.** Without one (pass-through), callers get the vendor's body as the vendor wrote it, and it may name the vendor. Say so and let the integration owner decide.

## Task router

| The user wants to... | Read |
| --- | --- |
| Set up console, hubctl, curl or hubmcp access; check roles | [references/access.md](references/access.md) |
| Create a tenant, mint/rotate/revoke API keys, choose scopes | [references/tenants-and-keys.md](references/tenants-and-keys.md) |
| Onboard a new third-party API end to end (worked example: the agify age estimate) | [references/onboard-an-api.md](references/onboard-an-api.md) |
| Onboard a vendor, add endpoints with their request schema, create an account (with credentials, or none for a public API), change vendor/account config, rotate credentials | [references/vendors-and-accounts.md](references/vendors-and-accounts.md) |
| Register a callback endpoint, subscribe events, verify signatures | [references/callbacks.md](references/callbacks.md) |
| Create an integration with its operations and input schema, publish its first version, switch vendor or account, canary with weights, shadow, roll back, decide re-resolve vs pinned | [references/cutover.md](references/cutover.md) |
| Map the caller's input to the vendor's request and back, samples, error mapping, pass-through, dry-run | [references/transformers.md](references/transformers.md) |
| Multi-step flows (OTP send then verify, Aadhaar e-KYC), several operations on one integration, `X-Hub-Session` | [references/multi-step-flows.md](references/multi-step-flows.md) |

## Onboarding a new API end to end (the usual order)

Everything below can be done from the console, without code or a PR. The full walk-through, with the console clicks, hubctl and hubmcp calls for each step, is [references/onboard-an-api.md](references/onboard-an-api.md).

1. **Tenant + key** for the calling team ([tenants-and-keys.md](references/tenants-and-keys.md)).
2. **Vendor** (auth scheme, regions) if the vendor is new ([vendors-and-accounts.md](references/vendors-and-accounts.md)).
3. **Endpoint** with its **request schema**: paste a sample request, generate the fields, mark each required or optional.
4. **Account**: with credentials (entered by a human), or none for a public API on an auth `none` vendor.
5. **Request transformer** with samples: sample input + the vendor request it must produce ([transformers.md](references/transformers.md)).
6. **Response transformer** with samples, only if callers should get something other than the vendor's own answer. Without one the call is pass-through (sync only).
7. **Callback endpoint** if the integration is `async_callback` ([callbacks.md](references/callbacks.md)).
8. **Integration**: create the id, then draft v1 with its **operations** (input schema from a sample, required/optional, `validate_input`) and a target that names the transformers. Validate, publish ([cutover.md](references/cutover.md)).
9. Give the calling team the integration id and point them at the consumer skill `clients/claude-skill/integration-hub/` (it works in Claude Code, Codex, Cursor and other agents). Discovery shows them each operation's request schema.

## Where the truth lives

- Behaviour: `docs/DESIGN.md` (§3 entities, §5 delivery modes, §6 cutover, §7 transformers, §12 webhooks, §14 sessions), as changed by ADR-0023 (schemas on the integration and the endpoint, samples, pass-through).
- Admin API: `contracts/openapi/hub-admin.v1.yaml`; caller API: `contracts/openapi/hub-public.v1.yaml`; MCP tools: `contracts/mcp/hubmcp-tools.json` and its `README.md`.
- hubctl: `hubctl` with no arguments prints every command and flag.
- Runnable examples: `examples/agify-age/` (a public API onboarded with auth `none`, the walk-through in [references/onboard-an-api.md](references/onboard-an-api.md)), `examples/otp-account-flow/` (OTP then bank account), `examples/aadhaar-okyc/` (Aadhaar OTP e-KYC); use cases: `docs/use-cases/README.md`.
- Runbooks (alerts, credential rotation): `docs/runbooks/`.
- Transformer authoring detail for platform engineers: `backend/.claude/skills/hub-transformers/`.
- Feature doc for the schema registry: `docs/features/F-23-schema-registry.md`.

If something here disagrees with the contract or the code, the contract and code win: tell the user and fix this skill.
