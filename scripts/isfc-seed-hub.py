#!/usr/bin/env python3
"""Creates, in ISFC, everything the Integration Hub needs for login and authorization
(AUTH_PLAN section 2.4, 7.4, HUB-462, HUB-477). Safe to re-run: everything is looked up first and only
what is missing is created or topped up. Nothing is ever deleted or narrowed.

It talks to auth-service's admin APIs (the same ones ISFC's local-dev seed-*.py scripts use), by
default through the ISFC api-gateway at ISFC_AUTH_BASE (local: http://127.0.0.1:8000/auth). It
needs the access token of an ISFC admin (ISFC_ADMIN) in ISFC_ADMIN_TOKEN: it never logs in, never
asks for a password, and never prints a token, a client secret or an API key.

What it makes (names, not ids: ISFC generates client ids):

  clients       integration-hub-console   web (public, PKCE), INTERNAL, tenancy required, redirect URI
                                          <console origin>/auth/callback, allowed_domains, no
                                          user creation on login. Login methods EMAIL_OTP (copied from
                                          --login-methods-from) and, with --microsoft-meta-file,
                                          MICROSOFT_SSO (D9: enable it in production only after ISFC-CR-4).
                integration-hub           backend_services, INTERNAL, client_credentials, tenancy none,
                                          enable_rego_bundle_push (package = client id without '-').
                                          Issues the API keys of callers and automation.
  permissions   the catalogue of authz-policies/integration-hub/catalogue.json (generated from the
                admin contract by scripts/gen-authz.mjs) on BOTH clients: the console client for
                users, integration-hub for machine keys. Plus HUB:INVOKE, HUB:DRY_RUN,
                HUB:REQUESTS_READ on integration-hub (caller plane).
  roles         HUB_ADMIN, HUB_EDITOR, HUB_VIEWER, HUB_TENANT_ADMIN, HUB_TENANT_VIEWER on the console
                client; on integration-hub: the same five (for keys), HUB_CALLER and HUB_CI.
  tenants       the platform tenant (key INTEGRATION_HUB_PLATFORM, first in the console client's
                tenants), plus any --tenant KEY=Name (demo tenants). The console client's
                `tenants` list is topped up in that order: existing entries keep their place.
  mapping       the console client is an allowed client of integration-hub (the policies'
                validate_client); optional --auth-service-client / --user-service-client ids get the
                console client in their allow-lists too (it logs in through them).
  automation    with --ci-client NAME (default integration-hub-ci when the flag has no value): a
                backend_services client plus one ACTIVE API key issued by integration-hub to it with
                role HUB_CI. The key is fetched later by that client with its own credentials
                (GET /api/v1/client/api-key), so it is never shown here.
  metadata      <env>_client_id in authz-policies/integration-hub/metadata.yaml (--env, default local).

Prints the settings the Hub then needs (HUB_ISFC_CONSOLE_CLIENT_ID, HUB_ISFC_PLATFORM_TENANT_ID,
HUB_OPA_CLIENT_ID). Needs the API shapes of auth-service as in ISFC local-dev/seed-s2s.py,
seed-panels.py and seed-tenants.py; it cannot be run against a real ISFC from CI (tests never call a
real ISFC): scripts/tests/isfc_seed_hub_test.sh runs it against a fake auth-service.

Usage:
  ISFC_ADMIN_TOKEN=... scripts/isfc-seed-hub.py [--dry-run] [--env local|qa|stage|prod]
      [--console-origin http://localhost:3000] [--allowed-domain indiashelter.in]...
      [--tenant KEY=Name]... [--ci-client [NAME]] [--group-id UUID]
      [--auth-service-client ID] [--user-service-client ID]
      [--login-methods-from isfc-admin-ui] [--microsoft-meta-file FILE] [--no-metadata]
"""
import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, ".."))
POLICY_DIR = os.path.join(ROOT, "authz-policies", "integration-hub")
CATALOGUE = os.path.join(POLICY_DIR, "catalogue.json")

