# Contract Changes (CCR process)

Rules: AGENTS §4; rationale: ADR-0004. Actors: `contract-steward` (sole editor of `contracts/`), `backend-engineer`, `frontend-engineer`, plus the MCP server and consumer skill owners as consumers.

## 1. When a CCR is needed
Any change to: an endpoint, path/query parameter, header, request or response field, status code, error code, example, event JSON Schema, or MCP tool definition in `contracts/`. Not a CCR: an integration operation's request schema or a vendor endpoint's request schema. Those are registry data, edited in the console and versioned with the integration or endpoint (ADR-0023). If you are about to code against something that is not in `contracts/`, stop and file a CCR (backend/AGENTS.md, frontend/AGENTS.md).

## 2. Flow
```mermaid
sequenceDiagram
  autonumber
  participant R as Requester (backend / frontend / mcp agent)
  participant T as TICKETS.md
  participant S as contract-steward
  participant B as backend-engineer
  participant F as frontend-engineer
  R->>T: add HUB-### (type: contract) with the CCR text (§3)
  R->>S: hand off CCR (ticket id)
  S->>S: review against checklist (§4); clarify or reject
  S->>S: edit contracts/, bump info.version, contracts/CHANGELOG.md, examples
  S->>S: commit contract(<area>): <change> (HUB-###) + CHANGELOG.md entry
  S->>B: notice (§6): operationIds, what to implement
  S->>F: notice (§6): run npm run gen:api, what to adapt
  B->>B: implement; contract tests green
  F->>F: regenerate client; svelte-check + tests green
```

## 3. CCR template
Copy into the ticket description (`docs/tickets/TICKETS.md`):
```markdown
### CCR: <short title>
- **Requested by**: backend-engineer | frontend-engineer | hubmcp | skill  (ticket HUB-###)
- **Why**: <user story / feature (US-xxx, F-XX) and the problem>
- **Document**: hub-public.v1.yaml | hub-admin.v1.yaml | schemas/events/... | mcp tools
- **Operation / schema**: <operationId or schema $id>, <METHOD /path>
- **Before**:
  ```yaml
  # current snippet, or "does not exist"
  ```
- **After**:
  ```yaml
  # proposed snippet including at least one example
  ```
- **Breaking?**: no (additive) | yes — why it cannot be additive
- **Consumers affected**: backend handlers, frontend routes, hubmcp tools, pkg/hubclient, claude-skill
- **Error codes**: canonical codes used (DESIGN §4.4); any new code requested
- **Rollout**: same PR train | contract PR first
```

## 4. Steward review checklist
- [ ] **No vendor leakage**: nothing in `/v1` (including discovery, errors, headers, examples) names a vendor, account, transformer or version (D12, ADR-0007). Debug header `X-Hub-Integration-Version` stays documented as debug-only.
- [ ] **Versioning**: `info.version` bumped — additive = minor, breaking = major, docs/examples only = patch. Caller-breaking → rejected; propose a new integration ID instead (D11). Admin-breaking → user approval recorded in the ticket.
- [ ] **Examples updated**: every new/changed response has an example; examples validate against their schemas (they feed MSW mocks and contract tests).
- [ ] **Error codes canonical**: all errors use the single envelope (DESIGN §4.3) and codes from DESIGN §4.4; a new code needs a DESIGN/ADR reference.
- [ ] `operationId` unique, stable, camelCase; tags consistent.
- [ ] All `$ref`s resolve; spec lints clean.
- [ ] Pagination, filtering and auth (tenant key vs OIDC role) declared; RBAC role noted in the operation description (SECURITY §7).
- [ ] PII: `x-pii` annotations on any personal data in contract examples (operation request schemas carry their own tags, ADR-0023); not returned to callers if stripped by policy.
- [ ] `contracts/CHANGELOG.md` entry written.

## 5. Commit order
1. `contract(<area>): <change> (HUB-###)` — contracts only (+ root `CHANGELOG.md` entry).
2. `feat(<backend scope>): … (HUB-###)` — backend implementation + contract tests.
3. `feat(frontend): … (HUB-###)` — regenerated client + UI.
4. MCP/skill updates if callers see the change (AGENTS §8.9).

The contract commit may merge alone (its own PR) so other agents can start immediately. Backend and frontend must never merge a shape that is not already on `main` in `contracts/`.

## 6. Notifying agents
The steward writes a notice in the ticket (and in the PR description) — written artifacts are the channel (AGENTS §11):
```markdown
### Contract notice — HUB-### — hub-admin.v1.yaml 1.4.0 → 1.5.0 (minor)
- Changed: `listDeadLetters` adds query param `reason`; `DeadLetter.reason` enum adds `malformed_response`.
- Backend: implement filter; extend contract test for 200 with `reason`.
- Frontend: `npm run gen:api`; add reason filter on `/dlq`.
- hubmcp / skill: no change.
```
The backend and frontend agents link the notice from their tickets.

## 7. Frontend regeneration
```bash
cd frontend
npm run gen:api      # openapi-typescript ../contracts/openapi/hub-admin.v1.yaml -o src/lib/api/schema.d.ts
npm run check        # type errors show every place the change touches
npm test
```
`src/lib/api/schema.d.ts` is committed; MSW handlers update from the new examples. Never edit the generated file by hand.

## 8. How drift is caught
| Drift | Caught by |
| --- | --- |
| Backend returns a shape not in the spec | backend contract tests validate every response (TESTING_STRATEGY §4.1) |
| Spec has an operation the backend lacks | operation coverage report in CI |
| Frontend uses a stale client | CI regenerates and fails on `git diff --exit-code` |
| Frontend uses a field that does not exist | `svelte-check` with generated types |
| MSW mocks diverge from spec | mock-vs-spec validation test |
| Contract edited outside the steward | CODEOWNERS + `contract(...)` commit-type check for `contracts/` paths |
| `contracts/` changed without version bump | CI contracts job |
| Vendor/account/version leak in `/v1` | leak scanner (TESTING_STRATEGY §4.1) |
