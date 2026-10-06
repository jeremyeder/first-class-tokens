#!/usr/bin/env bash
set -euo pipefail

oc_bin=${OC_BIN:-oc}
timeout_seconds=${UWM_TIMEOUT_SECONDS:-300}
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
run_oc() { "$oc_bin" "$@"; }

merge_config() {
  local name=$1 namespace=$2 desired_file=$3 existing current merged patch
  existing=$(run_oc get configmap "$name" -n "$namespace" -o json --ignore-not-found) || die "cannot read ConfigMap $namespace/$name"
  if [[ -n "$existing" ]]; then
    current=$(jq -r '.data["config.yaml"] // "{}"' <<<"$existing")
    merged=$(CURRENT_CONFIG="$current" ruby -ryaml - "$desired_file" <<'RUBY'
require "yaml"
desired_path = ARGV.fetch(0)
base = YAML.safe_load(ENV.fetch("CURRENT_CONFIG"), aliases: true) || {}
desired_document = YAML.safe_load(File.read(desired_path), aliases: true) || {}
desired = desired_document.fetch("data", {}).fetch("config.yaml", "{}")
addition = YAML.safe_load(desired, aliases: true) || {}
merge = lambda do |left, right|
  right.each do |key, value|
    left[key] = if left[key].is_a?(Hash) && value.is_a?(Hash)
      merge.call(left[key], value)
    else
      value
    end
  end
  left
end
puts YAML.dump(merge.call(base, addition))
RUBY
    ) || die "cannot merge supported settings for $namespace/$name"
    [[ "$merged" != "$current" ]] || return 0
    patch=$(jq -n --arg config "$merged" '{data:{"config.yaml":$config}}')
    run_oc patch configmap "$name" -n "$namespace" --type=merge -p "$patch"
  else
    current=$(ruby -ryaml -rjson -e 'd=YAML.safe_load(File.read(ARGV.fetch(0)), aliases:true); puts JSON.generate(d)' "$desired_file") || die "invalid desired ConfigMap: $desired_file"
    printf '%s\n' "$current" | run_oc apply -f -
  fi
}

run_oc whoami >/dev/null || die 'OpenShift login is required'
run_oc get storageclass gp3-csi >/dev/null || die 'required gp3-csi StorageClass is unavailable'

merge_config cluster-monitoring-config openshift-monitoring "$repo_root/manifests/observability/cluster-monitoring-config.yaml"

attempts=$(( (timeout_seconds + 2) / 3 ))
(( attempts > 0 )) || attempts=1
uwm_config=''
for attempt in $(seq 1 "$attempts"); do
  uwm_config=$(run_oc get configmap user-workload-monitoring-config -n openshift-user-workload-monitoring -o name --ignore-not-found 2>/dev/null || true)
  [[ -n "$uwm_config" ]] && break
  sleep 3
done
[[ -n "$uwm_config" ]] || die "user-workload monitoring ConfigMap did not appear within ${timeout_seconds}s"
merge_config user-workload-monitoring-config openshift-user-workload-monitoring "$repo_root/manifests/observability/user-workload-monitoring-config.yaml"

for attempt in $(seq 1 "$attempts"); do
  pods=$(run_oc get pods -n openshift-user-workload-monitoring -o json) || die 'cannot read user-workload monitoring pods'
  if jq -e '
    (.items | length > 0) and
    all(.items[]; .status.phase == "Running" and
      ((.status.containerStatuses // []) | length > 0) and
      all(.status.containerStatuses[]; .ready == true))
  ' <<<"$pods" >/dev/null; then
    printf 'user-workload monitoring is ready (%d pods); retained for 7d with bounded persistent storage\n' "$(jq '.items|length' <<<"$pods")"
    exit 0
  fi
  sleep 3
done

run_oc get pods -n openshift-user-workload-monitoring -o wide >&2 || true
die "user-workload monitoring pods did not become ready within ${timeout_seconds}s"
