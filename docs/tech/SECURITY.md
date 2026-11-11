# Integration Hub — Security

| | |
| --- | --- |
| Status | Phase 1 (plan); HUB-234 hardening checks in CI and tests |
| Date | 2026-10-31 (updated 2026-11-07, HUB-234; 2026-11-11, HUB-468) |
| Behaviour source | DESIGN §9 (credentials), §12 (webhooks), §13.2 (payload policies), §16.1 (RBAC), §16.5 (security) |

## 1. Assets and trust boundaries

| Asset | Sensitivity |
| --- | --- |
| Vendor credentials (account secrets, OAuth tokens, mTLS keys) | critical |
| Tenant API keys | critical |
| Registrar signing secrets, inbound webhook secrets | high |
| Sealed payloads (bureau, Aadhaar, PAN, AA data), per-tenant sealing keys | critical (regulated) |
| Masked/full payloads in the audit trail | high (PII) |
| Registry config (transformers, integrations) | high (integrity: controls where data goes) |
| Admin identity (OIDC tokens), console session cookies | high |
| ISFC caller API keys (planned, EP-15; plaintext in ISFC, hashed by the Hub on read) | critical |

```mermaid
flowchart LR
  subgraph Internet-facing
    VEN[Vendors]
  end
  subgraph Internal network
    TEN[Tenant services]
    OPS[Operators / console users]
    MCPC[MCP clients]
  end
  subgraph Hub cluster
    GW[gateway] --- WK[worker] --- SCH[scheduler]
    ADM[admin]
    HM[hubmcp]
    CON[console]
  end
  DB[(Postgres)] & RD[(Redis)] & KMS[KMS/Vault] & OBJ[(Object storage)]
  TEN -- "B1 tenant key / mTLS" --> GW
  VEN -- "B2 inbound webhooks (signed)" --> GW
  GW & WK -- "B3 egress allowlist, TLS 1.2+" --> VEN
  WK -- "B4 signed callbacks, SSRF guard" --> TEN
  OPS -- "B5 OIDC (dev issuer today; ISFC login planned, EP-15)" --> CON --> ADM
  MCPC -- "B6 tenant key / admin token" --> HM --> GW & ADM
  GW & WK & SCH & ADM -- "B7 service creds" --> DB & RD & KMS & OBJ
```

### 1.1 Trust boundaries added by EP-15 (planned, not built)
Source: [AUTH_PLAN](AUTH_PLAN.md) §4, §7, §8; [ADR-0026](adr/0026-isfc-identity-and-per-operation-admin-policies.md), [ADR-0028](adr/0028-caller-auth-with-isfc-backend-clients-checked-in-memory.md). Today only the existing boundaries above apply.

