# HubAdminAuthzUnavailable

**Severity:** warn · **Rule group:** `hub-isfc` · **Labels:** none

## What fired
More than 1% of admin authorization decisions had `decision="error"` for 5 minutes (`hub_admin_authz_duration_ms_count`). In `HUB_ADMIN_AUTH_MODE=isfc` every `/admin/v1` request asks the OPA sidecar; an error or a timeout (`HUB_OPA_TIMEOUT`, default 200 ms) fails closed.

## Impact
- Admin API and console requests fail with 503 `authz_unavailable`. Nobody can change configuration; reads fail too.
- The `/v1` data plane, the worker and the scheduler do not use OPA and keep running. Automation keys are affected like users, because OPA decides for them too.
- The metric exists only on admin processes in ISFC mode; other deployments never fire this alert.

## First 5 minutes
1. `kubectl logs <admin pod> -c opa --tail=100` and the admin's `admin.authz` warn lines (reason, no token).
2. `curl localhost:8181/health?bundles` through a port-forward: is the `integration-hub` bundle active?
3. Compare the `decision` split on the Platform dashboard's admin panels with `hub_admin_authz_duration_ms` p99.

## Likely causes → fix
| Cause | How you know | Fix |
| --- | --- | --- |
| OPA container crashed or not ready | pod shows 1/2 ready, `opa` log errors | Restart the container; check resource limits. |
| Bundle missing, invalid or unreachable | `health?bundles` not ok | ISFC platform team: bundle server and `authz-policies/integration-hub/`. |
| OPA too slow | p99 near `HUB_OPA_TIMEOUT` | Resource limits, bundle size; raise `HUB_OPA_TIMEOUT` only with the ISFC team's agreement. |

## Escalate when
- The bundle cannot be restored: the ISFC platform team owns it. There is no Hub-side bypass (`HUB_OPA_DISABLED` is refused outside tests).

## Related
- [ISFC authentication runbook](isfc-auth.md), [runbook index](README.md), [deploy/isfc](../../deploy/isfc/README.md)
- [AUTH_PLAN §7.1, §11](../tech/AUTH_PLAN.md), [ADR-0026](../tech/adr/0026-isfc-identity-and-per-operation-admin-policies.md)
