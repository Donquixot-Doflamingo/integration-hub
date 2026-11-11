#!/usr/bin/env bash
# Tests `scripts/dev-up.sh --isfc` (HUB-497): the ISFC settings are validated before anything
# starts, unknown arguments are refused, and the admin and console environments carry ISFC mode
# (never the dev issuer). Hermetic: dev-up is stopped by its own guards (or by a busy port this
# test opens), so it builds nothing, starts nothing, and never calls an ISFC, docker or a database.
#   bash scripts/tests/dev_up_isfc_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
up="$here/../dev-up.sh"
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi; }
contains() { case "$2" in *"$3"*) echo "ok   $1" ;; *) echo "FAIL $1: output lacks '$3'"; fails=$((fails + 1)) ;; esac; }
lacks() { case "$2" in *"$3"*) echo "FAIL $1: output has '$3'"; fails=$((fails + 1)) ;; *) echo "ok   $1" ;; esac; }

tmp="$(mktemp -d)"
srv=""
trap 'kill $srv 2>/dev/null; rm -rf "$tmp"' EXIT
clean_env() { env -i PATH="$PATH" HOME="$tmp" "$@"; }

# --- the guards need nothing but bash ---------------------------------------------------------------
out="$(clean_env DEV_STATE="$tmp/st" "$up" --isfc 2>&1)"
rc=$?
check "no settings: exit status" "$rc" 1
for name in ISFC_AUTH_URL ISFC_OIDC_ISSUER ISFC_ADMIN_UPSTREAM HUB_OPA_URL; do
  contains "no settings: names $name" "$out" "$name is required with --isfc"
done
check "no settings: nothing was created" "$([ -e "$tmp/st" ] && echo created || echo none)" none

out="$(clean_env DEV_STATE="$tmp/st" ISFC_AUTH_URL=auth.local ISFC_OIDC_ISSUER=ftp://x ISFC_ADMIN_UPSTREAM='http://gw/hub admin' HUB_OPA_URL=http://127.0.0.1:8181 "$up" --isfc 2>&1)"
rc=$?
check "malformed URLs: exit status" "$rc" 1
contains "malformed auth URL" "$out" "ISFC_AUTH_URL must be an absolute http or https URL"
contains "malformed issuer" "$out" "ISFC_OIDC_ISSUER must be an absolute http or https URL"
contains "malformed upstream" "$out" "ISFC_ADMIN_UPSTREAM must be an absolute http or https URL"
lacks "a valid URL is not blamed" "$out" "HUB_OPA_URL must"

out="$(clean_env "$up" --isfc --nope 2>&1)"
rc=$?
check "unknown argument: exit status" "$rc" 2
contains "unknown argument message" "$out" "unknown argument --nope"
out="$(clean_env "$up" --help 2>&1)"
contains "--help mentions --isfc" "$out" "dev-up.sh --isfc"

# --- the environments handed to the roles --------------------------------------------------------------
# shellcheck source=../lib/isfc.sh
source "$here/../lib/isfc.sh"
export ISFC_AUTH_URL=http://127.0.0.1:8300 ISFC_OIDC_ISSUER=http://127.0.0.1:4444 ISFC_ADMIN_UPSTREAM=http://127.0.0.1:8300/hub/admin HUB_OPA_URL=http://127.0.0.1:8181
isfc_check >/dev/null
check "valid settings pass the check" "$?" 0
admin="$(isfc_admin_env)"
contains "admin runs in ISFC mode" "$admin" "HUB_ADMIN_AUTH_MODE=isfc"
contains "admin gets the issuer" "$admin" "HUB_OIDC_ISSUER=http://127.0.0.1:4444"
contains "admin gets OPA" "$admin" "HUB_OPA_URL=http://127.0.0.1:8181"
contains "console client defaults" "$admin" "HUB_ISFC_CONSOLE_CLIENT_ID=integration-hub-console"
lacks "no platform tenant unless set" "$admin" "PLATFORM_TENANT"
console="$(isfc_console_env)"
contains "console in ISFC mode" "$console" "PUBLIC_AUTH_MODE=isfc"
contains "console upstream is the gateway route" "$console" "HUB_ADMIN_UPSTREAM=http://127.0.0.1:8300/hub/admin"
lacks "console has no dev issuer" "$console" "HUB_DEV_OIDC_ISSUER"
lacks "admin has no dev issuer" "$admin" "dev"
ISFC_PLATFORM_TENANT_ID=11111111-1111-4111-8111-111111111111 ISFC_GATEWAY_URL=http://127.0.0.1:8300
contains "platform tenant reaches the admin" "$(isfc_admin_env)" "HUB_ISFC_PLATFORM_TENANT_ID=11111111-1111-4111-8111-111111111111"
contains "gateway URL reaches the console" "$(isfc_console_env)" "HUB_ISFC_GATEWAY_URL=http://127.0.0.1:8300"

# The default (dev issuer) console start keeps working: the console refuses PUBLIC_AUTH_MODE=dev
# without NODE_ENV=test, and only that one line may set it.
check "default console start sets NODE_ENV=test with the dev mode" "$(grep -c 'NODE_ENV=test PUBLIC_AUTH_MODE=dev' "$up")" 1
check "ISFC console start never sets NODE_ENV=test" "$(isfc_console_env | grep -c NODE_ENV)" 0

# --- valid settings get past the guard: dev-up then stops at a busy port, before building anything ------------
for d in /opt/homebrew/opt/node@22/bin /usr/local/opt/node@22/bin; do [ -d "$d" ] && PATH="$d:$PATH"; done
tools=1
for c in go node npm curl python3; do command -v "$c" >/dev/null 2>&1 || tools=0; done
[ "$tools" = 1 ] && [ "$(node -p 'process.versions.node.split(".")[0]')" = 22 ] || { echo "skip: go, node 22, npm, curl and python3 are needed for the busy-port case"; tools=0; }
if [ "$tools" = 1 ]; then
  python3 - "$tmp/ports" <<'PY' &
import socket, sys, time
socks, ports = [], []
for _ in range(2):
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    s.listen(8)
    socks.append(s)
    ports.append(s.getsockname()[1])
open(sys.argv[1], "w").write(" ".join(map(str, ports)))
time.sleep(60)
PY
  srv=$!
  for _ in $(seq 1 50); do [ -s "$tmp/ports" ] && break; sleep 0.1; done
  read -r redis_port busy_port <"$tmp/ports"
  out="$(cd "$tmp" && clean_env PATH="$PATH" DEV_STATE=st2 DEV_DOCKER=0 HUB_PG_DSN='postgres://x:x@127.0.0.1:1/x' HUB_REDIS_ADDR="127.0.0.1:$redis_port" \
    ISFC_AUTH_URL="$ISFC_AUTH_URL" ISFC_OIDC_ISSUER="$ISFC_OIDC_ISSUER" ISFC_ADMIN_UPSTREAM="$ISFC_ADMIN_UPSTREAM" HUB_OPA_URL="$HUB_OPA_URL" \
    DEV_CONSOLE_PORT="$busy_port" DEV_GATEWAY_PORT=1 DEV_ADMIN_PORT=1 DEV_MOCKVENDOR_PORT=1 DEV_MOCKVENDOR_CONTROL_PORT=1 DEV_OPS_PORT_BASE=1 \
    "$up" --isfc 2>&1)"
  rc=$?
  check "valid settings: dev-up reaches its port check" "$rc" 1
  contains "valid settings: the guard let it through" "$out" "port $busy_port is already in use"
  lacks "valid settings: not refused as unconfigured" "$out" "--isfc is not configured"
fi

[ "$fails" = 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "all ok"