CONSOLE = "integration-hub-console"
BACKEND = "integration-hub"
PLATFORM_KEY, PLATFORM_NAME = "INTEGRATION_HUB_PLATFORM", "Integration Hub platform"
DEFAULT_GROUP = "11111111-1111-1111-1111-111111111111"  # "ISFC Platform" (ISFC local-dev seed.sh)
CALLER_PERMS = ["HUB:INVOKE", "HUB:DRY_RUN", "HUB:REQUESTS_READ"]
CALLER_ROLE = ("HUB_CALLER", CALLER_PERMS)
# What automation (CI hubctl apply, scripts) needs and nothing more; never unseal or purge (D7).
CI_ROLE = ("HUB_CI", ["HUB_INTEGRATION:READ", "HUB_INTEGRATION:WRITE", "HUB_TRANSFORMER:READ", "HUB_TRANSFORMER:WRITE",
                      "HUB_VENDOR:READ", "HUB_ACCOUNT:READ", "HUB_TENANT:READ", "HUB_PLATFORM:READ"])
EMAIL_OTP, MICROSOFT_SSO = "EMAIL_OTP", "MICROSOFT_SSO"
# Login-method metadata copied from another client: only these keys (no secret, no hash).
SAFE_METHOD_KEYS = ["allowed_domains", "otp_length", "max_retry_limit", "max_send_limit", "retry_limit_window"]
UPDATE_FIELDS = ["name", "description", "redirect_uris", "grant_types", "allowed_cors_origins",
                 "authorization_code_grant_access_token_lifespan", "authorization_code_grant_refresh_token_lifespan",
                 "client_credentials_grant_access_token_lifespan"]


