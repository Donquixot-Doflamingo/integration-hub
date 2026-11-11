#!/usr/bin/env bash
# Structure checks for deploy/isfc (HUB-497): the NetworkPolicy opens the admin port to the gateway
# and labelled automation only, the route matches AUTH_PLAN §9, the values enable the OPA sidecar
# for platform integration-hub and hold no secret value, and every alert has its runbook file.
# Text checks only (no YAML parser or cluster needed).
#   bash scripts/tests/isfc_deploy_test.sh
set -uo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
d="$here/../../deploy/isfc"
rb="$here/../../docs/runbooks"
fails=0
has() { if grep -Eq -- "$3" "$2"; then echo "ok   $1"; else echo "FAIL $1: $2 lacks /$3/"; fails=$((fails + 1)); fi; }
hasnt() { if grep -Eq -- "$3" "$2"; then echo "FAIL $1: $2 has /$3/"; fails=$((fails + 1)); else echo "ok   $1"; fi; }

np="$d/networkpolicy-hub-admin.yaml"
has "policy is ingress only" "$np" 'policyTypes: \[Ingress\]'
has "policy selects the admin" "$np" 'app.kubernetes.io/name: hub-admin'
has "gateway may reach 8081" "$np" 'app.kubernetes.io/name: api-gateway'
has "automation is label-gated" "$np" 'integration-hub.io/admin-access: "true"'
hasnt "no open CIDR" "$np" '0\.0\.0\.0/0|ipBlock'
hasnt "no empty selector (matches everything)" "$np" '(namespaceSelector|podSelector): \{\}'
n8081="$(grep -c 'port: 8081' "$np")"
[ "$n8081" = 2 ] && echo "ok   exactly two rules open 8081" || { echo "FAIL 8081 rules: $n8081"; fails=$((fails + 1)); }

rt="$d/gateway-route.yaml"
for kv in 'name: integration-hub-admin' 'prefix: /hub/admin' 'strip_prefix: true' 'rewrite_prefix: /admin' 'auth: required' 'tenant: required' 'rate_limit: per-user' 'timeout: 30s' ':8081$'; do
  has "route $kv" "$rt" "^[ -]*(upstream: .*)?$kv|upstream: .*$kv"
done

v="$d/values-integration-hub-admin.yaml"
has "opa enabled" "$v" '^  enabled: true'
has "opa platform" "$v" '^  platform: integration-hub'
has "admin in isfc mode" "$v" 'HUB_ADMIN_AUTH_MODE: isfc'
has "opa on loopback" "$v" 'HUB_OPA_URL: http://127\.0\.0\.1:8181'
hasnt "no secret value inline" "$v" 'HUB_ISFC_(CLIENT_SECRET|EVENTS_SECRET): [^ ]'
hasnt "OPA is never disabled" "$v" 'HUB_OPA_DISABLED'

for a in $(grep -E '^ +- alert: ' "$here/../../deploy/alerts/hub.rules.yaml" | sed 's/.*alert: //'); do
  [ -f "$rb/$a.md" ] && echo "ok   runbook for $a" || { echo "FAIL no runbook $rb/$a.md"; fails=$((fails + 1)); }
done
has "isfc-auth covers OPA down" "$rb/isfc-auth.md" '^## .*OPA'
has "isfc-auth covers sync stale" "$rb/isfc-auth.md" '^## .*[Ss]ync'
has "isfc-auth covers key revocation" "$rb/isfc-auth.md" '^## .*revocation'

[ "$fails" = 0 ] || { echo "$fails failure(s)"; exit 1; }
echo "all ok"
