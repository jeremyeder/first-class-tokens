#!/usr/bin/env bash
set -euo pipefail

oc_bin=${OC_BIN:-oc}
namespace=${DEMO_NAMESPACE:-kueue-demo}
timeout_seconds=${FIXTURE_E2E_TIMEOUT_SECONDS:-360}
rendered=${FIXTURE_RENDER_PATH:-artifacts/generated/business-work-items.json}
event_version=${FIXTURE_REPLAY_EVENT_VERSION:-2}
replay_rendered=${FIXTURE_REPLAY_RENDER_PATH:-artifacts/generated/business-work-items-v${event_version}.json}
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fixture_jobs_manifest=${FIXTURE_JOBS_MANIFEST:-$repo_root/manifests/business-fixtures/10-synthetic-jobs.yaml}
evidence_root=${DEMO_CAPTURE_ROOT:-$repo_root/artifacts}/$namespace/fixture-e2e
replay_items=(work-item-001 work-item-002 work-item-003)
baseline_waiter_jobs=(renewal-recovery-plan documentation-refresh)

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
run_oc() { "$oc_bin" "$@"; }
policy_min_confidence=$(ruby -ryaml -e 'puts YAML.safe_load_file(ARGV.fetch(0)).dig("spec", "decision", "minimumConfidence")' "$repo_root/config/policy/fare-policy.yaml")
[[ -n "$policy_min_confidence" ]] || die 'FarePolicy minimumConfidence is missing'

is_low_confidence_abstention() {
  local item_json=$1
  jq -e --argjson minimum "$policy_min_confidence" '
    (.status.decision.confidence < $minimum) and
    any(.status.conditions[]?; .type == "DecisionReady" and .status == "False" and .reason == "LowConfidence") and
    any(.status.conditions[]?; .type == "ProjectionReady" and .status == "False" and .reason == "LowConfidence")
  ' <<<"$item_json" >/dev/null
}

[[ -f "$rendered" ]] || die "rendered fixture list not found: $rendered"
[[ -f "$fixture_jobs_manifest" ]] || die "fixture Job manifest not found: $fixture_jobs_manifest"
[[ "$event_version" =~ ^[2-9][0-9]*$ ]] || die "FIXTURE_REPLAY_EVENT_VERSION must be an integer >= 2"
mkdir -p "$evidence_root"