class Seed:
    def __init__(self, base, token, group, dry_run):
        self.base, self.token, self.group, self.dry = base.rstrip("/"), token, group, dry_run
        self.created = []  # human lines of what changed (or would change)

    # ---- http -------------------------------------------------------------------------------
    def call(self, method, path, body=None, write=False):
        """One call -> (status, parsed body). In --dry-run writes are skipped and answer (0, None)."""
        if write and self.dry:
            return 0, None
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method,
                                     headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=30) as res:  # noqa: S310 (operator-supplied ISFC URL)
                raw = res.read()
                return res.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw) if raw else None
            except ValueError:
                return e.code, None

    def must(self, method, path, body=None, what=""):
        st, parsed = self.call(method, path, body, write=method != "GET")
        if self.dry and method != "GET":
            return {}
        if not 200 <= st < 300:
            die(what or f"{method} {path} failed", st, parsed)
        return data_of(parsed)

    def note(self, line):
        self.created.append(line)
        print(("would " if self.dry else "") + line)

    # ---- clients ----------------------------------------------------------------------------
    def clients(self):
        rows = self.must("GET", f"/api/v1/group/{self.group}/client?per_page=100", what="listing clients")
        return {c["name"]: c for c in (rows or []) if c.get("status") == "ACTIVE"}

    def client_body(self, name, kind, origin, domains, extra_meta):
        meta = {"client_type": "INTERNAL", "max_device_limit": 3 if kind == "web" else 0, "create_user_if_not_exists": False,
                "allowed_domains": domains if kind == "web" else [], "allowed_ips": [], "include_child_rbac": False,
                "enable_es_listing": False, "allow_unverified_contact": False, "is_guest_login_allowed": False,
                "application_type": kind, "client_auth_domain": "", "tenancy": "none", "tenants": [],
                "enable_rego_bundle_push": kind == "backend_services", "client_domain": origin if kind == "web" else ""}
        meta.update(extra_meta)
        body = {"name": name, "group_id": self.group, "description": "Integration Hub (scripts/isfc-seed-hub.py)",
                "redirect_uris": [], "metadata": meta}
        if kind == "web":
            body.update({"redirect_uris": [origin + "/auth/callback"], "grant_types": ["authorization_code", "refresh_token"],
                         "authorization_code_grant_access_token_lifespan": "15m",
                         "authorization_code_grant_refresh_token_lifespan": "10h",
                         "client_credentials_grant_access_token_lifespan": "24h"})
        else:
            body["grant_types"] = ["client_credentials"]
        return body

    def ensure_client(self, existing, name, kind, origin="", domains=(), extra_meta=None):
        extra_meta = extra_meta or {}
        c = existing.get(name)
        if not c:
            self.note(f"create client {name} ({kind})")
            made = self.must("POST", f"/api/v1/group/{self.group}/client",
                             self.client_body(name, kind, origin, list(domains), extra_meta), f"creating client {name}")
            cid = (made or {}).get("client_id", f"<{name}>")
            existing[name] = {"client_id": cid, "name": name, "metadata": {}}
            check_package(cid, name)
            return cid
        cid = c["client_id"]
        check_package(cid, name)
        meta = c.get("metadata") or {}
        want = {"application_type": kind, **({"enable_rego_bundle_push": True} if kind == "backend_services" else {})}
        if any(meta.get(k) != v for k, v in want.items()):
            self.update_client(c, {**want})
        print(f"client {name}: exists ({cid})")
        return cid

    def update_client(self, c, meta_changes):
        """PUT replaces the client, so everything is sent back as read (as ISFC's seed scripts do)."""
        cid = c["client_id"]
        full = self.must("GET", f"/api/v1/group/{self.group}/client/{cid}", what=f"reading client {cid}") or c
        body = {k: full[k] for k in UPDATE_FIELDS if full.get(k) not in (None, "")}
        meta = dict(full.get("metadata") or {})
        meta.pop("master_client", None)
        meta.update(meta_changes)
        body["metadata"] = meta
        self.note(f"update client {cid} metadata {sorted(meta_changes)}")
        self.must("PUT", f"/api/v1/group/{self.group}/client/{cid}", body, f"updating client {cid}")

    # ---- permissions and roles ----------------------------------------------------------------
    def ensure_permissions(self, cid, names):
        rows = self.must("GET", f"/api/v1/group/{self.group}/client/{cid}/permission?per_page=500", what="listing permissions") if real(cid) else []
        have = {p["name"]: p for p in (rows or []) if p.get("status", "ACTIVE") == "ACTIVE"}
        for n in names:
            if n not in have:
                self.note(f"create permission {n} on {cid}")
                have[n] = self.must("POST", f"/api/v1/group/{self.group}/client-permission", {"client_id": cid, "name": n},
                                    f"creating permission {n}") or {"permission_id": f"<{n}>"}
        return have

    def ensure_role(self, cid, name, perms, have_perms):
        rows = self.must("GET", f"/api/v1/group/{self.group}/client/{cid}/role?per_page=500", what="listing roles") if real(cid) else []
        role = next((r for r in (rows or []) if r.get("name") == name and r.get("status", "ACTIVE") == "ACTIVE"), None)
        ids = [have_perms[p]["permission_id"] for p in perms]
        if not role:
            self.note(f"create role {name} on {cid} ({len(perms)} permissions)")
            made = self.must("POST", f"/api/v1/group/{self.group}/client-role",
                             {"client_id": cid, "name": name, "permission_ids": ids}, f"creating role {name}")
            return {"name": name, "role_id": (made or {}).get("role_id", f"<{name}>")}
        detail = self.must("GET", f"/api/v1/group/{self.group}/client-role/{role['role_id']}", what=f"reading role {name}") or {}
        cur = {p.get("name"): p.get("permission_id") for p in (detail.get("permissions") or []) if p.get("status", "ACTIVE") == "ACTIVE"}
        missing = [p for p in perms if p not in cur]
        if missing:
            self.note(f"role {name} on {cid}: add permissions {missing}")
            all_ids = sorted({*ids, *[i for i in cur.values() if i]})
            self.must("PUT", f"/api/v1/group/{self.group}/client-role/{role['role_id']}", {"permission_ids": all_ids},
                      f"updating role {name}")
        else:
            print(f"role {name} on {cid}: exists")
        return {"name": name, "role_id": role["role_id"]}

    # ---- tenants ------------------------------------------------------------------------------
    def ensure_tenants(self, wanted):
        rows = self.must("GET", "/api/v1/tenant?per_page=100", what="listing tenants (is the tenant API deployed?)")
        have = {t["key"]: t for t in (rows or [])}
        out = {}
        for key, name in wanted:
            t = have.get(key)
            if not t:
                self.note(f"create tenant {key}")
                t = self.must("POST", "/api/v1/tenant", {"key": key, "name": name}, f"creating tenant {key}") or {"id": f"<{key}>"}
            elif t.get("status") != "ACTIVE":
                self.note(f"re-activate tenant {key}")
                self.must("PATCH", f"/api/v1/tenant/{t['id']}", {"status": "ACTIVE"}, f"activating tenant {key}")
            else:
                print(f"tenant {key}: exists ({t['id']})")
            out[key] = t["id"]
        return out

    def ensure_console_tenancy(self, console, platform_id, extra_ids):
        """tenancy required; the platform tenant first, then the existing tenants in their order, then new ones."""
        cid = console["client_id"]
        c = self.must("GET", f"/api/v1/group/{self.group}/client/{cid}", what="reading the console client") if real(cid) else console
        meta = (c or {}).get("metadata") or {}
        cur = list(meta.get("tenants") or [])
        want = [platform_id] + [t for t in cur if t != platform_id] + [t for t in extra_ids if t not in cur and t != platform_id]
        if meta.get("tenancy") == "required" and want == cur:
            print("console client: tenancy required, tenants unchanged")
            return
        self.update_client(c or console, {"tenancy": "required", "tenants": want})

    # ---- mapping, keys, login methods ------------------------------------------------------------
    def ensure_mapping(self, target, allowed):
        cur = self.must("GET", f"/api/v1/group/{self.group}/client-mapping/{target}", what="reading client mapping") or {}
        have = [x["client_id"] for x in (cur.get("allowed_client_ids") or []) if x.get("status") == "ACTIVE"]
        missing = [a for a in allowed if a not in have]
        if not missing:
            print(f"allowed clients of {target}: all present")
            return
        self.note(f"allow clients {missing} on {target}")
        self.must("PUT", f"/api/v1/group/{self.group}/client-mapping/{target}", {"allowed_client_ids": have + missing},
                  f"updating the allow-list of {target}")

    def ensure_key(self, target, caller, caller_name, role_ref, expiry):
        rows = self.must("GET", f"/api/v1/group/{self.group}/client/{target}/key?per_page=500&status=ACTIVE", what="listing keys")
        for k in rows or []:
            if k.get("status") == "ACTIVE" and k.get("allowed_client_id") == caller:
                d = self.must("GET", f"/api/v1/group/{self.group}/client/key/{k['id']}", what="reading a key") or {}
                if role_ref["name"] in {r.get("name") for r in (d.get("roles") or [])}:
                    print(f"API key {target} -> {caller_name}: exists")
                    return
        self.note(f"issue an API key from {target} to {caller_name} with role {role_ref['name']}")
        # The response carries the key; it is dropped here on purpose (never print or store it).
        self.must("POST", f"/api/v1/group/{self.group}/client/{target}/key",
                  {"key_name": f"hub-{caller_name}", "expire_at": expiry, "allowed_client_id": caller,
                   "enable_rotation": False, "roles": [role_ref]}, f"issuing the key to {caller_name}")

    def method_ids(self):
        rows = self.must("GET", "/api/v1/auth-method?per_page=100", what="listing login methods") or []
        return {m["method_code"]: m["method_id"] for m in rows if m.get("status", "ACTIVE") == "ACTIVE"}

    def client_methods(self, cid):
        rows = self.must("GET", f"/api/v1/group/{self.group}/client-auth-method/client/{cid}", what="listing client login methods") if real(cid) else []
        return {r.get("auth_method_name") or "": r for r in rows or []}, rows or []

    def ensure_login_method(self, cid, code, method_ids, meta):
        mid = method_ids.get(code)
        if not mid:
            print(f"login method {code}: not in the ISFC catalogue, skipped")
            return
        _, rows = self.client_methods(cid)
        if any(str(r.get("auth_method_id")) == str(mid) and r.get("status") == "ACTIVE" for r in rows):
            print(f"login method {code} on {cid}: exists")
            return
        self.note(f"enable login method {code} on {cid}")
        self.must("POST", f"/api/v1/group/{self.group}/client-auth-method/{cid}", {"auth_method_id": mid, "meta_data": meta},
                  f"enabling {code}")


