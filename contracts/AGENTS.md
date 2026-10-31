# contracts/AGENTS.md

Rules for anyone working in `contracts/`. Inside this folder they override the root `AGENTS.md` where the two differ.

## Who may edit
- **Only the `contract-steward` agent** (or a human acting as steward) edits files here.
- `backend-engineer`, `frontend-engineer` and `docs-keeper` **never** edit this folder. They file a contract change request (CCR) as described in `docs/process/CONTRACT_CHANGES.md` and wait for the steward.

## CCR flow
1. The requester writes the CCR: the problem, the proposed shape (YAML/JSON snippet), additive or breaking, the affected `operationId`s and tools, and example request and response.
2. The steward checks it against `docs/DESIGN.md`:
   - **Reject anything that leaks vendor names, account names, or integration/transformer versions into `hub-public`** (D12), or that lets a caller select them. If a request needs this, stop and ask the user.
   - Prefer additive changes. A breaking caller change means a new capability major plus a new integration ID (D11), never an edit in place. Breaking admin changes need the user's approval.
3. The steward edits the YAML/JSON and updates or adds examples.
4. The steward bumps the version (semver, per document) and adds a `contracts/CHANGELOG.md` entry (newest first, with date, document, version, CCR/HUB id and summary).
5. The steward runs validation (see `README.md` § Validate locally). It must be clean.
6. The change goes in its own commit before any dependent code: `contract(<area>): <change> (HUB-###)`, plus the root `CHANGELOG.md` entry.
7. The steward notifies backend and frontend: what changed, which operationIds, and what each side must do (backend implements and passes contract tests, frontend runs `npm run gen:api`).

## Every change must
- Keep OpenAPI **3.1.0** with `jsonSchemaDialect` 2020-12. JSON Schemas declare `$schema` 2020-12.
- Give every operation a unique camelCase `operationId`, a declared tag, a `summary`, a `description`, and request and response schemas via `$ref` to `components`.
- **Include examples**: at least one per request body and per response body. Examples must validate against their schemas, and IDs must be well-formed ULIDs. Quote every timestamp in YAML (`'2026-10-31T09:00:00Z'`), or YAML parsers turn it into a date.
- Use the canonical error envelope for every 4xx/5xx. A new error code goes into the enum **and** the README catalogue.
- Admin: set `x-required-role` on every operation. Every mutation requires `reason` and returns `change_id`.
- Use ms integers for durations, RFC 3339 for timestamps, and cursor pagination (`limit` ≤ 200, `next_cursor`).
- Never put a real secret, key, token or customer data in an example. Use obviously fake values (`example-passcode-not-real`, `hk_live_...EXAMPLE...`).
- Keep `mcp/hubmcp-tools.json` in sync. `maps_to` operationIds must exist, and tenant tools must not expose vendor, account or version.

## YAML style
- Use block style for anything with prose. In flow style (`{ ... }`), **quote any string containing `,` `:` `#` `{` `}` `[` `]`**. Unquoted, these silently change the parsed document.
- Don't use anchors or aliases (not every toolchain handles them).
- Keep the file per major (`*.v1.yaml`). Bump `info.version` inside it.
