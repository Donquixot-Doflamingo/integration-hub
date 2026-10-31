# hubmcp tool contract

`hubmcp-tools.json` is the frozen contract for the `hubmcp` MCP server (`backend/cmd/hubmcp`). Through it, an AI assistant uses the Integration Hub like any other tenant, with optional read-only operator tools. The backend implements exactly these tools, with these names, descriptions and input/output schemas. Change them only through a CCR (`contracts/AGENTS.md`).

## Configuration

| Env | Required | Meaning |
| --- | --- | --- |
| `HUB_BASE_URL` | yes | Gateway base URL (e.g. `http://localhost:8080`) |
| `HUB_API_KEY` | yes | Tenant API key. It needs scope `invoke`, and also `dry_run` for `hub_dry_run`. Secret: never logged, never in tool output |
| `HUB_ADMIN_TOKEN` | no | OIDC token (role `viewer` or `tenant_viewer`). If unset, the `hub_admin_*` tools are **not registered** |
| `HUB_ADMIN_BASE_URL` | no | Control-plane URL. Defaults to `HUB_BASE_URL` + `/admin/v1` |

## Tools

| Tool | Audience | Maps to (operationId) | Side effects |
| --- | --- | --- | --- |
| `hub_list_integrations` | tenant | `hub-public#listIntegrations` | none |
| `hub_describe_integration` | tenant | `hub-public#getIntegration` + `#getCapability` | none |
| `hub_dry_run` | tenant (developers) | `hub-public#dryRunIntegration` | none (reveals upstream request shape, redacted) |
| `hub_invoke` | tenant | `hub-public#invokeIntegration` / `#invokeIntegrationOperation` (pre-check via `#getIntegration`) | **yes** |
| `hub_get_request` | tenant | `hub-public#getRequestStatus` | none |
| `hub_admin_search_requests` | admin (read) | `hub-admin#searchRequests` | none |
| `hub_admin_request_timeline` | admin (read) | `hub-admin#getRequestTimeline` + `#listRequestAttempts` | none |
| `hub_admin_breaker_states` | admin (read) | `hub-admin#listBreakers` | none |

Each tool in the JSON carries `description` (written for an LLM), `inputSchema` and `outputSchema` (JSON Schema 2020-12), `maps_to`, MCP `annotations` (`readOnlyHint`, `destructiveHint`, …), `safety` notes and examples.

## Safety rules (the backend must enforce them, not just document them)

1. **No vendor leakage from tenant tools.** Tenant tool outputs carry only caller-API fields. `hubmcp` drops the `X-Hub-Integration-Version` and `X-Hub-Served-By` headers. The one documented exception is `hub_dry_run`, which shows the upstream request (secrets `***`) by design, and its description warns the model.
2. **Confirmation for money or quota.** Before invoking, `hub_invoke` reads `getIntegration` (cached for at most 60 s). If `moves_money_or_quota` is true and the input lacks `confirm: true` or an `idempotency_key`, it returns `outcome: "refused"` with `error.code` `CONFIRMATION_REQUIRED` or `IDEMPOTENCY_KEY_REQUIRED` and **does not call the Hub**. These two refusal codes exist only in MCP output and never come from the Hub.
3. **Queued is not done.** A 202 maps to `outcome: "queued"`. The tool description tells the model to report it as pending and follow up with `hub_get_request`.
4. **Admin tools are read-only.** They never call unseal, replay, purge, pause or force operations, and they return summaries and masked payloads only. Sealed payloads stay sealed.
5. **Errors are results.** Hub errors come back as tool results with `isError: true` and the canonical error object, so the model can read `retryable` and `retry_after_ms`. Auto-retry happens at most once, only for retryable errors, and only when an `idempotency_key` is present.
6. **Secrets.** `HUB_API_KEY` and `HUB_ADMIN_TOKEN` never appear in logs, errors or outputs.

## Versioning
`version` in the JSON follows semver like the OpenAPI documents. Renaming a tool or removing an input field is breaking. Adding an optional input or an output field is minor.
