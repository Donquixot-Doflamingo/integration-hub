package {__package_name__}

# Hand-written tests of entry.rego: the decision shape, the scopes, the clients and the API-key
# rules that the generated rbac_matrix_test.rego does not spell out. Paths use the shape the Hub
# sends (/admin/v1/...).

e_clients := [{"client_id": "integration-hub-console", "status": "ACTIVE"}]

e_keys := [
	{"key": "k-ci", "allowed_client_id": "integration-hub-ci", "status": "ACTIVE", "roles": [], "permissions": ["HUB_INTEGRATION:WRITE", "HUB_INTEGRATION:READ"]},
	{"key": "k-dup-a", "allowed_client_id": "twin", "status": "ACTIVE", "roles": [], "permissions": ["HUB_VENDOR:READ"]},
	{"key": "k-dup-a", "allowed_client_id": "twin", "status": "ACTIVE", "roles": [], "permissions": ["HUB_ACCOUNT:READ"]},
]

e_user(method, path, attrs, tenant) := {
	"http": {"method": method, "path": path},
	"sub": object.union({"id": "u-1", "type": "USER"}, attrs),
	"client": {"clientId": "integration-hub-console"},
	"tenant": {"id": tenant},
}

e_allowed(i) if {
	decision.allow with input as i with data.{__package_name__}.allowed_client_ids as e_clients with data.{__package_name__}.allowed_api_keys as e_keys
}

e_decision(i) := d if {
	d := decision with input as i with data.{__package_name__}.allowed_client_ids as e_clients with data.{__package_name__}.allowed_api_keys as e_keys
}

