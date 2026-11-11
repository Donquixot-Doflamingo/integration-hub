#!/usr/bin/env bash
# Checks the Integration Hub admin policies (HUB-478, HUB-479): the generator's --check, then
# `opa check --strict` and `opa test` on authz-policies/integration-hub with the package placeholder
# substituted the way ISFC's authz-policies/scripts/check.sh does it.
#
#   scripts/check-authz.sh            generator --check + opa check + opa test
#   VERBOSE=1 scripts/check-authz.sh  opa test -v
#   SKIP_GEN=1 scripts/check-authz.sh skip the generator check
#
# opa comes from $OPA_BIN or PATH; otherwise from docker ($OPA_IMAGE, default
# docker.io/openpolicyagent/opa:1.4.2-static, the version ISFC pins). A docker VM such as colima
# shares only $HOME with the host, so the work directory lives under $HOME when docker is used.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/authz-policies/integration-hub"
PKG="integrationhub"
OPA_IMAGE="${OPA_IMAGE:-docker.io/openpolicyagent/opa:1.4.2-static}"
log() { printf '[authz] %s\n' "$*" >&2; }

if [[ -z "${SKIP_GEN:-}" ]]; then
  log "gen-authz --check"
  node "$ROOT/scripts/gen-authz.mjs" --check
fi

mode=bin
OPA="${OPA_BIN:-$(command -v opa || true)}"
if [[ -z "$OPA" ]]; then
  command -v docker >/dev/null 2>&1 || { log "opa not found: install opa or docker"; exit 2; }
  mode=docker
fi
base="${TMPDIR:-/tmp}"
[[ "$mode" == docker ]] && base="$HOME"
work="$(mktemp -d "$base/.hub-authz.XXXXXX")"
trap 'rm -rf "$work"' EXIT
(cd "$SRC" && find . -name '*.rego' -print0 | while IFS= read -r -d '' f; do
  mkdir -p "$work/$(dirname "$f")"
  sed "s|{__package_name__}|$PKG|g" "$f" >"$work/$f"
done)
chmod -R a+rX "$work"

run_opa() {
  if [[ "$mode" == bin ]]; then (cd "$work" && "$OPA" "$@"); else
    docker run --rm -v "$work:/work:ro" -w /work "$OPA_IMAGE" "$@"
  fi
}
log "opa check --strict (${mode})"
run_opa check --strict .
log "opa test"
run_opa test ${VERBOSE:+-v} .