item_names=()
while IFS= read -r name; do item_names[${#item_names[@]}]=$name; done < <(
  jq -r --arg namespace "$namespace" '.items[] | select(.kind == "BusinessWorkItem" and .metadata.namespace == $namespace) | .metadata.name' "$rendered"
)
(( ${#item_names[@]} == 20 )) || die "rendered fixture list has ${#item_names[@]} items; want 20"

fixture_job_names=()
while IFS= read -r name; do fixture_job_names[${#fixture_job_names[@]}]=$name; done < <(
  ruby -ryaml -e 'YAML.load_stream(File.read(ARGV[0])).compact.each { |doc| puts doc.dig("metadata", "name") if doc["kind"] == "Job" }' "$fixture_jobs_manifest"
)
(( ${#fixture_job_names[@]} == 17 )) || die "fixture Job manifest has ${#fixture_job_names[@]} Jobs; want 17"

for name in "${item_names[@]}"; do
  [[ "$name" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || die "unsafe generated BusinessWorkItem name: $name"
  run_oc get businessworkitem "$name" -n "$namespace" -o yaml > "$evidence_root/$name-before.yaml" 2>/dev/null || true
done

restore_fixtures() {
  # On an interrupted/failed run, restore only the v1 BusinessWorkItems. The
  # 17 fixture Jobs remain suspended and are intentionally left in place.
  run_oc apply -f "$rendered" >/dev/null || true
}
cleanup_on_exit() {
  local status=$?
  if (( status != 0 )); then restore_fixtures; fi
  return "$status"
}
trap cleanup_on_exit EXIT

assert_capacity_holder() {
  run_oc get job auth-regression-fix -n "$namespace" -o json | jq -e '
    .spec.suspend == false and (.status.active // 0) == 1
  ' >/dev/null || return 1
  run_oc get workloads -n "$namespace" -o json | jq -e '
    [.items[] | select(.metadata.name | startswith("job-auth-regression-fix-")) |
      select(.status.admission != null and any(.status.conditions[]?; .type == "Admitted" and .status == "True"))] | length == 1
  ' >/dev/null
}

assert_fixture_jobs_suspended() {
  local jobs_json pods_json job_name
  jobs_json=$(run_oc get jobs -n "$namespace" -l demo.kueue.io/fixture=synthetic-event-feed -o json) || return 1
  jq -e '
    (.items | length == 20) and
    all(.items[];
      if .metadata.name == "auth-regression-fix" then
        .spec.suspend == false and ((.status.active // 0) == 1)
      else
        .spec.suspend == true and ((.status.active // 0) == 0)
      end)
  ' <<<"$jobs_json" >/dev/null || return 1

  # Suspended fixture Jobs must not have created worker Pods. The auth holder's
  # admitted Pod is intentionally excluded by checking every other fixture
  # Job name.
  for job_name in "${fixture_job_names[@]}" "${baseline_waiter_jobs[@]}"; do
    pods_json=$(run_oc get pods -n "$namespace" -l "batch.kubernetes.io/job-name=$job_name" -o name) || return 1
    [[ -z "$pods_json" ]] || return 1
  done
}

run_oc apply -f "$fixture_jobs_manifest" >/dev/null
assert_capacity_holder || die 'capacity holder is not admitted before fixture jobs are submitted'
assert_fixture_jobs_suspended || die 'fixture Jobs are not all suspended with zero worker Pods'
for name in "${item_names[@]}"; do
  job_name=$(jq -r --arg name "$name" '.items[] | select(.kind == "BusinessWorkItem" and .metadata.name == $name) | .spec.link.jobName // .spec.link.name // empty' "$rendered")
  [[ -n "$job_name" ]] || die "rendered BusinessWorkItem $name has no linked Job"
  run_oc get job "$job_name" -n "$namespace" -o json | jq -r '.metadata.labels["kueue.x-k8s.io/priority-class"] // ""' > "$evidence_root/$name-job-priority-before"
done
run_oc get jobs -n "$namespace" -l demo.kueue.io/fixture=synthetic-event-feed -o yaml > "$evidence_root/fixture-jobs-before.yaml"

# Recreate only the synthetic BusinessWorkItems so v1 must request fresh
# decisions. The fixture Jobs are not deleted; they are all suspended, and the
# existing auth holder remains the sole admitted workload.
for name in "${item_names[@]}"; do
  run_oc delete businessworkitem "$name" -n "$namespace" --ignore-not-found --wait=true
done
started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
run_oc apply -f "$rendered"

item_ready() {
  local name=$1 item_json job_name workload_name item_priority job_json workload_json decision_status decision_reason before_priority current_priority
  item_json=$(run_oc get businessworkitem "$name" -n "$namespace" -o json) || return 1
  jq -e --arg started_at "$started_at" '
    .spec.provenance == "synthetic" and
    .status.observedGeneration == .metadata.generation and
    .status.decision.evaluatedAt >= $started_at and
    (.status.decision.fareClass | IN("first", "business", "economy", "First class", "Business", "Economy"))
  ' <<<"$item_json" >/dev/null || return 1

  job_name=$(jq -r '.spec.link.jobName // .spec.link.name // empty' <<<"$item_json")
  [[ -n "$job_name" ]] || return 1
  job_json=$(run_oc get job "$job_name" -n "$namespace" -o json) || return 1
  decision_status=$(jq -r '[.status.conditions[]? | select(.type == "DecisionReady") | .status][0] // ""' <<<"$item_json")
  decision_reason=$(jq -r '[.status.conditions[]? | select(.type == "DecisionReady") | .reason][0] // ""' <<<"$item_json")

  if [[ "$decision_status" == "False" && "$decision_reason" == "LowConfidence" ]] && is_low_confidence_abstention "$item_json"; then
    before_priority=$(<"$evidence_root/$name-job-priority-before")
    current_priority=$(jq -r '.metadata.labels["kueue.x-k8s.io/priority-class"] // ""' <<<"$job_json")
    [[ "$current_priority" == "$before_priority" ]] || return 1
    run_oc get businessworkitem "$name" -n "$namespace" -o yaml > "$evidence_root/$name-after.yaml"
    run_oc get job "$job_name" -n "$namespace" -o yaml > "$evidence_root/$name-job-after.yaml"
    return 0
  fi

  [[ "$decision_status" == "True" && "$decision_reason" == "DecisionAccepted" ]] || return 1
  jq -e --argjson minimum "$policy_min_confidence" '
    (.status.decision.confidence >= $minimum) and
    (.status.decision.priorityValue > 0) and
    (.status.decision.priorityClass | IN("fare-first-class", "fare-business", "fare-economy"))
  ' <<<"$item_json" >/dev/null || return 1

  workload_name=$(jq -r '.status.linkedWorkload.name // empty' <<<"$item_json")
  item_priority=$(jq -r '.status.priorityClass // empty' <<<"$item_json")
  [[ -n "$job_name" && -n "$workload_name" && -n "$item_priority" ]] || return 1
  workload_json=$(run_oc get workload "$workload_name" -n "$namespace" -o json) || return 1

  if jq -e '.status.linkedWorkload.jobPatched == true' <<<"$item_json" >/dev/null; then
    jq -e --arg class "$item_priority" '.metadata.labels["kueue.x-k8s.io/priority-class"] == $class' <<<"$job_json" >/dev/null || return 1
    jq -e '.status.admission == null and (any(.status.conditions[]?; .type == "Admitted" and .status == "True") | not)' <<<"$workload_json" >/dev/null || return 1
  else
    # The only valid unpatched fixture is work-item-002, whose linked Job is
    # the deliberately admitted capacity holder.
    [[ "$name" == "work-item-002" ]] || return 1
    jq -e '.status.admission != null and any(.status.conditions[]?; .type == "Admitted" and .status == "True")' <<<"$workload_json" >/dev/null || return 1
  fi

  run_oc get businessworkitem "$name" -n "$namespace" -o yaml > "$evidence_root/$name-after.yaml"
  run_oc get job "$job_name" -n "$namespace" -o yaml > "$evidence_root/$name-job-after.yaml"
  run_oc get workload "$workload_name" -n "$namespace" -o yaml > "$evidence_root/$name-workload-after.yaml"
}

wait_for_initial_decisions() {
  local attempt all_ready name
  local attempts=$(( (timeout_seconds + 2) / 3 ))
  (( attempts > 0 )) || attempts=1
  for attempt in $(seq 1 "$attempts"); do
    all_ready=1
    for name in "${item_names[@]}"; do
      if ! item_ready "$name"; then all_ready=0; fi
    done
    if (( all_ready == 1 )); then return 0; fi
    sleep 3
  done
  return 1
}

wait_for_initial_decisions || {
  printf 'fixture v1 decisions did not settle within %ss; current state:\n' "$timeout_seconds" >&2
  run_oc get businessworkitems -n "$namespace" -o wide >&2 || true
  run_oc get jobs,workloads -n "$namespace" -o wide >&2 || true
  die 'expected 20 accepted or policy-rejected v1 decisions with only the auth holder admitted'
}
assert_capacity_holder || die 'capacity holder changed while v1 fixture decisions were evaluated'
assert_fixture_jobs_suspended || die 'fixture Jobs created worker Pods during v1 evaluation'
run_oc get jobs,workloads -n "$namespace" -o yaml > "$evidence_root/v1-jobs-workloads.yaml"
v1_state=$(run_oc get businessworkitems -n "$namespace" -o json)
v1_accepted=$(jq '[.items[] | select(any(.status.conditions[]?; .type == "DecisionReady" and .status == "True" and .reason == "DecisionAccepted"))] | length' <<<"$v1_state")
v1_abstained=$(jq '[.items[] | select(any(.status.conditions[]?; .type == "DecisionReady" and .status == "False" and .reason == "LowConfidence"))] | length' <<<"$v1_state")

# Render and apply a bounded temporal replay. Only three items have v2 events;
# the other 17 remain byte-for-byte v1 facts. Generation and history deltas are
# the proof that the controller observed new business input and re-decided. A
# successful run intentionally leaves this v2 state visible; a failed run is
# restored to v1 by cleanup_on_exit.
mkdir -p "$(dirname "$replay_rendered")"
(cd "$repo_root" && go run ./cmd/fixture-loader -namespace "$namespace" -event-version "$event_version") > "$replay_rendered"
replay_count=$(jq '[.items[] | select(.metadata.annotations["tokens.jeder.github.com/event-version"] == "2")] | length' "$replay_rendered")
(( replay_count == ${#replay_items[@]} )) || die "replay rendered $replay_count versioned items; want ${#replay_items[@]}"

for name in "${replay_items[@]}"; do
  item_json=$(run_oc get businessworkitem "$name" -n "$namespace" -o json) || die "missing v1 BusinessWorkItem $name before replay"
  jq -r '.metadata.generation' <<<"$item_json" > "$evidence_root/$name-replay-before-generation"
  jq -r '(.status.history // []) | length' <<<"$item_json" > "$evidence_root/$name-replay-before-history"
  job_name=$(jq -r '.spec.link.jobName // .spec.link.name // empty' <<<"$item_json")
  [[ -n "$job_name" ]] || die "BusinessWorkItem $name has no linked Job before replay"
  run_oc get job "$job_name" -n "$namespace" -o json | jq -r '.metadata.labels["kueue.x-k8s.io/priority-class"] // ""' > "$evidence_root/$name-replay-job-priority-before"
done
replay_started_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
run_oc apply -f "$replay_rendered"

replay_item_ready() {
  local name=$1 item_json old_generation old_history decision_status decision_reason job_name job_json before_priority current_priority
  old_generation=$(<"$evidence_root/$name-replay-before-generation")
  old_history=$(<"$evidence_root/$name-replay-before-history")
  item_json=$(run_oc get businessworkitem "$name" -n "$namespace" -o json) || return 1
  jq -e --arg started_at "$replay_started_at" --argjson old_generation "$old_generation" --argjson old_history "$old_history" '
    .metadata.generation > $old_generation and
    .status.observedGeneration == .metadata.generation and
    .status.decision.evaluatedAt >= $started_at and
    ((.status.history // []) | length) > $old_history and
    .metadata.annotations["tokens.jeder.github.com/event-version"] == "2" and
    (.status.decision.fareClass | IN("first", "business", "economy", "First class", "Business", "Economy"))
  ' <<<"$item_json" >/dev/null || return 1

  decision_status=$(jq -r '[.status.conditions[]? | select(.type == "DecisionReady") | .status][0] // ""' <<<"$item_json")
  decision_reason=$(jq -r '[.status.conditions[]? | select(.type == "DecisionReady") | .reason][0] // ""' <<<"$item_json")
  if [[ "$decision_status" == "False" && "$decision_reason" == "LowConfidence" ]] && is_low_confidence_abstention "$item_json"; then
    job_name=$(jq -r '.spec.link.jobName // .spec.link.name // empty' <<<"$item_json")
    [[ -n "$job_name" ]] || return 1
    job_json=$(run_oc get job "$job_name" -n "$namespace" -o json) || return 1
    before_priority=$(<"$evidence_root/$name-replay-job-priority-before")
    current_priority=$(jq -r '.metadata.labels["kueue.x-k8s.io/priority-class"] // ""' <<<"$job_json")
    [[ "$current_priority" == "$before_priority" ]] || return 1
    run_oc get businessworkitem "$name" -n "$namespace" -o yaml > "$evidence_root/$name-replay-after.yaml"
    return 0
  fi

  [[ "$decision_status" == "True" && "$decision_reason" == "DecisionAccepted" ]] || return 1
  jq -e --argjson minimum "$policy_min_confidence" '.status.decision.confidence >= $minimum' <<<"$item_json" >/dev/null || return 1
  return 0
}

replay_attempts=$(( (timeout_seconds + 2) / 3 ))
(( replay_attempts > 0 )) || replay_attempts=1
replay_ready=0
for attempt in $(seq 1 "$replay_attempts"); do
  replay_ready=1
  for name in "${replay_items[@]}"; do
    if ! replay_item_ready "$name"; then replay_ready=0; fi
  done
  (( replay_ready == 1 )) && break
  sleep 3
done
(( replay_ready == 1 )) || die 'versioned replay did not create new generations and decisions for all three update events'

for name in "${replay_items[@]}"; do
  run_oc get businessworkitem "$name" -n "$namespace" -o yaml > "$evidence_root/$name-replay-after.yaml"
done
run_oc get jobs,workloads -n "$namespace" -o yaml > "$evidence_root/replay-jobs-workloads.yaml"
replay_state=$(run_oc get businessworkitems "${replay_items[@]}" -n "$namespace" -o json)
replay_accepted=$(jq '[.items[] | select(any(.status.conditions[]?; .type == "DecisionReady" and .status == "True" and .reason == "DecisionAccepted"))] | length' <<<"$replay_state")
replay_abstained=$(jq '[.items[] | select(any(.status.conditions[]?; .type == "DecisionReady" and .status == "False" and .reason == "LowConfidence"))] | length' <<<"$replay_state")
assert_capacity_holder || die 'capacity holder changed during versioned replay'
printf 'synthetic fixture churn E2E passed: v1 %d accepted/%d low-confidence abstentions; v2 %d accepted/%d abstentions; 19 of 20 fixture Jobs remained suspended and auth holder remained admitted\nEvidence: %s\n' "$v1_accepted" "$v1_abstained" "$replay_accepted" "$replay_abstained" "$evidence_root"