def data_of(body):
    return body.get("data") if isinstance(body, dict) else None


def real(cid):
    """False for the placeholder id of a client a --dry-run only pretended to create."""
    return not cid.startswith("<")


def die(msg, st=None, body=None):
    detail = ""
    if st is not None:
        detail = f": HTTP {st}"
        if isinstance(body, dict):  # error text only, never a request value
            detail += " " + json.dumps({k: body.get(k) for k in ("error_code", "message", "error") if k in body})[:300]
    raise SystemExit("isfc-seed-hub: " + msg + detail)


def check_package(client_id, name):
    if client_id.startswith("<"):
        return
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", client_id.replace("-", "")):
        die(f"client id {client_id} of {name} is not a valid OPA package name once '-' is removed")


def load_catalogue():
    try:
        with open(CATALOGUE) as f:
            cat = json.load(f)
    except OSError:
        die(f"{CATALOGUE} is missing: run `node scripts/gen-authz.mjs` first")
    return cat


def write_metadata(env, client_id):
    p = os.path.join(POLICY_DIR, "metadata.yaml")
    s = open(p).read()
    new = re.sub(rf'(?m)^{env}_client_id:.*$', f'{env}_client_id: "{client_id}"', s)
    if new != s:
        open(p, "w").write(new)
        print(f"authz-policies/integration-hub/metadata.yaml: {env}_client_id = {client_id}")


