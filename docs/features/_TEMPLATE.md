# F-XX — <Feature name>

> Copy this file to `docs/features/F-XX-<slug>.md`, fill every section (write "None" rather than deleting a section), and add a row to `README.md`. Cite the design as "DESIGN §x.y". Keep it to roughly 60–150 lines; link out instead of repeating DESIGN.md.

## Summary
One or two sentences: what the feature does and for whom.

## Status
`proposed` | `in-progress` | `shipped` — date of last change.

## Owner
Agent or person accountable (e.g. `backend-engineer`, `frontend-engineer`, `contract-steward`, lead).

## Milestone / Epic
`Mx` (DESIGN §19) · `EP-xx` · Ticket range `HUB-###–###`.

## Problem
What hurts today and for which persona ([PERSONAS](../product/PERSONAS.md)).

## Goals / Non-goals
**Goals**
- …

**Non-goals**
- …

## User stories
- [US-FFNN](../product/USER_STORIES.md#us-ffnn--short-title) — short title

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Caller API | `POST /v1/...` | `contracts/openapi/hub-public.v1.yaml#operationId` |
| Admin API | `GET /admin/v1/...` | `contracts/openapi/hub-admin.v1.yaml#operationId` |
| CLI / console / MCP | `hubctl ...`, screen name, tool name | — |

## Behaviour & rules
Numbered, testable rules. State defaults and limits. Reference DESIGN sections.

## Data model impact
Tables / columns / indexes added or changed (DESIGN §18.2). Migrations needed.

## Config impact
New or changed fields in vendor (endpoints, request schemas) / account / transformer / integration (operations) / tenant YAML. Defaults and validation.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `hub_...` | labels |
| Log | event name | fields (never secrets) |
| Trace | span name | attributes |
| Alert | rule | severity (page / warn) |

Also add these to `docs/tech/OBSERVABILITY.md`.

## Security & PII
Authn/authz, secrets handling, PII tagging, SSRF/egress, vendor-identity leakage checks (DESIGN §0).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| … | … | … |

## Testing
- **Unit** (table-driven, `-race`): …
- **Integration** (real Postgres/Redis): …
- **Chaos** (mockvendor): …
- **Contract** (responses validated against OpenAPI): …
- **Golden** (transformer fixtures): …

## Rollout & migration
Flags, ordering, backfills, how to roll back.

## Open questions
- …

## Tickets
`HUB-###` – `HUB-###` (see `docs/tickets/TICKETS.md`, filter by `feature: F-XX`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| YYYY-MM-DD | Created | … |
