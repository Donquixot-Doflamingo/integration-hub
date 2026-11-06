#!/usr/bin/env bash
# Restart hook of `hubload soak` (deploy/load/run.sh): stops worker 1 with SIGTERM (graceful,
# in-flight attempts finish) or SIGKILL (claims expire and are reclaimed after the visibility
# timeout), then starts it again with the same command. Usage: restart-worker.sh term|kill
set -euo pipefail
state="${LOAD_STATE:?LOAD_STATE is set by deploy/load/run.sh}"
pid="$(cat "$state/worker1.pid")"
sig=TERM
[ "${1:-term}" = kill ] && sig=KILL
kill -s "$sig" "$pid" 2>/dev/null || true
for _ in $(seq 1 300); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
  echo "worker $pid still running 30 s after SIG$sig" >&2
  exit 1
fi
# Detach stdio so the caller (hubload) does not wait on the new worker's pipes.
"$state/worker1.sh" </dev/null >>"$state/worker1.log" 2>&1 &
echo $! >"$state/worker1.pid"
ops="$(cat "$state/worker1.ops")"
for _ in $(seq 1 150); do
  curl -fsS "http://$ops/readyz" >/dev/null 2>&1 && exit 0
  sleep 0.2
done
echo "restarted worker not ready at $ops" >&2
exit 1
