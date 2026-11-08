#!/usr/bin/env bash
# Age by name through a local Hub (HUB-435): int_age_by_name turns {firstName, lastName} into
# GET https://api.agify.io?name=<firstName><lastName> and returns only {"age"}. See README.md.
#
#   examples/agify-age/run.sh                     # offline: mockvendor answers like agify ("harsh Khandelwal", 29)
#   examples/agify-age/run.sh unknown-name        # another mock scenario (see mockvendor/*.json)
#   examples/agify-age/run.sh rate-limited
#   AGIFY_TARGET=live examples/agify-age/run.sh   # the real public API (free, small daily allowance)
#   examples/agify-age/run.sh serve               # start and keep running; call it yourself
#
# Optional: FIRST_NAME (default harsh), LAST_NAME (default Khandelwal), HUB_PG_DSN, HUB_REDIS_ADDR, and the
# ports of ../lib/local-hub.sh. Postgres + Redis must be up.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
# shellcheck source=../lib/local-hub.sh
source "$root/examples/lib/local-hub.sh"

target="${AGIFY_TARGET:-mock}"
scenario="${1:-found}"
first="${FIRST_NAME:-harsh}"
last="${LAST_NAME:-Khandelwal}"
export HUB_DEMO_KEY=hk_test_example_age_demo_local_only # local test key (tenant age-demo)

hub_setup "${HUB_DEMO_STATE_DIR:-$root/.demo/agify-age}" "$here"
# agify is a public API: its vendor uses auth scheme none, so there is no credential to store.
case "$target" in
  mock)
    hub_set_region agify-public.yaml mock
    hub_start --mockvendor
    if [ "$scenario" = serve ]; then
      mock_scenario "$here/mockvendor/found.json"
    else
      [ -f "$here/mockvendor/$scenario.json" ] || hub_die "unknown scenario $scenario (see $here/mockvendor/)"
      mock_scenario "$here/mockvendor/$scenario.json"
    fi
    ;;
  live)
    [ "$scenario" = found ] || [ "$scenario" = serve ] || hub_die "scenarios are for the mock target only; the live API answers what it answers"
    hub_start
    hub_log "target: the public API https://api.agify.io (each call uses one name of its free daily allowance)"
    ;;
  *) hub_die "AGIFY_TARGET must be mock or live, got $target" ;;
esac

url="$HUB_URL/v1/integrations/int_age_by_name/invoke"
body="{\"firstName\": \"$first\", \"lastName\": \"$last\"}"
if [ "$scenario" = serve ]; then
  cat <<MSG

The Hub is running on $HUB_URL ($target). Try, in another terminal:

  curl -s $url \\
    -H 'Authorization: Bearer $HUB_DEMO_KEY' -H 'Content-Type: application/json' \\
    -d '$body'

Press Ctrl-C to stop.
MSG
  wait
  exit 0
fi

printf '\n\033[1mThe call\033[0m (the same as):\n'
printf "  curl -s %s \\\\\n    -H 'Authorization: Bearer %s' -H 'Content-Type: application/json' \\\\\n    -d '%s'\n" "$url" "$HUB_DEMO_KEY" "$body"
hub_post /v1/integrations/int_age_by_name/invoke "" "" "$body"
printf '\nThe vendor never appears above: the caller sends a first and last name and gets only the age.\n'
