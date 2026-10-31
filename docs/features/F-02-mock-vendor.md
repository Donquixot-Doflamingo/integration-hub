# F-02 — Mock vendor (programmable fake vendor)

## Summary
`backend/cmd/mockvendor`: an HTTP server that impersonates any vendor with per-route programmable behaviour and request recording. It is the only "vendor" any test or local run talks to (DESIGN §0, §19 M0).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M0 · EP-01 · HUB-020–039.

## Problem
The Hub's value is in failure handling (timeouts, 429s, outages, malformed bodies). Real vendors cannot be made to fail on demand, and calling them in tests is forbidden.

## Goals / Non-goals
**Goals**
- Control API to program behaviour per route (method + path, optionally per account header).
- Behaviours: fixed/random latency, status code, error ratio, 429 with `Retry-After`, hard down (connection refused), slow drip, malformed body, fixed response body/headers.
- Record every received request for assertions; reset between tests.
- Send inbound webhooks to the Hub on command (signed), for Release-2 tests.
- Usable as a Go test helper (in-process) and as a standalone binary (docker-compose).

**Non-goals**
- Faithful emulation of any vendor's business logic; contract tests against real vendor sandboxes.

## User stories
- [US-0201](../product/USER_STORIES.md) — Programmable behaviour per route
- [US-0202](../product/USER_STORIES.md) — Request recording for assertions
- [US-0203](../product/USER_STORIES.md) — Outbound webhooks from the mock

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Control API | `GET /__control/routes`, `GET`/`PUT`/`DELETE /__control/routes/{route_id}` (behaviour), `PUT /__control/default` | `contracts/openapi/mockvendor.v1.yaml#listRoutes`, `#getRoute`, `#setRouteBehaviour`, `#deleteRoute`, `#setDefaultBehaviour` |
| Control API | `GET /__control/requests`, `DELETE /__control/requests` (recordings) | `contracts/openapi/mockvendor.v1.yaml#listRecordedRequests`, `#clearRecordedRequests` |
| Control API | `POST /__control/webhooks` (send an inbound webhook to the Hub), `POST /__control/reset`, `GET /__control/healthz` | `contracts/openapi/mockvendor.v1.yaml#triggerInboundWebhook`, `#resetMock`, `#getMockHealth` |
| Vendor surface | Any path, e.g. `POST /1/upload` (CleverTap-like) | — |
| Go helper | `mockvendor.Start(t) *Server` with typed behaviour builders | — |

## Behaviour & rules
1. Behaviour is matched by the most specific route; unmatched routes return 404 with a recognisable body so misconfigured tests fail loudly.
2. Randomness (latency jitter, error ratio) uses a seed settable via the control API, so chaos tests are reproducible.
3. `hard_down` closes the listener for that route's virtual host (connection refused), distinct from `status: 503`.
4. `slow_drip` writes the body in chunks over `duration_ms` to exercise read timeouts.
5. `malformed` returns invalid JSON with `Content-Type: application/json`.
6. Recorded requests keep raw headers and body bytes; tests use them to assert transformer output and secret placement.
7. Behaviours can be scheduled (e.g. "down for 45 s then healthy") to script M5 chaos scenarios (DESIGN §19 M5).

## Data model impact
None (in-memory).

## Config impact
Dev vendor definitions in `backend/config/vendors/` point base URLs at mockvendor (DESIGN §19 M1).

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `mockvendor_requests_total{route,status}` | for load-test sanity |
| Log | `mock_request` | route, status, latency (debug level) |

## Security & PII
Test-only; never deployed to production. Recorded data is fake.

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Unknown route | 404 `{"mock":"no route"}` | Test fails with clear message |
| Control API misuse | 400 with validation message | — |

## Testing
- **Unit**: each behaviour (table tests), seed determinism, scheduled behaviour transitions.
- **Integration**: hub → mockvendor round trip in M3 e2e.
- **Chaos**: mockvendor is the chaos engine for M5 and the M8 soak.

## Rollout & migration
Ships in M0; extended as later milestones need (webhook send in Release 2).

## Open questions
- Multi-host emulation (separate ports per vendor region) or one port with `Host` routing?

## Tickets
HUB-020 – HUB-039 (shared range with F-01 M0 work).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
