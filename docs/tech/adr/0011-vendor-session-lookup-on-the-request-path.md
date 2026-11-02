# ADR-0011: Vendor session lookup on the request path

| | |
| --- | --- |
| Status | Accepted |
| Date | 2026-11-02 |
| Deciders | backend-engineer (for lead review) |
| DESIGN refs | §3.1, §6.3 row 6, §14, §18.2, §18.5 rule 1 |

## Context
DESIGN §18.2 stores vendor sessions in Postgres (`vendor_sessions`) and §6.3 pins step ≥ 2 of a multi-step operation to the version and account that opened the session. §18.5 rule 1 forbids DB reads on the sync path. A step-2 request carries only `X-Hub-Session`; the version, account and data it needs exist only in that row, and sessions are created at request rate, so they cannot live in the config snapshot.

## Options considered
1. **Postgres primary-key read for requests that carry `X-Hub-Session`** (and one insert when step 1 opens a session).
2. **Redis** for sessions (like idempotency, §18.5 rule 6) — a second store for the same data, and the retire guard (§6.5) and audit would need Postgres anyway.
3. **Encode version/account/data in the session id** (signed token) — leaks routing state to callers (D12) and data could not expire server-side.

## Decision
Option 1. Only requests with `X-Hub-Session` read `hub.vendor_sessions` (one indexed PK read, timed in the `resolve` stage); only a successful step 1 whose response transformer maps a `session:` writes it. Every other request keeps a DB-free sync path. Step ≥ 2 has no failover (the vendor txn id lives at one account).

## Consequences
- Multi-step flows depend on Postgres availability: a lookup error is `INTERNAL_ERROR` (retryable), not `SESSION_NOT_FOUND`.
- The gateway uses `Deps.Sessions` (defaults to the queueing pool). A gateway without a database answers every `X-Hub-Session` with `SESSION_NOT_FOUND`.
- A session is opened only when the caller receives the 200 (sync, or wait within its window). An async step 1 opens nothing (callers cannot receive the header from a 202); interactive multi-step capabilities are sync-only anyway (DESIGN §5.4). A wait-mode step 1 that completes after its 202 opens a session nobody learns; it expires with its TTL.
- When several targets of one version share the session's account, the first in file order serves step 2 (targets, then failover).
