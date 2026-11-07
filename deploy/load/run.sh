#!/usr/bin/env bash
# M8 load and soak tests (HUB-235, HUB-236; DESIGN §19 M8, OBSERVABILITY §12, TESTING_STRATEGY §10).
#
# Starts mockvendor and the Postgres-backed Hub roles (gateway, worker, scheduler) on local
# ports, then runs the phases in $PHASES with backend/bin/hubload and writes every result,
# CPU profile and a Markdown summary to $LOAD_OUT (default deploy/load/results/<UTC time>/).
#
#   HUB_PG_DSN='postgres://hub:hub@localhost:5432/hub_load?sslmode=disable' \
#   HUB_REDIS_ADDR=localhost:6385 deploy/load/run.sh            # all phases, ~25 min on 2 CPUs
#   PHASES=soak deploy/load/run.sh                               # only the soak
#
# Phases: compare (direct to mockvendor vs through the Hub, sync), sync (ramp), async (ramp),
# soak (async with random vendor outages and worker restarts, then reconciliation).
# Every size below is an environment variable; the defaults are sized for a 2-CPU machine.
# Full-length settings for proper hardware: deploy/load/README.md.
#
# The run TRUNCATES hub.outbox and hub.callback_deliveries and FLUSHES the Redis database, so
# the database name must contain "load". Never point it at a shared or production database.
# mockvendor is the only vendor: no request leaves this machine.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root"
export NO_PROXY="${NO_PROXY:-localhost,127.0.0.1,::1}" no_proxy="${no_proxy:-localhost,127.0.0.1,::1}"
: "${HUB_PG_DSN:?set HUB_PG_DSN to a dedicated load database (its name must contain 'load')}"
: "${HUB_REDIS_ADDR:?set HUB_REDIS_ADDR to a dedicated Redis}"
export HUB_PG_DSN HUB_REDIS_ADDR
db="${HUB_PG_DSN##*/}"
db="${db%%\?*}"
if [[ "$db" != *load* ]]; then
  echo "refusing: database '$db' does not look like a load database (its name must contain 'load')" >&2
  exit 2
fi

PHASES="${PHASES:-compare sync async soak}"
CMP_LATENCIES="${CMP_LATENCIES:-0 50 200}"   # mock vendor latency (ms) per comparison pair
CMP_RPS="${CMP_RPS:-100}"
CMP_DURATION="${CMP_DURATION:-20s}"
SYNC_STEPS="${SYNC_STEPS:-100 200 300 400 500}"
SYNC_LATENCY_MS="${SYNC_LATENCY_MS:-20}"        # OBSERVABILITY §12 L1
SYNC_DURATION="${SYNC_DURATION:-20s}"
SYNC_PROFILE_RPS="${SYNC_PROFILE_RPS:-${SYNC_STEPS##* }}"
ASYNC_STEPS="${ASYNC_STEPS:-250 500 1000 2000}"
ASYNC_LATENCY_MS="${ASYNC_LATENCY_MS:-20}"
ASYNC_DURATION="${ASYNC_DURATION:-15s}"
ASYNC_DRAIN="${ASYNC_DRAIN:-100s}"
ASYNC_PROFILE_RPS="${ASYNC_PROFILE_RPS:-1000}"
SOAK_RPS="${SOAK_RPS:-100}"
SOAK_DURATION="${SOAK_DURATION:-4m}"
SOAK_DRAIN="${SOAK_DRAIN:-6m}"
SOAK_RESTARTS="${SOAK_RESTARTS:-0}"              # worker restarts (term, kill, ...); 0 until HUB-406 is fixed
SOAK_ARGS="${SOAK_ARGS:-}"                       # extra hubload soak flags (e.g. -seed 42)
SETTLE_LOAD="${SETTLE_LOAD:-1.0}"                # wait for this 1-min load average before measuring
SETTLE_MAX="${SETTLE_MAX:-40m}"                  # ... at most this long before the first phase
STEP_SETTLE_MAX="${STEP_SETTLE_MAX:-20s}"        # ... and this long before each step
PROFILE_SECONDS="${PROFILE_SECONDS:-20}"
WORKERS="${WORKERS:-1}"

out="${LOAD_OUT:-$root/deploy/load/results/$(date -u +%Y%m%dT%H%M%SZ)}"
state="$out/state"
mkdir -p "$state"
export LOAD_STATE="$state"
bin="$root/backend/bin"
gw_http=127.0.0.1:28780
gw_ops=127.0.0.1:28790
wk_ops_base=28791
sc_ops=127.0.0.1:28792
mv_addr=127.0.0.1:28799
mv_ctl=127.0.0.1:28798
receiver=127.0.0.1:28797
redis_host="${HUB_REDIS_ADDR%:*}"
redis_port="${HUB_REDIS_ADDR##*:}"

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$out/run.log" >&2; }

log "building binaries"
make -C backend build >/dev/null
"$bin/hub" migrate >/dev/null

