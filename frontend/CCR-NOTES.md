# Contract change requests from the admin console

Requests from `frontend-engineer` to `contract-steward` (format: `docs/process/CONTRACT_CHANGES.md` §3).
They become `contract` tickets in `docs/tickets/TICKETS.md` once the lead files them. Until the contract
changes, the console works around each gap with a `TODO(CCR-xxx)` in code; nothing here is coded as if it
already were in the contract.

### CCR-001: declare `X-Hub-Request-Id` on admin responses
- **Requested by**: frontend-engineer (ticket HUB-251)
- **Why**: F-19 / admin-console-ux §6 require every error panel and toast to show a request id that operators
  paste into the request explorer. `AdminError.request_id` covers error bodies, but successes and non-envelope
  failures (proxy 502, HTML) have no id. hub-public already declares `X-Hub-Request-Id` on every response;
  hub-admin does not, so the console cannot rely on it and backend contract tests will not require it.
- **Document**: `hub-admin.v1.yaml`
- **Operation / schema**: all 79 operations (every response), new `components.headers.X-Hub-Request-Id`
- **Before**:
  ```yaml
  # components.headers has only ETag; no admin response declares X-Hub-Request-Id
  ```
- **After**:
  ```yaml
  components:
    headers:
      X-Hub-Request-Id:
        description: Hub request id for this admin call (`req_` + ULID). Present on every response, success or error. Equals `error.request_id` on errors.
        required: true
        schema:
          $ref: '#/components/schemas/RequestIdValue'
        example: req_01J9Z3K8X2V7Q4M6N8P0R2S4T6
  # and on every response object (including components.responses.*):
  #   headers:
  #     X-Hub-Request-Id:
  #       $ref: '#/components/headers/X-Hub-Request-Id'
  ```
- **Breaking?**: no (additive response header) -> minor bump 1.0.0 -> 1.1.0
- **Consumers affected**: backend admin handlers (middleware sets it), frontend `src/lib/api/client.ts`, hubmcp (optional)
- **Error codes**: none new
- **Rollout**: contract PR first
- **Current workaround**: `client.ts` reads the header as a best-effort fallback (`TODO(CCR-001)`); error
  bodies' `request_id` always wins.

### CCR-002: active-version filter on `listIntegrations`
- **Requested by**: frontend-engineer (ticket HUB-256)
- **Why**: HUB-256 (F-19, US-1902) asks for an integrations list filterable by tenant, capability, paused and
  **active version**. Operators look for integrations that have no active version yet (created, never published:
  invocations answer 503 `INTEGRATION_PAUSED` / `no_active_version`) and for those whose active version lags the
  latest (a draft or unpublished version waiting). `listIntegrations` has `tenant_id`, `capability`, `vendor_id`,
  `account_id`, `paused` and `q`, but nothing on versions. Filtering one page client-side would be wrong with cursor
  pagination (matches on later pages would be missed), so the console does not offer the filter until the API does.
- **Document**: `hub-admin.v1.yaml`
- **Operation / schema**: `listIntegrations`, `GET /integrations`
- **Before**:
  ```yaml
  # parameters: TenantFilter, capability, vendor_id, account_id, paused, Q, Cursor, Limit
  # (no version filter)
  ```
- **After**:
  ```yaml
        - name: has_active_version
          in: query
          description: true = only integrations with an active (published) version; false = only those without one.
          schema:
            type: boolean
          example: false
        - name: active_version_behind
          in: query
          description: true = only integrations whose latest version is newer than the active one (an unpublished draft or version is waiting).
          schema:
            type: boolean
          example: true
  ```
- **Breaking?**: no (additive optional query parameters) -> minor bump
- **Consumers affected**: backend list handler (registry query), frontend `routes/(app)/integrations` filters, hubmcp
  (optional, list tool)
- **Error codes**: none new (`VALIDATION_FAILED` for a non-boolean value, as for `paused`)
- **Rollout**: contract PR first
- **Current workaround**: none in the UI; `TODO(CCR-002)` in `src/routes/(app)/integrations/filters.ts`. The list
  shows the active and latest version per row and marks "No active version" and "Draft" so operators can scan for them.
