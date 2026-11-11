# deploy/isfc: the Hub's admin plane in ISFC mode (HUB-497)

What a platform team applies, or copies into the ISFC repo, to run `HUB_ADMIN_AUTH_MODE=isfc`
(AUTH_PLAN §7, §9, ADR-0026). Nothing here is applied by CI, and none of it has run against a real
cluster or the ISFC chart yet; the files say what to check.

| File | What | Goes to |
| --- | --- | --- |
| `values-integration-hub-admin.yaml` | OPA sidecar (`opa.enabled: true`, `opa.platform: integration-hub`) and the admin's ISFC settings, as values for ISFC's `deploy/charts/isfc-service` | ISFC chart release of the Hub admin |
| `gateway-route.yaml` | The `integration-hub-admin` route (AUTH_PLAN §9) and who points at it | ISFC api-gateway routing document |
| `networkpolicy-hub-admin.yaml` | Admin listener `:8081` reachable only from the gateway and allow-listed automation; ops port `:9090` from monitoring | The Hub's namespace |

Rules these files carry:
- **One way in for people:** console, hubctl and hubmcp go through the gateway route. The gateway
  authenticates the Hydra token and adds the `x-isfc-*` headers; the admin verifies the token again.
- **One way in for machines:** `x-isfc-client-id` + `x-isfc-api-key` straight to the admin, from pods
  labelled `integration-hub.io/admin-access: "true"` in an allow-listed namespace. Machines can never
  unseal audit payloads or purge the DLQ (D7).
- **No OPA on `/v1`:** the sidecar serves the admin only. The caller plane checks ISFC keys in memory
  (`HUB_EDGE_ISFC_AUTH=on` on the gateway role, ADR-0028).
- **Fail closed:** an OPA error is a 503 `authz_unavailable`, alert `HubAdminAuthzUnavailable`
  ([runbook](../../docs/runbooks/HubAdminAuthzUnavailable.md), [procedures](../../docs/runbooks/isfc-auth.md)).
  `HUB_OPA_DISABLED` is refused unless `HUB_ENV=test`.

Check before the first deploy: `helm template` the ISFC chart with the values file and confirm the
admin pod gets the label the NetworkPolicy selects; `kubectl auth`-style probes from an unlabelled
pod to `hub-admin:8081` must time out.