| Boundary | Crossing | Trust rule |
| --- | --- | --- |
| B8: ISFC gateway → `hub admin` | Console and human requests arrive with a Hydra JWT plus `x-isfc-*` headers | The admin listener accepts them only from the gateway (network policy, HUB-497) **and** with a JWT the Hub verified itself (signature against Hydra JWKS, `iss`, `exp`, `nbf`). `sub` must equal `x-isfc-sub-id`, `client_id` must equal `x-isfc-client-id` and the console client id. Headers without a valid token are 401 (gap G5: ISFC's own sidecar trusts them) |
| B9: automation → `hub admin` | CI, hubctl, hubmcp (HTTP) call directly with `x-isfc-client-id` + `x-isfc-api-key` | OPA checks the key; machine principal `client:<id>`; refused for `unsealRequest` and `purgeDlq` (D7) |
| B10: `hub admin` → OPA sidecar | `POST /v1/data/integrationhub/decision` per admin call | Fail closed: deny 403, error or timeout 503 `authz_unavailable`. Not used on `/v1` |
| B11: Hub worker → ISFC auth-service | Reconciler reads tenants, client metadata, client configuration with its own S2S key (`TENANT:READ`, `CLIENT:READ`, ISFC-CR-5) | Read-only; failures keep the last good state and never disable tenants; keys are hashed the moment they are read |
| B12: caller → `hub gateway` with ISFC credentials | `x-isfc-client-id` + `x-isfc-api-key` on `/v1` | In-memory hashed lookup, no network hop, no OPA, no ISFC call on the request path |

## 2. Threat model (STRIDE-lite)

### 2.1 Gateway (`/v1`, B1)
| Threat | STRIDE | Mitigation |
| --- | --- | --- |
| Stolen/guessed tenant key | S | 256-bit random keys, stored as SHA-256 hash (high-entropy keys do not need a slow KDF), constant-time lookup via hash map; shown once; rotation with overlap; optional mTLS identity (DESIGN §4.2) |
| Tenant invokes another tenant's integration | E | grant check on every request (owner or `integration_grants`) before validation; returns 403 `FORBIDDEN`, and 404 for integrations the tenant cannot see in discovery (no existence oracle) |
| Caller learns vendor/account/version | I | contract scanner (TESTING §4.1), D12; the one documented exception is `dryRunIntegration` (exact upstream request, secrets `***`), gated by key scope `dry_run` that is not granted by default (PRD DV-6); `X-Hub-Integration-Version` debug header **disabled in prod** by default; error envelope `upstream` carries only numeric status/code; vendor error messages passed through for `UPSTREAM_REJECTED` are a leak risk → transformers must map them to canonical text where they name the vendor (review checklist) |
| Pass-through body names the vendor (ADR-0023) | I | An operation without a response transformer returns the vendor's status and body unchanged (`X-Hub-Pass-Through: true`). Headers are filtered: only the vendor's `Content-Type` and `Retry-After` pass, plus the Hub's own, so no header names a vendor, account or version. **The body is not filtered**: whatever the vendor writes about itself (name, links, echoed input) reaches the caller. Accepting that is the integration owner's choice when they skip the response transformer; the hub-operator skill tells operators to warn the owner, and a response transformer is the fix. Pass-through applies to sync answers only; requests completed after a 202 keep canonical `UPSTREAM_*` codes |
| Caller sends headers the transformer should not see | I/T | Only headers declared in the operation's `request_schema.headers` reach the transformer as `hdr.*`; the Hub's own headers (`Authorization`, `Idempotency-Key`, `Prefer`, `X-Request-Deadline`, `X-Hub-Session`, `traceparent`) can never be declared; hubmcp refuses them in `headers` |
| Body bombs / slowloris | D | body limit 1 MB (4 MB LLM) (DESIGN §16.5; `TestBodyLimitsAtTheEdge`: declared and chunked bodies, 413 `PAYLOAD_TOO_LARGE` before validation or any vendor call), `ReadHeaderTimeout` 5 s, read timeout and max header bytes (not set yet: HUB-249), global in-flight limit, tenant token buckets |
| Replay of side-effecting calls | T | `Idempotency-Key` mandatory for integrations with `moves_money_or_quota: true` (DESIGN §4.2), enforced at the edge with 400 `IDEMPOTENCY_KEY_REQUIRED` |
| Request smuggling | T | single HTTP parser (Go), ingress normalises; reject ambiguous `Content-Length`/`Transfer-Encoding` |
| Repudiation of calls | R | audit `request.received` with tenant key hash + key id |
| ISFC API keys are plaintext in ISFC and in its OPA bundle (planned, EP-15; G4) | I | The Hub hashes with `registry.HashKey` the moment a key is read and never stores or logs plaintext; logger redacts `x-isfc-api-key` by value; ISFC-CR-4 asks for `key_sha256` so the Hub never receives plaintext |
| ISFC key revoked but still accepted (planned; G15) | E | Revocation takes effect within `HUB_ISFC_RECONCILE_INTERVAL` (default 5 min) without ISFC events, within seconds once ISFC-CR-2 lands; "Sync now" forces it. Slower than `hk_` keys (≤ 10 s with the snapshot). Documented trade-off (ADR-0028) |

### 2.2 Admin plane (`/admin/v1`, console, hubctl, B5)
| Threat | STRIDE | Mitigation |
| --- | --- | --- |
| Unauthorised config change redirects data to an attacker host | T/E | RBAC (§7), egress allowlist is part of vendor definitions and itself an admin-only change; publish validation; every mutation in `change_log` with before/after/reason |
| Token theft from the browser | S | BFF pattern: tokens only server-side in the SvelteKit session (TECH_SPEC §17.3); today the session cookie is HMAC-signed, **not encrypted** (gap G14, HUB-282; AES-GCM planned in HUB-492); `HttpOnly`, `Secure`, `SameSite=Lax`; CSRF protection on form actions (SvelteKit origin check); the console server proxies `/admin/v1` and adds the session's token itself, forwarding only contract headers and refusing cross-origin mutations (ADR-0022); its answers carry `cache-control: no-store` unless the API set one, so admin data is not kept in the browser cache (HUB-290) |
| Over-broad unseal | I | `admin` only, mandatory reason, audited as an event on the request and in `change_log`; rate-limited; alert on unseal volume |
| CSRF/XSS in console | T | Svelte auto-escaping, no `{@html}` with API data; **a strict CSP (`default-src 'self'`) is the intended control but is not set yet (gap G12, planned in HUB-498)**; transformer YAML shown in a code viewer, never rendered as HTML |
| Credentials typed in the console (HUB-291) | I | Account credentials are write-only: password inputs, cleared from page state as soon as the form is submitted (whatever the outcome), posted only to the console's own form action (JavaScript-enhanced submissions only, so no resubmittable plain POST), passed to `createAccount` / `addNextCredential` and never returned; any error text that echoes a submitted value is replaced by `[redacted]` before it reaches the browser (`scrubSecrets`); nothing is logged. The API returns only slot metadata (version, fingerprint, dates) |
| Insider replays DLQ to a changed transformer | T | replay requires `editor`+, recorded in `change_log`, replay re-resolves through the published (validated) version only |
| Node impersonation fetching snapshots | S/I | node service credential for `/admin/v1/snapshots/*` (TECH_SPEC §22.6); snapshots contain no secrets |
| Forged `x-isfc-*` headers reach the admin listener (planned, EP-15; G5) | S/E | JWT verified by the Hub, token/header cross-check, network policy to the gateway (HUB-497); metric `hub_admin_authn_failures_total{reason}` |
| ISFC platform admin silently becomes Hub admin (planned; G7) | E | Hub `entry.rego` drops ISFC's `ISFC_ADMIN` shortcut (D6); a Hub role is needed in the platform tenant |
| Weak Microsoft id_token check in ISFC auth-service (planned; G6) | S | Microsoft SSO is built but enabled in production only after ISFC-CR-4 (D9) |
| OPA unavailable or bypassed (planned) | E/D | Fail closed (503 `authz_unavailable`); `HUB_OPA_DISABLED=true` refused unless `HUB_ENV=test`; in-process `x-required-role` check kept as a second layer |

### 2.3 Webhooks in (B2) and out (B4)
| Threat | STRIDE | Mitigation |
| --- | --- | --- |
| Forged vendor webhook | S | verify per account scheme (HMAC / basic / IP allowlist) before parsing; reject timestamps > 5 min (DESIGN §12.2); constant-time compare |
| Replay of a vendor webhook | T | timestamp window + dedup on vendor event id (24 h Redis set) |
| Webhook correlation spoofing (attach events to another tenant's request) | E | correlate only via Hub-issued request id embedded in the outbound call or `vendor_ref` recorded in audit for **that account** |
| SSRF via registrar URL | E/I | host allowlist per tenant; resolve host, refuse private, loopback, link-local, CGNAT, IPv6 ULA/link-local, metadata IPs; dial the **resolved IP** (prevents DNS rebinding); no redirects; 5 s timeout (DESIGN §12.1); HTTPS only outside local |
| Tenant can't verify Hub callbacks | S | `X-Hub-Signature: t=…,v1=HMAC-SHA256(secret, t.body)`; secret rotation with dual-sign overlap |
| Callback flood to a slow tenant | D | per-endpoint concurrency cap, own retry schedule and DLQ, independent of vendor delivery |

### 2.4 Transformers
| Threat | STRIDE | Mitigation |
| --- | --- | --- |
| Expression exfiltrates credentials into the body | I | `creds.*` only in header/query/auth targets (compile-time) unless `allow_creds_in_body: true` (reviewed flag); goldens + secret grep test |
| DoS via expensive expression | D | no loops (only `map`/`filter`), `MaxNodes`, max depth, memory budget, max output size, runtime budget (ADR-0002); compile once at snapshot build |
| Sandbox escape (network, disk) | E | expr env exposes only the stdlib in DESIGN §7.3; no reflection on arbitrary Go values; env built from plain maps |
| Malicious transformer PR from another team | T | D7: PRs gated by goldens + CODEOWNERS review by the platform team; publish validation |
| Transformer authored in the console sends a wrong or oversized vendor request | T | ADR-0023: creation needs `editor`, is recorded in the change log, and is refused unless every sample produces exactly its expected request and fits the vendor endpoint's `request_schema` (`endpoint_schema`); field reads are checked against the operation at publish. Live vendor requests are not re-checked (egress allowlist and body limits still apply) |
| Public API accounts (auth `none`) | I | No credentials are stored or sent; credentials posted for such a vendor are refused (400 at `/credentials`), credential operations answer 409. MCP still cannot create any account (`createAccount` is forbidden there) |

### 2.5 MCP server (`hubmcp`, B6)
| Threat | STRIDE | Mitigation |
| --- | --- | --- |
| Prompt injection via vendor response causes unwanted `hub_invoke` | E/T | `hub_invoke` annotated destructive so clients ask for confirmation; idempotency key on every invoke; scoped tenant key (grants limit blast radius); recommend `hub_dry_run` first; results are returned as data |
| Leakage of vendor/account/version to the LLM | I | tools built on discovery endpoints only (ADR-0007); leak test (TESTING §13) |
| PII to an LLM context | I | admin tools are metadata-only, never unseal, never return payloads; tenant tools return what the tenant already receives |
| Prompt injection or a confused model deletes or disables something | T/D | no delete path by construction (ADR-0020): no tool maps to an HTTP DELETE or a `forbidden_operations` entry (purge, replay, unseal, retire, credential ops, breaker force, pause, key revoke/rotate); the admin client's allowlist is the registered tools' `maps_to`; every hubmcp HTTP client refuses DELETE in its transport; a contract copy that maps one refuses to load; no kill-switch inputs (`disabled`, `paused`, `state`) on write tools (`TestNoToolMapsToDeleteOrForbidden`, `TestAdminClientCannotDelete`, `TestSourceHasNoDeletePath`) |
| Model changes live traffic without the user's say | T | `requires_confirm` tools (publish, weights, rollback, tenant/vendor/account/callback updates, key and signing-secret minting) refuse locally with `CONFIRMATION_REQUIRED` and make no Hub call without `confirm: true`; `expected_active_version` / `if_match` make a concurrent change fail with `CONFLICT` / `PRECONDITION_FAILED`; write tools are `readOnlyHint: false`, so MCP clients prompt (`TestWriteToolConfirmGuard`) |
| Credentials or minted secrets through the LLM | I | no tool input property is a secret (`forbidden_input_properties`); any argument matching `secret_patterns` (Hub API keys, signing secrets) is refused with `SECRET_IN_INPUT` before any Hub call, `hub_invoke` included; minted secrets (`sensitive_output`) are returned in that one result, never logged, then redacted by value from every later result and log line (`TestSecretInInputIsRefused`, `TestSensitiveOutputIsReturnedOnceAndNeverLogged`) |
| Token handling | S | stdio: key from env; HTTP: the client's `Authorization` bearer is its tenant key, checked with the Hub on first use (fingerprint cached 60 s, fail closed when the Hub is unreachable), never stored or logged; bind `127.0.0.1` by default, `-allow-remote` required otherwise; cross-origin and DNS-rebinding requests refused; TLS (terminating proxy) required when exposed; `HUB_ADMIN_TOKEN` refused on a remote-reachable listener (ADR-0018) |
| Admin token misuse | E/R | admin tools exist only when `HUB_ADMIN_TOKEN` is set, and it is the signed-in user's own token, never a shared or service token; write tools are registered on stdio only, never on Streamable HTTP (loopback included), and the token is refused on a remote-reachable listener; the Hub's RBAC decides every call (a 403 is an `isError` result), so a `viewer` token can only read; every mutation is in the change log under the user's subject with the reason plus " (via hubmcp)" (ADR-0020, `TestToolRegistrationByTransport`, `TestMCPEndToEnd`) |

## 3. Secrets handling
- **At rest**: envelope encryption, per-account data key wrapped by KMS/Vault transit (DESIGN §9). DB stores ciphertext + wrapped key only. Registrar and webhook secrets use the same mechanism (`secret_ref`).
- **In memory**: decrypted cache only (TTL 5 min, warmed on snapshot load); no decrypt on the request path; values registered with the redactor (TECH_SPEC §9).
- **In logs/traces/metrics**: never. Redaction by value plus key-name denylist (OBSERVABILITY §4.3). The process redaction set holds the decrypted credentials and registrar signing secrets (credential cache) and, since HUB-234, the Postgres DSN password and `HUB_AUDIT_MASK_KEY` (`roles.Run`, for every entry point). Span attributes come from an allowlist (`telemetry.AllowedSpanAttributes`, OBSERVABILITY §5.2) enforced by tests; span statuses carry error classes, never error text. Audit `request.transformed` stores redacted headers. Redaction (logs and audit payloads, dead letters included) matches each secret in its JSON-escaped, URL-escaped and base64 forms as well as raw (HUB-448): a credential such as `pa&ss<w>"rd\1` is masked inside a JSON body or a percent-encoded query.
- **Trace ids to third parties**: only vendors listed in `HUB_TRACE_FORWARD_VENDORS` receive the Hub's `traceparent`; `tracestate` is never sent (ADR-0013).
- **In Git**: never. `backend/config/` holds references; dev secrets in `dev-secrets.enc` encrypted with a git-ignored dev key. Secret scanning (gitleaks) in CI.
- **In fixtures**: fixed fake values clearly marked (`test-passcode-not-real`).
- **Rotation**: accounts `active`/`next` (DESIGN §6.6); tenant keys overlap; registrar secrets dual-sign window; KMS key rotation by the provider.
- **Deletion**: credentials deleted only at account `retired` (DESIGN §6.5).

## 4. PII policies (DESIGN §13.2)
| Policy | Stored | Readable by | Default for |
| --- | --- | --- | --- |
| `full` | raw bytes at every stage | `viewer`+ for platform, `tenant_viewer` for own tenant | engagement, messaging (only when no `x-pii` fields require masking) |
| `masked` | `x-pii` fields masked/hashed (e.g. `maskPAN`, `maskAadhaar`, `sha256` for identifiers), rest raw | same as full | default |
| `sealed` | raw bytes, field-level encrypted with a per-tenant key | only through audited **unseal** (`admin`) | bureau, Aadhaar, PAN, AA (D6); 90 d |
| `metadata_only` | no bodies; sha256 + size only | `viewer`+ | LLM prompts (DESIGN §15) |

Always: SHA-256 + byte size of every payload, so losslessness is verifiable (DESIGN §13.3). Aadhaar numbers are never stored, even sealed, in vendor sessions (DESIGN §14). `x-pii` tags live in each operation's `request_schema` (ADR-0023; they used to come from the capability schema); a `full` policy on an integration whose operation schemas tag regulated data (government ids, financial) should be refused at publish (rule kept from DESIGN §13.2).

### 4.1 Subject erasure (DESIGN §16.5, HUB-233, ADR-0012)
`hub erase-subject -tenant T -actor WHO -reason TEXT [-since RFC3339] [-until RFC3339] [-dry-run] < identifier` purges every stored body of the tenant's requests that hold a data subject's identifier, and keeps hashes, sizes, timelines and metadata.

| Aspect | Rule |
| --- | --- |
| Input | The identifier is read from **stdin only**, never from flags (process listings, shell history). It is registered with the redacting logger, never logged and never stored. Requires `HUB_PG_DSN`, `HUB_AUDIT_MASK_KEY` (the gateway's key) and, if the tenant has sealed payloads, the seal key file (`-dev-key-file`). Without it the job fails (`ErrNoSealKey`) rather than report a partial search as done |
| Forms searched | raw value and its JSON-escaped forms (full payloads, sealed plaintext, outbox/DLQ envelopes, kept results, callback payloads), the per-tenant HMAC used by `masked` for `identifier`/`ip_address`/`device_id`, and HMAC(local part)@domain for `email`. Kinds masked to the last four characters or `***` keep nothing identifying and are not searched |
| Where | `request_events.payload` (clear payloads in SQL, sealed ones opened one by one), `request_results.result/error`, `outbox.envelope`, `dead_letters.envelope/last_vendor_request/last_vendor_response`, `callback_deliveries.payload`, `vendor_sessions.data/vendor_txn_id`, scoped to the tenant (and to the window by request creation time) |
| Purge | For every matched request: event payloads → NULL, `payload_state = omitted`, `meta.pii_erased = <change_id>`; kept result/error → NULL; dead-letter canonical → null and last vendor exchange dropped (replay refused); delivered/dead-lettered callback payloads emptied; matching vendor sessions deleted. Event rows, sha256, bytes, types, times, `requests`, `request_attempts`, dead-letter and callback metadata remain |
| In flight | Requests still in the outbox or with callbacks to deliver are **deferred** (listed, exit code 3) and erased by a re-run once they are terminal |
| Audit | One `change_log` row per run (`eraseSubjectPayloads`, `object_type = request`, reason ≥ 10 characters) with tenant, counts, request ids and `subject_ref` (an HMAC with its own domain label, never equal to a stored form). A dry run writes nothing |
| Not covered | Idempotency replay records in Redis (24 h TTL, hashed keys; releasing them could re-run money-moving calls; HUB-246). Identifiers used as `Idempotency-Key` or ordering keys: callers must not put PII in keys. Object-storage spill (HUB-098, when it lands). Lookup is a scan of the tenant's rows; a hashed subject index is HUB-245. An admin API operation with RBAC is requested in HUB-247 (CCR) |

## 5. Egress allowlist and TLS
- Allowed hosts = the union of hosts in vendor definitions (`regions`, endpoint hosts) of the **current snapshot** plus registrar hosts. The upstream dialer refuses any other host (checked after DNS resolution, same IP-range rules as SSRF for non-vendor targets).
- Network layer: Kubernetes NetworkPolicy / egress gateway mirrors the allowlist (defence in depth); changes go through the deploy repo.
- TLS 1.2+ with verification; no `InsecureSkipVerify` (lint rule); optional certificate pinning per account for regulated partners; per-account mTLS client certs (DESIGN §9). The floor is explicit (`MinVersion: tls.VersionTLS12`) in both egress clients, `upstream.Client` (vendors) and `callbacks.Guard` (tenant callbacks; its test seam can add roots but never lower the floor), rather than relying on Go's default client floor.
- Tests (HUB-234): `TestTLS` (upstream) runs the real client against vendors that speak only TLS 1.0 or 1.1 and expects a tls-phase `ErrConnect` with no request served, with a positive control proving those vendors complete the old handshake with a permissive client; TLS 1.2-only and 1.3 vendors succeed. `TestTLSFloorIsExplicit` pins the configured floor, and `TestGuardRefusesTLSBelow12` covers callbacks. Note for test authors: since Go 1.22 a Go **server** defaults to a TLS 1.2 floor, so a test server with only `MaxVersion: TLS11` speaks no version at all and proves nothing; set `MinVersion` explicitly.
- Egress tests: `TestUndeclaredHostRefusedBeforeDial` (undeclared host, port, scheme, unparsable URL and plain http to a remote host are refused before the dialer, counted in `upstream_egress_denied_total`) and `TestDialerChecksEgress` (the dial functions refuse undeclared addresses and dials without an allowlist).
- Regional accounts enforce data residency: an integration version cannot target an account outside the tenant's allowed regions (publish check).

## 6. Tenant key lifecycle
```mermaid
stateDiagram-v2
  [*] --> active: create (shown once)
  active --> expiring: rotate (successor minted, overlap_ms)
  expiring --> revoked: overlap elapsed (expires_at reached)
  active --> revoked: revoke (immediate)
  revoked --> [*]
```
- Format: `hk_live_…` / `hk_test_…` + 32 random bytes base62 (contract `bearerAuth`); the prefix lets secret scanners detect leaks; the admin API shows only the first 12 characters (`prefix`) and `key_id` (`key_` + ULID), both safe to log.
- Stored: `key_hash = SHA-256(key)`, `key_id`, `tenant_id`, `scopes[]` (`invoke`, `dry_run`, `requests_read`, each optionally `<scope>:<integration_id>`, DESIGN §16.5; `dry_run` never granted by default), `created_at`, `expires_at`, `revoked_at`, `last_used_at` (updated asynchronously, sampled).
- Gateway loads hashes from the snapshot (in-memory hash set, DESIGN §5.1); revocation propagates with the snapshot (≤ 10 s).
- Rotation overlap default 7 days; alerts on keys unused for 90 days.

## 7. RBAC matrix (`/admin/v1`, DESIGN §16.1)
This matrix is **derived from the contract**: the minimum role of each operation is its `x-required-role` in `contracts/openapi/hub-admin.v1.yaml`, and `TestRBACMatrix` (`backend/internal/control/adminapi/server_test.go`) proves every route against every role (and 401 for no, expired, foreign and garbage tokens), while `TestRoutesMatchContract` checks that route, method and role equal the contract. Ranks: `admin` ⊃ `editor` ⊃ `viewer` ⊃ `tenant_viewer`; a principal holds its highest role. 74 operations: 17 `tenant_viewer`, 19 `viewer`, 15 `editor`, 23 `admin`.

Legend: **R** read · **W** create/update · **X** execute action · — denied. `tenant_viewer` sees only objects owned by (or granted to) their tenant (404 otherwise) and never vendor credentials.

| Resource / operations | admin | editor | viewer | tenant_viewer |
| --- | --- | --- | --- | --- |
| `getMe` | R | R | R | R |
| vendors and vendor endpoints: list, get | R | R | R | — |
| vendors and endpoints: create, update (`createVendor`, `updateVendor`, `createVendorEndpoint`) | W | — | — | — |
| accounts: list, get (metadata) | R | R | R | — |
| accounts: create, update, credential `next` / `promote` / `delete-old`, lifecycle | W / X | — | — | — |
| accounts: `testAccountConnection` | X | X | — | — |
| transformers and versions: list, get | R | R | R | — |
| transformers: `createTransformerVersion`, `dryRunTransformer`, `runTransformerGoldens` | X | X | — | — |
| integrations, versions, diff: list, get | R | R | R | R (own) |
| integrations: create, update, drafts, validate, publish, rollback, pause / resume, weights | W / X | W / X | — | — |
| tenants: `listTenants` | R | R | R | — |
| tenants: `getTenant` | R | R | R | R (own) |
| tenants: `createTenant`, `updateTenant` | W | — | — | — |
| api-keys: list, get (metadata) | R | R | R | R (own) |
| api-keys: create, rotate, revoke | X | — | — | — |
| registrar-endpoints: list, get | R | R | R | R (own) |
| registrar-endpoints: create, update, delete, rotate-secret | W / X | — | — | — |
| snapshots: list, latest, get, blob | R | R | R | — |
| requests: search, get, timeline, attempts (policy-filtered) | R | R | R | R (own) |
| requests: `unsealRequest` | X (reason required) | — | — | — |
| dlq: list, get | R | R | R | R (own) |
| dlq: `replayDeadLetters` | X | X | — | — |
| dlq: `purgeDeadLetters` | X | — | — | — |
| breakers: list, get | R | R | R | — |
| breakers: force-open / force-close / clear-force | X | — | — | — |
| change log: list, get | R | R | R | — |

Corrections to the matrix that stood here before 2026-11-11 (gap G12, HUB-468): editors cannot create vendors, accounts, tenants, API keys or registrar endpoints, and cannot run credential or lifecycle actions (those are `admin`); `tenant_viewer` cannot read the change log or list tenants; transformer dry-run and run-samples need `editor`, not `viewer`; the old "api-keys: non-own tenants require admin" rule does not exist because creating keys is `admin` only.

Every W/X writes `change_log` (actor, action, object, before, after, reason). Irreversible actions require a reason (frontend/AGENTS.md). The admin API is the enforcement point; the console only hides controls.

### 7.1 Planned (EP-15): ISFC roles and per-operation policies
Not built yet ([ADR-0026](adr/0026-isfc-identity-and-per-operation-admin-policies.md)). With `HUB_ADMIN_AUTH_MODE=isfc` the matrix above must not change; it gains a second layer (OPA) and a new role.

| ISFC role | Where it counts | Hub role | Scope |
| --- | --- | --- | --- |
| `HUB_ADMIN`, `HUB_EDITOR`, `HUB_VIEWER` | platform tenant | `admin`, `editor`, `viewer` | all tenants (unscoped) |
| `HUB_TENANT_VIEWER` | a linked tenant | `tenant_viewer` | that tenant only |
| `HUB_TENANT_ADMIN` (new, D4) | a linked tenant | `tenant_admin`: tenant viewer plus finishing setup and managing callback endpoints of its own tenant | that tenant only |
| `ISFC_ADMIN` | anywhere | none (D6) | — |

Each operation gets its own Rego policy (generated from the contract, tested by a generated matrix); permissions are per resource (`HUB_VENDOR:READ`, ...) and roles bundle them to reproduce the table above, with `x-required-role` the source of truth. Where AUTH_PLAN §7.4 groups a resource more loosely than the contract (for example tenants, API keys and registrar endpoints are `admin` writes today), the contract wins. Machine principals (ISFC API keys on the admin API) get the highest role their permissions fully cover and can never call `unsealRequest` or `purgeDeadLetters` (D7).

## 8. Supply chain
- **Go**: `go.mod`/`go.sum` committed; minimal dependency set (TECH_SPEC §13); `govulncheck` and `gosec` in CI (blocking, §8.1); Renovate/Dependabot weekly with grouped minor updates; `GOFLAGS=-mod=readonly`; the toolchain is pinned in `go.mod` (`toolchain go1.26.10` since HUB-234: go1.26.0 had 36 stdlib vulnerabilities reachable from Hub code, among them crypto/x509, net/url and html/template).
- **Node**: `package-lock.json` committed, `npm ci` only, exact versions for generators (`openapi-typescript`, `openapi-fetch`); `npm audit --audit-level=high` blocking; `ignore-scripts=true` in `.npmrc` except allowlisted packages.
- **Images**: base images pinned by digest; distroless/non-root; SBOM (syft) and provenance (SLSA, `actions/attest-build-provenance`) on release; image scan (grype/trivy) blocking on critical.
- **CI**: GitHub Actions pinned by commit SHA; least-privilege `permissions:`; no secrets on PRs from forks.
- **Vendored skills** (`.claude/skills/`, `backend/.claude/skills/`, `frontend/.claude/skills/`): third-party skills are vendored with their `LICENSE.txt`, listed in `THIRD_PARTY_SKILLS.md` with source URL and commit, reviewed before adding (scripts read in full), and never added from an unknown source without the user's approval (AGENTS §12). Skill scripts are not executed in CI.
- **Secret scanning**: gitleaks on every PR, plus the `hub_` key prefix rule.
- **Contracts**: only the steward edits `contracts/`; CODEOWNERS enforces review.

### 8.1 Static analysis and vulnerability gates (HUB-234)
| Gate | Command (backend/) | Tool version | Fails on |
| --- | --- | --- | --- |
| CI job `govulncheck` | `make vuln` | `golang.org/x/vuln` v1.9.0 | any known vulnerability in a called symbol of the stdlib or a module (imported-but-not-called findings are informational) |
| CI job `gosec` | `make gosec` | `github.com/securego/gosec/v2` v2.30.0, `-severity medium` | any medium- or high-severity finding in non-test code that is not triaged (gosec does not scan `_test.go`) |
| CI job `lint` | `golangci-lint run` | v2.15.0 (bundled gosec) | as before; honours the same `#nosec` annotations |

Both tools run with `go run <module>@<version>` (pinned in the Makefile; bump deliberately). Locally: `make security`.

**Triage policy.** Fix real findings. A false positive gets a narrowly scoped annotation on the flagged line: `// #nosec <rule> -- <reason>` (one rule ID, a reason a reviewer can check). Both gosec and golangci-lint honour it; `//nolint:gosec` is not used because standalone gosec ignores it. No gosec config-level exclusions exist. The annotations in the tree at HUB-234 (all false positives, reviewed):

| Rule | Where | Why it is safe |
| --- | --- | --- |
| G115 (integer conversion) | `audit/store.go`, `audit/ledger.go` (`int64(SnapshotVersion)`), `control/guard.go` (`int32` version), `audit/seal.go` (`byte(len(id))`), `registry/watch.go`, `core/ids.go`, `core/state.go`, `resolve/resolve.go`, `testkit/mockvendor/server.go` | values are bounded (versions far below 2^31, id length checked to 1..255, 48-bit timestamps, modulo 100) or only hashed/compared for equality |
| G304 (file path from variable) | `creds/localfile.go` (`HUB_DEV_KEY_FILE`), `control/snapshots.go` (path confined to the blob root), `cmd/hubctl/ops.go` (file named by the operator), `cmd/hubload/report.go` (the operator's run directory), `cmd/hubload/sys.go` (`/proc` entries from a fixed glob) | operator configuration or confined path, never request input |
| G301 / G306 (dir 0755 / file 0644) | `cmd/hubctl/config.go` | writes registry config pulled from a snapshot: readable by design, no secrets (snapshots contain none) |
| G705 (XSS via `Write`) | `control/adminapi/snapshots.go` (`getSnapshot`) | archived registry JSON served with `Content-Type: application/json`, never HTML; `nosniff` hardening tracked in HUB-248 |
| G107 / G704 (request to variable URL) | `testkit/contract/contract.go` | test helper calling the test's own server |
| G704 (SSRF via taint analysis) | `cmd/hubmcp/adminclient.go` (`adminClient.do`, two lines; HUB-315) | the host is `HUB_ADMIN_BASE_URL` (operator configuration, validated at start); tool arguments only fill `url.PathEscape`d path segments (dot segments refused) of an allowlisted route and query values; the client is method-guarded (no DELETE) |
| G404 (math/rand) | `delivery/backoff.go`, `testkit/mockvendor/server.go`, `cmd/hubload/soak.go` (seeded chaos schedule) | retry jitter and a deterministic test double, not security |
| G101 (hardcoded credentials) | `cmd/hubctl/creds.go`, `cmd/hubmcp/httpserve.go` (`tokenExtra*`) | usage text that mentions `signing_secret=`; context map key names; no credential |
| G204 (subprocess with variable input) | `cmd/hubload/soak.go` | the soak's restart hook is a path the operator passes to the load-test tool; never part of the Hub binaries |

## 9. Security testing
SSRF, signature, redaction and secret-grep tests (TESTING_STRATEGY §2, §3, §5); the role-log secret grep (HUB-234, below); fuzzing of parsers (§8); RBAC table test generated from §7 (every role × action → expected 2xx/403); dependency and image scans (§8); a pre-production review of the threat model before the first real vendor account is created.

**Role-log secret grep (HUB-234).** In `cmd/hub/internal/roles`, every role a test starts (`startRoleWith`) logs through the production logger (`telemetry.NewLogger`: JSON, value redaction, debug level) into one capture shared by the package. Tests declare every secret they hand to the roles with `forbid` (vendor credentials, registrar signing secrets, the dev master key in base64/hex forms, the audit mask key, tenant API keys including unknown ones a caller presented, admin access tokens). `TestMain` fails the package if any declared value appears in the captured logs of the whole run, printing labels, never values. `TestRoleLogsCarryNoSecrets` runs gateway, worker, scheduler and admin together on the encrypted localfile credential source and drives every secret-handling path: a sync call (positive control: the vendor receives the passcode), a vendor that echoes the credentials in 400/401 error bodies (gateway and worker), async delivery with a signed callback (positive control: `X-Hub-Signature` received), an admin connection test, a rotation (add next, test, promote) followed by a call that uses the rotated value, an unknown account, and an unknown API key. It also asserts lines from every role, a request-path line and the rotation lines reached the capture. Mutation check: logging a vendor error body is redacted (`***`); with credential registration disabled the test fails.

Scope note: per-package integration tests outside `roles` keep their own log captures and greps (`TestE2EErrorClasses` in `edge` greps its gateway logs and the stored audit events); the Postgres password of the local test DSN (`hub`) is too short and common to grep and is covered by `TestRunRegistersConfigSecrets` instead.
