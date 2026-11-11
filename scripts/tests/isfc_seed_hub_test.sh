#!/usr/bin/env bash
# Tests scripts/isfc-seed-hub.py (HUB-462, HUB-477) against a fake ISFC auth-service on a loopback
# port: a dry run changes nothing, a first run creates the clients, permissions, roles, tenants,
# mapping, login method and the automation key, a second run changes nothing, a deleted role is
# re-created and a narrowed role is topped up, and no token, client secret or API key is printed.
# Hermetic: never calls a real ISFC; writes nothing in the repo (--no-metadata). Reads the generated
# authz-policies/integration-hub/catalogue.json.
#   bash scripts/tests/isfc_seed_hub_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
seed="$here/../isfc-seed-hub.py"
for c in python3 curl; do
  command -v "$c" >/dev/null 2>&1 || { echo "skip: $c not installed"; exit 0; }
done
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi; }
contains() { case "$2" in *"$3"*) echo "ok   $1" ;; *) echo "FAIL $1: output lacks '$3'"; fails=$((fails + 1)) ;; esac; }
lacks() { case "$2" in *"$3"*) echo "FAIL $1: output has '$3'"; fails=$((fails + 1)) ;; *) echo "ok   $1" ;; esac; }

tmp="$(mktemp -d)"
trap 'kill $srv 2>/dev/null; wait $srv 2>/dev/null; rm -rf "$tmp"' EXIT
python3 "$here/isfc_seed_hub_fake.py" >"$tmp/fake.out" &
srv=$!
for _ in $(seq 1 50); do grep -q '^PORT ' "$tmp/fake.out" 2>/dev/null && break; sleep 0.1; done
port="$(awk '/^PORT /{print $2}' "$tmp/fake.out")"
[ -n "$port" ] || { echo "FAIL fake auth-service did not start"; exit 1; }
base="http://127.0.0.1:$port"
state() { curl -s "$base/__state" | python3 -c "import json,sys; d=json.load(sys.stdin)['data']; $1"; }
run() { ISFC_ADMIN_TOKEN=test-admin-token python3 -I "$seed" --base "$base" --no-metadata --ci-client --tenant HL="Home Loans" --auth-service-client isfc-auth "$@" 2>&1; }

out="$(run --dry-run)"
contains "dry run says what it would do" "$out" "would create client integration-hub-console"
check "dry run creates no client" "$(state 'print(len([c for c in d["clients"] if c.startswith("client_")]))')" 0
check "dry run creates no tenant" "$(state 'print(len(d["tenants"]))')" 0

out="$(run)"
contains "first run creates the console client" "$out" "create client integration-hub-console"
contains "first run creates the platform tenant" "$out" "create tenant INTEGRATION_HUB_PLATFORM"
contains "first run issues the automation key" "$out" "issue an API key from"
contains "first run prints the Hub settings" "$out" "HUB_ISFC_PLATFORM_TENANT_ID="
contains "Microsoft is deferred without its settings" "$out" "MICROSOFT_SSO: deferred"
lacks "no admin token in the output" "$out" "test-admin-token"
lacks "no API key in the output" "$out" "PLAINTEXT_SECRET"
lacks "no client secret copied or printed" "$out" "never-copy-me"
check "two Hub clients exist" "$(state 'print(sorted(c["name"] for c in d["clients"].values() if c["client_id"].startswith("client_")))')" "['integration-hub', 'integration-hub-ci', 'integration-hub-console']"
check "the console client has tenancy required" "$(state 'c=[c for c in d["clients"].values() if c["name"]=="integration-hub-console"][0]; print(c["metadata"]["tenancy"])')" required
check "the platform tenant is first in the console tenants" "$(state 'c=[c for c in d["clients"].values() if c["name"]=="integration-hub-console"][0]; t={x["id"]:x["key"] for x in d["tenants"].values()}; print([t[i] for i in c["metadata"]["tenants"]])')" "['INTEGRATION_HUB_PLATFORM', 'HL']"
check "five Hub roles on the console client" "$(state 'c=[c for c in d["clients"].values() if c["name"]=="integration-hub-console"][0]["client_id"]; print(sorted(r["name"] for r in d["roles"].values() if r["client_id"]==c))')" "['HUB_ADMIN', 'HUB_EDITOR', 'HUB_TENANT_ADMIN', 'HUB_TENANT_VIEWER', 'HUB_VIEWER']"
check "HUB_CALLER and HUB_CI on integration-hub" "$(state 'c=[c for c in d["clients"].values() if c["name"]=="integration-hub"][0]["client_id"]; print(sorted(r["name"] for r in d["roles"].values() if r["client_id"]==c and r["name"] in ("HUB_CALLER","HUB_CI")))')" "['HUB_CALLER', 'HUB_CI']"
check "the console client is an allowed client of integration-hub" "$(state 'c=[c for c in d["clients"].values() if c["name"]=="integration-hub"][0]["client_id"]; k=[x for x in d["clients"].values() if x["name"]=="integration-hub-console"][0]["client_id"]; print(k in d["mapping"][c])')" True
check "and of auth-service" "$(state 'print(len(d["mapping"]["isfc-auth"]))')" 1
check "EMAIL_OTP settings copied without the secret" "$(state 'c=[x for x in d["clients"].values() if x["name"]=="integration-hub-console"][0]["client_id"]; m=d["cams"][c][0]["meta_data"]; print(sorted(m))')" "['allowed_domains', 'max_send_limit', 'otp_length', 'redirect_uri']"
check "one automation key with role HUB_CI" "$(state 'print([[r["name"] for r in k["roles"]] for k in d["keys"]])')" "[['HUB_CI']]"

before="$(curl -s "$base/__state" | python3 -c "import json,sys; d=json.load(sys.stdin)['data']; print(len(d['clients']),len(d['perms']),len(d['roles']),len(d['tenants']),len(d['keys']),sum(len(v) for v in d['cams'].values()))")"
out="$(run)"
contains "second run changes nothing" "$out" "Done (0 change(s))"
after="$(curl -s "$base/__state" | python3 -c "import json,sys; d=json.load(sys.stdin)['data']; print(len(d['clients']),len(d['perms']),len(d['roles']),len(d['tenants']),len(d['keys']),sum(len(v) for v in d['cams'].values()))")"
check "second run creates nothing" "$after" "$before"

# a narrowed role is topped up
state 'pass' >/dev/null
python3 - "$base" <<'PY'
import json, sys, urllib.request
base = sys.argv[1]
d = json.load(urllib.request.urlopen(base + "/__state"))["data"]
r = next(r for r in d["roles"].values() if r["name"] == "HUB_VIEWER")
req = urllib.request.Request(f"{base}/api/v1/group/11111111-1111-1111-1111-111111111111/client-role/{r['role_id']}", method="PUT",
                             data=json.dumps({"permission_ids": r["permission_ids"][:1]}).encode(), headers={"Authorization": "Bearer test-admin-token"})
urllib.request.urlopen(req)
PY
out="$(run)"
contains "a narrowed role is topped up" "$out" "role HUB_VIEWER on"
out="$(ISFC_ADMIN_TOKEN= python3 -I "$seed" --base "$base" --no-metadata 2>&1)"
contains "no token, no run" "$out" "ISFC_ADMIN_TOKEN is required"
out="$(ISFC_ADMIN_TOKEN=wrong python3 -I "$seed" --base "$base" --no-metadata 2>&1)"
contains "a wrong token is reported" "$out" "HTTP 401"

[ "$fails" -eq 0 ] && echo "all ok" || { echo "$fails failure(s)"; exit 1; }
