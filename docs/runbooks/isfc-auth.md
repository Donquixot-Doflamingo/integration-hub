# Runbook: ISFC authentication and authorization

Covers the Hub's admin plane in `HUB_ADMIN_AUTH_MODE=isfc` and the ISFC data it depends on (AUTH_PLAN §7, §8, ADR-0026 to 0028). Manifests: [`deploy/isfc/`](../../deploy/isfc/README.md). Local stack: [isfc-local.md](isfc-local.md). Written from the design and code; none of it has been run against a production ISFC yet.

Always true, so do not look for a bypass: authorization fails closed, `HUB_OPA_DISABLED=true` is refused unless `HUB_ENV=test`, and the `/v1` data plane never calls OPA or ISFC on the request path (it checks ISFC keys in memory).

## OPA is down (alert `HubAdminAuthzUnavailable`)
**Symptom:** admin and console requests answer 503 `authz_unavailable`; the console shows "access unavailable"; `hub_admin_authz_duration_ms_count{decision="error"}` climbs; the admin logs `admin.authz` at warn. Callers on `/v1` are unaffected.
1. Is the sidecar up? `kubectl -n <ns> get pod -l app.kubernetes.io/name=hub-admin` (both containers ready?), `kubectl logs <pod> -c opa --tail=100`.
2. Does it have its bundle? `kubectl port-forward <pod> 8181` then `curl -s localhost:8181/health?bundles`. Not ready usually means the ISFC bundle server is unreachable or the bundle for platform `integration-hub` does not exist or is invalid: ask the ISFC platform team (bundle pipeline, `authz-policies/integration-hub/`).
3. Is it slow, not down? Compare `hub_admin_authz_duration_ms` p99 with `HUB_OPA_TIMEOUT` (default 200 ms). Timeouts count as errors.
4. Wrong client id? A deny on every request (not 503) after a deploy means `HUB_OPA_CLIENT_ID` does not match the ISFC client's Rego package (id without `-`).
**Fix:** restart the OPA container or the pod; restore the bundle. Recovery is automatic on the next request. There is no Hub-side override: for an emergency change use ISFC's own tooling or escalate to the ISFC platform team.

## ISFC sync is stale (alert `HubIsfcSyncStale`)
Procedure and causes: [HubIsfcSyncStale.md](HubIsfcSyncStale.md). In short: find the leader worker (`hub_isfc_sync_lag_seconds > 0`), read `hub.isfc_sync_runs` and the `isfc.sync` logs, fix the cause (auth-service reachability, the Hub backend client's secret and S2S permissions), then run "Sync now" (`syncTenants`). The Hub keeps its last good tenants and caller keys meanwhile, so nothing is disabled by a failed fetch, but a key revoked in ISFC keeps working until a run succeeds.

## Key revocation
Who: an ISFC admin (or the tenant admin once ISFC-CR-1 lands) revokes the key in ISFC. The Hub never stores or shows the key itself, only a hash.
- **Caller key (`/v1`, `x-isfc-api-key`):** takes effect at the edge at the next successful sync: within `HUB_ISFC_RECONCILE_INTERVAL` (default 5 min), or seconds once ISFC events flow. To make it immediate, run "Sync now" (`syncTenants`) right after the revocation. Verify: the caller's next call answers 401 `UNAUTHORIZED`. If the sync is stale (above) and the key must stop now, disable that caller's tenant with `updateTenant` (reversible; the reason lands in the change log).
- **Automation key (admin API, `integration-hub-ci` and similar):** OPA checks it against the bundle's `allowed_api_keys`, so it stops when ISFC rebuilds the bundle and OPA activates it (watch `curl localhost:8181/health?bundles` and try the key; the delay is ISFC's bundle interval and not measured here). The key owner fetches the new key from auth-service on the next 401 or 403 (hubctl and the SDK do this once per run).
- **A leaked key:** revoke first, then check the change log (`listChangeLog`, actor `client:<id>`) and `request.received` audit events (`isfc_key_id`, `caller_client_id`) for what it did. Machines cannot unseal audit payloads or purge the DLQ.
- **Rotation, not leak:** issue the new key before revoking the old one; clients refresh once on 401 or 403.