def parse_args(argv):
    ap = argparse.ArgumentParser(description="Seed ISFC for the Integration Hub (idempotent).")
    ap.add_argument("--dry-run", action="store_true", help="only read; print what would be created")
    ap.add_argument("--env", default="local", choices=["local", "qa", "stage", "prod"])
    ap.add_argument("--base", default=os.environ.get("ISFC_AUTH_BASE", "http://127.0.0.1:8000/auth"), help="auth-service base URL (via the gateway)")
    ap.add_argument("--group-id", default=os.environ.get("ISFC_GROUP_ID", DEFAULT_GROUP))
    ap.add_argument("--console-origin", default=os.environ.get("HUB_CONSOLE_ORIGIN", "http://localhost:3000"))
    ap.add_argument("--allowed-domain", action="append", default=None, help="email domain allowed to log in (repeatable)")
    ap.add_argument("--tenant", action="append", default=[], metavar="KEY=Name", help="extra (demo) ISFC tenant to create and link (repeatable)")
    ap.add_argument("--ci-client", nargs="?", const="integration-hub-ci", default=None, metavar="NAME", help="also create an automation client with a HUB_CI key")
    ap.add_argument("--key-expiry", default="2099-12-31T00:00:00Z", help="expire_at of the automation key")
    ap.add_argument("--auth-service-client", default="", help="client id of ISFC auth-service: allow the console client on it")
    ap.add_argument("--user-service-client", default="", help="client id of ISFC user-service: allow the console client on it")
    ap.add_argument("--login-methods-from", default="isfc-admin-ui", help="client whose EMAIL_OTP settings are copied")
    ap.add_argument("--microsoft-meta-file", default="", help="JSON meta_data for MICROSOFT_SSO (Entra client_id, client_secret, tenant_id); omitted = deferred (D9)")
    ap.add_argument("--no-metadata", action="store_true", help="do not write metadata.yaml")
    a = ap.parse_args(argv)
    a.allowed_domain = a.allowed_domain or ["indiashelter.in"]
    a.console_origin = a.console_origin.rstrip("/")
    return a


