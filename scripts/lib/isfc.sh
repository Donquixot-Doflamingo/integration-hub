#!/usr/bin/env bash
# ISFC mode of scripts/dev-up.sh (`dev-up.sh --isfc`, HUB-497). Source it; do not run it.
#
# The console signs in through a running ISFC stack (auth-service, Hydra, api-gateway) and the Hub
# admin runs in HUB_ADMIN_AUTH_MODE=isfc: gateway identity headers, a Hydra token and OPA. dev-up
# starts none of those; it only points the Hub at them, so every URL comes from the environment:
#   ISFC_AUTH_URL         the shared ISFC auth host (login, token exchange)         -> HUB_ISFC_AUTH_URL
#   ISFC_OIDC_ISSUER      the Hydra issuer that signs the access tokens             -> HUB_OIDC_ISSUER
#   ISFC_ADMIN_UPSTREAM   the ISFC api-gateway route to this Hub's admin API       -> HUB_ADMIN_UPSTREAM
#   ISFC_PUBLIC_ADMIN_URL (optional)  link target to change ISFC-owned tenant data     -> PUBLIC_ISFC_ADMIN_URL
#   HUB_OPA_URL           the OPA that decides admin requests (ISFC bundle)
#   ISFC_PLATFORM_TENANT_ID (optional)  ISFC UUID of the platform tenant           -> HUB_ISFC_PLATFORM_TENANT_ID
#   ISFC_GATEWAY_URL (optional)  the ISFC api-gateway host, for the tenant switcher -> HUB_ISFC_GATEWAY_URL
#   HUB_ISFC_CONSOLE_CLIENT_ID (optional, default integration-hub-console)
# Nothing here is ever sent to a real ISFC unless the URLs you export point at one.

# isfc_is_http_url VALUE: succeeds for an absolute http(s) URL without spaces.
isfc_is_http_url() {
  [[ "$1" =~ ^https?://[^/[:space:]]+(/[^[:space:]]*)?$ ]]
}

# isfc_check: prints one line per problem and returns 1 when the ISFC settings are missing or malformed.
isfc_check() {
  local bad=0 name val
  for name in ISFC_AUTH_URL ISFC_OIDC_ISSUER ISFC_ADMIN_UPSTREAM HUB_OPA_URL; do
    val="${!name:-}"
    if [ -z "$val" ]; then
      echo "$name is required with --isfc"
      bad=1
    elif ! isfc_is_http_url "$val"; then
      echo "$name must be an absolute http or https URL"
      bad=1
    fi
  done
  for name in ISFC_GATEWAY_URL ISFC_PUBLIC_ADMIN_URL; do
    val="${!name:-}"
    if [ -n "$val" ] && ! isfc_is_http_url "$val"; then
      echo "$name must be an absolute http or https URL"
      bad=1
    fi
  done
  return "$bad"
}

# isfc_admin_env: KEY=VALUE lines for the hub admin role. No dev issuer (the role refuses it).
isfc_admin_env() {
  echo "HUB_ADMIN_AUTH_MODE=isfc"
  echo "HUB_OIDC_ISSUER=$ISFC_OIDC_ISSUER"
  echo "HUB_ISFC_CONSOLE_CLIENT_ID=${HUB_ISFC_CONSOLE_CLIENT_ID:-integration-hub-console}"
  echo "HUB_OPA_URL=$HUB_OPA_URL"
  echo "HUB_ISFC_AUTH_URL=$ISFC_AUTH_URL"
  [ -z "${ISFC_PLATFORM_TENANT_ID:-}" ] || echo "HUB_ISFC_PLATFORM_TENANT_ID=$ISFC_PLATFORM_TENANT_ID"
}

# isfc_console_env: KEY=VALUE lines for the console dev server (HUB_SESSION_SECRET is added by the caller).
isfc_console_env() {
  echo "PUBLIC_AUTH_MODE=isfc"
  echo "HUB_ISFC_AUTH_URL=$ISFC_AUTH_URL"
  echo "HUB_ISFC_CLIENT_ID=${HUB_ISFC_CONSOLE_CLIENT_ID:-integration-hub-console}"
  echo "HUB_ADMIN_UPSTREAM=$ISFC_ADMIN_UPSTREAM"
  [ -z "${ISFC_GATEWAY_URL:-}" ] || echo "HUB_ISFC_GATEWAY_URL=$ISFC_GATEWAY_URL"
  [ -z "${ISFC_PUBLIC_ADMIN_URL:-}" ] || echo "PUBLIC_ISFC_ADMIN_URL=$ISFC_PUBLIC_ADMIN_URL"
  [ -z "${ISFC_PLATFORM_TENANT_ID:-}" ] || echo "HUB_ISFC_PLATFORM_TENANT_ID=$ISFC_PLATFORM_TENANT_ID"
}
