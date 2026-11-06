#!/usr/bin/env bash
# M0 acceptance smoke test (DESIGN §19 M0, HUB-032):
# docker compose up + `hub gateway` serves /healthz and /readyz (Postgres, Redis and the dev config
# snapshot), then stops cleanly on SIGTERM.
# Usage: deploy/smoke/m0.sh   (from the repo root; CI runs it too)
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export HUB_PG_DSN="${HUB_PG_DSN:-postgres://hub:hub@localhost:5432/hub?sslmode=disable}"
export HUB_REDIS_ADDR="${HUB_REDIS_ADDR:-localhost:6379}"
ops="127.0.0.1:${SMOKE_OPS_PORT:-19090}"
http_addr="127.0.0.1:${SMOKE_HTTP_PORT:-18080}"
log="$(mktemp)"
data="$(mktemp -d)"

if [ "${SMOKE_SKIP_COMPOSE:-}" != "1" ]; then
  docker compose -f deploy/docker-compose.yml up -d --wait
fi
make -C backend build migrate >/dev/null

backend/bin/hub gateway -ops-addr "$ops" -http-addr "$http_addr" -ready-drain 200ms \
  -config-dir backend/config -contracts-dir contracts -data-dir "$data" -blob-url "file://$data/blobs" 2>"$log" &
pid=$!
cleanup() { kill "$pid" 2>/dev/null || true; rm -rf "$log" "$data"; }
trap cleanup EXIT

for _ in $(seq 1 50); do
  curl -fsS "http://$ops/healthz" >/dev/null 2>&1 && break
  sleep 0.1
done

fail() { echo "FAIL: $*"; echo "--- hub log ---"; cat "$log"; exit 1; }

healthz="$(curl -fsS "http://$ops/healthz")" || fail "/healthz not reachable"
echo "/healthz -> $healthz"
[[ "$healthz" == *'"status":"ok"'* ]] || fail "/healthz body"

readyz="$(curl -fsS "http://$ops/readyz")" || fail "/readyz not 200"
echo "/readyz  -> $readyz"
[[ "$readyz" == *'"postgres":"ok"'* && "$readyz" == *'"redis":"ok"'* && "$readyz" == *'"snapshot":"ok"'* ]] || fail "/readyz checks"

# Read the whole body first: `curl | grep -q` under pipefail fails whenever grep exits on its
# match before curl has written the rest (curl exit 23), so it flakes as /metrics grows.
metrics="$(curl -fsS "http://$ops/metrics")" || fail "/metrics not 200"
grep -q '^hub_build_info' <<<"$metrics" || fail "/metrics missing hub_build_info"

kill -TERM "$pid"
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
kill -0 "$pid" 2>/dev/null && fail "gateway still running 10 s after SIGTERM"
wait "$pid" || fail "gateway exited non-zero after SIGTERM"
grep -q '"role stopped"' "$log" || fail "no clean-stop log line"
echo "M0 smoke: OK"
