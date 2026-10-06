#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
oc_bin=${OC_BIN:-oc}
namespace=kueueviz
route_timeout_seconds=${KUEUEVIZ_ROUTE_TIMEOUT_SECONDS:-120}
rollout_timeout=${KUEUEVIZ_ROLLOUT_TIMEOUT:-120s}
manifest="$repo_root/manifests/kueueviz-v0.19.4-openshift.yaml"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
run_oc() { "$oc_bin" "$@"; }

command -v "$oc_bin" >/dev/null || die "oc client not found: $oc_bin"
command -v jq >/dev/null || die 'jq is required to render the browser runtime config'
[[ -f "$manifest" ]] || die "KueueViz manifest not found: $manifest"
[[ "$route_timeout_seconds" =~ ^[1-9][0-9]*$ ]] || die 'KUEUEVIZ_ROUTE_TIMEOUT_SECONDS must be a positive integer'
run_oc whoami >/dev/null || die 'oc authentication is required'

route_host() {
  local name=$1 host deadline route_json admitted
  deadline=$((SECONDS + route_timeout_seconds))
  while (( SECONDS < deadline )); do
    route_json=$(run_oc get route "$name" -n "$namespace" -o json 2>/dev/null || true)
    if [[ -n "$route_json" ]]; then
      host=$(jq -r '.spec.host // empty' <<<"$route_json")
      admitted=$(jq -r --arg host "$host" '
        any(.status.ingress[]?;
          .host == $host and any(.conditions[]?; .type == "Admitted" and .status == "True"))
      ' <<<"$route_json")
      if [[ -n "$host" && "$admitted" == true ]]; then
        [[ "$host" =~ ^[A-Za-z0-9.-]+$ ]] || die "OpenShift returned an invalid host for Route $namespace/$name"
        printf '%s' "$host"
        return 0
      fi
    fi
    sleep 2
  done
  die "OpenShift did not generate and admit Route $namespace/$name within ${route_timeout_seconds}s"
}

run_oc apply -f "$manifest"
frontend_host=$(route_host kueueviz)
backend_host=$(route_host kueueviz-backend)

websocket_url="wss://${backend_host}"
frontend_origin="https://${frontend_host}"
window_env=$(jq -cn --arg url "$websocket_url" \
  '{VITE_WEBSOCKET_URL:$url,REACT_APP_WEBSOCKET_URL:$url}')
env_js="window.env = ${window_env};"
configmap_patch=$(jq -cn --arg env_js "$env_js" '{data:{"env.js":$env_js}}')

run_oc patch configmap kueueviz-frontend-env -n "$namespace" \
  --type=merge --patch "$configmap_patch"
run_oc set env deployment/kueueviz-backend -n "$namespace" \
  "KUEUEVIZ_ALLOWED_ORIGINS=${frontend_origin}"

# env.js is mounted via subPath, so restart the frontend to load the new value.
run_oc rollout restart deployment/kueueviz-frontend -n "$namespace"
run_oc rollout status deployment/kueueviz-backend -n "$namespace" --timeout="$rollout_timeout"
run_oc rollout status deployment/kueueviz-frontend -n "$namespace" --timeout="$rollout_timeout"

printf 'KueueViz ready: https://%s (backend WebSocket wss://%s)\n' "$frontend_host" "$backend_host"
