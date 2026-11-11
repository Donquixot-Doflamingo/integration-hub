# hubmcp tool contract

`hubmcp-tools.json` is the frozen contract for the `hubmcp` MCP server (`backend/cmd/hubmcp`). Through it, an AI assistant uses the Integration Hub like any other tenant. With the signed-in user's own admin token, it can also read operational state and create or change what a team needs on the control plane. It can never delete anything. The backend implements exactly these tools, with these names, descriptions and input/output schemas. Change them only through a CCR (`contracts/AGENTS.md`).

## Configuration

| Env | Required | Meaning |
| --- | --- | --- |
| `HUB_BASE_URL` | yes | Gateway base URL (e.g. `http://localhost:8080`) |
| `HUB_API_KEY` | one of | Tenant API key. It needs scope `invoke`, and also `dry_run` for `hub_dry_run`. Required unless the ISFC credential below is set (both set: startup error). Secret: never logged, never in tool output |
| `HUB_ISFC_CLIENT_ID`, `HUB_ISFC_API_KEY` | one of | ISFC backend-client credential, sent as `x-isfc-client-id` and `x-isfc-api-key` on tenant tools (hub-public `isfcS2S`). Set both or neither. This is the credential for the streamable-http transport, where a shared server acts for a backend client. The key is a secret: never logged, never in tool output |
| `HUB_TENANT_ID` | no | ISFC tenant UUID, sent as `X-Tenant-Id`. Needed on tenant tools when the ISFC credential covers several tenants; on `hub_admin_*` calls it picks the ISFC tenant the user acts in. Ignored with `HUB_API_KEY` |
| `HUB_ADMIN_TOKEN` | no | **The signed-in user's own** access token (OIDC token, or in ISFC mode the user's ISFC JWT), never a shared or service token. Get it from your identity provider (locally: `hubctl login -dev`). If unset, no `hub_admin_*` tool is registered. Secret: never logged, never in tool output |
| `HUB_ADMIN_BASE_URL` | no | Control-plane URL. Defaults to `HUB_BASE_URL` + `/admin/v1`. In ISFC mode set it to the ISFC api-gateway route to the Hub admin API: the admin listener only accepts gateway traffic |

## Tools

Tenant tools (always registered):

| Tool | Maps to (operationId) | Method | Side effects |
| --- | --- | --- | --- |
| `hub_list_integrations` | `hub-public#listIntegrations` | GET | none |
| `hub_describe_integration` | `hub-public#getIntegration` (operations with their `request_schema`) | GET | none |
| `hub_dry_run` | `hub-public#dryRunIntegration` | POST | none (reveals upstream request shape, redacted) |
| `hub_invoke` | `hub-public#invokeIntegration` / `#invokeIntegrationOperation` (pre-check via `#getIntegration`) | POST | **yes** |
| `hub_get_request` | `hub-public#getRequestStatus` | GET | none |

Admin read tools (registered when `HUB_ADMIN_TOKEN` is set; `readOnlyHint: true`):

| Tool | Maps to (`hub-admin#`) | Method | Min. role |
| --- | --- | --- | --- |
| `hub_admin_search_requests` | `searchRequests` | GET | tenant_viewer |
| `hub_admin_request_timeline` | `getRequestTimeline` + `listRequestAttempts` | GET | tenant_viewer |
| `hub_admin_breaker_states` | `listBreakers` | GET | viewer |
| `hub_admin_list_tenants` | `listTenants` | GET | viewer |
| `hub_admin_get_tenant` | `getTenant` | GET | tenant_viewer |
| `hub_admin_list_vendors` | `listVendors` | GET | viewer |
| `hub_admin_get_vendor` | `getVendor` | GET | viewer |
| `hub_admin_list_vendor_endpoints` | `listVendorEndpoints` | GET | viewer |
| `hub_admin_get_vendor_endpoint` | `getVendorEndpoint` | GET | viewer |
| `hub_admin_list_accounts` | `listAccounts` | GET | viewer |
| `hub_admin_get_account` | `getAccount` | GET | viewer |
| `hub_admin_get_transformer` | `getTransformer` | GET | viewer |
| `hub_admin_run_transformer_goldens` | `runTransformerGoldens` | POST (non-mutating check) | editor |
| `hub_admin_dry_run_transformer` | `dryRunTransformer` | POST (non-mutating check) | editor |
| `hub_admin_list_integrations` | `listIntegrations` | GET | tenant_viewer |
| `hub_admin_get_integration` | `getIntegration` | GET | tenant_viewer |
| `hub_admin_get_integration_version` | `getIntegrationVersion` | GET | tenant_viewer |
| `hub_admin_diff_integration_versions` | `diffIntegrationVersions` | GET | tenant_viewer |
| `hub_admin_validate_integration_version` | `validateIntegrationVersion` | POST (non-mutating check) | editor |
| `hub_admin_get_registrar_endpoint` | `getRegistrarEndpoint` | GET | tenant_viewer |

