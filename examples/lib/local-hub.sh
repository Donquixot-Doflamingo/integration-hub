#!/usr/bin/env bash
# Shared helpers for the runnable examples (HUB-351, HUB-352, HUB-353): start a local Hub gateway
# (and mockvendor) with the repository's dev config plus one example overlay, on ports that do
# not clash with `make run` / `make mockvendor` / `make dev-up`. Source it; do not run it.
#
#   source "$root/examples/lib/local-hub.sh"
#   hub_setup <state-dir> <overlay-dir>     # build, merge config, migrate, dev key
#   hub_set_creds <account> KEY=VALUE...    # encrypted local credential file (hubctl creds set;
#                                           # not needed for an auth none vendor)
#   hub_start [--mockvendor]                # start the gateway (and mockvendor), wait until ready
#   mock_scenario <file.json>               # program mockvendor with a scenario file
#
# Safety (HUB-456): the default ports (gateway 18080/18090, mockvendor 18099/18098) are outside the
# dev stack's (8080/8081/8098/8099/5173/19190-19193). hub_start refuses to start when a port it needs
# is already listening, mock_scenario only talks to the mockvendor this script started, and pid files
# under <state>/pids let the next hub_setup stop an orphan left by a SIGKILLed or crashed run
# (only after checking the process is the Hub binary).
#
# Needs: Go (to build), curl, python3, and Postgres + Redis
# (docker compose -f deploy/docker-compose.yml up -d). Everything stays on this machine except
# what a sandbox target explicitly calls.

# Ports (override with the environment). hub_setup rewrites the overlay's mock region to
# MOCKVENDOR_ADDR. Never default to 8099/8098: those are `make dev-up`'s mockvendor.
HUB_DEMO_HTTP="${HUB_DEMO_HTTP:-127.0.0.1:18080}"
HUB_DEMO_OPS="${HUB_DEMO_OPS:-127.0.0.1:18090}"
MOCKVENDOR_ADDR="${MOCKVENDOR_ADDR:-127.0.0.1:18099}"
MOCKVENDOR_CONTROL_ADDR="${MOCKVENDOR_CONTROL_ADDR:-127.0.0.1:18098}"
HUB_URL="http://$HUB_DEMO_HTTP"
# The examples use their own database unless HUB_PG_DSN is set: sharing the dev stack's database
# (make dev-up, the console) made an example's gateway see the console's published snapshots
# (HUB-445). hub_setup creates hub_examples when it is missing.
if [ -z "${HUB_PG_DSN:-}" ]; then
  export HUB_PG_DSN="postgres://hub:hub@localhost:5432/hub_examples?sslmode=disable"
  _hub_own_db=hub_examples
fi
export HUB_REDIS_ADDR="${HUB_REDIS_ADDR:-localhost:6379}"
export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}" no_proxy="${no_proxy:-localhost,127.0.0.1,::1}"

_hub_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
_hub_bin="$_hub_root/backend/bin"
_hub_pids=()
_hub_mock_pid="" # set only when this script started the mockvendor it will program
# shellcheck source=../../scripts/lib/pids.sh
source "$_hub_root/scripts/lib/pids.sh"

