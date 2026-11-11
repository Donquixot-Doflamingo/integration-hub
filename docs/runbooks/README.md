# Runbooks

One runbook per alert in [`deploy/alerts/hub.rules.yaml`](../../deploy/alerts/hub.rules.yaml). Each alert's `runbook_url` points at `docs/runbooks/<AlertName>.md`, so the file names here must match the alert names exactly (HUB-237).

| Alert | Severity | What it means | Runbook |
| --- | --- | --- | --- |
| `BreakerOpenTooLong` | page | A vendor `(account, endpoint)` circuit has not closed for 5 minutes | [BreakerOpenTooLong.md](BreakerOpenTooLong.md) |
| `ParkedOldestAgeHigh` | page | Parked work for an `(account, endpoint)` has waited more than 10 minutes | [ParkedOldestAgeHigh.md](ParkedOldestAgeHigh.md) |
| `QueueOldestAgeHigh` | page | Workers of a pool are more than 2 minutes behind on due work | [QueueOldestAgeHigh.md](QueueOldestAgeHigh.md) |
| `DLQSurge` | page | More than 50 new dead letters in 10 minutes for one account | [DLQSurge.md](DLQSurge.md) |
| `UpstreamAuthFailed` | page | A vendor rejected the Hub's credentials for an account | [UpstreamAuthFailed.md](UpstreamAuthFailed.md) |
| `TransformFailed` | page | A transformer failed on an integration (always a Hub bug) | [TransformFailed.md](TransformFailed.md) |
| `SnapshotBehind` | warn | A node serves an older config snapshot than the rest of the fleet | [SnapshotBehind.md](SnapshotBehind.md) |
| `CredentialExpiringSoon` | warn | An account's credential slot expires in less than 7 days | [CredentialExpiringSoon.md](CredentialExpiringSoon.md) |
| `HubOverheadP99High` | warn | Hub overhead p99 on sync and wait requests is above 5 ms (SLO S1) | [HubOverheadP99High.md](HubOverheadP99High.md) |
| `AuditEventsDropped` | warn | Audit events were dropped; some requests have an incomplete audit trail | [AuditEventsDropped.md](AuditEventsDropped.md) |
| `HubIsfcSyncStale` | warn | The ISFC tenant and caller-key sync has not succeeded for 15 minutes, or failed 3 runs in a row | [HubIsfcSyncStale.md](HubIsfcSyncStale.md) |
| `HubAdminAuthzUnavailable` | warn | More than 1% of admin authorization decisions (OPA) failed for 5 minutes; the admin API fails closed | [HubAdminAuthzUnavailable.md](HubAdminAuthzUnavailable.md) |

Procedures used by several runbooks:
- [ISFC authentication](isfc-auth.md): OPA down, sync stale, key revocation.
- [Credential rotation](credential-rotation.md): add `next`, test, promote, watch 15 minutes, delete old (DESIGN §6.6).

## Before you start

**Dashboards** (Grafana JSON in [`deploy/dashboards/`](../../deploy/dashboards/)): Platform (uid `hub-platform`), Vendor and account (uid `hub-vendor`, variables `vendor`, `account`, `endpoint`), Tenant (uid `hub-tenant`). Panel names in the runbooks are the panel titles.

**Admin API** (`/admin/v1`, contract [`contracts/openapi/hub-admin.v1.yaml`](../../contracts/openapi/hub-admin.v1.yaml)). Runbooks name each action by its `operationId`. Every mutation needs a `reason` and returns a `change_id` that lands in the change log (`listChangeLog`).

```sh
export HUB_ADMIN_URL=https://<admin host>       # hubctl default: http://localhost:8081
export HUB_ADMIN_TOKEN=<OIDC access token>      # dev only: hubctl login -dev -role admin
hubctl whoami                                    # checks the token: subject, roles, tenant scope
curl -sS -H "Authorization: Bearer $HUB_ADMIN_TOKEN" "$HUB_ADMIN_URL/admin/v1/breakers?state=open"
```

Roles: reads need `viewer`; `pauseIntegration`, `replayDeadLetters` and `testAccountConnection` need `editor`; breaker holds, credential rotation and `purgeDeadLetters` need `admin`.

**hubctl** (`backend/cmd/hubctl`) has these operations commands today: `login` (`-isfc` or `-dev`), `whoami`, `export`, `plan`, `apply`, `dry-run`, `replay`, `transform test`, `creds`. Everything else in the runbooks goes through the admin API.

**Logs** are JSON; search by `msg` (for example `breaker.opened`). Logs never hold bodies or secrets: for payloads use the request timeline (`getRequestTimeline`), subject to the integration's payload policy.

## What callers see

Callers never see vendor, account or version names (DESIGN §0). They see the canonical codes of DESIGN §4.4, for example `UPSTREAM_UNAVAILABLE` (503, retryable, with `retry_after_ms`), `UPSTREAM_AUTH_FAILED` (502), `TRANSFORM_FAILED` (500), `INTEGRATION_PAUSED` (503), or a 202 for queued work. When you talk to a tenant, use the integration ID and these codes only.

## Related
- [OBSERVABILITY.md](../tech/OBSERVABILITY.md): metrics catalogue (§3), debugging guide (§9), dashboards (§10), alert rules (§11)
- [F-18 Observability](../features/F-18-observability.md)
- [DESIGN.md](../DESIGN.md): §6 cutover and rotation, §9 credentials, §10 resilience, §13 audit, §16 observability
