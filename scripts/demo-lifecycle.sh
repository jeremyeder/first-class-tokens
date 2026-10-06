#!/usr/bin/env bash
set -euo pipefail

# @spec DEMO-LIFECYCLE-001, DEMO-LIFECYCLE-002, DEMO-LIFECYCLE-003, DEMO-LIFECYCLE-004, DEMO-LIFECYCLE-006

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
manifest="$repo_root/manifests/00-demo-lane.yaml"
oc_bin=${OC_BIN:-oc}
namespace=${DEMO_NAMESPACE:-kueue-demo}
kueue_namespace=${KUEUE_NAMESPACE:-openshift-kueue-operator}
kueue_instance=${KUEUE_INSTANCE:-default-kueue}
timeout_seconds=${DEMO_TIMEOUT_SECONDS:-90}
capture_root=${DEMO_CAPTURE_ROOT:-$repo_root/artifacts}
fixture_jobs_manifest=${FIXTURE_JOBS_MANIFEST:-$repo_root/manifests/business-fixtures/10-synthetic-jobs.yaml}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }

validate_namespace() {
  [[ "$namespace" =~ ^kueue-demo(-[a-z0-9]([-a-z0-9]{0,50}[a-z0-9])?)?$ ]] || die "refusing namespace outside demo allowlist: $namespace"
}

run_oc() { "$oc_bin" "$@"; }

render_resources() {
  local selection=$1
  ruby -ryaml - "$manifest" "$namespace" "$selection" <<'RUBY'
path, namespace, selection = ARGV
documents = YAML.load_stream(File.read(path)).compact
documents.each do |document|
  metadata = document.fetch("metadata", {})
  name = metadata["name"]
  include_document = case selection
  when "foundation" then document["kind"] != "Job"
  when "holder" then document["kind"] == "Job" && name == "auth-regression-fix"
  when "waiters" then document["kind"] == "Job" && ["renewal-recovery-plan", "documentation-refresh"].include?(name)
  else false
  end
  next unless include_document
  if document["kind"] == "Namespace"
    metadata["name"] = namespace
  elsif metadata.key?("namespace")
    metadata["namespace"] = namespace
  end
  puts YAML.dump(document)
  puts "---"
end
RUBY
}

apply_selection() { render_resources "$1" | run_oc apply --server-side -f -; }
state_json() { run_oc get jobs,workloads -n "$namespace" -o json; }

auth_admitted() {
  state_json | jq -e '
    ([.items[] | select(.kind == "Job" and .metadata.name == "auth-regression-fix") | select(.status.active == 1)] | length == 1) and
    ([.items[] | select(.kind == "Workload" and any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "auth-regression-fix")) | select(any(.status.conditions[]?; (.type == "Admitted" and .status == "True")))] | length == 1)
  ' >/dev/null
}

assert_baseline() {
  state_json | jq -e '
    ([.items[] | select(.kind == "Job" and .metadata.name == "auth-regression-fix") | select(.status.active == 1)] | length == 1) and
    ([.items[] | select(.kind == "Job" and (.metadata.name == "renewal-recovery-plan" or .metadata.name == "documentation-refresh")) | select(.spec.suspend == true)] | length == 2) and
    ([.items[] | select(.kind == "Workload" and any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "auth-regression-fix")) | select(any(.status.conditions[]?; (.type == "Admitted" and .status == "True")))] | length == 1) and
    ([.items[] | select(.kind == "Workload" and any(.metadata.ownerReferences[]?; .kind == "Job" and (.name == "renewal-recovery-plan" or .name == "documentation-refresh"))) | select(.status.admission == null)] | length == 2)
  ' >/dev/null || die "baseline invariant failed"
}

renewal_pending() {
  state_json | jq -e '
    ([.items[] | select(.kind == "Job" and .metadata.name == "renewal-recovery-plan") | select(.spec.suspend == true)] | length == 1) and
    ([.items[] | select(.kind == "Workload" and any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "renewal-recovery-plan")) | select(.status.admission == null)] | length == 1)
  ' >/dev/null
}

renewal_promoted() {
  run_oc get workloads -n "$namespace" -o json | jq -e '
    [.items[] | select(any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "renewal-recovery-plan")) | select(.spec.priority == 1000 and .spec.priorityClassRef.name == "fare-first-class")] | length == 1
  ' >/dev/null
}

show_state() {
  run_oc get jobs,workloads -n "$namespace" -o wide
  run_oc get clusterqueue business-priority-demo -o yaml
  run_oc get events -n "$namespace" --sort-by=.lastTimestamp
}

wait_for() {
  local description=$1 predicate=$2 attempts=$((timeout_seconds / 3)) attempt
  (( attempts > 0 )) || attempts=1
  for attempt in $(seq 1 "$attempts"); do
    if "$predicate"; then printf '%s ready (attempt %s)\n' "$description" "$attempt"; return 0; fi
    sleep 3
  done
  printf '%s did not converge within %ss\n' "$description" "$timeout_seconds" >&2
  show_state >&2 || true
  return 1
}

