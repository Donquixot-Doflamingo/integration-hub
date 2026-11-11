#!/usr/bin/env bash
# Pid-file helpers shared by scripts/dev-up.sh, scripts/dev-down.sh and examples/lib/local-hub.sh
# (HUB-456). Source it; do not run it.
#
# A pid file is only a hint: after a reboot or a crash its PID may belong to an unrelated process.
# Nothing here treats a PID as ours (running, or fit to signal) before its command line contains
# the binary this tooling started.

# abs_path PATH: PATH made absolute against the current directory (it need not exist).
abs_path() {
  local p="$1"
  case "$p" in
  /*) ;;
  *) p="$PWD/${p#./}" ;;
  esac
  while [ "${#p}" -gt 1 ] && [ "${p%/}" != "$p" ]; do p="${p%/}"; done
  printf '%s\n' "$p"
}

# pid_cmdline PID: the command line of PID, or nothing when there is no such process.
pid_cmdline() { ps -o command= -p "$1" 2>/dev/null || true; }

# pid_matches PID NEEDLE: succeeds when PID is a live process whose command line contains NEEDLE.
pid_matches() {
  local cmd
  [[ "$1" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$1" 2>/dev/null || return 1
  cmd="$(pid_cmdline "$1")"
  [[ "$cmd" == *"$2"* ]]
}

# pidfile_status FILE NEEDLE: prints "none" (no file), "live" (the pid is a process running NEEDLE)
# or "stale" (the pid is dead, reused by another program, or the file is garbage).
pidfile_status() {
  local pid
  if [ ! -f "$1" ]; then
    echo none
    return 0
  fi
  pid="$(cat "$1" 2>/dev/null || true)"
  if pid_matches "$pid" "$2"; then echo live; else echo stale; fi
}

# stop_tree PID: TERM PID and its descendants, children first (the console's npm wraps vite).
stop_tree() {
  local pid="$1" child
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do stop_tree "$child"; done
  kill -TERM "$pid" 2>/dev/null || true
}

# stop_pidfile FILE NEEDLE: stop the process in FILE when, and only when, its command line contains
# NEEDLE (TERM, up to 10 s, then KILL), then remove FILE. Prints live, stale or none to stdout
# (what the pid file turned out to be) so callers can report; never signals a foreign process.
stop_pidfile() {
  local f="$1" needle="$2" st pid
  st="$(pidfile_status "$f" "$needle")"
  if [ "$st" = live ]; then
    pid="$(cat "$f")"
    stop_tree "$pid"
    for _ in $(seq 1 50); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.2
    done
    if pid_matches "$pid" "$needle"; then kill -KILL "$pid" 2>/dev/null || true; fi
  fi
  rm -f "$f"
  echo "$st"
}

# dev_needle NAME BIN: what the command line of the dev stack process NAME must contain. BIN is the
# backend/bin directory of this checkout, so a stack from another checkout is not ours either.
dev_needle() {
  case "$1" in
  mockvendor) printf '%s\n' "$2/mockvendor" ;;
  console) printf '%s\n' "npm" ;; # npm run dev (the process shows as npm or as node .../npm)
  *) printf '%s\n' "$2/hub $1" ;;
  esac
}
