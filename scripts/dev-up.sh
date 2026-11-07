#!/usr/bin/env bash
# One-command local stack for manual testing of the admin console against a real Hub (HUB-286).
#
#   scripts/dev-up.sh            # or: make dev-up
#   scripts/dev-down.sh          # or: make dev-down
#
# Starts, in order:
#   1. Postgres 16 + Redis 7 from deploy/docker-compose.yml when Docker (or Colima) is running and
#      HUB_PG_DSN / HUB_REDIS_ADDR are not set; otherwise it uses the services those variables name.
#   2. `hub migrate`, then a dev registry seed: a copy of backend/config pointed at this stack's
#      mockvendor, and freshly generated dev credentials (never committed, never printed).
#   3. mockvendor, then the hub roles admin (with the dev OIDC issuer), gateway, worker, scheduler.
#   4. A little traffic through the gateway (successes and one dead letter), so the request
#      explorer, DLQ and change log have something to show. Skip with DEV_SEED_TRAFFIC=0.
#   5. The console dev server (Vite), signed in through the admin's dev issuer and talking to the
#      real /admin/v1 through the console's same-origin proxy.
#
# Everything runs in the background; logs and pids live in $DEV_STATE (default .dev-stack/).
# Works on macOS arm64 (Homebrew Go + node@22, Colima) and on Linux. Needs: go, node 22, npm, curl.
#
# Settings (environment):
#   HUB_PG_DSN, HUB_REDIS_ADDR   use these services instead of docker compose
#   DEV_DOCKER=0|1               force not using / using docker compose (default: auto)
#   DEV_STATE                    state directory (default <repo>/.dev-stack)
#   DEV_CONSOLE_PORT (5173), DEV_GATEWAY_PORT (8080), DEV_ADMIN_PORT (8081),
#   DEV_MOCKVENDOR_PORT (8099), DEV_MOCKVENDOR_CONTROL_PORT (8098), DEV_OPS_PORT_BASE (19190)
#   DEV_SEED_TRAFFIC=0           do not send the sample requests
#   DEV_SKIP_BUILD=1             reuse backend/bin and frontend/node_modules as they are
#   DEV_RESET=1                  recreate the compose Postgres and Redis when the database holds
#                                snapshots from an earlier run whose files are gone (HUB-430)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

# Homebrew's node@22 is keg-only on macOS (CLAUDE.md); prefer it when present.
for d in /opt/homebrew/opt/node@22/bin /usr/local/opt/node@22/bin; do
  [ -d "$d" ] && PATH="$d:$PATH"
done
export PATH
# Loopback traffic must never go through a corporate or sandbox proxy.
export NO_PROXY="${NO_PROXY:+$NO_PROXY,}localhost,127.0.0.1,::1" no_proxy="${no_proxy:+$no_proxy,}localhost,127.0.0.1,::1"

