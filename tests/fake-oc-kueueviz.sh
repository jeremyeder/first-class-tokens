#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$(ruby -rjson -e 'puts JSON.generate(ARGV)' "$@")" >>"$KUEUEVIZ_OC_LOG"

case ${1:-} in
  whoami)
    printf 'demo-user\n'
    ;;
  apply)
    [[ "$*" == *manifests/kueueviz-v0.19.4-openshift.yaml* ]] || exit 2
    ;;
  get)
    [[ ${2:-} == route ]] || exit 2
    case ${3:-} in
      kueueviz) host=frontend.generated.example.test ;;
      kueueviz-backend) host=backend.generated.example.test ;;
      *) exit 2 ;;
    esac
    admitted=${KUEUEVIZ_FAKE_ROUTE_ADMITTED:-True}
    printf '{"spec":{"host":"%s"},"status":{"ingress":[{"host":"%s","conditions":[{"type":"Admitted","status":"%s"}]}]}}\n' \
      "$host" "$host" "$admitted"
    ;;
  patch)
    [[ ${2:-} == configmap && ${3:-} == kueueviz-frontend-env ]] || exit 2
    ;;
  set)
    [[ ${2:-} == env && ${3:-} == deployment/kueueviz-backend ]] || exit 2
    ;;
  rollout)
    case ${2:-} in
      restart) [[ ${3:-} == deployment/kueueviz-frontend ]] || exit 2 ;;
      status) [[ ${3:-} == deployment/kueueviz-backend || ${3:-} == deployment/kueueviz-frontend ]] || exit 2 ;;
      *) exit 2 ;;
    esac
    ;;
  *)
    printf 'unexpected fake oc command: %s\n' "$*" >&2
    exit 2
    ;;
esac
