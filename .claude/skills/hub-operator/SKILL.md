---
name: hub-operator
description: "Operate the Integration Hub control plane: create a tenant and its API keys (scopes), onboard a vendor (the third-party API, sometimes called the client), its endpoints and an account with credentials, register callback endpoints and verify X-Hub-Signature, change vendor or account config safely, switch an integration to another vendor or account (draft a new version, validate, publish, weights or canary, shadow, rollback, re-resolve vs pinned), write transformers that map the caller's canonical body to the vendor request and the vendor response back (error mapping, golden fixtures, dry-run), and set up multi-step flows such as Aadhaar OTP (send_otp then verify_otp with X-Hub-Session). Every task shows the console clicks, the hubctl command and the hubmcp tool calls. Use whenever someone asks how to onboard, configure, cut over, map, test or troubleshoot anything behind an integration id, mentions tenant, API key, vendor, account, credentials, callback, registrar, transformer, golden, publish, rollback, weights, canary, OTP or session, or names hubctl, hubmcp hub_admin_* tools or the admin console."
---

# Operating the Integration Hub

Words used here (DESIGN §3.1):

| Word | Means | Example |
| --- | --- | --- |
| **Tenant** | An internal service or team that **calls** the Hub. Owns API keys and callback endpoints. | `orders-service` |
| **Vendor** (people also say "client") | The third-party API the Hub **calls**: base URLs, endpoints, auth scheme. | `clevertap` |
| **Account** | One credentialed instance of a vendor: secrets, non-secret config, quota. | `ct-brandA-in` |
| **Capability** | The vendor-neutral contract: canonical request/response JSON Schemas per operation. | `events.track@v1` |
| **Transformer** | Immutable, versioned mapping: canonical → vendor request, or vendor response → canonical. | `T1@7`, `R1@2` |
| **Integration** | The stable id a tenant calls. Its content lives in immutable **versions** (targets, delivery, webhooks, audit). | `int_evt_brandA` v7 |
| **Registrar endpoint** | A tenant's callback URL + signing secret, registered once. | `reg_orders_cb` |

The one rule behind everything: **callers send an integration id + a canonical body and never see vendor, account or version** (D12). Every change on this page is Hub-side; the calling team deploys nothing.

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
5. **Dry-run and goldens before publish.** A transformer whose goldens fail cannot be created, and a version that fails validation cannot be published; do not try to work around either.
6. **No real personal data in tests.** Use obviously fake PAN, Aadhaar and phone numbers; tests run against `mockvendor`, never a real vendor.

## Task router

| The user wants to... | Read |
| --- | --- |
| Set up console, hubctl, curl or hubmcp access; check roles | [references/access.md](references/access.md) |
| Create a tenant, mint/rotate/revoke API keys, choose scopes | [references/tenants-and-keys.md](references/tenants-and-keys.md) |
| Onboard a vendor, add endpoints, create an account with credentials, change vendor/account config, rotate credentials | [references/vendors-and-accounts.md](references/vendors-and-accounts.md) |
| Register a callback endpoint, subscribe events, verify signatures | [references/callbacks.md](references/callbacks.md) |
| Create an integration, publish its first version, switch vendor or account, canary with weights, shadow, roll back, decide re-resolve vs pinned | [references/cutover.md](references/cutover.md) |
| Map the caller's input to the vendor's input and back, error mapping, goldens, dry-run | [references/transformers.md](references/transformers.md) |
| Multi-step flows (OTP send then verify, Aadhaar e-KYC), operations, `X-Hub-Session` | [references/multi-step-flows.md](references/multi-step-flows.md) |

## Onboarding a new team end to end (the usual order)

1. **Tenant + key** for the calling team ([tenants-and-keys.md](references/tenants-and-keys.md)).
2. **Vendor + endpoints** if the vendor is new; **account** with credentials ([vendors-and-accounts.md](references/vendors-and-accounts.md)).
3. **Capability**: it must already exist in `contracts/schemas/capabilities/`. A new one is a contract change by the contract-steward (`.claude/skills/contract-change/`), never created ad hoc.
4. **Transformers** (request + response, per operation for multi-step capabilities) with goldens ([transformers.md](references/transformers.md)).
5. **Callback endpoint** if the integration is `async_callback` ([callbacks.md](references/callbacks.md)).
6. **Integration**: create the id, draft v1, validate, publish ([cutover.md](references/cutover.md)).
7. Give the calling team the integration id and point them at the consumer skill `clients/claude-skill/integration-hub/` (it works in Claude Code, Codex, Cursor and other agents).

## Where the truth lives

- Behaviour: `docs/DESIGN.md` (§3 entities, §5 delivery modes, §6 cutover, §7 transformers, §12 webhooks, §14 sessions).
- Admin API: `contracts/openapi/hub-admin.v1.yaml`; caller API: `contracts/openapi/hub-public.v1.yaml`; MCP tools: `contracts/mcp/hubmcp-tools.json` and its `README.md`.
- hubctl: `hubctl` with no arguments prints every command and flag.
- Runnable examples: `examples/otp-account-flow/` (OTP then bank account), `examples/aadhaar-okyc/` (Aadhaar OTP e-KYC); use cases: `docs/use-cases/README.md`.
- Runbooks (alerts, credential rotation): `docs/runbooks/`.
- Transformer authoring detail for platform engineers: `backend/.claude/skills/hub-transformers/`.

If something here disagrees with the contract or the code, the contract and code win: tell the user and fix this skill.
