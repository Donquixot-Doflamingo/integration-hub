#!/usr/bin/env bash
# Shared helpers for the runnable examples (HUB-351, HUB-352, HUB-353): start a local Hub gateway
# (and mockvendor) with the repository's dev config plus one example overlay, on ports that do
# not clash with `make run` / `make mockvendor`. Source it; do not run it.
#
#   source "$root/examples/lib/local-hub.sh"
#   hub_setup <state-dir> <overlay-dir>     # build, merge config + contracts, migrate, dev key
#   hub_set_creds <account> KEY=VALUE...    # encrypted local credential file (hubctl creds set)
#   hub_start [--mockvendor]                # start the gateway (and mockvendor), wait until ready
#   mock_scenario <file.json>               # program mockvendor with a scenario file
#
# Needs: Go (to build), curl, python3, and Postgres + Redis
# (docker compose -f deploy/docker-compose.yml up -d). Everything stays on this machine except
# what a sandbox target explicitly calls.

# Ports (override with the environment). mockvendor's must match the overlay's mock region.
HUB_DEMO_HTTP="${HUB_DEMO_HTTP:-127.0.0.1:18080}"
HUB_DEMO_OPS="${HUB_DEMO_OPS:-127.0.0.1:18090}"
MOCKVENDOR_ADDR="${MOCKVENDOR_ADDR:-127.0.0.1:8099}"
MOCKVENDOR_CONTROL_ADDR="${MOCKVENDOR_CONTROL_ADDR:-127.0.0.1:8098}"
HUB_URL="http://$HUB_DEMO_HTTP"
export HUB_PG_DSN="${HUB_PG_DSN:-postgres://hub:hub@localhost:5432/hub?sslmode=disable}"
export HUB_REDIS_ADDR="${HUB_REDIS_ADDR:-localhost:6379}"
export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}" no_proxy="${no_proxy:-localhost,127.0.0.1,::1}"

_hub_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
_hub_bin="$_hub_root/backend/bin"
_hub_pids=()

hub_log() { printf '\033[2m%s\033[0m %s\n' "$(date +%H:%M:%S)" "$*" >&2; }
hub_die() { printf 'error: %s\n' "$*" >&2; exit 1; }

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
  mkdir -p "$state"
  command -v curl >/dev/null || hub_die "curl is required"
  command -v python3 >/dev/null || hub_die "python3 is required"
  hub_log "building the Hub binaries (backend/bin)"
  make -C "$_hub_root/backend" build >/dev/null
  hub_log "checking Postgres ($HUB_PG_DSN) and migrating"
  "$_hub_bin/hub" migrate >/dev/null 2>"$state/migrate.log" ||
    hub_die "cannot migrate $HUB_PG_DSN (is Postgres up? docker compose -f deploy/docker-compose.yml up -d); see $state/migrate.log"
  rm -rf "$state/config" "$state/contracts" "$state/data"
  cp -R "$_hub_root/backend/config" "$state/config"
  cp -R "$_hub_root/contracts" "$state/contracts"
  _merge_overlay "$overlay/config" "$state/config"
  _merge_overlay "$overlay/contracts" "$state/contracts"
  # Point every mock region at the mockvendor this script starts.
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
  for p in "${_hub_pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}

_wait_url() {
  for _ in $(seq 1 150); do
    curl -fsS "$1" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  hub_log "$2 did not become ready at $1; last log lines:"
  tail -20 "$HUB_DEMO_STATE/$2.log" >&2 || true
  exit 1
}

hub_start() {
  trap _hub_cleanup EXIT
  if [ "${1:-}" = "--mockvendor" ]; then
    "$_hub_bin/mockvendor" -addr "$MOCKVENDOR_ADDR" -control-addr "$MOCKVENDOR_CONTROL_ADDR" 2>"$HUB_DEMO_STATE/mockvendor.log" &
    _hub_pids+=("$!")
    _wait_url "http://$MOCKVENDOR_CONTROL_ADDR/__control/healthz" mockvendor
  fi
  "$_hub_bin/hub" gateway -http-addr "$HUB_DEMO_HTTP" -ops-addr "$HUB_DEMO_OPS" \
    -config-dir "$HUB_DEMO_STATE/config" -contracts-dir "$HUB_DEMO_STATE/contracts" \
    -data-dir "$HUB_DEMO_STATE/data" -blob-url "file://$HUB_DEMO_STATE/data/blobs" \
    -ready-drain 100ms -log-level "${HUB_LOG_LEVEL:-warn}" 2>"$HUB_DEMO_STATE/gateway.log" &
  _hub_pids+=("$!")
  _wait_url "http://$HUB_DEMO_OPS/readyz" gateway
  hub_log "gateway ready on $HUB_URL (logs: $HUB_DEMO_STATE/gateway.log)"
}

# mock_scenario FILE: reset mockvendor, then PUT every route of the scenario file
# ({"description", "routes": {id: RouteSpec}}, contracts/openapi/mockvendor.v1.yaml).
mock_scenario() {
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
  python3 -m json.tool "$HUB_DEMO_STATE/last.body" 2>/dev/null | sed 's/^/< /' || sed 's/^/< /' "$HUB_DEMO_STATE/last.body"
  HUB_LAST_STATUS="${out% *}"
}

# hub_last_header NAME: a header of the last hub_post response.
hub_last_header() {
  grep -i "^$1:" "$HUB_DEMO_STATE/last.headers" | head -1 | cut -d: -f2- | tr -d ' \r'
}
