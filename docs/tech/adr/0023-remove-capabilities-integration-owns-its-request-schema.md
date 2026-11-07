# ADR-0023: Remove capabilities; the integration owns its request schema, the endpoint owns its

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-07 |
| Deciders | Mayank (product owner), lead |
| Tickets | HUB-431 to HUB-438 (EP-14) |
| DESIGN refs | §0, §2 (D11, D12), §3, §4, §7, §8.3 |
| Supersedes | ADR-0004 (capability schemas live in contracts/) and every capability rule elsewhere |

## Context
Every integration had to be bound to a **capability**: a vendor-neutral request and response schema per operation, stored as JSON Schema files under `contracts/schemas/capabilities/` and changed by PR through the contract-steward. Request transformers were checked against the capability request schema, and response transformers had to produce exactly the capability response schema.

In practice this blocked the main job of the Hub: an operator onboarding a new third-party API from the panel. The first real attempt (an age estimate from agify.io: `{firstName, lastName}` in, `GET /?name=<first><last>` out, only `age` back) needed a code change before any integration could exist. The console had no page for capabilities, and the capability added nothing the integration and the endpoint could not say themselves. The product owner's model is:

1. A **schema registry** on both sides, field by field, each field **required or optional**:
   - the integration's input: the caller's request **body and headers**;
   - the vendor endpoint's request: **query parameters, headers and body** (and path parameters).

   The operator builds each one in the panel from a pasted sample, then marks fields required or optional.
2. A **request transformer** always turns the caller's input into the vendor request.
3. **Samples validate the transformer.** The operator gives a sample input and the vendor request it should produce. The Hub runs the transformer on the sample. It refuses the transformer, with the reason in the panel, when:
   - the output differs from the expected sample; or
   - the output breaks the endpoint's registry (a required field missing, a wrong type).

   This is a check on the transformer, not on callers.
4. Whatever the vendor answers (status, headers, body) goes through a **response transformer if the integration has one**, and its output is returned as it is. Without one, the vendor's body is returned as it is. The vendor's response schema is optional and never enforced.

## Options considered
1. **Keep capabilities, add a console page.** Still a separate object with no purpose of its own, and response schemas forced on every vendor. Rejected by the product owner.
2. **Integration owns input and output schemas; shared capabilities optional.** Keeps a canonical output. Rejected: the owner does not want the output constrained or a second concept kept.
3. **Remove capabilities.** The integration owns each operation's request schema, the endpoint owns its request schema, response transformers are optional and unconstrained. **Chosen.**

## Decision
Capabilities are removed from the product: no capability objects, files, APIs, fields or UI.