state="${DEV_STATE:-$root/.dev-stack}"
console_port="${DEV_CONSOLE_PORT:-5173}"
gw_port="${DEV_GATEWAY_PORT:-8080}"
admin_port="${DEV_ADMIN_PORT:-8081}"
mv_port="${DEV_MOCKVENDOR_PORT:-8099}"
mv_ctl_port="${DEV_MOCKVENDOR_CONTROL_PORT:-8098}"
ops_base="${DEV_OPS_PORT_BASE:-19190}"
host=127.0.0.1
admin_url="http://$host:$admin_port"
gw_url="http://$host:$gw_port"
mv_url="http://$host:$mv_port"
mv_ctl_url="http://$host:$mv_ctl_port"
console_url="http://localhost:$console_port"

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() { printf '\033[31mdev-up:\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required ($2)"; }

need go "brew install go, or https://go.dev/dl"
need node "brew install node@22"
need npm "comes with node"
need curl "brew install curl"
node_major="$(node -p 'process.versions.node.split(".")[0]')"
[ "$node_major" = 22 ] || die "node 22 is required (found $(node --version)); on macOS: brew install node@22"

if [ -f "$state/pids/console.pid" ] && kill -0 "$(cat "$state/pids/console.pid")" 2>/dev/null; then
  die "the stack is already running (scripts/dev-down.sh stops it)"
fi
mkdir -p "$state/pids" "$state/logs"

# ---- 1. Postgres and Redis -------------------------------------------------------------------
use_docker="${DEV_DOCKER:-auto}"
if [ "$use_docker" = auto ]; then
  use_docker=0
  if [ -z "${HUB_PG_DSN:-}" ] && [ -z "${HUB_REDIS_ADDR:-}" ] && command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
    use_docker=1
  fi
fi
if [ "$use_docker" = 1 ]; then
  say "starting Postgres and Redis (docker compose)"
  docker compose -f deploy/docker-compose.yml up -d --wait
  echo compose >"$state/services"
else
  rm -f "$state/services"
  if [ -z "${HUB_PG_DSN:-}" ] && ! command -v docker >/dev/null 2>&1; then
    say "Docker is not available: using Postgres and Redis on localhost (set HUB_PG_DSN / HUB_REDIS_ADDR to change)"
  elif [ -z "${HUB_PG_DSN:-}" ]; then
    say "Docker is not running (on macOS: colima start): using Postgres and Redis on localhost"
  fi
fi
export HUB_PG_DSN="${HUB_PG_DSN:-postgres://hub:hub@localhost:5432/hub?sslmode=disable}"
export HUB_REDIS_ADDR="${HUB_REDIS_ADDR:-localhost:6379}"

port_open() { (exec 3<>"/dev/tcp/$1/$2") 2>/dev/null; }
redis_host="${HUB_REDIS_ADDR%:*}"
redis_port="${HUB_REDIS_ADDR##*:}"
port_open "$redis_host" "$redis_port" || die "Redis is not reachable at $HUB_REDIS_ADDR (start it, or run Docker/Colima so compose can)"
for p in "$console_port" "$gw_port" "$admin_port" "$mv_port" "$mv_ctl_port"; do
  port_open "$host" "$p" && die "port $p is already in use (set the DEV_*_PORT variables to move this stack)"
done

# ---- 2. Build, migrate, seed ------------------------------------------------------------------
if [ "${DEV_SKIP_BUILD:-0}" != 1 ]; then
  say "building hub, hubctl and mockvendor"
  make -s -C backend build >"$state/logs/build.log" 2>&1 || { tail -30 "$state/logs/build.log" >&2; die "backend build failed"; }
  if [ ! -d frontend/node_modules ] || [ frontend/package-lock.json -nt frontend/node_modules/.package-lock.json ]; then
    say "installing console dependencies (npm ci)"
    (cd frontend && npm ci --no-audit --no-fund >"$state/logs/npm-ci.log" 2>&1) || { tail -30 "$state/logs/npm-ci.log" >&2; die "npm ci failed"; }
  fi
fi
bin="$root/backend/bin"
[ -x "$bin/hub" ] || die "backend/bin/hub is missing (run without DEV_SKIP_BUILD=1)"

say "applying database migrations"
"$bin/hub" migrate >"$state/logs/migrate.log" 2>&1 || { cat "$state/logs/migrate.log" >&2; die "migrations failed: is Postgres reachable at the HUB_PG_DSN you set?"; }

# The snapshot archive is split in two: its index (hub.config_snapshots) lives in Postgres and the
# snapshot files under $state/blobs. A database kept from an earlier run (the compose volume
# survives a fresh clone or a deleted .dev-stack/) points at files that are gone, and every admin
# page that reads the registry then fails with INTERNAL_ERROR (HUB-430). Check before starting.
sql() { # sql <query>: one value from the dev database; fails when no client is available
  if [ "$use_docker" = 1 ]; then
    docker compose -f deploy/docker-compose.yml exec -T postgres psql -U hub -d hub -XAtqc "$1"
  elif command -v psql >/dev/null 2>&1; then
    psql "$HUB_PG_DSN" -XAtqc "$1"
  else
    return 1
  fi
}
latest_ref="$(sql "SELECT blob_ref FROM hub.config_snapshots ORDER BY version DESC LIMIT 1" 2>/dev/null || true)"
if [[ "$latest_ref" == file://* ]] && [ ! -f "${latest_ref#file://}" ]; then
  if [ "$use_docker" = 1 ] && [ "${DEV_RESET:-0}" = 1 ]; then
    say "DEV_RESET=1: recreating the compose Postgres and Redis (their volume is dropped)"
    docker compose -f deploy/docker-compose.yml down -v
    docker compose -f deploy/docker-compose.yml up -d --wait
    rm -rf "$state/blobs" "$state"/data-*
    "$bin/hub" migrate >"$state/logs/migrate.log" 2>&1 || { cat "$state/logs/migrate.log" >&2; die "migrations failed after the reset"; }
  elif [ "$use_docker" = 1 ]; then
    die "the dev database holds registry snapshots from an earlier run whose files are gone
  (${latest_ref#file://}), so every admin page would fail with INTERNAL_ERROR.
  Start from a clean dev database (drops only the docker compose Postgres and Redis volume):
    DEV_RESET=1 make dev-up"
  else
    die "the database at HUB_PG_DSN holds registry snapshots from an earlier run whose files are gone
  (${latest_ref#file://}), so every admin page would fail with INTERNAL_ERROR.
  Point HUB_PG_DSN at an empty database, or restore that file. This script never drops your database."
  fi
fi

# The registry seed: the dev config with its vendor regions and callback receiver pointed at this
# stack's mockvendor (which answers 200 {} on any path). The admin archives it as the first
# snapshot when the archive is empty; after that, published changes win (ADR-0010).
rm -rf "$state/config"
cp -R backend/config "$state/config"
for f in "$state/config/vendors/clevertap.yaml" "$state/config/registrar/reg_orders_cb.yaml"; do
  sed -e "s#http://127.0.0.1:8099#$mv_url#g" -e "s#http://127.0.0.1:8097#$mv_url#g" "$f" >"$f.tmp" && mv "$f.tmp" "$f"
done

# Dev credentials: a local key and encrypted file for this stack only (random values; never shown).
export HUB_DEV_KEY_FILE="$state/dev.key" HUB_CRED_SOURCE="localfile://$state/dev-secrets.enc"
if [ ! -f "$HUB_DEV_KEY_FILE" ]; then
  rand() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }
  "$bin/hubctl" creds init-key >/dev/null
  "$bin/hubctl" creds set ct-brandA-in account_id=DEV-ACCOUNT-A "passcode=$(rand 12)" >/dev/null
  "$bin/hubctl" creds set ct-brandB-us account_id=DEV-ACCOUNT-B "passcode=$(rand 12)" >/dev/null
  "$bin/hubctl" creds set reg_orders_cb "signing_secret=$(rand 24)" >/dev/null
fi
[ -f "$state/audit-mask.key" ] || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$state/audit-mask.key"
HUB_AUDIT_MASK_KEY="$(cat "$state/audit-mask.key")"
[ -f "$state/session.secret" ] || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$state/session.secret"
chmod 600 "$state/audit-mask.key" "$state/session.secret"
export HUB_AUDIT_MASK_KEY

# ---- 3. mockvendor and the hub roles -----------------------------------------------------------
start() { # start <name> <command...>: background process with its log and pid file
  local name="$1"
  shift
  nohup "$@" </dev/null >>"$state/logs/$name.log" 2>&1 &
  echo $! >"$state/pids/$name.pid"
}
wait_url() { # wait_url <url> <name> [tries]
  for _ in $(seq 1 "${3:-150}"); do
    curl -fsS -o /dev/null "$1" 2>/dev/null && return 0
    sleep 0.2
  done
  tail -25 "$state/logs/$2.log" >&2 || true
  die "$2 did not become ready at $1 (log: $state/logs/$2.log)"
}

common=(-config-dir "$state/config" -contracts-dir "$root/contracts" -blob-url "file://$state/blobs"
  -admin-url "$admin_url" -ready-drain 200ms -shutdown-grace 5s)

say "starting mockvendor on $mv_url"
start mockvendor "$bin/mockvendor" -addr "$host:$mv_port" -control-addr "$host:$mv_ctl_port"
wait_url "$mv_ctl_url/__control/healthz" mockvendor

say "starting hub admin on $admin_url (dev OIDC issuer on)"
start admin "$bin/hub" admin -http-addr "$host:$admin_port" -ops-addr "$host:$ops_base" \
  -data-dir "$state/data-admin" -admin-dev-oidc -admin-public-url "$admin_url" "${common[@]}"
wait_url "http://$host:$ops_base/readyz" admin

say "starting hub gateway, worker and scheduler"
start gateway "$bin/hub" gateway -http-addr "$host:$gw_port" -ops-addr "$host:$((ops_base + 1))" \
  -data-dir "$state/data-gateway" "${common[@]}"
start worker "$bin/hub" worker -ops-addr "$host:$((ops_base + 2))" -data-dir "$state/data-worker" \
  -callback-allow-loopback "${common[@]}"
start scheduler "$bin/hub" scheduler -ops-addr "$host:$((ops_base + 3))" -data-dir "$state/data-scheduler" "${common[@]}"
wait_url "http://$host:$((ops_base + 1))/readyz" gateway
wait_url "http://$host:$((ops_base + 2))/readyz" worker
wait_url "http://$host:$((ops_base + 3))/readyz" scheduler

# ---- 4. Sample traffic -------------------------------------------------------------------------
dev_key=hk_test_dev_orders_service_local_only # test value, documented in backend/config/README.md
if [ "${DEV_SEED_TRAFFIC:-1}" != 0 ]; then
  say "sending sample requests through the gateway"
  invoke() { # invoke <integration> <user> <event>
    curl -sS -o /dev/null -w '%{http_code}' -X POST "$gw_url/v1/integrations/$1/invoke" \
      -H "Authorization: Bearer $dev_key" -H 'Content-Type: application/json' \
      -d "{\"user\":{\"id\":\"$2\"},\"event\":{\"name\":\"$3\",\"properties\":{\"source\":\"dev-up\"}}}" || echo 000
  }
  seeded=""
  for i in 1 2 3; do seeded+=" $(invoke int_evt_brandA "u_dev_$i" order_placed)"; done
  seeded+=" $(invoke int_evt_brandB "u_dev_4" cart_viewed)"
  # One dead letter: the vendor rejects the next call with a non-retryable 400.
  curl -fsS -o /dev/null -X PUT "$mv_ctl_url/__control/routes/dev-up-reject" -H 'Content-Type: application/json' \
    -d '{"match":{"method":"POST","path":"/1/upload"},"priority":10,"behaviour":{"status":400,"body":{"status":"fail","error":"invalid event (dev-up sample)"}},"times":1}' || true
  seeded+=" $(invoke int_evt_brandB "u_dev_5" cart_abandoned)"
  echo "    gateway answered:$seeded"
fi

# ---- 5. Console --------------------------------------------------------------------------------
say "starting the console on $console_url"
(
  cd frontend
  PUBLIC_AUTH_MODE=dev \
    HUB_DEV_OIDC_ISSUER="$admin_url" \
    HUB_ADMIN_UPSTREAM="$admin_url" \
    HUB_SESSION_SECRET="$(cat "$state/session.secret")" \
    start console npm run dev -- --host localhost --port "$console_port" --strictPort
)
wait_url "$console_url/login" console 300

cat <<EOF

$(printf '\033[1m')Integration Hub dev stack is up.$(printf '\033[0m')

  Console        $console_url
                 Sign in: pick a role (admin, editor, viewer or tenant viewer with tenant
                 orders-service). The admin's dev issuer mints the token; no password.
  Admin API      $admin_url/admin/v1        (hubctl login -dev -admin-url $admin_url)
  Gateway        $gw_url/v1
  mockvendor     $mv_url   control $mv_ctl_url/__control
  Ops            admin :$ops_base  gateway :$((ops_base + 1))  worker :$((ops_base + 2))  scheduler :$((ops_base + 3))  (/healthz /readyz /metrics)
  Database       $HUB_PG_DSN
  Redis          $HUB_REDIS_ADDR

  Tenant API key (orders-service, local test value):
    $dev_key
  Try it:
    curl -X POST $gw_url/v1/integrations/int_evt_brandA/invoke \\
      -H 'Authorization: Bearer $dev_key' -H 'Content-Type: application/json' \\
      -d '{"user":{"id":"u_1"},"event":{"name":"order_placed"}}'
  Mint another key: Console > Tenants > orders-service > API keys > Create API key (admin role); the key is shown once.

  Logs   $state/logs/<name>.log      Stop   scripts/dev-down.sh (or make dev-down)
EOF