hub_log() { printf '\033[2m%s\033[0m %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
hub_die() { printf 'error: %s\n' "$*" >&2; exit 1; }

# _hub_ensure_db NAME: create database NAME on the local dev Postgres when it does not exist yet,
# through the docker compose service or psql, whichever is there.
_hub_ensure_db() {
  local db="$1" admin="postgres://hub:hub@localhost:5432/hub?sslmode=disable"
  local q="SELECT 1 FROM pg_database WHERE datname = '$db'"
  local compose=(docker compose -f "$_hub_root/deploy/docker-compose.yml")
  if command -v docker >/dev/null 2>&1 && [ -n "$("${compose[@]}" ps -q postgres 2>/dev/null)" ]; then
    "${compose[@]}" exec -T postgres psql -U hub -d hub -tAc "$q" | grep -q 1 ||
      "${compose[@]}" exec -T postgres createdb -U hub "$db"
  elif command -v psql >/dev/null 2>&1; then
    psql "$admin" -tAc "$q" | grep -q 1 || psql "$admin" -qc "CREATE DATABASE $db"
  else
    return 1
  fi
}

# _hub_listening HOST:PORT: succeeds when something accepts connections there.
_hub_listening() { (exec 3<>"/dev/tcp/${1%:*}/${1##*:}") 2>/dev/null; }

# _hub_listener_pids PORT: pids listening on PORT (needs lsof; prints nothing without it).
_hub_listener_pids() {
  command -v lsof >/dev/null 2>&1 || return 0
  lsof -nP -iTCP:"$1" -sTCP:LISTEN -t 2>/dev/null || true
}

# _hub_require_free ADDR WHAT: stop with a clear message when ADDR is already listening, instead of
# starting a process that cannot bind and then talking to whatever owns the port.
_hub_require_free() {
  local addr="$1" what="$2" port="${1##*:}" owner
  _hub_listening "$addr" || return 0
  owner="$(_hub_listener_pids "$port" | head -1)"
  hub_die "port $port ($what, $addr) is already in use${owner:+ by pid $owner ($(pid_cmdline "$owner" | cut -c1-100))}.
  Likely culprit: the dev stack (make dev-up; scripts/dev-down.sh stops it), or an earlier example run
  that was killed and left its gateway or mockvendor running (stale pid files: $_hub_root/.demo/*/pids).
  Free the port, or move this run with HUB_DEMO_HTTP, HUB_DEMO_OPS, MOCKVENDOR_ADDR, MOCKVENDOR_CONTROL_ADDR.
  This script does not start anything on, or send control calls to, a port it does not own."
}

# _hub_pidfile_needle NAME: the command line fragment that identifies NAME's process.
_hub_pidfile_needle() {
  case "$1" in
  mockvendor) printf '%s\n' "$_hub_bin/mockvendor" ;;
  *) printf '%s\n' "$_hub_bin/hub $1" ;;
  esac
}

# _hub_stop_stale: stop gateways and mockvendors left by earlier example runs (SIGKILL, crash:
# the EXIT trap does not run). Only pids whose command line is this checkout's Hub binary are
# signalled; any other pid file is just removed.
_hub_stop_stale() {
  local f name st
  for f in "$_hub_root"/.demo/*/pids/*.pid; do
    [ -f "$f" ] || continue
    name="$(basename "$f" .pid)"
    st="$(stop_pidfile "$f" "$(_hub_pidfile_needle "$name")")"
    [ "$st" != live ] || hub_log "stopped a $name left running by an earlier example run ($f)"
  done
}

# _merge_overlay SRC DST: copy every file of SRC into DST, refusing to overwrite (the overlay
# only adds objects, like the Go tests' mergeOverlay).
_merge_overlay() {
  local src="$1" dst="$2" f rel
  [ -d "$src" ] || return 0
  while IFS= read -r -d '' f; do
    rel="${f#"$src"/}"
    if [ -e "$dst/$rel" ]; then
      hub_die "overlay $src would overwrite $rel"
    fi
    mkdir -p "$(dirname "$dst/$rel")"
    cp "$f" "$dst/$rel"
  done < <(find "$src" -type f -print0)
}

hub_setup() {
  local state="$1" overlay="$2"
  HUB_DEMO_STATE="$state"
  mkdir -p "$state/pids"
  _hub_stop_stale
  command -v curl >/dev/null || hub_die "curl is required"
  command -v python3 >/dev/null || hub_die "python3 is required"
  hub_log "building the Hub binaries (backend/bin)"
  make -C "$_hub_root/backend" build >/dev/null
  if [ -n "${_hub_own_db:-}" ]; then
    _hub_ensure_db "$_hub_own_db" >/dev/null 2>"$state/migrate.log" ||
      hub_die "cannot create the $_hub_own_db database (is Postgres up? docker compose -f deploy/docker-compose.yml up -d), or set HUB_PG_DSN; see $state/migrate.log"
  fi
  hub_log "checking Postgres ($HUB_PG_DSN) and migrating"
  "$_hub_bin/hub" migrate >/dev/null 2>"$state/migrate.log" ||
    hub_die "cannot migrate $HUB_PG_DSN (is Postgres up? docker compose -f deploy/docker-compose.yml up -d); see $state/migrate.log"
  rm -rf "$state/config" "$state/contracts" "$state/data"
  # Every schema lives in the config tree (ADR-0023): the overlay is config only.
  if [ -d "$overlay/contracts" ]; then
    hub_die "$overlay/contracts is the pre-ADR-0023 layout; schemas live in the config"
  fi
  cp -R "$_hub_root/backend/config" "$state/config"
  _merge_overlay "$overlay/config" "$state/config"
  # Point every mock region at the mockvendor this script starts (the configs say 127.0.0.1:8099,
  # the dev stack's port, which the examples do not use).
  if [ "$MOCKVENDOR_ADDR" != "127.0.0.1:8099" ]; then
    find "$state/config/vendors" -name '*.yaml' -exec sed -i.bak "s#http://127.0.0.1:8099#http://$MOCKVENDOR_ADDR#g" {} +
    find "$state/config/vendors" -name '*.bak' -delete
  fi
  export HUB_DEV_KEY_FILE="$state/dev.key" HUB_CRED_SOURCE="localfile://$state/secrets.enc"
  rm -f "$HUB_DEV_KEY_FILE" "$state/secrets.enc"
  "$_hub_bin/hubctl" creds init-key >/dev/null
}

hub_set_creds() {
  "$_hub_bin/hubctl" creds set "$@" >/dev/null
}

# hub_set_region FILE REGION: switch an account file of the merged config to another region.
hub_set_region() {
  local file="$HUB_DEMO_STATE/config/accounts/$1"
  [ -f "$file" ] || hub_die "no account file $file"
  sed -i.bak "s/^region: .*/region: $2/" "$file" && rm -f "$file.bak"
}

_hub_cleanup() {
  local p
  trap - EXIT
  for p in ${_hub_pids[@]+"${_hub_pids[@]}"}; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -f "$HUB_DEMO_STATE"/pids/*.pid
}

# _hub_spawn NAME CMD...: run in the background with its log and pid file; sets _hub_last_pid.
_hub_spawn() {
  local name="$1"
  shift
  "$@" 2>"$HUB_DEMO_STATE/$name.log" &
  _hub_last_pid=$!
  _hub_pids+=("$_hub_last_pid")
  echo "$_hub_last_pid" >"$HUB_DEMO_STATE/pids/$name.pid"
}

# _wait_url URL NAME PID: wait for URL to answer while PID (the process this script started) is alive.
# A child that died (for example, it could not bind) is a failure even if something else answers.
_wait_url() {
  for _ in $(seq 1 150); do
    kill -0 "$3" 2>/dev/null || break
    curl -fsS "$1" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  hub_log "$2 did not become ready at $1 (pid $3); last log lines:"
  tail -20 "$HUB_DEMO_STATE/$2.log" >&2 || true
  exit 1
}

# _hub_verify_listener PID PORT NAME: PID must be what listens on PORT (when lsof can tell).
_hub_verify_listener() {
  local pids
  kill -0 "$1" 2>/dev/null || hub_die "$3 (pid $1) exited right after start; see $HUB_DEMO_STATE/$3.log"
  command -v lsof >/dev/null 2>&1 || return 0
  pids="$(_hub_listener_pids "$2")"
  case " $(echo $pids) " in
  *" $1 "*) ;;
  *) hub_die "port $2 is held by pid ${pids:-?}, not by the $3 this script started (pid $1); refusing to continue" ;;
  esac
}

hub_start() {
  # Check every port before starting anything, so a refusal leaves nothing behind.
  _hub_require_free "$HUB_DEMO_HTTP" "example gateway"
  _hub_require_free "$HUB_DEMO_OPS" "example gateway ops"
  if [ "${1:-}" = "--mockvendor" ]; then
    _hub_require_free "$MOCKVENDOR_ADDR" "example mockvendor"
    _hub_require_free "$MOCKVENDOR_CONTROL_ADDR" "example mockvendor control"
  fi
  trap _hub_cleanup EXIT
  trap 'exit 130' INT TERM HUP
  mkdir -p "$HUB_DEMO_STATE/pids"
  if [ "${1:-}" = "--mockvendor" ]; then
    _hub_spawn mockvendor "$_hub_bin/mockvendor" -addr "$MOCKVENDOR_ADDR" -control-addr "$MOCKVENDOR_CONTROL_ADDR"
    _hub_mock_pid="$_hub_last_pid"
    _wait_url "http://$MOCKVENDOR_CONTROL_ADDR/__control/healthz" mockvendor "$_hub_mock_pid"
    _hub_verify_listener "$_hub_mock_pid" "${MOCKVENDOR_CONTROL_ADDR##*:}" mockvendor
  fi
  _hub_spawn gateway "$_hub_bin/hub" gateway -http-addr "$HUB_DEMO_HTTP" -ops-addr "$HUB_DEMO_OPS" \
    -config-dir "$HUB_DEMO_STATE/config" \
    -data-dir "$HUB_DEMO_STATE/data" -blob-url "file://$HUB_DEMO_STATE/data/blobs" \
    -ready-drain 100ms -log-level "${HUB_LOG_LEVEL:-warn}"
  _wait_url "http://$HUB_DEMO_OPS/readyz" gateway "$_hub_last_pid"
  _hub_verify_listener "$_hub_last_pid" "${HUB_DEMO_OPS##*:}" gateway
  hub_log "gateway ready on $HUB_URL (logs: $HUB_DEMO_STATE/gateway.log)"
}

# mock_scenario FILE: reset mockvendor, then PUT every route of the scenario file
# ({"description", "routes": {id: RouteSpec}}, contracts/openapi/mockvendor.v1.yaml).
mock_scenario() {
  # Control calls reset the mockvendor's routes: only ever send them to the one this script started.
  [ -n "$_hub_mock_pid" ] || hub_die "mock_scenario needs hub_start --mockvendor: refusing to program a mockvendor this script did not start"
  _hub_verify_listener "$_hub_mock_pid" "${MOCKVENDOR_CONTROL_ADDR##*:}" mockvendor
  pid_matches "$_hub_mock_pid" "$_hub_bin/mockvendor" || hub_die "pid $_hub_mock_pid is not the mockvendor this script started"
  python3 - "$1" "http://$MOCKVENDOR_CONTROL_ADDR" <<'PY'
import json, sys, urllib.request
path, base = sys.argv[1], sys.argv[2]
spec = json.load(open(path))
def send(method, url, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        r.read()
send("POST", base + "/__control/reset")
for rid, route in spec["routes"].items():
    send("PUT", base + "/__control/routes/" + rid, route)
print(spec.get("description", ""), file=sys.stderr)
PY
}

# hub_post PATH IDEMPOTENCY_KEY SESSION BODY: POST to the local gateway as the example tenant
# ($HUB_DEMO_KEY), print the request and the response, and leave the response headers in
# $HUB_DEMO_STATE/last.headers and the body in $HUB_DEMO_STATE/last.body.
hub_post() {
  local path="$1" key="$2" session="$3" body="$4" args=()
  args=(-sS -o "$HUB_DEMO_STATE/last.body" -D "$HUB_DEMO_STATE/last.headers" -w '%{http_code} %{time_total}'
    -H "Authorization: Bearer $HUB_DEMO_KEY" -H "Content-Type: application/json")
  [ -n "$key" ] && args+=(-H "Idempotency-Key: $key")
  [ -n "$session" ] && args+=(-H "X-Hub-Session: $session")
  printf '\n\033[1m> POST %s\033[0m\n' "$path"
  [ -n "$key" ] && printf '> Idempotency-Key: %s\n' "$key"
  [ -n "$session" ] && printf '> X-Hub-Session: %s\n' "$session"
  printf '> %s\n' "$body"
  local out
  out="$(curl "${args[@]}" -X POST --data "$body" "$HUB_URL$path")"
  printf '< HTTP %s in %ss\n' "${out% *}" "${out#* }"
  grep -i '^x-hub-' "$HUB_DEMO_STATE/last.headers" | tr -d '\r' | sed 's/^/< /'
  # Pretty-print JSON; any other body (an HTML 502 from a proxy, plain text) is printed as it came.
  if python3 -m json.tool "$HUB_DEMO_STATE/last.body" >"$HUB_DEMO_STATE/last.pretty" 2>/dev/null; then
    sed 's/^/< /' "$HUB_DEMO_STATE/last.pretty"
  else
    sed 's/^/< /' "$HUB_DEMO_STATE/last.body"
    [ ! -s "$HUB_DEMO_STATE/last.body" ] || echo
  fi
  HUB_LAST_STATUS="${out% *}"
}

# hub_last_header NAME: a header of the last hub_post response.
hub_last_header() {
  grep -i "^$1:" "$HUB_DEMO_STATE/last.headers" | head -1 | cut -d: -f2- | tr -d ' \r'
}