Admin write tools (registered when `HUB_ADMIN_TOKEN` is set **and** the transport is stdio; `readOnlyHint: false`, `destructiveHint: false`):

| Tool | Maps to (`hub-admin#`) | Method | Min. role | `requires_confirm` |
| --- | --- | --- | --- | --- |
| `hub_admin_create_tenant` | `createTenant` | POST | admin | |
| `hub_admin_update_tenant` | `updateTenant` | PATCH | tenant_admin | `live_traffic` |
| `hub_admin_create_api_key` | `createApiKey` | POST | admin | `mints_secret` (output `/secret` sensitive) |
| `hub_admin_create_vendor` | `createVendor` | POST | admin | |
| `hub_admin_update_vendor` | `updateVendor` | PUT | admin | `live_traffic` |
| `hub_admin_create_vendor_endpoint` | `createVendorEndpoint` | POST | admin | |
| `hub_admin_update_account` | `updateAccount` | PATCH | admin | `live_traffic` |
| `hub_admin_create_transformer_version` | `createTransformerVersion` | POST | editor | |
| `hub_admin_create_integration` | `createIntegration` | POST | editor | |
| `hub_admin_create_integration_draft` | `createIntegrationDraft` | POST | editor | |
| `hub_admin_update_integration_draft` | `updateIntegrationDraft` | PUT | editor | |
| `hub_admin_publish_integration_version` | `publishIntegrationVersion` | POST | editor | `live_traffic` |
| `hub_admin_set_integration_weights` | `updateIntegrationWeights` | POST | editor | `live_traffic` |
| `hub_admin_rollback_integration` | `rollbackIntegration` | POST | editor | `live_traffic` |
| `hub_admin_create_registrar_endpoint` | `createRegistrarEndpoint` | POST | tenant_admin | `mints_secret` (output `/signing_secret` sensitive) |
| `hub_admin_update_registrar_endpoint` | `updateRegistrarEndpoint` | PATCH | tenant_admin | `live_traffic` |

Vendor endpoint versions are immutable. To change an endpoint, call `hub_admin_create_vendor_endpoint` with the same name: it creates `<name>@<n+1>`. Accounts cannot be created through MCP yet, because `createAccount` takes credential values (required for every auth scheme except `none`, hub-admin 2.0.0; see `forbidden_operations`).

Each tool in the JSON carries:

- `description` (written for an LLM), `inputSchema` and `outputSchema` (JSON Schema 2020-12);
- `maps_to` (`spec`, `operationId`, `http_method`);
- MCP `annotations` (`readOnlyHint`, `destructiveHint`, `idempotentHint`, `openWorldHint`);
- `safety` notes and examples.

