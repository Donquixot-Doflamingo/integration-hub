# F-22 — Release-2 capabilities

## Summary
The DESIGN §19 "Phase 2" items that were not pulled into v1, grouped here as Release 2. Each item gets its own feature doc (new `F-XX`) when work starts; this doc fixes scope and pointers.

## Status
`proposed` — 2026-10-31.

## Owner
Lead (scoping); `backend-engineer` per item.

## Milestone / Epic
Release 2 (after M8) · EP-12 · HUB-330–399.

## Problem
v1 covers engagement-style traffic (CleverTap pilot). Lending capabilities (bureau, Aadhaar, PAN, AA, eSign, eNACH), payment partners and LLMs need extra auth schemes, multi-step flows, sealed audit and managed key storage (DESIGN §14, §15).

## Goals / Non-goals
**Goals** — items:

| # | Item | What | DESIGN | Story |
| --- | --- | --- | --- | --- |
| R2-1 | Inbound vendor webhooks | `/webhooks/in/{vendor}/{account}`, verify (HMAC/basic/IP allowlist), reject replays > 5 min, correlate by request id or `vendor_ref`, inbound transformer → canonical event, 200 immediately, fan out via subscriptions | §12.2 | US-2201 |
| R2-2 | Micro-batching | Worker groups envelopes per account + endpoint in a 50–200 ms window for `batch:` transformers; splits per-record results | §7.5 | US-2202 |
| R2-3 | Auth schemes | OAuth2 client credentials (per-account token cache, refresh 60 s early, single-flight), HMAC request signing, mTLS client certs | §9 | US-2203 |
| R2-4 | LLM streaming | `llm.chat@v1` / `llm.embed@v1`; SSE relay without buffering; `stream:` mapping; `max_stream_s`; failover only before first byte; `supports` check; token accounting and budgets; `metadata_only` audit | §15, §5.4 | US-2204 |
| R2-5 | Vendor sessions | `X-Hub-Session`, `vendor_sessions` store (10 min TTL), pinned to version + account; `SESSION_NOT_FOUND`; Aadhaar number never stored | §3.1, §14 | US-2205 |
| R2-6 | Redirect hand-offs | `handoff_url`, `GET /handoff/return/{session_id}`, status webhook to tenant (DigiLocker, eSign, eNACH) | §4.1, §14 | US-2205 |
| R2-7 | Account Aggregator | Long-running requests (TTL in days), consent artefact store, inbound notifications as state-machine steps, final data via callback | §14 | US-2206 |
| R2-8 | Sealed audit + unseal | Field-level encryption with per-tenant key; default for bureau/Aadhaar/PAN, 90 days; audited unseal admin action | §13.2, D6 | US-2207 |
| R2-9 | KMS / Vault | `CredentialSource` implementations; envelope encryption with managed master keys; audited decrypts | §9 | US-2208 |
| R2-10 | Other DESIGN Phase-2 items | Tenant self-service; UI changes landing as Git commits; transformer editor in the console; 99.95 % availability target | §16.1, §19, §1 | — |
| R2-11 | Lookup result cache | Optional per-integration result cache for sync lookups (e.g. PAN–Aadhaar link, 24 h); HUB-347 | §14 | US-2205 |
| R2-12 | Upload + job | Large payloads in object storage, job poll/webhook adapters (bank statement analysis, bulk CKYC); HUB-348 | §14 | US-2205 |

**Non-goals**
- Release 3 items: broker-backed queue (NATS/RabbitMQ), per-key ordering at scale, warm-standby region, Java/Node SDKs, cost dashboards, transformer template library (DESIGN §19 Phase 3).

## User stories
- [US-2201](../product/USER_STORIES.md) … [US-2208](../product/USER_STORIES.md)

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Inbound | `POST /webhooks/in/{vendor}/{account}` | `contracts/openapi/hub-public.v1.yaml#receiveVendorWebhook` (`x-internal: true`; vendor-facing, not tenant-facing) |
| Hand-off | `GET /handoff/return/{session_id}` | `#handoffReturn` (`x-status: planned`) |
| Sessions | `X-Hub-Session` header on `invokeIntegrationOperation` | `contracts/openapi/hub-public.v1.yaml#/components/parameters/HubSession` |
| Admin | `POST /admin/v1/requests/{request_id}/unseal` | `contracts/openapi/hub-admin.v1.yaml#unsealRequest` |
| Events | `message.delivered@v1`, `aa.fi_ready@v1` | Not in `contracts/schemas/events/` yet (missing — needs CCR, Release 2) |

**Status of R2-5 (2026-11-06, HUB-342).** The vendor session store itself shipped in M6 (HUB-175, HUB-183): `X-Hub-Session`, pinning to version + account, 10-min expiry, Aadhaar-like data refused. What R2-5 still needs: per-operation transformers so `generate` and `verify` can call different vendor endpoints (HUB-343, admin contract HUB-344; until then the registry should refuse such configs, HUB-345), a canonical `reason` for OTP rejections (HUB-346), and the production `kyc.aadhaar_otp@v1` capability with a licensed KUA/ASA (HUB-334). The caller's view, with a runnable example, is use case 6 of the [use-case catalogue](../use-cases/README.md#6-two-step-flows-otp-then-verify-then-bank-account).

## Behaviour & rules
Defined per item in DESIGN; key invariants carried into each item's doc:
1. Inbound webhooks are vendor-scoped URLs but tenants only ever see canonical events (DESIGN §0).
2. Sessions pin version + account; cutover never moves step 2 (DESIGN §6.3).
3. Interactive capabilities stay sync; a 202 is never an answer to a person waiting (DESIGN §5.4).
4. Unseal is itself audited (actor, reason, time) and visible in the change log.

## Data model impact
`vendor_sessions`; consent artefact table (new); per-tenant seal keys; OAuth2 token cache (memory).

## Config impact
Account webhook scheme; transformer `stream:`, `batch:`, `supports:`; capability `interactive`; integration `audit.payloads: sealed`.

## Observability
`llm_tokens_total`, `llm_cost_usd_total`, inbound webhook verification failures, session expiries, unseal count (new).

## Security & PII
Sealed payloads unreadable without unseal; KMS decrypts audited; mTLS and HMAC keys per account; consent references stored on requests.

## Failure modes
Per item doc.

## Testing
Chaos and golden coverage per item; mockvendor inbound webhook support (F-02 US-0203).

## Rollout & migration
Order depends on PRD Q3 (first production capabilities after the CleverTap pilot) and Q7 (KMS vs Vault).

## Open questions
- Which items gate the first lending vendor (likely R2-3, R2-5, R2-8, R2-9)?
- DESIGN §19 lists sealed audit as Phase 2, but D6 makes `sealed` the default for bureau/Aadhaar/PAN — no such integration can go live before R2-8.

## Tickets
HUB-330 – HUB-399.

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
| 2026-11-06 | R2-5 status after HUB-342; R2-11 (HUB-347) and R2-12 (HUB-348) added; link to the use-case catalogue | backend-engineer |
