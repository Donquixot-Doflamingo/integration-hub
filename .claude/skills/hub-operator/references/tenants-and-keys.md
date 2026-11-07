# Tenants and API keys

A **tenant** is the internal service or team that calls the Hub (`orders-service`, `loan-onboarding`). It owns API keys, callback endpoints, a rate limit (`rps`, `burst`) and the integrations it created; other tenants can be **granted** an integration. `tenant_id` is permanent (3–64 characters: letters, digits, `_`, `-`): agree on it first.

## Contents
- [Create a tenant](#create-a-tenant)
- [Choose scopes](#choose-scopes)
- [Mint an API key](#mint-an-api-key)
- [Rotate, revoke, disable (console only for MCP users)](#rotate-revoke-disable)
- [Change rate limit or contact](#change-rate-limit-or-contact)
- [Let another tenant call an integration (grants)](#grants)

## Create a tenant

Role: `admin`.

- **Console:** Tenants → **Create tenant** → tenant id, display name, description, contact, requests per second, burst, reason → create.
- **hubctl:** not supported (`apply` refuses new tenants). Use another way.
- **MCP:**
  ```json
  hub_admin_create_tenant {
    "tenant_id": "orders-service", "display_name": "Orders service",
    "contact": "orders-team@example.com", "rate_limit": {"rps": 200, "burst": 400},
    "reason": "Onboard orders events, OPS-123"
  }
  ```
- **Admin API:**
  ```bash
  curl -sS -X POST "$HUB_ADMIN_URL/admin/v1/tenants" \
    -H "Authorization: Bearer $HUB_ADMIN_TOKEN" -H "Content-Type: application/json" \
    -d '{"tenant_id":"orders-service","display_name":"Orders service","contact":"orders-team@example.com","rate_limit":{"rps":200,"burst":400},"reason":"Onboard orders events, OPS-123"}'
  ```

A new tenant has no keys and owns no integrations.

## Choose scopes

| Scope | Allows | Give it to |
| --- | --- | --- |
| `invoke` | Invoke, request status, discovery (`/v1/integrations`, `/v1/integrations/{id}`: operations and their request schemas) | Every service key |
| `dry_run` | `POST /v1/integrations/{id}/dry-run`: shows the vendor request (secrets `***`) | **Developer keys only, never production services.** It is the one place callers see what is behind an integration (PRD DV-6), so it is never granted by default |
| `requests_read` | Defined for read-only request access (DESIGN §16.5, SECURITY.md); request status already works with `invoke` keys, so most tenants do not need it | Rarely needed |

Each scope can be narrowed to one integration: `invoke:int_evt_brandA`. Grant the least that works. A missing scope answers 403 `FORBIDDEN`, reason `missing_scope`.

Typical set: production key `["invoke:int_evt_brandA"]`; a developer's sandbox key `["invoke", "dry_run"]` with an `expires_at`.

## Mint an API key

Role: `admin`. The key (`hk_live_...` / `hk_test_...`) is **shown once**; the Hub stores only its hash. Agree tenant, scopes, description and expiry with the user before minting.

- **Console:** Tenants → *tenant* → API keys → **Create API key** → scopes, description, expiry, reason. The secret appears once with a **Copy** button; **Done** hides it forever.
- **hubctl:** not supported (`apply` refuses API keys: they hold secrets).
- **MCP:** after the user agrees:
  ```json
  hub_admin_create_api_key {
    "tenant_id": "orders-service", "scopes": ["invoke:int_evt_brandA"],
    "description": "orders prod", "expires_at": null,
    "reason": "Initial production key", "confirm": true
  }
  ```
  The result's `secret` goes to the user once, verbatim, with "store it in your secret manager now". Never repeat it, write it to a file, or pass it to another tool; refer to the key by `key_id` or `prefix` afterwards. (hubmcp refuses any later call whose arguments contain a key.)
- **Admin API:** `POST $HUB_ADMIN_URL/admin/v1/api-keys` with `{"tenant_id", "scopes", "description", "expires_at", "reason"}`; the response holds `api_key` (metadata) and `secret`.

Hand the key to the calling team through their secret manager, plus the integration id. They read it as `HUB_API_KEY` (consumer skill: `clients/claude-skill/integration-hub/`).

## Rotate, revoke, disable

These are destructive or secret-minting and are **not MCP tools**. Do them in the console (role `admin`), or with the admin API:

| Action | Console | Admin API |
| --- | --- | --- |
| Rotate with overlap (old key stays valid for the overlap, then is revoked automatically; new secret shown once) | Tenant → API keys → **Rotate** | `rotateApiKey` `POST /api-keys/{key_id}/rotate` |
| Revoke now | Tenant → API keys → **Revoke** | `revokeApiKey` `POST /api-keys/{key_id}/revoke` |
| Disable the whole tenant (all its calls are refused) | Tenant → **Disable tenant** (and **Enable tenant**) | `updateTenant` with `"disabled": true` |

hubctl can also set `disabled` through `apply` (tenants file). A leaked key: revoke it at once, then mint a new one.

## Change rate limit or contact

Role: `admin`. Applies to live traffic.

- **Console:** Tenant → **Edit tenant**.
- **hubctl:** edit `tenants/<id>.yaml` (`display_name`, `description`, `contact`, `rate_limit`) in an exported tree, then `plan` and `apply -reason ... -yes`. Changing `api_keys` there is refused.
- **MCP:** `hub_admin_get_tenant` → show current and new values → after approval:
  ```json
  hub_admin_update_tenant {"tenant_id": "orders-service", "rate_limit": {"rps": 300, "burst": 600},
    "reason": "Diwali peak, OPS-130", "if_match": "<etag from get>", "confirm": true}
  ```
- **Admin API:** `PATCH /admin/v1/tenants/{tenant_id}`.

## Grants

An integration has one owner tenant; `grants` lists other tenants allowed to invoke it.

- **hubctl:** edit `grants` in `integrations/<id>/integration.yaml` and `apply`.
- **MCP:** set `grants` when creating with `hub_admin_create_integration`; changing them later is not an MCP tool.
- **Admin API:** `updateIntegration` `PATCH /admin/v1/integrations/{id}` with `{"grants": [...], "reason": ...}`.
- **Console:** not editable yet (shown on the integration page as "Other tenants allowed").
