# F-07 — Credentials & vendor auth schemes

## Summary
The `CredentialSource` abstraction, a decrypted credential cache warmed on snapshot load, the local AES-GCM file source for dev, value-based log redaction, and the v1 auth-scheme plugins: static headers, bearer, basic (and query param, discouraged) (DESIGN §9).

## Status
`proposed` — 2026-10-31.

## Owner
`backend-engineer`.

## Milestone / Epic
M3 · EP-04 · HUB-080–109.

## Problem
Today keys are copied into every service. The Hub must hold them once, encrypted, inject them without request-path decryption, and never leak them into logs or audit.

## Goals / Non-goals
**Goals**
- `CredentialSource.Get(ctx, accountID)` returns the decrypted `active` slot (DESIGN §18.3).
- Cache keyed `account + cred_version`, TTL 5 min, invalidated on snapshot ping, warmed on snapshot load; **no decryption on the request path**.
- Local `CredentialSource`: AES-GCM-encrypted file, dev key from env.
- Auth plugins in `internal/creds`: `static_headers`, `bearer`, `basic`, `query_param`.
- Redactor learns every loaded secret value.

**Non-goals**
- KMS/Vault sources, OAuth2 client credentials, HMAC signing, mTLS (Release 2, F-22). Rotation workflow (F-15).

## User stories
- [US-0701](../product/USER_STORIES.md) — No decryption on the request path
- [US-0702](../product/USER_STORIES.md) — Static headers, bearer, basic
- [US-0703](../product/USER_STORIES.md) — Local encrypted credential file

## UX / API surface
| Surface | Item | Contract ref |
| --- | --- | --- |
| Admin API | `POST /admin/v1/accounts/{account_id}/credentials/next` (write-only) | `contracts/openapi/hub-admin.v1.yaml#addNextCredential` |
| Admin API | `POST /admin/v1/accounts/{account_id}/test-connection` | `#testAccountConnection` |
| Files | `backend/config/accounts/*.yaml` with `credentials_ref`; `secrets.dev.enc` | — |

## Behaviour & rules
1. Credentials never appear in the snapshot or registry YAML; accounts carry `credentials_ref` (DESIGN §8.3).
2. Storage is envelope encryption: per-account data key wrapped by the master key (dev: file key; prod: KMS/Vault). DB sees ciphertext + wrapped key only (`account_credentials`).
3. Every decrypt and credential change emits an audit/change-log record (DESIGN §9).
4. Injection happens during transform via `creds.*` in header/query/auth targets (F-04 rule) or via the scheme plugin.
5. Credential injection budget 0.02 ms (DESIGN §18.6).
6. Redaction is by value: the logger handler and audit writer replace any occurrence of a known secret (and its base64 for basic auth) with `***`.
7. Query-param credentials are supported but the URL is redacted before any log or audit write.
8. Admin APIs never return credential values; read endpoints show slot, version, created_at only.

## Data model impact
`account_credentials(account_id, cred_version, slot, ciphertext, wrapped_key)` (DESIGN §18.2).

## Config impact
Vendor `auth:` scheme name and shape (header names); account `credentials_ref`.

## Observability
| Kind | Name | Notes |
| --- | --- | --- |
| Metric | `credential_decrypts_total{account}` | must stay flat under steady traffic |
| Metric | `credential_cache_misses_total` | |
| Metric | `credential_expiry_days{account}` | where expiry known |
| Alert | credential expiring < 7 days | warn (DESIGN §16.3) |

## Security & PII
Core security feature. Dev key never committed; `secrets.dev.enc` contains only test values for mockvendor. Grep test over logs and `request_events` in M3 e2e (F-08).

## Failure modes
| Failure | Behaviour | Caller sees |
| --- | --- | --- |
| Decrypt fails at warm-up | Account marked unavailable; `/readyz` stays green for others | Requests to that account → 500 `INTERNAL_ERROR` (retryable; contract catalogue) |
| Wrong dev key | Readiness fails with clear error | — |
| Vendor rejects creds | Classified | 502 `UPSTREAM_AUTH_FAILED`, page |

## Testing
- **Unit**: each scheme builds correct headers; redactor covers base64 basic-auth form.
- **Integration**: decrypt counter flat over 1,000 requests; cache invalidation on ping.
- **Golden**: transformer fixtures assert `***` in recorded audit, real value at mockvendor.

## Rollout & migration
Local source only in v1 dev; production requires KMS/Vault (Release 2, PRD Q7) — or an interim decision.

## Open questions
- What production uses before Release-2 KMS/Vault lands (GA blocker if production precedes Release 2).
- Error code when an account's credentials cannot be decrypted (no dedicated code in DESIGN §4.4).

## Tickets
HUB-080 – HUB-109 (EP-04, filter `feature: F-07`).

## Changelog of this doc
| Date | Change | Author |
| --- | --- | --- |
| 2026-10-31 | Created | docs agent |