Admin tools also carry `requires` (`env`, the spec's `x-required-role` and, for write tools, `transport: ["stdio"]`). Some carry `requires_confirm` and `sensitive_output`.

## Safety rules (the backend must enforce them, not just document them)

1. **No vendor leakage from tenant tools.** Tenant tool outputs carry only caller-API fields. `hubmcp` drops the `X-Hub-Integration-Version` and `X-Hub-Served-By` headers. The one documented exception is `hub_dry_run`, which shows the upstream request (secrets `***`) by design, and its description warns the model. A pass-through result of `hub_invoke` (`pass_through: true`, since 2.0.0: the integration has no response transformer) is the third party's body exactly as the Hub returned it (ADR-0023); `hubmcp` does not alter it.
2. **Confirmation for money or quota.** Before invoking, `hub_invoke` reads `getIntegration` (cached for at most 60 s). If `moves_money_or_quota` is true and the input lacks `confirm: true` or an `idempotency_key`, it returns `outcome: "refused"` with `error.code` `CONFIRMATION_REQUIRED` or `IDEMPOTENCY_KEY_REQUIRED` and **does not call the Hub**.
3. **Queued is not done.** A 202 maps to `outcome: "queued"`. The tool description tells the model to report it as pending and follow up with `hub_get_request`.
4. **Admin read tools never change anything.** They call only GET operations or the three non-mutating POST checks (`validateIntegrationVersion`, `runTransformerGoldens`, `dryRunTransformer`). Sealed payloads stay sealed.
5. **Errors are results.** Hub errors come back as tool results with `isError: true` and the canonical error object, so the model can read `retryable` and `retry_after_ms`. Auto-retry happens at most once, only for retryable errors, and only when an `idempotency_key` is present. The local refusal codes `CONFIRMATION_REQUIRED`, `IDEMPOTENCY_KEY_REQUIRED` and `SECRET_IN_INPUT` exist only in MCP output and never come from the Hub.
6. **Secrets.** `HUB_API_KEY` and `HUB_ADMIN_TOKEN` never appear in logs, errors or outputs.

## Admin write tools

The user's requirement: a team's Claude can create what the team needs on the panel, using the user's own authentication and authorization, and it can **never delete** anything. Deleting stays manual in the console, for people whose role allows it.

### Identity and authorization

- Write tools act **as the signed-in user**. `HUB_ADMIN_TOKEN` is that user's own OIDC token: from their identity provider, or locally `hubctl login -dev`. It is passed in the MCP client's environment to a stdio `hubmcp` the user runs themselves.
- Write tools are registered **only on the stdio transport**. On streamable HTTP they are never registered, even on loopback, because one token there would serve every client. Read admin tools keep the existing rule: `HUB_ADMIN_TOKEN` is refused on a remote-reachable listener (ADR-0018).
- **The Hub's RBAC is the authority.** Every operation's `x-required-role` (copied into `requires`) decides, so `hubmcp` does not pre-filter by role. A viewer calling a write tool gets `FORBIDDEN` (403) back as an `isError` result, and the model tells the user they lack the role. An expired token gives `UNAUTHORIZED`, and the model asks the user to sign in again.
- **Change log.** Every mutation is recorded in the Hub's change log under the token's subject, with before, after and the reason. Every mutating tool requires `reason` (3 to 1000 characters). `hubmcp` sends it to the Hub with ` (via hubmcp)` appended, so auditors can tell MCP changes from console ones. `change_id` comes back in every write result.

### Hard exclusions (machine-checked)

- `forbidden_operations` names every excluded hub-admin operation with its method and the reason:
  - every HTTP DELETE;
  - purge, replay and unseal;
  - account lifecycle (drain or retire);
  - `createAccount`, because its body carries credentials (for every auth scheme except `none`);
  - every credential operation (add-next, promote, delete-old);
  - breaker force-open, force-close and clear-force;
  - pause and resume;
  - API-key revoke and rotate;
  - signing-secret rotation.
- No tool maps to any of them.
- `not_exposed_operations` lists the rest of hub-admin that has no tool yet, with the reason. Every one of the 74 operations appears exactly once: mapped by a tool, forbidden, or not exposed. A new admin operation therefore fails the check until someone classifies it.
- **No secret input.** No admin tool input has a property named in `forbidden_input_properties` (`credentials`, `secret`, `signing_secret`, `password`, `token`, ...). Credentials and signing secrets are set in the console or with `hubctl`, never through an LLM. Free-form objects (account `config`, transformer `input`) are documented as non-secret.
- **No kill switches.** No mutating tool input has a property named in `forbidden_mutation_input_properties` (`disabled`, `paused`, `state`). Disabling a tenant or a callback endpoint, pausing an integration and changing account lifecycle stay manual console actions.
- **Secret patterns.** `hubmcp` refuses any tool call whose arguments contain a string matching `secret_patterns` (Hub API keys, signing secrets). The refusal is an `isError` result with `error.code` `SECRET_IN_INPUT` (for `hub_invoke`, `outcome: "refused"`), and the Hub is not called. A minted key or secret therefore cannot be smuggled into a later call.

`make contracts-check` (`contracts/tools/check.mjs` §7b) enforces all of the above on the contract:

- every `http_method` equals the spec's method;
- no tool maps to a DELETE or a forbidden operation, and every DELETE is forbidden;
- every operation is classified exactly once;
- input property names, recursively through the schemas;
- annotations, `requires.x-required-role`, stdio-only write tools, a required `reason`, `confirm` for `requires_confirm`, and `sensitive_output`.

The backend proves the same rules at runtime (HUB-315).

### Confirmation and live traffic

- `requires_confirm: "live_traffic"` marks the following. The first three come from the requirement; the updates are added because the Hub applies them to live traffic right away or with the next snapshot.
  - publish, set weights and rollback;
  - updates to tenants (rate limit), vendors (base URLs, auth, breaker defaults), accounts (config, quota) and callback URLs.
- `requires_confirm: "mints_secret"` marks API-key and callback-endpoint creation.
- Without `confirm: true`, these tools refuse locally with `CONFIRMATION_REQUIRED` and call nothing. Each description tells the model what to show first:
  - publish: the validation result and the diff from the active version;
  - rollback: the diff to `to_version`;
  - weights: current next to new weights;
  - updates: current and new values;
  - secrets: owner, scopes or URL, and expiry.
- Only after the user explicitly approves does the model set `confirm: true`.
- `confirm` is a speed bump, not proof of consent: a model can set it without asking. The real controls are the user's own RBAC, the MCP client's approval prompt for tools that are not read-only, and the change log. Publish, weights and rollback also carry `expected_active_version`, and updates take `if_match` (the `etag` returned by the matching `hub_admin_get_*` tool). A concurrent change therefore fails with `CONFLICT` / `PRECONDITION_FAILED` and nothing is overwritten.

### Annotations

- Read tools: `readOnlyHint: true`, `idempotentHint: true`.
- Write tools: `readOnlyHint: false`. `idempotentHint` is true only for merge-patch or replace updates (`update_*`, `update_integration_draft`), and false for creates (a repeat gets `CONFLICT` or creates the next version) and for publish, weights and rollback (each creates a version).
- `openWorldHint: false` for every admin tool: they talk only to the Hub, and the transformer dry-run makes no upstream call.
- **No admin tool has `destructiveHint: true`**, because destructive operations are excluded. Here "destructive" means a delete or irreversible loss. Every admin mutation exposed here is recorded in the change log and can be undone by a further change (a rollback for publish and weights, an update for updates).
  - The MCP spec's own wording ("only additive updates" when false) is stricter than this. A client that relies on `destructiveHint` alone to decide whether to prompt would not prompt for live-traffic tools; `requires_confirm` covers that gap on the server side.
- `hub_invoke` (tenant) keeps `destructiveHint: true`: it has real-world effects at vendors.

### Sensitive output

`sensitive_output` lists JSON pointers to write-once secrets in a tool's result: `/secret` for `hub_admin_create_api_key` and `/signing_secret` for `hub_admin_create_registrar_endpoint`.

- `hubmcp` never logs these fields, and returns them only in that one result.
- The description tells the model to:
  - give the secret to the user once, verbatim, to store in their secret manager;
  - never repeat it, never put it in another tool call, file, code sample, commit or memory;
  - afterwards refer to the key by `key_id` or `prefix`.
- A lost secret is rotated in the console. Rotation is not exposed through MCP.

## Versioning
`version` in the JSON follows semver like the OpenAPI documents. Renaming a tool or removing an input field is breaking. Adding a tool, an optional input or an output field is minor. Moving an operation out of `forbidden_operations` (for example `createAccount` once credentials become optional) is a minor bump, and a CCR must justify it.
