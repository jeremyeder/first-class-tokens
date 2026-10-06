#!/usr/bin/env bash
set -euo pipefail

oc_bin=${OC_BIN:-oc}
namespace=kueue-demo
timeout_seconds=${DEMO_TIMEOUT_SECONDS:-120}
item=work-item-001
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
manifest="$repo_root/manifests/business-work-items/10-renewal-work-item.yaml"
evidence_root=${DEMO_CAPTURE_ROOT:-$repo_root/artifacts}/$namespace/intake-e2e

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
run_oc() { "$oc_bin" "$@"; }

# Preserve the prior synthetic status/history locally, then recreate only the
# exact fixture CR so this run must obtain a fresh model decision.
mkdir -p "$evidence_root"
run_oc get businessworkitem "$item" -n "$namespace" -o yaml > "$evidence_root/businessworkitem-before.yaml" || die "expected seeded synthetic item $namespace/$item"
run_oc delete businessworkitem "$item" -n "$namespace" --wait=true
restore_seed() { run_oc apply -f "$manifest" >/dev/null || true; }
trap restore_seed EXIT
started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
run_oc apply -f "$manifest"

decision_ready() {
  local object job_name job workload_name
  object=$(run_oc get businessworkitem "$item" -n "$namespace" -o json) || return 1
  jq -e --arg started_at "$started_at" '
    .spec.provenance == "synthetic" and
    .status.observedGeneration == .metadata.generation and
    .status.decision.evaluatedAt >= $started_at and
    .status.decision.fareClass == "First class" and
    .status.decision.priorityClass == "fare-first-class" and
    .status.decision.priorityValue == 1000 and
    any(.status.conditions[]?; .type == "DecisionReady" and .status == "True" and .reason == "DecisionAccepted") and
    any(.status.conditions[]?; .type == "ProjectionReady" and .status == "True" and .reason == "JobPatched") and
    .status.linkedWorkload.jobPatched == true and
    .status.linkedWorkload.admitted != true and
    .status.linkedWorkload.reserved != true
  ' <<<"$object" >/dev/null || return 1

  job_name=$(jq -r '.spec.link.jobName // .spec.link.name // empty' <<<"$object")
  [[ -n "$job_name" ]] || return 1
  job=$(run_oc get job "$job_name" -n "$namespace" -o json) || return 1
  jq -e '.metadata.labels["kueue.x-k8s.io/priority-class"] == "fare-first-class"' <<<"$job" >/dev/null || return 1

  workload_name=$(jq -r '.status.linkedWorkload.name // empty' <<<"$object")
  [[ -n "$workload_name" ]] || return 1
  workload=$(run_oc get workload "$workload_name" -n "$namespace" -o json) || return 1
  jq -e '
    .spec.priority == 1000 and
    .spec.priorityClassRef.name == "fare-first-class" and
    (.status.admission == null) and
    (any(.status.conditions[]?; .type == "Admitted" and .status == "True") | not)
  ' <<<"$workload" >/dev/null
}

attempts=$(( (timeout_seconds + 2) / 3 ))
(( attempts > 0 )) || attempts=1
for attempt in $(seq 1 "$attempts"); do
  if decision_ready; then
    object=$(run_oc get businessworkitem "$item" -n "$namespace" -o json)
    job_name=$(jq -r '.spec.link.jobName // .spec.link.name' <<<"$object")
    workload_name=$(jq -r '.status.linkedWorkload.name' <<<"$object")
    run_oc get businessworkitem "$item" -n "$namespace" -o yaml > "$evidence_root/businessworkitem-after.yaml"
    run_oc get job "$job_name" -n "$namespace" -o yaml > "$evidence_root/job-after.yaml"
    run_oc get workload "$workload_name" -n "$namespace" -o yaml > "$evidence_root/workload-after.yaml"
    printf 'synthetic intake E2E passed: fresh model decision for %s/%s projected to an unreserved fare-first-class Workload\nEvidence: %s\n' "$namespace" "$item" "$evidence_root"
    exit 0
  fi
  sleep 3
done

printf 'intake E2E did not converge within %ss; current BusinessWorkItem and queue state:\n' "$timeout_seconds" >&2
run_oc get businessworkitem "$item" -n "$namespace" -o yaml >&2 || true
run_oc get jobs,workloads -n "$namespace" -o wide >&2 || true
die 'expected accepted model decision and pending fare-first-class Workload'