log "resetting outbox, callbacks and Redis of $db / :$redis_port"
psql "$HUB_PG_DSN" -qc 'TRUNCATE hub.outbox, hub.callback_deliveries' >/dev/null
redis-cli -h "$redis_host" -p "$redis_port" FLUSHDB >/dev/null

# Registry: dev config plus the load overlay. Credentials are generated for this run only.
rm -rf "$state/config"
cp -R backend/config "$state/config"
cp -R deploy/load/config/. "$state/config/"
export HUB_DEV_KEY_FILE="$state/dev.key" HUB_CRED_SOURCE="localfile://$state/secrets.enc"
HUB_AUDIT_MASK_KEY="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
export HUB_AUDIT_MASK_KEY
rm -f "$HUB_DEV_KEY_FILE" "$state/secrets.enc"
"$bin/hubctl" creds init-key >/dev/null
"$bin/hubctl" creds set ct-load account_id=LOAD-ACCOUNT-ID "passcode=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')" >/dev/null
"$bin/hubctl" creds set reg_load_cb "signing_secret=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')" >/dev/null
export HUBLOAD_API_KEY=hk_test_load_tenant_local_only # test value, see deploy/load/config/tenants

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  for f in "$state"/worker*.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

common=(-config-dir "$state/config" -blob-url "file://$state/blobs" -ready-drain 200ms -log-level warn)

"$bin/mockvendor" -addr "$mv_addr" -control-addr "$mv_ctl" 2>"$state/mockvendor.log" &
mv_pid=$!
pids+=("$mv_pid")
"$bin/hub" gateway -http-addr "$gw_http" -ops-addr "$gw_ops" -data-dir "$state/data-gateway" "${common[@]}" 2>"$state/gateway.log" &
gw_pid=$!
pids+=("$gw_pid")
"$bin/hub" scheduler -ops-addr "$sc_ops" -data-dir "$state/data-scheduler" "${common[@]}" 2>"$state/scheduler.log" &
sc_pid=$!
pids+=("$sc_pid")
# Workers are started through a script so the soak's restart hook can start them again.
for i in $(seq 1 "$WORKERS"); do
  ops="127.0.0.1:$((i == 1 ? wk_ops_base : wk_ops_base + i))" # 28791, then 28793, 28794, ... (28792 is the scheduler)
  cat >"$state/worker$i.sh" <<EOF
#!/usr/bin/env bash
exec "$bin/hub" worker -ops-addr "$ops" -data-dir "$state/data-worker$i" -callback-allow-loopback $(printf '%q ' "${common[@]}")
EOF
  chmod +x "$state/worker$i.sh"
  "$state/worker$i.sh" </dev/null >>"$state/worker$i.log" 2>&1 &
  echo $! >"$state/worker$i.pid"
  echo "$ops" >"$state/worker$i.ops"
done

wait_ready() {
  for _ in $(seq 1 150); do
    curl -fsS "http://$1/readyz" >/dev/null 2>&1 && return 0
    sleep 0.2
  done
  echo "FAIL: $2 not ready at $1" >&2
  tail -20 "$state/$2.log" >&2 || true
  exit 1
}
wait_ready "$gw_ops" gateway
wait_ready "$sc_ops" scheduler
for i in $(seq 1 "$WORKERS"); do wait_ready "$(cat "$state/worker$i.ops")" "worker$i"; done
curl -fsS "http://$mv_ctl/__control/healthz" >/dev/null
log "roles ready: gateway $gw_pid, scheduler $sc_pid, worker $(cat "$state/worker1.pid"), mockvendor $mv_pid"

redis_pid="$(pgrep -f "redis-server .*:$redis_port" | head -1 || true)"
metrics=(-metrics "gateway=http://$gw_ops/metrics" -metrics "worker=http://$(cat "$state/worker1.ops")/metrics" -metrics "scheduler=http://$sc_ops/metrics")
cpu_flags() {
  # postgres = this database's backends; pg_cluster = every postgres process (WAL writer,
  # checkpointer and other databases of a shared cluster included).
  local f=(-cpu "gateway=$gw_pid" -cpu "scheduler=$sc_pid" -cpu "mockvendor=$mv_pid" -cpu "worker=$(cat "$state/worker1.pid")" -cpu "postgres=cmd: $db " -cpu pg_cluster=comm:postgres)
  [ -n "$redis_pid" ] && f+=(-cpu "redis=$redis_pid")
  printf '%s\n' "${f[@]}"
}
mapfile -t cpu < <(cpu_flags)
hubload() { "$bin/hubload" "$@" 2>>"$out/run.log"; }
step() { hubload rate -mock-control "http://$mv_ctl" "${metrics[@]}" "${cpu[@]}" -settle-load "$SETTLE_LOAD" -settle-max "$STEP_SETTLE_MAX" "$@"; }

log "waiting for load average < $SETTLE_LOAD (max $SETTLE_MAX)"
echo "load average before phases: $("$bin/hubload" settle -below "$SETTLE_LOAD" -poll 30s -max "$SETTLE_MAX" 2>>"$out/run.log")" | tee -a "$out/run.log"