capture() {
  local stage=$1
  local destination="$capture_root/$namespace/$stage"
  mkdir -p "$destination"
  run_oc get namespace "$namespace" -o yaml > "$destination/namespace.yaml"
  run_oc get jobs,workloads -n "$namespace" -o yaml > "$destination/workloads.yaml"
  run_oc get clusterqueue business-priority-demo -o yaml > "$destination/clusterqueue.yaml"
  run_oc get events -n "$namespace" --sort-by=.lastTimestamp -o yaml > "$destination/events.yaml"
  printf 'captured %s\n' "$destination"
}

demo_check() {
  command -v "$oc_bin" >/dev/null || die "oc client not found: $oc_bin"
  run_oc whoami >/dev/null
  run_oc api-resources --api-group=kueue.x-k8s.io -o name | grep -Eq '^workloads(\.kueue\.x-k8s\.io)?$'
  run_oc get kueue "$kueue_instance" -n "$kueue_namespace" -o json | jq -e 'any(.status.conditions[]?; .type == "Ready" and .status == "True")' >/dev/null || die "Kueue instance is not Ready"
}

demo_down() {
  validate_namespace
  run_oc delete namespace "$namespace" --ignore-not-found --wait=true --timeout="${timeout_seconds}s"
}

delete_synthetic_business_items() {
  # Only this labeled synthetic input set is owned by the demo. Do not clear
  # the namespace: it also contains the controller, model, ImageStreams, and
  # the persistent model cache PVC.
  if run_oc api-resources --api-group=tokens.jeder.github.com -o name | grep -Eq '^businessworkitems(\.tokens\.jeder\.github\.com)?$'; then
    run_oc delete businessworkitems -n "$namespace" \
      -l app.kubernetes.io/part-of=business-priority-demo,data-provenance=synthetic \
      --ignore-not-found --wait=true --timeout="${timeout_seconds}s"
  fi
}

reset_lane_resources() {
  delete_synthetic_business_items
  run_oc delete job auth-regression-fix renewal-recovery-plan documentation-refresh \
    -n "$namespace" --ignore-not-found --wait=true --timeout="${timeout_seconds}s"
}

fixture_job_names_json() {
  [[ -f "$fixture_jobs_manifest" ]] || die "fixture Job manifest not found: $fixture_jobs_manifest"
  ruby -ryaml -rjson - "$fixture_jobs_manifest" <<'RUBY'
path = ARGV.fetch(0)
documents = YAML.load_stream(File.read(path)).compact
baseline_names = %w[auth-regression-fix renewal-recovery-plan documentation-refresh]
abort "fixture manifest must contain exactly 17 Job documents" unless documents.length == 17 && documents.all? { |doc| doc["kind"] == "Job" }
names = documents.map do |doc|
  metadata = doc.fetch("metadata", {})
  name = metadata["name"]
  labels = metadata.fetch("labels", {})
  abort "fixture manifest contains an invalid Job name" unless name&.match?(/\A[a-z0-9]([-a-z0-9]*[a-z0-9])?\z/)
  abort "fixture manifest Job #{name} lacks the synthetic fixture label" unless labels["demo.kueue.io/fixture"] == "synthetic-event-feed"
  abort "fixture manifest must not include baseline Job #{name}" if baseline_names.include?(name)
  name
end
abort "fixture manifest contains duplicate Job names" unless names.uniq.length == 17
puts JSON.generate(names)
RUBY
}