### Integration (versioned)
Each integration **version** carries its operations, so a schema change goes through draft, validate, publish, diff and the change log like any other change:
```yaml
operations:
  default:                       # single-operation integrations use "default"
    description: Estimate the age for a first and last name.
    idempotent: true
    request_schema:              # The caller's input. Required. JSON Schema 2020-12 underneath; the
      body:                      # panel edits it as a field list (name, type, required/optional).
        type: object
        required: [firstName, lastName]
        properties:
          firstName: { type: string, minLength: 1 }
          lastName:  { type: string, minLength: 1 }
      headers:                   # Optional: caller headers the transformer reads (as hdr.*).
        type: object
        properties: { X-Request-Source: { type: string } }
    validate_input: true         # Check live calls against the required fields (400 VALIDATION_FAILED). Default true.
```
- `request_schema.body` is mandatory for every operation (`{}`-typed `object` when a call has no body). `request_schema.headers` is optional. A required field is listed in `required`; every other field is optional. The schemas may carry `x-pii` tags, which drive audit masking (they used to come from the capability schema).
- Integration-level flags stay on the integration: `interactive` (operations are steps of a session, `X-Hub-Session`; replaces the capability's `interactive`) and `moves_money_or_quota`.
- Each target maps every operation to a pair `{ request: X@n, response: Y@n | null }`: `transformer` for `default` (or the first operation), `operations.<name>` for the others, as before.
- `delivery.ttl` defaults to the Hub default (it used to fall back to the capability's `default_ttl`).
- D11 becomes guidance: a change to a request schema that breaks callers should use a new integration ID. Publishing shows the schema diff, and the change log records it.

### Vendor endpoint (versioned, `name@N`)
Each endpoint version declares the request it accepts. This is mandatory:
```yaml
endpoints:
  estimate@1:
    method: GET
    path: /
    request_schema:              # Required. Each key is optional; {} means "nothing besides method and path".
      query:   { type: object, required: [name], properties: { name: { type: string, minLength: 1 } } }
      # path:    JSON Schema of the path parameters object
      # headers: JSON Schema of the non-credential headers object
      # body:    JSON Schema of the body
    response_schema: { ... }     # Optional, informational only, never enforced.
```
The request transformer's output (`path` parameters, `query`, non-credential `headers`, `body`) is validated against it with the samples, never on live calls:
- at transformer creation, on every sample (golden fixture): the produced request must equal the expected sample and fit the endpoint's registry;
- at integration validate and publish, the integration's samples go through the same check.

On live calls the vendor request is not validated, so this adds nothing to the hot path. If a real call produces something the vendor rejects, the call fails with the vendor's error.

Credential headers or query values set from `creds.*` are not part of the validated object.

### Transformers
- `capability` and `operation` fields are removed from transformer files and from `TransformerVersionCreate`. A request transformer names its `vendor_endpoint`. A response transformer stands alone.
- **Request transformer: always required, with at least one sample.** A sample is an input (`body`, optional `headers`) and the expected vendor request. Field references such as `req.firstName` and `hdr.X-Request-Source` are checked at integration validate and publish against the operation's `request_schema`, and the samples' inputs must fit it. The transformer itself no longer knows a caller schema.
- **Response transformer: optional; when present, at least one sample** (a vendor response and the expected output). Input is `res.status`, `res.headers`, `res.body`, `req`, `account`. Its `response` output is returned to the caller as it is, without a schema check. `errors` rules and `session` work as before.
- **No response transformer (pass-through):**
  - A vendor 2xx returns the vendor's status and body unchanged. JSON stays JSON; anything else is returned with its `Content-Type`.
  - Non-2xx keeps the default classification for retries and the breaker: timeouts, 429 and 5xx are retryable, other 4xx are not. Once retries are exhausted, the caller gets the vendor's status and body unchanged.
  - Headers: only `Content-Type` and `Retry-After` pass through, plus the Hub's own (`X-Hub-Request-Id`, …). Every other vendor header is dropped, so the caller never sees vendor identity from headers (DESIGN §0). What a vendor's body says about itself is the integration owner's choice when they skip the response transformer.

### Vendors and accounts
- A new auth scheme, `none`, is for public APIs. Its accounts have no credentials (`credentials` may be omitted or `{}`).

### Caller API and MCP
- Discovery (`listIntegrations`, `hub_list_integrations`) returns each integration's `operations` with their `request_schema` (and `idempotent`) instead of a `capability`. The caller invokes with the integration ID, the operation and a body that matches the schema, as before.
- Breaking change to the caller and admin contracts, approved by the product owner on 2026-11-07 (pre-1.0, nothing released): the major version is bumped.

### Removed
- The admin API `/capabilities*` operations and every `capability` field and filter (integrations, transformers, requests, audit, discovery).
- `contracts/schemas/capabilities/`, `backend/config/capabilities/` and the examples' capability overlays.
- `registry.Capability` and the capability loader.
- The `hub.capabilities` table. A new migration drops it and the `capability` columns, and copies existing capability schemas into integration versions' `operations`. Migrations stay append-only.
- Capability fields, filters and columns in the console, MCP tool contract, skills and docs.

## Consequences
- Positive:
  - An operator onboards an API entirely from the panel: vendor, endpoint with its request schema, account, transformers with goldens, integration with its request schema, publish. No PR, no code.
  - Fewer concepts.
  - Vendor responses don't need schemas.
- Negative:
  - No canonical output. Switching an integration to another vendor can change what callers receive, unless the new response transformer reproduces the old output. The panel's version diff shows response goldens side by side, so the change is visible before publishing.
  - Pass-through returns vendor bodies as they are.
  - The live-call input check (`validate_input`) is the existing compiled-schema check; it can be switched off per operation.
- Follow-ups (EP-14):
  - Contracts: admin, public and MCP (HUB-432).
  - Backend: registry, transform, edge, delivery, audit, admin API and migration (HUB-433, HUB-434), and auth `none` (HUB-438).
  - hubctl, hubmcp, examples and seed config (HUB-435).
  - Console (HUB-436).
  - Skills and docs (HUB-437).

## Deviation from DESIGN.md
- §3 (capability as the canonical contract), §2 D11 and D12 (D11 becomes guidance; D12 stands: callers still never pick versions), §4 (response validation against a canonical schema; caller validation becomes the per-operation `validate_input` switch) and §7 (transformers bound to a capability) are replaced by this ADR. DESIGN.md is not rewritten; it points here.
- DESIGN §0 "callers never see vendor names" is kept for headers. In pass-through, the body is returned as the vendor sent it.
