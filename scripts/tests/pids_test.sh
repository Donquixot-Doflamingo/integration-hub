#!/usr/bin/env bash
# Tests for scripts/lib/pids.sh (HUB-456). Starts only throwaway `sleep` processes of its own.
#   bash scripts/tests/pids_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=../lib/pids.sh
source "$here/../lib/pids.sh"

fails=0
check() { # check <name> <got> <want>
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi
}
tmp="$(mktemp -d)"
victim="" other=""
trap 'kill $victim $other 2>/dev/null; rm -rf "$tmp"' EXIT

# abs_path
check "abs absolute" "$(abs_path /a/b/)" "/a/b"
check "abs root" "$(abs_path /)" "/"
check "abs relative" "$(cd /tmp && pwd -P >/dev/null && PWD=/tmp abs_path ./x/y)" "/tmp/x/y"
check "abs bare" "$(PWD=/tmp abs_path st/)" "/tmp/st"

# A live process whose command line has a marker, and one without it.
sleep 61 &
victim=$!
sleep 62 &
other=$!

echo "$victim" >"$tmp/a.pid"
check "live with matching needle" "$(pidfile_status "$tmp/a.pid" "sleep 61")" live
check "same pid, other needle is stale (pid reuse)" "$(pidfile_status "$tmp/a.pid" "backend/bin/hub")" stale
check "no file" "$(pidfile_status "$tmp/none.pid" x)" none
echo "not-a-pid" >"$tmp/g.pid"
check "garbage file" "$(pidfile_status "$tmp/g.pid" x)" stale
echo "" >"$tmp/e.pid"
check "empty file" "$(pidfile_status "$tmp/e.pid" x)" stale
echo "999999" >"$tmp/d.pid"
check "dead pid" "$(pidfile_status "$tmp/d.pid" x)" stale

# stop_pidfile must not signal a process that does not match, but must remove the stale file.
echo "$other" >"$tmp/o.pid"
check "foreign pid reported stale" "$(stop_pidfile "$tmp/o.pid" "backend/bin/hub")" stale
if kill -0 "$other" 2>/dev/null; then alive=yes; else alive=no; fi
check "foreign process untouched" "$alive" yes
if [ -e "$tmp/o.pid" ]; then gone=present; else gone=gone; fi
check "stale file removed" "$gone" gone

# stop_pidfile stops a matching one.
check "matching pid stopped" "$(stop_pidfile "$tmp/a.pid" "sleep 61")" live
wait "$victim" 2>/dev/null
if kill -0 "$victim" 2>/dev/null; then alive=yes; else alive=no; fi
check "matching process gone" "$alive" no

if [ "$fails" = 0 ]; then echo "all passed"; else echo "$fails failed"; exit 1; fi