delete_fixture_jobs() {
  local names_json
  local -a fixture_job_names
  names_json=$(fixture_job_names_json) || die "could not validate fixture Job manifest"
  mapfile -t fixture_job_names < <(jq -r '.[]' <<<"$names_json")
  ((${#fixture_job_names[@]} == 17)) || die "validated fixture Job list changed unexpectedly"
  run_oc delete job "${fixture_job_names[@]}" -n "$namespace" \
    --ignore-not-found --wait=true --timeout="${timeout_seconds}s"
}

fixture_resources_gone() {
  local names_json
  names_json=$(fixture_job_names_json) || return 1
  run_oc get jobs -n "$namespace" -o json | jq -e --argjson fixture_jobs "$names_json" '
    [.items[] | select(.metadata.name as $name | ($fixture_jobs | index($name)) != null)] | length == 0
  ' >/dev/null || return 1
  run_oc get workloads -n "$namespace" -o json | jq -e --argjson fixture_jobs "$names_json" '
    [.items[] | select(any(.metadata.ownerReferences[]?; .kind == "Job" and (.name as $name | ($fixture_jobs | index($name)) != null)))] | length == 0
  ' >/dev/null
}

synthetic_business_items_gone() {
  if run_oc api-resources --api-group=tokens.jeder.github.com -o name | grep -Eq '^businessworkitems(\.tokens\.jeder\.github\.com)?$'; then
    run_oc get businessworkitems -n "$namespace" \
      -l app.kubernetes.io/part-of=business-priority-demo,data-provenance=synthetic -o json \
      | jq -e '.items | length == 0' >/dev/null
  fi
}

demo_fixtures_clean() {
  local validated_fixture_jobs
  validate_namespace
  demo_check
  run_oc get namespace "$namespace" -o name >/dev/null || die "demo namespace does not exist: $namespace"
  validated_fixture_jobs=$(fixture_job_names_json) || die "could not validate fixture Job manifest"
  [[ $(jq 'length' <<<"$validated_fixture_jobs") == 17 ]] || die "validated fixture Job list changed unexpectedly"

  # Remove controller inputs and extra fixtures while the capacity holder is
  # still running; then rebuild and verify the baseline lane.
  delete_synthetic_business_items
  delete_fixture_jobs
  wait_for "fixture Job and Workload cleanup" fixture_resources_gone || die "fixture Jobs or Workloads remain"

  demo_up
  synthetic_business_items_gone || die "synthetic BusinessWorkItems remain after cleanup"
  fixture_resources_gone || die "fixture Jobs or Workloads reappeared after cleanup"
  assert_baseline
  printf 'fixture cleanup complete: synthetic inputs removed; capacity-held baseline restored in %s\n' "$namespace"
}

demo_workloads_gone() {
  run_oc get workloads -n "$namespace" -o json | jq -e '
    [.items[] | select(any(.metadata.ownerReferences[]?; .kind == "Job" and (.name == "auth-regression-fix" or .name == "renewal-recovery-plan" or .name == "documentation-refresh")))] | length == 0
  ' >/dev/null
}

submit_holder_first() {
  local attempt
  for attempt in 1 2; do
    apply_selection holder
    if wait_for "auth capacity holder" auth_admitted; then return 0; fi
    run_oc delete job auth-regression-fix -n "$namespace" --ignore-not-found --wait=true --timeout="${timeout_seconds}s"
  done
  die "auth holder could not be admitted after retry"
}

demo_up() {
  validate_namespace
  demo_check
  if run_oc get namespace "$namespace" -o name >/dev/null 2>&1; then
    reset_lane_resources
    wait_for "previous demo Workloads cleanup" demo_workloads_gone || die "old demo Workloads did not clear"
  fi
  apply_selection foundation
  run_oc wait --for=jsonpath='{.status.phase}'=Active "namespace/$namespace" --timeout="${timeout_seconds}s"
  submit_holder_first
  apply_selection waiters
  wait_for "capacity-held baseline" assert_baseline || die "baseline was not established"
  capture baseline
}

demo_baseline() { validate_namespace; assert_baseline; capture baseline; }

demo_promote() {
  validate_namespace
  renewal_pending || die "renewal must be pending and unreserved before promotion"
  run_oc label job renewal-recovery-plan -n "$namespace" kueue.x-k8s.io/priority-class=fare-first-class --overwrite
  wait_for "renewal promotion" renewal_promoted || die "Kueue did not observe renewal priority 1000"
  capture promoted
}

demo_e2e() {
  local original_namespace=$namespace
  namespace="kueue-demo-e2e-$(date -u +%Y%m%d%H%M%S)-$RANDOM"
  validate_namespace
  e2e_cleanup_namespace=$namespace
  [[ ${KEEP:-0} == 1 ]] || trap 'DEMO_NAMESPACE="$e2e_cleanup_namespace" "$repo_root/scripts/demo-lifecycle.sh" down' EXIT
  demo_up
  demo_promote
  printf 'e2e proof completed in %s\n' "$namespace"
  [[ ${KEEP:-0} == 1 ]] && printf 'KEEP=1 retained %s\n' "$namespace"
  namespace=$original_namespace
}

usage() {
  cat <<'USAGE'
Usage: scripts/demo-lifecycle.sh {check|up|baseline|fixtures-clean|promote|e2e|down}
Environment: DEMO_NAMESPACE, DEMO_TIMEOUT_SECONDS, KEEP=1, OC_BIN,
KUEUE_NAMESPACE, KUEUE_INSTANCE, DEMO_CAPTURE_ROOT.

up resets only demo Jobs and labeled synthetic BusinessWorkItems. down deletes
the entire allowlisted namespace, including the model cache PVC.
fixtures-clean removes synthetic BusinessWorkItems and the 17 manifest-listed
fixture Jobs, waits for their Workloads to disappear, then restores the baseline.
USAGE
}

case ${1:-help} in
  check) demo_check ;;
  up) demo_up ;;
  baseline) demo_baseline ;;
  fixtures-clean) demo_fixtures_clean ;;
  promote) demo_promote ;;
  e2e) demo_e2e ;;
  down) demo_down ;;
  help|-h|--help) usage ;;
  *) usage >&2; exit 2 ;;
esac
