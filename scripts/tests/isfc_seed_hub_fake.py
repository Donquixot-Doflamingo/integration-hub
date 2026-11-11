#!/usr/bin/env python3
"""A fake ISFC auth-service for isfc_seed_hub_test.sh: just the admin endpoints scripts/isfc-seed-hub.py
uses, in memory, on a loopback port. Prints "PORT <n>" once ready and the request log on exit
(GET /__state dumps the state). Tests never call a real ISFC."""
import json
import re
import sys
import uuid
from http.server import BaseHTTPRequestHandler, HTTPServer

TOKEN = "test-admin-token"
GROUP = "11111111-1111-1111-1111-111111111111"
S = {"clients": {}, "perms": {}, "roles": {}, "tenants": {}, "mapping": {}, "keys": [], "cams": {}, "log": []}
METHODS = [{"method_id": "m-otp", "method_code": "EMAIL_OTP", "status": "ACTIVE"}, {"method_id": "m-ms", "method_code": "MICROSOFT_SSO", "status": "ACTIVE"}]
# a pre-existing client whose EMAIL_OTP settings get copied (with a secret that must not be copied)
S["clients"]["isfc-admin-ui"] = {"client_id": "isfc-admin-ui", "name": "ISFC Admin UI", "status": "ACTIVE", "metadata": {}}
S["cams"]["isfc-admin-ui"] = [{"auth_method_id": "m-otp", "status": "ACTIVE", "meta_data": {"otp_length": 6, "max_send_limit": 5, "client_secret": "never-copy-me", "otp_hash": "x"}}]
S["mapping"]["isfc-auth"] = []


def new_id(prefix):
    return prefix + uuid.uuid4().hex[:12]


class H(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def reply(self, status, data=None):
        raw = json.dumps({"data": data} if data is not None else {}).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def handle_any(self, method):
        n = int(self.headers.get("Content-Length") or 0)
        body = json.loads(self.rfile.read(n)) if n else None
        path = self.path.split("?")[0]
        if path == "/__state":
            return self.reply(200, S)
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self.reply(401, None)
        S["log"].append(f"{method} {path}")
        g = rf"/api/v1/group/{GROUP}"
        m = None

        def route(pattern):
            nonlocal m
            m = re.fullmatch(pattern, path)
            return m

        if route(g + r"/client") and method == "GET":
            return self.reply(200, list(S["clients"].values()))
        if route(g + r"/client") and method == "POST":
            cid = new_id("client_")
            S["clients"][cid] = {"client_id": cid, **body, "status": "ACTIVE"}
            return self.reply(201, S["clients"][cid])
        if route(g + r"/client/([^/]+)") and method in ("GET", "PUT") and m.group(1) != "key":
            c = S["clients"].get(m.group(1))
            if not c:
                return self.reply(404, None)
            if method == "PUT":
                c.update(body)
            return self.reply(200, c)
        if route(g + r"/client/([^/]+)/permission") and method == "GET":
            return self.reply(200, [p for p in S["perms"].values() if p["client_id"] == m.group(1)])
        if route(g + r"/client-permission") and method == "POST":
            p = {"permission_id": new_id("perm_"), "name": body["name"], "client_id": body["client_id"], "status": "ACTIVE"}
            S["perms"][p["permission_id"]] = p
            return self.reply(201, p)
        if route(g + r"/client/([^/]+)/role") and method == "GET":
            return self.reply(200, [r for r in S["roles"].values() if r["client_id"] == m.group(1)])
        if route(g + r"/client-role") and method == "POST":
            r = {"role_id": new_id("role_"), "name": body["name"], "client_id": body["client_id"], "permission_ids": body["permission_ids"], "status": "ACTIVE"}
            S["roles"][r["role_id"]] = r
            return self.reply(201, r)
        if route(g + r"/client-role/([^/]+)") and method in ("GET", "PUT"):
            r = S["roles"].get(m.group(1))
            if not r:
                return self.reply(404, None)
            if method == "PUT":
                r["permission_ids"] = body["permission_ids"]
            perms = [{**S["perms"][i], "status": "ACTIVE"} for i in r["permission_ids"]]
            return self.reply(200, {**r, "permissions": perms})
        if route(r"/api/v1/tenant") and method == "GET":
            return self.reply(200, list(S["tenants"].values()))
        if route(r"/api/v1/tenant") and method == "POST":
            t = {"id": str(uuid.uuid4()), "key": body["key"], "name": body["name"], "status": "ACTIVE"}
            S["tenants"][t["id"]] = t
            return self.reply(201, t)
        if route(r"/api/v1/tenant/([^/]+)") and method == "PATCH":
            S["tenants"][m.group(1)].update(body)
            return self.reply(200, S["tenants"][m.group(1)])
        if route(g + r"/client-mapping/([^/]+)"):
            if method == "PUT":
                S["mapping"][m.group(1)] = body["allowed_client_ids"]
            return self.reply(200, {"allowed_client_ids": [{"client_id": c, "status": "ACTIVE"} for c in S["mapping"].get(m.group(1), [])]})
        if route(g + r"/client/([^/]+)/key") and method == "GET":
            return self.reply(200, [k for k in S["keys"] if k["target"] == m.group(1)])
        if route(g + r"/client/([^/]+)/key") and method == "POST":
            k = {"id": new_id("key_"), "target": m.group(1), "key_name": body["key_name"], "allowed_client_id": body["allowed_client_id"],
                 "status": "ACTIVE", "roles": body["roles"], "key": "isfck_PLAINTEXT_SECRET_VALUE"}
            S["keys"].append(k)
            return self.reply(201, k)
        if route(g + r"/client/key/([^/]+)") and method == "GET":
            return self.reply(200, next(k for k in S["keys"] if k["id"] == m.group(1)))
        if route(r"/api/v1/auth-method") and method == "GET":
            return self.reply(200, METHODS)
        if route(g + r"/client-auth-method/client/([^/]+)") and method == "GET":
            return self.reply(200, S["cams"].get(m.group(1), []))
        if route(g + r"/client-auth-method/([^/]+)") and method == "POST":
            S["cams"].setdefault(m.group(1), []).append({"auth_method_id": body["auth_method_id"], "status": "ACTIVE", "meta_data": body.get("meta_data", {})})
            return self.reply(201, {})
        return self.reply(404, None)

    def do_GET(self):
        self.handle_any("GET")

    def do_POST(self):
        self.handle_any("POST")

    def do_PUT(self):
        self.handle_any("PUT")

    def do_PATCH(self):
        self.handle_any("PATCH")


srv = HTTPServer(("127.0.0.1", 0), H)
print("PORT", srv.server_address[1], flush=True)
try:
    srv.serve_forever()
except KeyboardInterrupt:
    pass
sys.exit(0)
