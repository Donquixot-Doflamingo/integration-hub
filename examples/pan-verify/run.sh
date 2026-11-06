#!/usr/bin/env bash
# PAN verification demo through a local Hub (HUB-352). See README.md in this folder.
#
#   examples/pan-verify/run.sh                       # mockvendor plays the provider (valid PAN)
#   examples/pan-verify/run.sh invalid               # another mock scenario (../aadhaar-okyc/mockvendor/pan-*.json)
#   HUB_DEMO_TARGET=sandbox \
#   CASHFREE_CLIENT_ID=... CASHFREE_CLIENT_SECRET=... \
#     examples/pan-verify/run.sh                     # the provider's real sandbox (test keys)
#
# Optional: PAN (default: the sandbox test PAN ABCPV1234D), NAME (default "John Doe"; empty to
# skip the name match), HUB_PG_DSN, HUB_REDIS_ADDR. The overlay (vendor, account, integration,
# transformers) is shared with the Aadhaar example: ../aadhaar-okyc/.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
overlay="$root/examples/aadhaar-okyc"
# shellcheck source=../lib/local-hub.sh
source "$root/examples/lib/local-hub.sh"

target="${HUB_DEMO_TARGET:-mock}"
scenario="${1:-valid}"
pan="${PAN:-ABCPV1234D}"
name="${NAME-John Doe}"
export HUB_DEMO_KEY=hk_test_example_kyc_onboarding_local_only # local test key (tenant kyc-onboarding)

hub_setup "${HUB_DEMO_STATE_DIR:-$root/.demo/pan-verify}" "$overlay"
case "$target" in
  mock)
    [ -f "$overlay/mockvendor/pan-$scenario.json" ] || hub_die "unknown scenario $scenario (see $overlay/mockvendor/pan-*.json)"
    hub_set_creds cf-sid-test client_id=mock-client-id client_secret=mock-client-secret
    hub_start --mockvendor
    mock_scenario "$overlay/mockvendor/pan-$scenario.json"
    ;;
  sandbox)
    : "${CASHFREE_CLIENT_ID:?set CASHFREE_CLIENT_ID to your sandbox (test) client id}"
    : "${CASHFREE_CLIENT_SECRET:?set CASHFREE_CLIENT_SECRET to your sandbox (test) client secret}"
    hub_set_creds cf-sid-test "client_id=$CASHFREE_CLIENT_ID" "client_secret=$CASHFREE_CLIENT_SECRET"
    hub_set_region cf-sid-test.yaml sandbox
    hub_start
    hub_log "target: the provider's sandbox (https://sandbox.cashfree.com/verification); your IP must be whitelisted there"
    ;;
  *) hub_die "HUB_DEMO_TARGET must be mock or sandbox, got $target" ;;
esac

body="{\"pan\": \"$pan\"}"
[ -n "$name" ] && body="{\"pan\": \"$pan\", \"name\": \"$name\"}"
key="pan-$(date +%s)-$$"
printf '\n\033[1mVerify the PAN\033[0m (one sync call; billed per call, so it carries an Idempotency-Key)\n'
hub_post /v1/integrations/int_pan_verify/invoke "$key" "" "$body"
printf '\n\033[1mThe same call again with the same key\033[0m: the Hub replays the stored answer, the provider is not called (or paid) twice\n'
hub_post /v1/integrations/int_pan_verify/invoke "$key" "" "$body"