test_platform_viewer_reads_and_cannot_write if {
	attrs := {"roles": ["HUB_VIEWER"], "permissions": ["HUB_VENDOR:READ"]}
	e_allowed(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"))
	e_allowed(e_user("GET", "/admin/v1/vendors/v_1", attrs, "t-platform"))
	not e_allowed(e_user("POST", "/admin/v1/vendors", attrs, "t-platform"))
	not e_allowed(e_user("PUT", "/admin/v1/vendors/v_1", attrs, "t-platform"))
}

# D6: ISFC platform admins are not Hub admins, whatever else they hold.
test_isfc_admin_is_not_a_hub_admin if {
	attrs := {"roles": ["ISFC_ADMIN", "ISFC_READ_ADMIN"], "permissions": []}
	not e_allowed(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"))
	not e_allowed(e_user("POST", "/admin/v1/vendors", attrs, "t-platform"))
	not e_allowed(e_user("GET", "/admin/v1/me", attrs, "t-platform"))
	# even with the role AND the permission, ISFC_ADMIN adds nothing: the Hub permission alone decides
	with_perm := {"roles": ["ISFC_ADMIN"], "permissions": ["HUB_VENDOR:READ"]}
	not e_allowed(e_user("GET", "/admin/v1/vendors", with_perm, "t-platform")) # no HUB_* role: no scope
}

test_tenant_roles_stay_in_their_tenant_scope if {
	attrs := {"roles": ["HUB_TENANT_VIEWER"], "permissions": ["HUB_TENANT:READ", "HUB_INTEGRATION:READ", "HUB_REQUEST:READ", "HUB_DLQ:READ"]}
	e_allowed(e_user("GET", "/admin/v1/integrations", attrs, "t-hl"))
	e_allowed(e_user("GET", "/admin/v1/tenants/hl", attrs, "t-hl"))
	# platform-only operations stay closed even though the tenant viewer holds HUB_TENANT:READ
	not e_allowed(e_user("GET", "/admin/v1/tenants", attrs, "t-hl"))
	not e_allowed(e_user("GET", "/admin/v1/vendors", attrs, "t-hl"))
	not e_allowed(e_user("PATCH", "/admin/v1/tenants/hl", attrs, "t-hl"))
}

test_tenant_admin_finishes_setup_but_cannot_administer if {
	attrs := {"roles": ["HUB_TENANT_ADMIN"], "permissions": ["HUB_TENANT:READ", "HUB_INTEGRATION:READ", "HUB_REQUEST:READ", "HUB_DLQ:READ", "HUB_TENANT:SETUP"]}
	e_allowed(e_user("PATCH", "/admin/v1/tenants/hl", attrs, "t-hl"))
	e_allowed(e_user("POST", "/admin/v1/registrar-endpoints", attrs, "t-hl"))
	e_allowed(e_user("DELETE", "/admin/v1/registrar-endpoints/reg_1", attrs, "t-hl"))
	not e_allowed(e_user("POST", "/admin/v1/tenants", attrs, "t-hl"))
	not e_allowed(e_user("POST", "/admin/v1/api-keys", attrs, "t-hl"))
	not e_allowed(e_user("POST", "/admin/v1/tenants/sync", attrs, "t-hl"))
}

# A request without a tenant never passes: the console client has tenancy "required".
test_no_tenant_no_access if {
	attrs := {"roles": ["HUB_ADMIN"], "permissions": ["HUB_VENDOR:READ", "HUB_VENDOR:ADMIN"]}
	not e_allowed(e_user("GET", "/admin/v1/vendors", attrs, ""))
	not e_allowed(object.remove(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"), ["tenant"]))
}

test_unmapped_client_denied if {
	attrs := {"roles": ["HUB_ADMIN"], "permissions": ["HUB_VENDOR:READ"]}
	i := object.union(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"), {"client": {"clientId": "other-ui"}})
	not e_allowed(i)
	e_allowed(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"))
}

test_inactive_console_client_denied if {
	attrs := {"roles": ["HUB_ADMIN"], "permissions": ["HUB_VENDOR:READ"]}
	not decision.allow with input as e_user("GET", "/admin/v1/vendors", attrs, "t-platform") with data.{__package_name__}.allowed_client_ids as [{"client_id": "integration-hub-console", "status": "INACTIVE"}]
}

test_unknown_paths_and_methods_are_denied if {
	attrs := {"roles": ["HUB_ADMIN"], "permissions": ["HUB_VENDOR:READ", "HUB_VENDOR:ADMIN", "HUB_PLATFORM:READ"]}
	not e_allowed(e_user("GET", "/admin/v1/nope", attrs, "t-platform"))
	not e_allowed(e_user("DELETE", "/admin/v1/vendors/v_1", attrs, "t-platform"))
	not e_allowed(e_user("GET", "/admin/v1/vendors/v_1/extra/segments", attrs, "t-platform"))
	not e_allowed(e_user("GET", "/admin/v1/vendors/", attrs, "t-platform"))
	not e_allowed(e_user("GET", "/vendors", attrs, "t-platform")) # the prefix is part of the path the Hub sends
}

# gin-style shadowing: /snapshots/latest is its own operation, not a snapshot version.
test_static_route_shadows_parameterised_route if {
	attrs := {"roles": ["HUB_VIEWER"], "permissions": ["HUB_PLATFORM:READ"]}
	d := e_decision(e_user("GET", "/admin/v1/snapshots/latest", attrs, "t-platform"))
	d.allow
	d.meta.operation == "getLatestSnapshot"
	d2 := e_decision(e_user("GET", "/admin/v1/snapshots/12", attrs, "t-platform"))
	d2.meta.operation == "getSnapshot"
}

test_decision_shape if {
	attrs := {"roles": ["HUB_VIEWER"], "permissions": ["HUB_VENDOR:READ"]}
	d := e_decision(e_user("GET", "/admin/v1/vendors", attrs, "t-platform"))
	d.allow
	d.reason == "authorized by integration-hub"
	d.method == "GET"
	d.path == "/admin/v1/vendors"
	d.meta.permissions == ["HUB_VENDOR:READ"]
	d.meta.scope == "platform"
	denied := e_decision(e_user("POST", "/admin/v1/vendors", attrs, "t-platform"))
	not denied.allow
	denied.reason == "denied by integration-hub"
}

# ---- API keys (machine principals) ----

e_machine(method, path, key, client) := {
	"http": {"method": method, "path": path},
	"sub": {"id": client, "type": "INTERNAL"},
	"client": {"clientId": client, "apiKey": key},
}

test_key_with_permission_is_allowed_and_reports_its_permissions if {
	d := e_decision(e_machine("POST", "/admin/v1/integrations", "k-ci", "integration-hub-ci"))
	d.allow
	d.meta.key_permissions == ["HUB_INTEGRATION:READ", "HUB_INTEGRATION:WRITE"]
}

test_key_without_the_permission_is_denied if {
	not e_allowed(e_machine("POST", "/admin/v1/vendors", "k-ci", "integration-hub-ci"))
	not e_allowed(e_machine("POST", "/admin/v1/tenants", "k-ci", "integration-hub-ci"))
}

test_key_of_another_client_unknown_or_without_a_client_is_denied if {
	not e_allowed(e_machine("GET", "/admin/v1/integrations", "k-ci", "someone-else"))
	not e_allowed(e_machine("GET", "/admin/v1/integrations", "nope", "integration-hub-ci"))
	not e_allowed(e_machine("GET", "/admin/v1/integrations", "k-ci", ""))
}

# D7: no key may unseal audit payloads or purge the DLQ, not even one holding every permission.
test_keys_cannot_do_human_only_operations if {
	all := [{"key": "k-god", "allowed_client_id": "god", "status": "ACTIVE", "roles": ["ISFC_ADMIN"], "permissions": ["HUB_REQUEST:UNSEAL", "HUB_DLQ:PURGE", "HUB_DLQ:READ"]}]
	not decision.allow with input as e_machine("POST", "/admin/v1/requests/r1/unseal", "k-god", "god") with data.{__package_name__}.allowed_api_keys as all
	not decision.allow with input as e_machine("POST", "/admin/v1/dlq/purge", "k-god", "god") with data.{__package_name__}.allowed_api_keys as all
	decision.allow with input as e_machine("GET", "/admin/v1/dlq/d1", "k-god", "god") with data.{__package_name__}.allowed_api_keys as all
}

# Keys act on the platform: they send no tenant and still reach tenant-capable operations.
test_key_reaches_tenant_capable_operations_without_a_tenant if {
	e_allowed(e_machine("GET", "/admin/v1/integrations", "k-ci", "integration-hub-ci"))
}

# A user request that also carries an API key is judged as a key request, never as a user.
test_user_roles_do_not_count_on_a_key_request if {
	i := {
		"http": {"method": "GET", "path": "/admin/v1/vendors"},
		"sub": {"id": "integration-hub-ci", "type": "INTERNAL", "roles": ["HUB_ADMIN"], "permissions": ["HUB_VENDOR:READ"]},
		"client": {"clientId": "integration-hub-ci", "apiKey": "k-ci"},
		"tenant": {"id": "t-platform"},
	}
	not e_allowed(i)
}

# Two bundle entries with the same key: the union of their permissions, never an evaluation error.
test_duplicate_key_entries_union_their_permissions if {
	d := e_decision(e_machine("GET", "/admin/v1/vendors", "k-dup-a", "twin"))
	d.allow
	count(d.meta.key_permissions) == 2
	e_allowed(e_machine("GET", "/admin/v1/accounts", "k-dup-a", "twin"))
}

test_get_me_needs_any_authentication if {
	e_allowed(e_user("GET", "/admin/v1/me", {"roles": ["HUB_TENANT_VIEWER"], "permissions": []}, "t-hl"))
	e_allowed(e_machine("GET", "/admin/v1/me", "k-ci", "integration-hub-ci"))
	not e_allowed(e_machine("GET", "/admin/v1/me", "nope", "integration-hub-ci"))
	not e_allowed(e_user("GET", "/admin/v1/me", {"roles": [], "permissions": []}, "t-hl"))
}
