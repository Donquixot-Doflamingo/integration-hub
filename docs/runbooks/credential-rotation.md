# Credential rotation

Rotate a vendor account's credential without a version change and without dropping traffic (DESIGN §6.6). Accounts hold an `active` and a `next` slot. In-flight calls keep what they already injected; every later attempt, including a parked drain, uses `active` at send time.

Order: **add `next` → test → promote → watch 15 minutes → delete old.** Every step is an admin API call (role `admin`, `testAccountConnection` needs `editor`), recorded in the change log with your `reason`. See [the index](README.md#before-you-start) for the token and URL.

## Before you start
- Get the new secret from the vendor. If the vendor can keep two keys live, keep the old one valid until step 6. If issuing a new key kills the old one at once, plan a short window: requests fail with `UPSTREAM_AUTH_FAILED` between the vendor change and step 4.
- Use the same credential keys as the current credential (the vendor's auth scheme decides them, for example `passcode`). Every value must be at least 6 bytes: the log redactor ignores shorter values, so they would not be masked.
- Put the request body in a file readable only by you, not on the command line (shell history), and delete it afterwards.

## Steps
1. **Look at the slots.** `getAccount`:
   `curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/accounts/<account>"`.
   Note the `active` `cred_version`. An existing un-promoted `next` will be replaced.
2. **Add `next`.** `addNextCredential` with a body file `next.json` (mode 0600):
   ```json
   { "credentials": { "<key>": "<new secret>" }, "expires_at": "2027-11-01T00:00:00Z", "reason": "Quarterly rotation" }
   ```
   `curl -sS -X POST -H "Authorization: Bearer $HUB_ADMIN_TOKEN" -H "Content-Type: application/json" -d @next.json "$HUB_ADMIN_URL/admin/v1/accounts/<account>/credentials/next"`.
   Always set `expires_at`: without it [CredentialExpiringSoon](CredentialExpiringSoon.md) cannot warn next time. Note the new `next` `cred_version`.
3. **Test `next`.** `testAccountConnection` with `{"slot": "next"}` (add `"endpoint_ref": "<name>@<n>"` to test a specific endpoint; the default is the vendor's health-probe endpoint):
   `curl -sS -X POST -H "Authorization: Bearer $HUB_ADMIN_TOKEN" -H "Content-Type: application/json" -d '{"slot":"next"}' "$HUB_ADMIN_URL/admin/v1/accounts/<account>/test-connection"`.
   Continue only on `"ok": true`. On `false`, read `error_code`, `upstream_status` and `message`, fix the secret and repeat step 2.
4. **Promote.** `promoteNextCredential` with `{"expected_next_version": <n>, "reason": "Connection test passed"}` to `/admin/v1/accounts/<account>/credentials/promote`. The old `active` moves to `previous` (kept, not used). Nodes reload on a Redis ping (log `credentials reloaded after rotation`); if the ping fails (`credential ping failed: nodes reload on their next refresh`), they pick it up within the 5 minute cache refresh.
5. **Watch 15 minutes.** `sum by (vendor, account) (increase(hub_requests_total{account="<account>", code="UPSTREAM_AUTH_FAILED"}[15m]))` must stay 0, and [UpstreamAuthFailed](UpstreamAuthFailed.md) must not fire. **Vendor and account** dashboard: **Finished requests by code**, **Credential expiry (days)** (now the new date for `active`).
6. **Delete old.** `deleteOldCredential` with `{"expected_previous_version": <old>, "reason": "No UPSTREAM_AUTH_FAILED for 15 min after promotion"}` to `/admin/v1/accounts/<account>/credentials/delete-old`. Then revoke the old key at the vendor.

## If it goes wrong
- **Auth failures after step 4:** there is no operation to re-promote `previous`. If the old secret is still valid at the vendor, run steps 2 to 4 with the old value. Otherwise fix the new secret with the vendor and run steps 2 to 4 again. Replay what was dead-lettered once it works: `hubctl replay -account <account> -error-code UPSTREAM_AUTH_FAILED -reason "<fix>" -dry-run`, then `-yes`.
- **409 `CONFLICT` on promote or delete:** the expected version does not match (someone else rotated) or the slot is empty. Re-read the slots with `getAccount`.
- **500 on rotation endpoints:** the admin has no credential store (log `no credential store: rotation endpoints answer 500 until hubctl creds init-key`). Escalate.

## Local development only
The phase-1 credential store is an encrypted local file (`HUB_CRED_SOURCE`, default `localfile://.data/dev-secrets.enc`). `hubctl creds list` prints accounts, slots, versions and key names (never values); `hubctl creds set [-slot active|next] ACCOUNT KEY=VALUE...` edits the file directly, with no change log, no connection test and no expiry. Use the admin API steps above for any shared environment.

## Related
- [CredentialExpiringSoon](CredentialExpiringSoon.md), [UpstreamAuthFailed](UpstreamAuthFailed.md), [runbook index](README.md)
- DESIGN §6.6, §9; [TECH_SPEC §9](../tech/TECH_SPEC.md)
- [F-15 Account lifecycle and rotation](../features/F-15-account-lifecycle-rotation.md), [F-07 Credentials and auth schemes](../features/F-07-credentials-auth-schemes.md)
