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
- **Status**: shipped in hub-admin 1.1.0 (HUB-278). The console reads the documented header on every response
  (`client.ts`, no `TODO(CCR-001)` left); error bodies' `request_id` still wins; success notices show the id.

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
- **Status**: shipped in hub-admin 1.1.0 (HUB-283). The integrations list offers both filters (URL params and chips,
  `src/routes/(app)/integrations/filters.ts`); no `TODO(CCR-002)` left.

### CCR-003: dead-letter count per account on `listAccounts`
- **Requested by**: frontend-engineer (ticket HUB-261)
- **Why**: HUB-261 (F-19, US-1903) asks the vendor and account health board to show, per account, the breaker
  state, error rate, parked count/age, **DLQ count** and lifecycle state. Every other figure is in `listBreakers`
  or `listAccounts`, but nothing returns a dead-letter count: `listDeadLetters` is cursor-paged without totals, and
  `replayDeadLetters` / `purgeDeadLetters` with `dry_run` return `matched` only for editors/admins and are POSTs
  that need a reason. Counting DLQ pages in the browser would be wrong (14 days of entries, many pages) and slow
  on a board that refreshes every 5 s. The board therefore shows no DLQ column yet.
- **Document**: `hub-admin.v1.yaml`
- **Operation / schema**: `listAccounts` (`AccountSummary`), optionally `getAccount` (`Account`)
- **Before**:
  ```yaml
  AccountSummary:
    required: [account_id, vendor_id, region, state, active_cred_version, integrations_using, updated_at]
    # no dead-letter figure
  ```
- **After**:
  ```yaml
  AccountSummary:
    properties:
      dead_letter_count:
        description: Dead-lettered requests whose last attempt used this account, not yet replayed and still within retention (14 days). May lag by up to a minute.
        type: integer
        minimum: 0
        example: 12
      dead_letter_oldest_at:
        description: When the oldest of those entries was dead-lettered; null when there are none.
        type: [string, 'null']
        format: date-time
        example: '2026-10-31T11:02:40Z'
  # and the same two properties on Account (getAccount); examples updated (ct-brandA-in: 1, matching the DLQ example)
  ```
- **Breaking?**: no (additive optional properties) -> minor bump
- **Consumers affected**: backend accounts list/detail (count from the DLQ store), frontend `routes/(app)/health`
  (column "Dead letters" linking to `/dlq?account_id=`), hubmcp (optional)
- **Error codes**: none new
- **Rollout**: contract PR first
- **Status**: shipped in hub-admin 1.1.0 (HUB-284). The health board has the "Dead letters" column (count, oldest age,
  link to `/dlq?account_id=`, `src/routes/(app)/health/board.ts`); no `TODO(CCR-003)` left.

### CCR-004: example of the registrar SSRF refusal
- **Requested by**: frontend-engineer (ticket HUB-272)
- **Why**: HUB-272 (F-19, US-1906) requires "Private-range URL error from API shown on the field". The contract says
  `createRegistrarEndpoint` / `updateRegistrarEndpoint` reject private or link-local hosts with 400
  `VALIDATION_FAILED`, but only the generic `BadRequest` example exists (pointer `/reason`). The console maps any
  `VALIDATION_FAILED` pointer `/url` to the URL field, and its mock (`registrar-url-private` in
  `src/lib/api/mocks/scenarios.ts`) uses that envelope with pointer `/url`, `keyword: ssrf` and a made-up message.
  Backend and console should agree on the pointer (and ideally the keyword) so the error is guaranteed to land on
  the field. Nothing is blocked: the envelope shape is already in the contract.
- **Document**: `hub-admin.v1.yaml`
- **Operation / schema**: `createRegistrarEndpoint` and `updateRegistrarEndpoint`, 400 response (example only)
- **Before**:
  ```yaml
  '400':
    $ref: '#/components/responses/BadRequest'   # example: pointer /reason, keyword required
  ```
- **After** (proposal):
  ```yaml
  '400':
    description: Invalid body, or the URL host is not allowlisted / resolves to a private or link-local range.
    headers:
      X-Hub-Request-Id:
        $ref: '#/components/headers/X-Hub-Request-Id'
    content:
      application/json:
        schema:
          $ref: '#/components/schemas/AdminErrorEnvelope'
        examples:
          ssrf:
            value:
              error:
                code: VALIDATION_FAILED
                message: Callback URL refused
                retryable: false
                retry_after_ms: null
                details:
                  - { pointer: /url, keyword: ssrf, message: 'host orders.internal resolves to 10.0.0.12 (private range)' }
                meta: null
                request_id: req_01J9Z3M1A5B7C9D1E3F5G7H9KC
  ```
- **Breaking?**: no (example / description only) -> patch bump
- **Consumers affected**: backend registrar handlers (use pointer `/url`), frontend mock scenario (switch to the example)
- **Error codes**: none new
- **Rollout**: contract PR first; the console needs no code change, only the mock preset swaps to the example
- **Status**: requested (2026-11-01)

### CCR-005: credential keys a vendor's accounts need
- **Requested by**: frontend-engineer (ticket HUB-291)
- **Why**: HUB-291 (F-19, US-1908) lets admins create accounts and rotate credentials in the console. The
  contract's `Credentials` is "scheme-specific keys" (`additionalProperties: string`), and nothing says which
  keys a vendor's transformers read (`creds.account_id`, `creds.passcode` for CleverTap). The console therefore
  asks the operator to type each key name; a typo is only found when a call fails with UPSTREAM_AUTH_FAILED
  (the connection test catches it earlier, not at create time). Nothing is blocked: free-form keys work today.
- **Document**: `hub-admin.v1.yaml`
- **Operation / schema**: `VendorFields` (so `createVendor`, `updateVendor`, `getVendor`)
- **Before**:
  ```yaml
  VendorFields:
    properties:
      auth_scheme: { $ref: '#/components/schemas/AuthScheme' }
      # no list of credential keys
  ```
- **After** (proposal):
  ```yaml
  VendorFields:
    properties:
      credential_keys:
        description: Keys every account of this vendor must provide in `credentials` (what transformers read as `creds.<key>`). createAccount and addNextCredential answer 400 VALIDATION_FAILED (pointer `/credentials/<key>`) when one is missing.
        type: array
        items: { type: string, pattern: '^[A-Za-z_][A-Za-z0-9_]{0,63}$' }
        example: [account_id, passcode]
  ```
- **Breaking?**: no (additive optional property; the 400 applies only when the vendor declares keys) -> minor bump
- **Consumers affected**: backend vendor store and account validation, frontend accounts create / rotate (prefill
  the key rows, mark missing ones), vendor form, hubmcp (none: credentials are not exposed there)
- **Error codes**: none new
- **Rollout**: contract PR first
- **Status**: requested (2026-11-07)
