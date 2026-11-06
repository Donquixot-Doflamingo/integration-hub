#!/usr/bin/env bash
# Aadhaar OTP e-KYC demo through a local Hub (HUB-351). See README.md in this folder.
#
#   examples/aadhaar-okyc/run.sh                    # mockvendor plays the provider (no account needed)
#   examples/aadhaar-okyc/run.sh wrong-otp          # another mock scenario (see mockvendor/aadhaar-*.json)
#   HUB_DEMO_TARGET=sandbox \
#   CASHFREE_CLIENT_ID=... CASHFREE_CLIENT_SECRET=... \
#     examples/aadhaar-okyc/run.sh                  # the provider's real sandbox (test keys)
#   examples/aadhaar-okyc/run.sh serve              # start and keep running; call it yourself
#
# Optional: AADHAAR (default: the sandbox test number 655675523712), OTP (default: asked on the
# terminal; the sandbox OTP is always 111000 and no SMS is sent), HUB_PG_DSN, HUB_REDIS_ADDR.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
# shellcheck source=../lib/local-hub.sh
source "$root/examples/lib/local-hub.sh"

target="${HUB_DEMO_TARGET:-mock}"
scenario="${1:-happy}"
aadhaar="${AADHAAR:-655675523712}"
export HUB_DEMO_KEY=hk_test_example_kyc_onboarding_local_only # local test key (tenant kyc-onboarding)

hub_setup "${HUB_DEMO_STATE_DIR:-$root/.demo/aadhaar-okyc}" "$here"
case "$target" in
  mock)
    hub_set_creds cf-sid-test client_id=mock-client-id client_secret=mock-client-secret
    hub_start --mockvendor
    if [ "$scenario" != serve ]; then
      [ -f "$here/mockvendor/aadhaar-$scenario.json" ] || hub_die "unknown scenario $scenario (see $here/mockvendor/)"
      mock_scenario "$here/mockvendor/aadhaar-$scenario.json"
    else
      mock_scenario "$here/mockvendor/aadhaar-happy.json"
    fi
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

if [ "$scenario" = serve ]; then
  cat <<EOF

The Hub is running on $HUB_URL ($target). Try, in another terminal:

  curl -i $HUB_URL/v1/integrations/int_aadhaar_okyc/invoke/send_otp \\
    -H 'Authorization: Bearer $HUB_DEMO_KEY' -H 'Content-Type: application/json' \\
    -H "Idempotency-Key: send-\$(date +%s)" \\
    -d '{"aadhaar_number": "$aadhaar", "consent": true, "purpose": "KYC for a loan application"}'

  curl -i $HUB_URL/v1/integrations/int_aadhaar_okyc/invoke/verify_otp \\
    -H 'Authorization: Bearer $HUB_DEMO_KEY' -H 'Content-Type: application/json' \\
    -H "Idempotency-Key: verify-\$(date +%s)" -H 'X-Hub-Session: <X-Hub-Session from step 1>' \\
    -d '{"otp": "111000"}'

Press Ctrl-C to stop.
EOF
  wait
  exit 0
fi

run_id="$(date +%s)-$$"
printf '\n\033[1mStep 1: send the OTP\033[0m (the provider asks UIDAI to SMS an OTP to the mobile linked to the Aadhaar)\n'
hub_post /v1/integrations/int_aadhaar_okyc/invoke/send_otp "okyc-send-$run_id" "" \
  "{\"aadhaar_number\": \"$aadhaar\", \"consent\": true, \"purpose\": \"KYC for a loan application (demo)\"}"
session="$(hub_last_header X-Hub-Session)"
if [ "$HUB_LAST_STATUS" != 200 ] || [ -z "$session" ]; then
  printf '\nStep 1 did not open a session: the app shows the error and offers to try again. Done.\n'
  exit 0
fi

printf '\nThe app now shows "Enter the OTP sent to your Aadhaar-linked mobile", with Resend after 30 s.\n'
otp="${OTP:-}"
if [ -z "$otp" ]; then
  if [ "$target" = mock ]; then
    otp=111000
  else
    read -r -p "OTP (the sandbox sends no SMS; its OTP is 111000): " otp
  fi
fi

printf '\n\033[1mStep 2: verify the OTP\033[0m in the session step 1 opened\n'
hub_post /v1/integrations/int_aadhaar_okyc/invoke/verify_otp "okyc-verify-$run_id" "$session" "{\"otp\": \"$otp\"}"
printf '\nThe provider never appears above: the caller sees only int_aadhaar_okyc, X-Hub-Session and canonical fields.\n'
printf 'Audit rows of these requests are sealed (D6): psql "%s" -c "select request_id, payload_policy, status from hub.requests where integration_id = '"'"'int_aadhaar_okyc'"'"' order by created_at desc limit 2"\n' "$HUB_PG_DSN"