profile() { # profile <ops addr> <name>: CPU profile for PROFILE_SECONDS, then a text top list
  curl -fsS -o "$out/$2.cpu.pprof" "http://$1/debug/pprof/profile?seconds=$PROFILE_SECONDS" &&
    (cd backend && go tool pprof -top -nodecount=40 "$bin/hub" "$out/$2.cpu.pprof" >"$out/$2.cpu.top.txt" 2>/dev/null &&
      go tool pprof -top -cum -nodecount=60 "$bin/hub" "$out/$2.cpu.pprof" >"$out/$2.cpu.cum.txt" 2>/dev/null) || true
}

for phase in $PHASES; do
  case "$phase" in
  compare)
    for l in $CMP_LATENCIES; do
      log "compare: vendor ${l} ms, direct"
      step -label "cmp-direct-$l" -warmup 3s -mode direct -url "http://$mv_addr" -rps "$CMP_RPS" -duration "$CMP_DURATION" -vendor-latency-ms "$l" -out "$out/cmp-direct-$l.json"
      log "compare: vendor ${l} ms, through the Hub"
      step -label "cmp-hub-$l" -warmup 3s -mode sync -url "http://$gw_http" -integration int_load_sync -rps "$CMP_RPS" -duration "$CMP_DURATION" -vendor-latency-ms "$l" -out "$out/cmp-hub-$l.json"
    done
    ;;
  sync)
    for r in $SYNC_STEPS; do
      log "sync: $r RPS"
      step -label "sync-$r" -mode sync -url "http://$gw_http" -integration int_load_sync -rps "$r" -duration "$SYNC_DURATION" -vendor-latency-ms "$SYNC_LATENCY_MS" -out "$out/sync-$r.json"
    done
    log "sync: CPU profile of the gateway at $SYNC_PROFILE_RPS RPS"
    (sleep 8 && profile "$gw_ops" gateway-sync) &
    prof=$!
    step -label "profile-sync-$SYNC_PROFILE_RPS" -mode sync -url "http://$gw_http" -integration int_load_sync -rps "$SYNC_PROFILE_RPS" -duration "$((PROFILE_SECONDS + 5))s" -warmup 3s -vendor-latency-ms "$SYNC_LATENCY_MS" -out "$out/profile-sync.json"
    wait "$prof" || true
    ;;
  async)
    for r in $ASYNC_STEPS; do
      log "async: $r msg/s"
      step -label "async-$r" -mode async -url "http://$gw_http" -integration int_load_async -rps "$r" -duration "$ASYNC_DURATION" -warmup 3s -drain "$ASYNC_DRAIN" -vendor-latency-ms "$ASYNC_LATENCY_MS" -out "$out/async-$r.json"
    done
    log "async: CPU profiles of the worker and gateway at $ASYNC_PROFILE_RPS msg/s"
    (sleep 6 && profile "$(cat "$state/worker1.ops")" worker-async) &
    p1=$!
    (sleep 6 && profile "$gw_ops" gateway-async) &
    p2=$!
    step -label "profile-async-$ASYNC_PROFILE_RPS" -mode async -url "http://$gw_http" -integration int_load_async -rps "$ASYNC_PROFILE_RPS" -duration "$((PROFILE_SECONDS + 5))s" -warmup 3s -drain "$ASYNC_DRAIN" -vendor-latency-ms "$ASYNC_LATENCY_MS" -out "$out/profile-async.json"
    wait "$p1" "$p2" || true
    ;;
  soak)
    log "soak: $SOAK_RPS msg/s for $SOAK_DURATION with random outages and $SOAK_RESTARTS worker restarts"
    # shellcheck disable=SC2086 # SOAK_ARGS is a flag list
    hubload soak -label soak -url "http://$gw_http" -integration int_load_cb -mock-control "http://$mv_ctl" \
      -receiver-addr "$receiver" -restart-hook "$root/deploy/load/restart-worker.sh" -restarts "$SOAK_RESTARTS" \
      -rps "$SOAK_RPS" -duration "$SOAK_DURATION" -drain-timeout "$SOAK_DRAIN" -vendor-latency-ms "$ASYNC_LATENCY_MS" \
      -cpu "gateway=$gw_pid" -cpu "postgres=cmd: $db " -cpu pg_cluster=comm:postgres -ids-out "$out/soak.ids" -out "$out/soak.json" $SOAK_ARGS || log "soak: acceptance FAILED (see soak.json)"
    ;;
  *)
    echo "unknown phase $phase" >&2
    exit 2
    ;;
  esac
done

for r in gateway scheduler worker1 mockvendor; do
  n="$(grep -c '"level":"ERROR"' "$state/$r.log" 2>/dev/null || true)"
  echo "$r log: ${n:-0} error lines" >>"$out/run.log"
done
"$bin/hubload" report -dir "$out" >"$out/report.md"
log "done: $out/report.md"
