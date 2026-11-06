#!/usr/bin/env bash
# Stops the local stack started by scripts/dev-up.sh (HUB-286).
#
#   scripts/dev-down.sh            # stop the console, hub roles and mockvendor (data is kept)
#   scripts/dev-down.sh --purge    # also delete the state directory (logs, dev credentials,
#                                  # snapshot blobs) and, when dev-up started them, the compose
#                                  # Postgres and Redis containers with their volume
#
# Data in a Postgres you pointed HUB_PG_DSN at is never dropped by this script.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
state="${DEV_STATE:-$root/.dev-stack}"
purge=0
for a in "$@"; do
  case "$a" in
  --purge) purge=1 ;;
  -h | --help)
    sed -n '2,10p' "$0"
    exit 0
    ;;
  *)
    echo "dev-down: unknown argument $a" >&2
    exit 2
    ;;
  esac
done

# Children first (the console's npm wraps vite), then the process itself; TERM, then KILL.
stop_tree() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do stop_tree "$child"; done
  kill -TERM "$pid" 2>/dev/null || true
}

stopped=""
if [ -d "$state/pids" ]; then
  for name in console scheduler worker gateway admin mockvendor; do
    f="$state/pids/$name.pid"
    [ -f "$f" ] || continue
    pid="$(cat "$f")"
    if kill -0 "$pid" 2>/dev/null; then
      stop_tree "$pid"
      for _ in $(seq 1 50); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.2
      done
      kill -KILL "$pid" 2>/dev/null || true
      stopped+=" $name"
    fi
    rm -f "$f"
  done
fi
echo "stopped:${stopped:- nothing was running}"

if [ "$purge" = 1 ]; then
  if [ -f "$state/services" ] && [ "$(cat "$state/services")" = compose ]; then
    docker compose -f deploy/docker-compose.yml down -v
  fi
  rm -rf "$state"
  echo "removed $state"
elif [ -f "$state/services" ] && [ "$(cat "$state/services")" = compose ]; then
  echo "Postgres and Redis keep running in docker compose (scripts/dev-down.sh --purge removes them)"
fi
