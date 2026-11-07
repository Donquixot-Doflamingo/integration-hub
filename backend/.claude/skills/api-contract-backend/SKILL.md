---
name: api-contract-backend
description: "Implementing Integration Hub HTTP handlers against the frozen contracts in contracts/ - never add or change an endpoint, field, error code or event without a change request to the contract-steward (docs/process/CONTRACT_CHANGES.md), mapping handlers to operationIds, the canonical error envelope helper, kin-openapi response validation in tests, keeping vendor identity out of the public API, and versioning rules. Use whenever you add or modify a handler or route in internal/edge or internal/control, change a response shape, header or status code, add an error code, touch webhooks/callback payloads, or find the contract does not match what you need."
---

# Implementing against the contract

`contracts/` (OpenAPI 3.1, JSON Schemas for webhook events, the MCP tool contract) is the **only** interface between backend, frontend, MCP server and SDKs (AGENTS.md §4). The backend implements it; it never defines it.

## The rule
- Only `contract-steward` edits `contracts/`. You never do, not even a typo.
- No endpoint, field, header, status code, error code or event shape is coded before it exists in `contracts/`.
- Need something different? **Stop**, write a change request (template and process in `docs/process/CONTRACT_CHANGES.md`) addressed to `contract-steward`: what operation, the proposed shape, why, caller vs admin API, additive or breaking, affected clients. Wait for the contract commit (`contract(<area>): … (HUB-###)`) before coding against it. Meanwhile work on parts that don't depend on it.
- A contract change lands in its own commit before dependent code; it bumps `info.version` and `contracts/CHANGELOG.md` (steward's job).

## Versioning rules
| Change | Caller API (`/v1`) | Admin API (`/admin/v1`) |
| --- | --- | --- |
| Add optional field / new endpoint / new enum value consumers tolerate | minor bump, OK | minor bump, OK |
| Remove/rename field, change type, make optional → required, change status code meaning | **Not allowed** (D11: new integration ID instead) | major bump, needs the user's approval |
| Breaking change to an operation's `request_schema` (registry data, ADR-0023) | not a contract change; guidance: new integration ID callers adopt, old kept ≥ 6 months (D11) | — |
Backend must stay tolerant: ignore unknown request fields only where the schema allows (`additionalProperties`); never emit fields the schema doesn't declare.

## Handlers ↔ operationIds
- One handler per `operationId`, named after it: `operationId: invokeIntegration` → `func (h *Handlers) InvokeIntegration(w http.ResponseWriter, r *http.Request)`.
- Routes registered in one table per API (`internal/edge/routes.go`, `internal/control/routes.go`) whose paths are copied exactly from the spec (chi syntax `{id}` matches OpenAPI).
- Request/response Go types: either generated from the spec (preferred if a generator is adopted via ADR) or hand-written with a test that round-trips them through the schema validator. Field names, `omitempty` and nullability must match the schema exactly.
- A **route coverage test** walks the chi router (`chi.Walk`) and the spec's paths/operations and fails on any mismatch in either direction.

## Canonical error envelope (DESIGN §4.3/§4.4)
Every non-2xx response from `/v1` uses exactly one helper; no handler writes error JSON itself.
```go
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code         core.Code       `json:"code"`
	Message      string          `json:"message"`
	Retryable    bool            `json:"retryable"`
	RetryAfterMS *int64          `json:"retry_after_ms,omitempty"`
	Circuit      string          `json:"circuit,omitempty"`
	Upstream     *upstreamDetail `json:"upstream,omitempty"`
	Details      []fieldError    `json:"details,omitempty"`
	RequestID    string          `json:"request_id"`
}

func writeError(w http.ResponseWriter, r *http.Request, ce *core.CanonicalError) {
	d := errorDetail{
		Code: ce.Code, Message: ce.Message, Retryable: ce.Retryable(),
		Circuit: ce.Circuit, RequestID: requestIDFrom(r.Context()),
	}
	if ce.RetryAfter > 0 {
		ms := ce.RetryAfter.Milliseconds()
		d.RetryAfterMS = &ms
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(ce.RetryAfter.Seconds())), 10))
	}
	// ... upstream, details
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Hub-Request-Id", d.RequestID)
	w.WriteHeader(ce.HTTPStatus())
	_ = json.NewEncoder(w).Encode(errorBody{Error: d})
}
```
The exact field set, nullability (`upstream.status: null` in the DESIGN example) and whether `Retry-After` is sent are whatever the contract says — align the struct tags with the spec, and let the contract tests prove it. Non-canonical errors reaching the edge are bugs (logged once, see `go-logging`); §4.4 has no generic internal code, so if one is needed, that's a change request.

## Responses and headers
- 200: canonical body + `X-Hub-Request-Id`. `X-Hub-Integration-Version` is debug-only (if in contract) and SDKs must not act on it.
- 202: `{request_id, status, reason, poll}`; `pkg/hubclient` maps it to `Queued`, never success (§4.5).
- `IDEMPOTENT_REPLAY`: stored result + `X-Hub-Idempotent-Replay: true`.
- `X-Hub-Served-By: failover:<n>` — an index, never a vendor name.

## Keep vendor identity out of the public API
Callers never see vendor names, account names or versions (AGENTS.md §3.1). Check every caller-facing output:
- No `vendor`, `account`, transformer or integration-version fields in `/v1` bodies or callback payloads.
- Never copy vendor response headers to the caller response (allowlist our own headers only).
- Error `message` for `UPSTREAM_REJECTED` comes from the transformer's mapping — ensure it can't include vendor branding or account ids; `upstream.status/code` are vendor status values only, as the contract defines.
- No vendor-specific endpoints or headers on `/v1` (no "choose vendor" header, D12).
- Inbound webhook paths `/webhooks/in/{vendor}/{account}` are vendor-facing, not caller-facing; fine.
If a requested feature would leak vendor identity, stop and ask the user (AGENTS.md §3.1).

## Contract tests (required for every handler)
Test through the contract validator in `go-testing/references/integration-and-contract.md`: each handler test sends real requests to the router; request and response are validated against `contracts/openapi/*.yaml` with kin-openapi, with `IncludeResponseStatus: true` so undeclared statuses fail. Cover every declared response of every operation (200/202/each error code reachable from that operation). Callback and webhook event payloads are validated against `contracts/schemas/events/` with `santhosh-tekuri/jsonschema/v6`.

## Workflow for a new handler
1. Confirm the operation exists in the contract (path, method, operationId, schemas, errors). If not → change request, stop.
2. Write the contract test first (it fails: route missing).
3. Implement handler → domain call → map errors via the one helper.
4. Add metrics/spans (`go-observability`) and audit events if on the request path.
5. `make lint test itest` green; route coverage test green.
6. If callers see the change: notify docs-keeper to update `clients/claude-skill/` and MCP tools (AGENTS.md §8.9).
