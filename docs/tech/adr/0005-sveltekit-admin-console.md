# ADR-0005: SvelteKit admin console, pulled forward from phase 2

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-10-31 |
| Deciders | lead, user |
| DESIGN refs | §16.1, §19 (Phase 2 lists "admin UI") |

## Context
DESIGN schedules the admin UI after M8. Operating cutovers, DLQ replay, breaker control and request timelines through `hubctl` alone slows the M5–M7 acceptance work and hides UX problems in the admin API until late. With contract-first (ADR-0004), a frontend agent can build against MSW mocks in parallel without blocking the backend.

## Options considered
1. **Keep UI in phase 2** — less parallel work, but the admin API shape is never exercised by a real UI before it freezes.
2. **React/Next.js** — large ecosystem; heavier runtime and more boilerplate for a small internal tool.
3. **SvelteKit 2 + Svelte 5 (runes) + TypeScript** — small bundles, server-side `load` and form actions fit a BFF auth model, simple state model.

## Decision
Option 3, built in parallel from phase 2 milestone M0 onwards (epic EP-10, feature F-19). It talks **only** to `/admin/v1` via a client generated from `contracts/openapi/hub-admin.v1.yaml` (`openapi-typescript` + `openapi-fetch`); OIDC Authorization Code + PKCE handled server-side with a dev stub; RBAC enforced by the API. Architecture: TECH_SPEC §17.

## Consequences
- Admin API contract gets exercised early; CCRs from the frontend shape it.
- Adds Node 22 LTS to the toolchain (not yet installed, HUB-012) and a `frontend` image.
- Console features that depend on phase-2 backend features (unseal, inbound webhooks) show "not available" until the endpoints exist in the contract.

## Deviation from DESIGN.md
Admin UI moves from Phase 2 to run alongside M0–M8. Behaviour unchanged.
