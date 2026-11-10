#!/usr/bin/env bash
# Guard tests for scripts/dev-up.sh (HUB-456): a relative DEV_STATE, stale pid files and the
# "port already in use" message. Hermetic: dev-up is stopped at its port check, before it builds or
# starts anything, by a listener this test opens itself. It never touches 5173/8080/8081/8098/8099,
# docker or a database, and writes only under a temp directory.
#   bash scripts/tests/dev_up_guards_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
up="$here/../dev-up.sh"

for d in /opt/homebrew/opt/node@22/bin /usr/local/opt/node@22/bin; do [ -d "$d" ] && PATH="$d:$PATH"; done
for c in go node npm curl python3; do
  command -v "$c" >/dev/null 2>&1 || { echo "skip: $c not installed"; exit 0; }
done
[ "$(node -p 'process.versions.node.split(".")[0]')" = 22 ] || { echo "skip: node 22 is required by dev-up"; exit 0; }

fails=0
check() { # check <name> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi
}
contains() { # contains <name> <text> <needle>
  case "$2" in *"$3"*) echo "ok   $1" ;; *) echo "FAIL $1: output lacks '$3'"; fails=$((fails + 1)) ;; esac
}

tmp="$(mktemp -d)"
srv=""
trap 'kill $srv 2>/dev/null; rm -rf "$tmp"' EXIT

# Two free loopback ports held open for the whole test: one plays Redis (dev-up only connects),
# one is the "console port" that makes dev-up stop.
python3 - "$tmp/ports" <<'PY' &
import socket, sys, time
socks = []
ports = []
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

mkdir -p "$tmp/work/st/pids"
echo 1 >"$tmp/work/st/pids/gateway.pid"      # pid 1 is alive but is not our hub gateway: stale
echo 999999 >"$tmp/work/st/pids/admin.pid"   # dead pid: stale
echo junk >"$tmp/work/st/pids/console.pid"   # garbage: stale

out="$(cd "$tmp/work" && DEV_STATE=st DEV_DOCKER=0 HUB_PG_DSN='postgres://x:x@127.0.0.1:1/x' HUB_REDIS_ADDR="127.0.0.1:$redis_port" \
  DEV_CONSOLE_PORT="$busy_port" DEV_GATEWAY_PORT=1 DEV_ADMIN_PORT=1 DEV_MOCKVENDOR_PORT=1 DEV_MOCKVENDOR_CONTROL_PORT=1 DEV_OPS_PORT_BASE=1 \
  "$up" 2>&1)"
rc=$?

check "exit status" "$rc" 1
contains "message names the port" "$out" "port $busy_port is already in use"
contains "message points at dev-down" "$out" "scripts/dev-down.sh"
contains "message mentions the state dir (absolute)" "$out" "DEV_STATE=$tmp/work/st"
contains "message lists the stale pid files" "$out" "stale pid files removed: console.pid gateway.pid admin.pid"
check "relative DEV_STATE resolved against the caller's directory" "$([ -d "$tmp/work/st/logs" ] && echo yes || echo no)" yes
check "stale pid files removed" "$(ls "$tmp/work/st/pids")" ""

# dev-down with a pid file whose pid now belongs to an unrelated program (a reused pid): it must
# report the file as stale, delete it and leave the program alone. Own state dir, nothing shared.
sleep 77 &
bystander=$!
mkdir -p "$tmp/down/pids"
echo "$bystander" >"$tmp/down/pids/gateway.pid"
echo "$bystander" >"$tmp/down/pids/console.pid"
out="$(DEV_STATE="$tmp/down" "$here/../dev-down.sh" 2>&1)"
check "dev-down exit status" "$?" 0
contains "dev-down reports the stale pid file" "$out" "gateway.pid was stale"
contains "dev-down stopped nothing" "$out" "stopped: nothing was running"
check "dev-down left the unrelated process alive" "$(kill -0 "$bystander" 2>/dev/null && echo alive || echo killed)" alive
check "dev-down removed the pid files" "$(ls "$tmp/down/pids")" ""
kill "$bystander" 2>/dev/null

if [ "$fails" = 0 ]; then echo "all passed"; else echo "$fails failed"; exit 1; fi
