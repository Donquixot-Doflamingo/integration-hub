#!/usr/bin/env bash
# Tests for examples/lib/local-hub.sh (HUB-456): default ports, refusing to start on a busy port,
# never programming a mockvendor it did not start, stale pid files, non-JSON bodies in hub_post.
# Hermetic: its own listeners on free ports, a temp state dir and a fake checkout root; it does not
# build, start the Hub or touch 8080/8081/8098/8099/5173, docker or a database.
#   bash scripts/tests/local_hub_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
command -v python3 >/dev/null 2>&1 || { echo "skip: python3 not installed"; exit 0; }

fails=0
check() { # check <name> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi
}
contains() { # contains <name> <text> <needle>
  case "$2" in *"$3"*) echo "ok   $1" ;; *) echo "FAIL $1: lacks '$3' in: $2"; fails=$((fails + 1)) ;; esac
}

tmp="$(mktemp -d)"
srv="" fake=""
trap 'kill $srv $fake 2>/dev/null; rm -rf "$tmp"' EXIT

export HUB_PG_DSN='postgres://x:x@127.0.0.1:1/x' # keeps the lib from choosing its own database
# shellcheck source=../../examples/lib/local-hub.sh
source "$here/../../examples/lib/local-hub.sh"

# Defaults must stay clear of `make dev-up` (8080, 8081, 8098, 8099, 5173, 19190-19193).
check "gateway default" "$HUB_DEMO_HTTP" "127.0.0.1:18080"
check "ops default" "$HUB_DEMO_OPS" "127.0.0.1:18090"
check "mockvendor default" "$MOCKVENDOR_ADDR" "127.0.0.1:18099"
check "mockvendor control default" "$MOCKVENDOR_CONTROL_ADDR" "127.0.0.1:18098"

# A busy port and an HTML 502 server, both ours.
python3 - "$tmp/ports" <<'PY' &
import http.server, socket, sys, threading, time

class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        self.send_response(502)
        self.send_header("Content-Type", "text/html")
        body = b"<html>Bad Gateway from a proxy</html>"
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a):
        pass

busy = socket.socket(); busy.bind(("127.0.0.1", 0)); busy.listen(8)
web = http.server.HTTPServer(("127.0.0.1", 0), H)
threading.Thread(target=web.serve_forever, daemon=True).start()
open(sys.argv[1], "w").write("%d %d" % (busy.getsockname()[1], web.server_address[1]))
time.sleep(60)
PY
srv=$!
for _ in $(seq 1 50); do [ -s "$tmp/ports" ] && break; sleep 0.1; done
read -r busy_port web_port <"$tmp/ports"

# _hub_require_free: refuses a busy port with a message naming the port and the culprits.
out="$( (_hub_require_free "127.0.0.1:$busy_port" "example mockvendor") 2>&1)"
rc=$?
check "busy port: refused" "$rc" 1
contains "busy port: names the port" "$out" "port $busy_port (example mockvendor"
contains "busy port: names dev-up" "$out" "make dev-up"
contains "busy port: names the pid files" "$out" ".demo/*/pids"
out="$( (_hub_require_free "127.0.0.1:1" "example gateway") 2>&1)"
check "free port: allowed" "$?" 0

# mock_scenario must not send anything when this script did not start the mockvendor.
HUB_DEMO_STATE="$tmp/state"
mkdir -p "$HUB_DEMO_STATE/pids"
out="$( (mock_scenario /nonexistent.json) 2>&1)"
check "mock_scenario without a started mockvendor: refused" "$?" 1
contains "mock_scenario: message" "$out" "refusing to program a mockvendor this script did not start"

# Stale pid files: a reused pid is not signalled; a real (fake) hub gateway is stopped.
_hub_root="$tmp/root"
_hub_bin="$_hub_root/backend/bin"
mkdir -p "$_hub_bin" "$_hub_root/.demo/one/pids"
printf '#!/bin/bash\nsleep 88\n' >"$_hub_bin/hub"
chmod +x "$_hub_bin/hub"
sleep 89 &
bystander=$!
"$_hub_bin/hub" gateway &
fake=$!
echo "$bystander" >"$_hub_root/.demo/one/pids/mockvendor.pid"
echo "$fake" >"$_hub_root/.demo/one/pids/gateway.pid"
sleep 0.3
out="$(_hub_stop_stale 2>&1)"
contains "stale scan: reports the stopped gateway" "$out" "stopped a gateway left running"
if kill -0 "$bystander" 2>/dev/null; then alive=alive; else alive=killed; fi
check "stale scan: unrelated pid left alone" "$alive" alive
if kill -0 "$fake" 2>/dev/null; then alive=alive; else alive=stopped; fi
wait "$fake" 2>/dev/null
if kill -0 "$fake" 2>/dev/null; then alive=alive; else alive=stopped; fi
check "stale scan: hub gateway stopped" "$alive" stopped
check "stale scan: pid files removed" "$(ls "$_hub_root/.demo/one/pids")" ""
kill "$bystander" 2>/dev/null

# hub_post prints a non-JSON body as it came (it used to print nothing).
HUB_URL="http://127.0.0.1:$web_port"
HUB_DEMO_KEY=test
out="$(hub_post /x "" "" '{}' 2>&1)"
contains "hub_post: status shown" "$out" "< HTTP 502"
contains "hub_post: raw non-JSON body shown" "$out" "< <html>Bad Gateway from a proxy</html>"

if [ "$fails" = 0 ]; then echo "all passed"; else echo "$fails failed"; exit 1; fi
