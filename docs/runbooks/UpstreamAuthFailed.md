# UpstreamAuthFailed

**Severity:** page · **Rule group:** `hub-errors` · **Labels:** `vendor`, `account`

## What fired
At least one request to this vendor account ended in `UPSTREAM_AUTH_FAILED` in the last 5 minutes (vendor answered 401 or 403). It reads `hub_requests_total{code="UPSTREAM_AUTH_FAILED"}` (`increase(...[5m]) > 0`, plus a second branch that catches the first failure of a new label set). No `for`: one failure pages. Auth failures are never retried, so every one is counted.

## Impact
- **sync** and **wait** callers get 502 `UPSTREAM_AUTH_FAILED` (not retryable).
- **async** work is dead-lettered at once with reason `UPSTREAM_AUTH_FAILED`; callbacks report the failure. It can be replayed after the fix.
- Every integration whose target is this account is affected. Auth failures do not open the breaker, so traffic keeps failing until fixed.

## First 5 minutes
1. **Vendor and account** dashboard (`hub-vendor`), set `vendor`, `account`: **Finished requests by code**, **Credential expiry (days)**, **Credential cache misses**.
2. All requests or some? `sum by (code) (rate(hub_requests_total{vendor="<vendor>", account="<account>"}[5m]))`. All failing: the credential is bad. A few: one node has an old credential, or one endpoint needs a scope the key lacks.
3. Recent rotation? `listChangeLog`: `/admin/v1/change-log?object_type=account&object_id=<account>&limit=20` (look for `promoteNextCredential`, `addNextCredential`, `updateAccount`). Then `getAccount` (`/admin/v1/accounts/<account>`): credential slots, `cred_version`, `expires_at`.
4. Test the credential without traffic: `testAccountConnection` with `{"slot": "active"}` (and `{"slot": "next"}` if one is staged):
   `curl -sS -X POST -H "Authorization: Bearer $HUB_ADMIN_TOKEN" -H "Content-Type: application/json" -d '{"slot":"active"}' "$HUB_ADMIN_URL/admin/v1/accounts/<account>/test-connection"`.
5. Logs: `auth scheme failed`, `credentials unavailable`, `credential reload after rotation failed`, `credentials reloaded after rotation` (with `account`).

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| Credential expired or revoked at the vendor | `testAccountConnection` active fails; `credential_expiry_days` negative, or the vendor confirms a revoke | Rotate now: [credential rotation](credential-rotation.md). |
| Bad promotion | Failures started at a `promoteNextCredential` | There is no operation to re-promote `previous`. If the old secret is still valid at the vendor: `addNextCredential` with it, `testAccountConnection` (`slot: next`), `promoteNextCredential`. Otherwise get a new secret and rotate. |
| One node on an old credential | Failures from one instance only; `credential reload after rotation failed` on it | Nodes reload within the 5 minute cache refresh; restart that node if it does not. |
| Account config wrong | Credential fine but the account's non-secret config (project, region) changed | Check `listChangeLog` for `updateAccount`; fix it with `updateAccount`. |
| A new integration version sends credentials wrongly | Started at a `publishIntegrationVersion`; `hubctl dry-run <integration> body.json` shows a missing or wrong auth header (values print as `***`) | `rollbackIntegration` to the last good version; fix the transformer. |

After the fix, replay what was dead-lettered:
`hubctl replay -account <account> -error-code UPSTREAM_AUTH_FAILED -reason "<fix>" -dry-run`, then the same with `-yes`.

## Escalate when
- `testAccountConnection` fails with a credential the vendor says is valid.
- You cannot get a new secret from the vendor quickly (vendor account owner, out of hours).
- Several accounts of different vendors fail at once (a Hub-side bug in auth schemes or credential loading).

## Related
- [Credential rotation](credential-rotation.md), [CredentialExpiringSoon](CredentialExpiringSoon.md), [DLQSurge](DLQSurge.md), [runbook index](README.md)
- DESIGN §6.6 (rotation), §9 (credentials), §10.2 (retry policy)
- [F-07 Credentials and auth schemes](../features/F-07-credentials-auth-schemes.md), [F-15 Account lifecycle and rotation](../features/F-15-account-lifecycle-rotation.md), [OBSERVABILITY §9.4](../tech/OBSERVABILITY.md)
