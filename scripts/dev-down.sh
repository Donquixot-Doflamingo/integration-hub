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

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/pids.sh
source "$here/lib/pids.sh"
root="$(cd "$here/.." && pwd)"
state="$(abs_path "${DEV_STATE:-$root/.dev-stack}")"
cd "$root"
bin="$root/backend/bin"
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

# A pid file is only trusted when the process behind it runs the binary dev-up started (a pid
# reused after a reboot is left alone). Children first (the console's npm wraps vite), then TERM,
# then KILL.
stopped=""
if [ -d "$state/pids" ]; then
  for name in console scheduler worker gateway admin mockvendor; do
    f="$state/pids/$name.pid"
    [ -f "$f" ] || continue
    case "$(stop_pidfile "$f" "$(dev_needle "$name" "$bin")")" in
    live) stopped+=" $name" ;;
    stale) echo "dev-down: $f was stale (its process is gone or is not the dev stack's $name); not signalled, file removed" ;;
    esac
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