def main(argv=None):
    a = parse_args(argv if argv is not None else sys.argv[1:])
    token = os.environ.get("ISFC_ADMIN_TOKEN", "")
    if not token:
        die("ISFC_ADMIN_TOKEN is required (the access token of an ISFC admin; this script never logs in)")
    cat = load_catalogue()
    perms = cat["permissions"]
    roles = [(r["name"], r["permissions"]) for r in cat["roles"]]
    tenants = [(PLATFORM_KEY, PLATFORM_NAME)]
    for t in a.tenant:
        key, _, name = t.partition("=")
        if not key or key == PLATFORM_KEY:
            die(f"--tenant wants KEY=Name (and not the platform key), got {t!r}")
        tenants.append((key, name or key))

    s = Seed(a.base, token, a.group_id, a.dry_run)
    existing = s.clients()
    console_id = s.ensure_client(existing, CONSOLE, "web", a.console_origin, a.allowed_domain)
    backend_id = s.ensure_client(existing, BACKEND, "backend_services")

    # permissions and roles: users on the console client, keys on integration-hub
    ci_ref = None
    for cid in (console_id, backend_id):
        have = s.ensure_permissions(cid, perms + (CALLER_PERMS if cid == backend_id else []))
        for name, rperms in roles:
            s.ensure_role(cid, name, rperms, have)
        if cid == backend_id:
            s.ensure_role(cid, *CALLER_ROLE, have)
            ci_ref = s.ensure_role(cid, *CI_ROLE, have)

    tenant_ids = s.ensure_tenants(tenants)
    console = existing[CONSOLE]
    console["client_id"] = console_id
    s.ensure_console_tenancy(console, tenant_ids[PLATFORM_KEY], [tenant_ids[k] for k, _ in tenants[1:]])

    if real(backend_id):
        s.ensure_mapping(backend_id, [console_id])
    for target in (a.auth_service_client, a.user_service_client):
        if target:
            s.ensure_mapping(target, [console_id])

    methods = s.method_ids()
    meta = {"redirect_uri": a.console_origin + "/auth/callback", "allowed_domains": a.allowed_domain}
    if a.login_methods_from:
        st, parsed = s.call("GET", f"/api/v1/group/{a.group_id}/client-auth-method/client/{a.login_methods_from}")
        if st != 200:
            print(f"login methods of {a.login_methods_from}: not readable (HTTP {st}); EMAIL_OTP gets default settings")
        for row in data_of(parsed) or []:
            if row.get("auth_method_id") == methods.get(EMAIL_OTP) and row.get("status") == "ACTIVE":
                meta.update({k: v for k, v in (row.get("meta_data") or {}).items() if k in SAFE_METHOD_KEYS})
    s.ensure_login_method(console_id, EMAIL_OTP, methods, meta)
    if a.microsoft_meta_file:
        with open(a.microsoft_meta_file) as f:
            s.ensure_login_method(console_id, MICROSOFT_SSO, methods, json.load(f))
    else:
        print("login method MICROSOFT_SSO: deferred (no --microsoft-meta-file; D9: production only after ISFC-CR-4)")

    if a.ci_client:
        cid = s.ensure_client(existing, a.ci_client, "backend_services")
        s.ensure_key(backend_id, cid, a.ci_client, ci_ref, a.key_expiry)

    if not a.no_metadata and not a.dry_run and not backend_id.startswith("<"):
        write_metadata(a.env, backend_id)

    print("\n" + ("Dry run: nothing was changed.\n" if a.dry_run else f"Done ({len(s.created)} change(s)).\n") +
          "Set on `hub admin` (HUB_ADMIN_AUTH_MODE=isfc):\n"
          f"  HUB_ISFC_CONSOLE_CLIENT_ID={console_id}\n"
          f"  HUB_ISFC_PLATFORM_TENANT_ID={tenant_ids[PLATFORM_KEY]}\n"
          f"  HUB_OPA_CLIENT_ID={backend_id}\n"
          "Still manual: give platform-team users HUB_ADMIN / HUB_EDITOR / HUB_VIEWER in the platform tenant, "
          "and tenant users HUB_TENANT_ADMIN / HUB_TENANT_VIEWER in their tenants (user-service tenant rows), "
          "then rebuild the integration-hub OPA bundle (authz-policies/integration-hub).")


if __name__ == "__main__":
    main()
