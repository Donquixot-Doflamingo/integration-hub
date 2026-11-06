# CredentialExpiringSoon

**Severity:** warn · **Rule group:** `hub-platform` · **Labels:** `account`, `slot` (`active` or `next`)

## What fired
`min by (account, slot) (credential_expiry_days) < 7` for 1 hour: a credential slot of this account expires in less than 7 days. The value is computed at scrape time from the slot's `expires_at` and is negative once expired. Only slots stored with an `expires_at` have a series: a credential added without one never warns.

The expiry is the date entered when the credential was added (`addNextCredential`), not a value read from the vendor.

## Impact
- None yet. After the expiry the vendor rejects the credential: sync and wait callers get 502 `UPSTREAM_AUTH_FAILED`, async work is dead-lettered, and [UpstreamAuthFailed](UpstreamAuthFailed.md) pages.
- `slot="next"`: a staged, not yet promoted credential is expiring. Promoting it would only buy days.

## First 5 minutes
1. **Vendor and account** dashboard (`hub-vendor`), set `account`: **Credential expiry (days)** per slot.
2. Exact numbers: `min by (account, slot) (credential_expiry_days{account="<account>"})`.
3. Slots and versions: `getAccount` (`/admin/v1/accounts/<account>`): `credentials[]` with `slot`, `cred_version`, `fingerprint`, `expires_at`. Values are never returned.
4. Who added it, and when: `listChangeLog?object_type=account&object_id=<account>`.
5. Confirm the real expiry with the vendor (console or account manager): the stored date may be wrong.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| `active` is due for rotation | `slot="active"`, vendor confirms the date | Run the [credential rotation](credential-rotation.md) procedure now; set `expires_at` on the new credential. |
| Staged `next` is about to expire | `slot="next"` | Get a fresh secret and `addNextCredential` again (it replaces an un-promoted `next`), then continue the [rotation](credential-rotation.md). |
| Stored date is wrong, vendor key lives longer | Vendor shows a later expiry | There is no operation to edit `expires_at` alone. Re-add the same secret as `next` with the correct `expires_at`, test, promote (the rotation procedure with the same value). |
| Already expired (value < 0) | Negative value; auth failures likely | Treat as [UpstreamAuthFailed](UpstreamAuthFailed.md): rotate immediately. |

## Escalate when
- Less than 2 days left and no new secret from the vendor.
- The vendor cannot issue a second key while the first is live (rotation will cause a short outage): plan a window with the integration owners.

## Related
- [Credential rotation](credential-rotation.md), [UpstreamAuthFailed](UpstreamAuthFailed.md), [runbook index](README.md)
- DESIGN §6.6 (rotation), §9 (credentials)
- [F-15 Account lifecycle and rotation](../features/F-15-account-lifecycle-rotation.md), [F-07 Credentials and auth schemes](../features/F-07-credentials-auth-schemes.md)
