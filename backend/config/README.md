# backend/config/ — dev registry

The registry the Hub loads in local dev (DESIGN §8, §19 M1; layout: TECH_SPEC §8.2). One object per
file; file names equal IDs. `internal/registry` loads, validates and snapshots it
(`HUB_CONFIG_DIR`, default `config`). Every schema lives inline in its file (ADR-0023): the
caller's request schema per operation in the integration version, the request a vendor accepts
on the vendor endpoint. No secrets live here.

| Object | Files |
| --- | --- |
| Vendor | `vendors/clevertap.yaml` (dev regions point at mockvendor :8099); endpoint `upload@1` declares its `request_schema` (headers, body) and an informational `response_schema` |
| Accounts | `accounts/ct-brandA-in.yaml`, `accounts/ct-brandB-us.yaml` (`credentials_ref: localfile://<id>`) |
| Transformers | `transformers/T1/7.yaml`, `T2/3.yaml`, `R1/2.yaml`, `R2/1.yaml` (DESIGN §7.4; goldens arrive in M2) |
| Integrations | `int_evt_brandA` (wait + polling, versions 1–2), `int_evt_brandB` (async_callback), `int_evt_brandB_min` (DESIGN §3.4 without failover); each version declares `operations.default` with the events-tracking `request_schema.body` (`x-pii` tags drive audit masking) |
| Tenant | `tenants/orders-service.yaml` |
| Registrar | `registrar/reg_orders_cb.yaml` (local receiver :8097) |

**Dev API key (test value, local only):** `hk_test_dev_orders_service_local_only`. Its sha256 is the hash in
`tenants/orders-service.yaml`. Never use it outside local dev.

Validate after editing: `go test ./internal/registry -run DevConfig` (or start a role with `-config-watch`
and watch the log).
