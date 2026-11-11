package {__package_name__}

# Entry point of the Integration Hub admin API bundle (client `integration-hub`, bundle path prefix
# integration-hub). Adapted from the ISFC panel template (authz-policies/templates/panel-backend);
# the differences are listed at the end of this header. See docs/tech/AUTH_PLAN.md §7.
#
# The Hub (backend/internal/authz) posts the same input as the ISFC sidecar middleware to
#   POST /v1/data/integrationhub/decision
#   {"input": {"sub": {...}, "http": {...}, "client": {...}, "tenant": {...}}}
# and reads `result.allow`, `result.reason` and `result.meta`.
#
# input.sub     subject: id, type (USER | INTERNAL) plus every key of the x-isfc-sub-attrs header
#               (roles and permissions the user holds IN THE TENANT OF THE REQUEST). With an API key
#               the sidecar sets id = the caller's client id and type = INTERNAL.
# input.http    method (HTTP verb) and path as the Hub sees it: /admin/v1/vendors/v_1 (after the
#               gateway's prefix rewrite), without the query string.
# input.client  clientId (x-isfc-client-id), apiKey (x-isfc-api-key).
# input.tenant  id (x-isfc-tenant-id): the ISFC tenant the request acts in.
#
# data.{__package_name__}  the client configuration of auth-service
#               GET /api/v1/client/configuration/integration-hub (.data), shipped as data.json:
#               allowed_api_keys[] {key, allowed_client_id, status, roles, permissions}
#               allowed_client_ids[] {client_id, status}
#
# Every admin operation has one file under policies/admin/v1/ (generated from the admin contract
# by scripts/gen-authz.mjs) that adds to `permit` and `details`. A request is allowed only when a
# permit rule for its method and path holds.
#
# Differences from the panel template:
#   * ISFC_ADMIN and ISFC_READ_ADMIN are NOT shortcuts: ISFC platform admins are not Hub admins
#     (decision D6). Only the Hub permissions below count.
#   * Two scopes instead of one tenant check. The console client has tenancy "required", so every
#     human request carries a tenant. A request in the platform tenant holds a HUB_ADMIN / HUB_EDITOR
#     / HUB_VIEWER role (platform_scope, all tenants); a request in a linked tenant holds a
#     HUB_TENANT_ADMIN / HUB_TENANT_VIEWER role (tenant_scope, that tenant only).
#   * API keys (automation, `client:<id>`) act on the platform: ISFC keys carry no tenant yet
#     (AUTH_PLAN G1). The decision meta reports the key's permissions as `key_permissions`, from
#     which the Hub derives the roles it shows in /me and the change log.
#   * The two human-only operations (unsealRequest, purgeDeadLetters) never allow an API key
#     (decision D7); the Hub refuses machines for them as well.

default allow := false

allow if {
	permit[input.http.method][input.http.path]
}

decision := {
	"allow": allow,
	"method": input.http.method,
	"path": input.http.path,
	"reason": reason,
	"meta": meta,
}

reason := msg if {
	allow
	msg := "authorized by integration-hub"
} else := "denied by integration-hub"

meta := m if {
	d := details[input.http.method][input.http.path]
	m := object.union(d, key_meta)
} else := key_meta

# For an API key request: the permissions of the matching ACTIVE key (the Hub maps them to roles).
key_meta := {"key_permissions": sort(api_key_permissions)} if {
	has_api_key
	count(api_key_permissions) > 0
} else := {}

# ------------------------------------------------------------------ catalogue

# Roles of the Hub console client. Platform roles are held in the platform tenant, tenant roles in a
# linked tenant (AUTH_PLAN §2.2). The permissions they bundle are listed in AUTH_PLAN §7.4 and
# seeded by scripts/isfc-seed-hub.py.
platform_roles := {"HUB_ADMIN", "HUB_EDITOR", "HUB_VIEWER"}

tenant_roles := {"HUB_TENANT_ADMIN", "HUB_TENANT_VIEWER"}

# ------------------------------------------------------------------ scopes

# A signed-in user of the console acting on the platform: platform tenant role, any tenant.
platform_scope if {
	human_request
	some role in subject_list("roles")
	role in platform_roles
}

# A signed-in user acting inside one linked tenant.
tenant_scope if {
	human_request
	some role in subject_list("roles")
	role in tenant_roles
}

human_request if {
	has_tenant
	validate_client
	not has_api_key
}

# platform_can: platform users with every required permission, or an API key that holds them.
platform_can(required) if {
	platform_scope
	has_required_permissions(required)
}

platform_can(required) if has_required_permissions_on_api_key(required)

# platform_human_can: the same without API keys (unsealRequest, purgeDeadLetters: decision D7).
platform_human_can(required) if {
	platform_scope
	has_required_permissions(required)
}

# tenant_can: users inside a linked tenant with every required permission. Never an API key:
# keys have no tenant.
tenant_can(required) if {
	tenant_scope
	has_required_permissions(required)
}

# Any authenticated caller: a user with a Hub role in a tenant, or a valid API key (getMe).
authenticated if platform_scope

authenticated if tenant_scope

authenticated if valid_api_key

# ------------------------------------------------------------------ permissions

# The subject's (user's) permissions in this tenant. ISFC_ADMIN holds nothing special (D6).
has_required_permissions(required) if {
	user_permissions := subject_list("permissions")
	count(user_permissions) > 0
	every permission in required {
		permission in user_permissions
	}
}

# The API key sent by the caller (x-isfc-api-key) must be an ACTIVE key that this service's client
# issued to the calling client (x-isfc-client-id) and carry every required permission.
has_required_permissions_on_api_key(required) if {
	valid_api_key
	every permission in required {
		permission in api_key_permissions
	}
}

valid_api_key if {
	some allowed_key in config_allowed_api_keys
	matching_key(allowed_key)
}

matching_key(allowed_key) if {
	has_api_key
	allowed_key.key == input.client.apiKey
	allowed_key.allowed_client_id == input.client.clientId
	allowed_key.status == "ACTIVE"
	is_array(allowed_key.permissions)
	count(allowed_key.permissions) > 0
}

# The permissions of every matching key (normally one key), as a set.
api_key_permissions contains permission if {
	some allowed_key in config_allowed_api_keys
	matching_key(allowed_key)
	some permission in allowed_key.permissions
}

# ------------------------------------------------------------------ clients

# A user request must come from a client this service allows: an ACTIVE entry of
# allowed_client_ids (the console client is mapped to `integration-hub` in auth-service).
validate_client if {
	client_id := input.client.clientId
	is_string(client_id)
	client_id != ""
	some allowed_client in config_allowed_client_ids
	allowed_client.client_id == client_id
	allowed_client.status == "ACTIVE"
}

has_api_key if {
	is_string(input.client.apiKey)
	input.client.apiKey != ""
}

# ------------------------------------------------------------------ tenant

# The gateway resolved a tenant for this request (x-isfc-tenant-id).
has_tenant if {
	t := input.tenant.id
	is_string(t)
	t != ""
}

tenant_is(id) if {
	has_tenant
	input.tenant.id == id
}

# ------------------------------------------------------------------ helpers

# The client configuration (see the header). Empty when the bundle has none.
default config_allowed_api_keys := []

config_allowed_api_keys := data.{__package_name__}.allowed_api_keys

default config_allowed_client_ids := []

config_allowed_client_ids := data.{__package_name__}.allowed_client_ids

# input.sub[key] as a list (missing or non-array -> []).
subject_list(key) := v if {
	v := input.sub[key]
	is_array(v)
} else := []
