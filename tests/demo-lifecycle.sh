#!/usr/bin/env bash
set -euo pipefail

root_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root_dir"

test -f Makefile
test -x scripts/demo-lifecycle.sh
bash -n scripts/demo-lifecycle.sh
test -x scripts/demo-intake-e2e.sh
bash -n scripts/demo-intake-e2e.sh
test -x scripts/demo-fixtures-e2e.sh
bash -n scripts/demo-fixtures-e2e.sh
test -x scripts/demo-quality-gate.sh
bash -n scripts/demo-quality-gate.sh
grep -Fq 'LowConfidence' scripts/demo-fixtures-e2e.sh
grep -Fq 'job-priority-before' scripts/demo-fixtures-e2e.sh
grep -Fq 'minimumConfidence' scripts/demo-fixtures-e2e.sh
test -x scripts/update-image-lock.sh
bash -n scripts/update-image-lock.sh

for target in demo-check demo-up demo-baseline demo-fixtures-render demo-fixtures-apply demo-quality-gate demo-fixtures-e2e demo-fixtures-clean demo-observability-enable demo-promote demo-e2e demo-intake-e2e demo-down demo-kueueviz-up demo-rebuild demo-rebuild-full-reset registry-prepare registry-login registry-logout image-build image-push image-lock-update bootstrap-deploy decision-model-deploy; do
  grep -Eq "^${target}:" Makefile
done

grep -Fq 'DecisionAccepted' scripts/demo-intake-e2e.sh
grep -Fq 'ProjectionReady' scripts/demo-intake-e2e.sh
grep -Fq 'status.admission == null' scripts/demo-intake-e2e.sh
grep -Fq 'oc apply -k manifests/business-work-items' Makefile

# @spec DEMO-LIFECYCLE-001, DEMO-LIFECYCLE-002
grep -Fq 'submit_holder_first' scripts/demo-lifecycle.sh
grep -Fq 'assert_baseline' scripts/demo-lifecycle.sh
grep -Fq 'fixtures-clean) demo_fixtures_clean' scripts/demo-lifecycle.sh
grep -Fq 'run_oc delete job "${fixture_job_names[@]}" -n "$namespace"' scripts/demo-lifecycle.sh
grep -Fq 'fixture manifest must contain exactly 17 Job documents' scripts/demo-lifecycle.sh
grep -Fq 'fixture Job and Workload cleanup' scripts/demo-lifecycle.sh
if grep -Fq 'run_oc delete -f "$fixture_jobs_manifest"' scripts/demo-lifecycle.sh; then
  echo 'fixture cleanup must use explicit names in the selected namespace' >&2
  exit 1
fi
grep -Fq 'any(.metadata.ownerReferences[]?; .kind == "Job" and .name == "renewal-recovery-plan")' scripts/demo-lifecycle.sh
grep -Fq 'any(.metadata.ownerReferences[]?; .kind == "Job" and (.name == "auth-regression-fix" or .name == "renewal-recovery-plan" or .name == "documentation-refresh"))' scripts/demo-lifecycle.sh
if grep -Fq 'startswith("job-renewal-recovery-plan-")' scripts/demo-lifecycle.sh; then
  echo 'baseline Workload selectors must not match fixture Jobs by name prefix' >&2
  exit 1
fi
if sed -n '/demo_up()/,/^}/p' scripts/demo-lifecycle.sh | grep -Fq 'demo_down'; then
  echo 'demo-up must preserve namespace-scoped infrastructure' >&2
  exit 1
fi
grep -Fq 'reset_lane_resources' scripts/demo-lifecycle.sh
if sed -n '/reset_lane_resources()/,/^}/p' scripts/demo-lifecycle.sh | grep -Fq 'delete namespace'; then
  echo 'lane reset must not delete its namespace' >&2
  exit 1
fi
grep -Fq 'demo-rebuild-full-reset' Makefile

# @spec DEMO-LIFECYCLE-004, DEMO-LIFECYCLE-006
grep -Fq 'validate_namespace' scripts/demo-lifecycle.sh
grep -Fq 'kueue-demo-e2e-' scripts/demo-lifecycle.sh
grep -Fq 'e2e_cleanup_namespace' scripts/demo-lifecycle.sh

# @spec DEMO-LIFECYCLE-003
grep -Fq 'renewal_pending' scripts/demo-lifecycle.sh
grep -Fq 'kueue.x-k8s.io/priority-class=fare-first-class' scripts/demo-lifecycle.sh

# @spec DEMO-LIFECYCLE-005
ruby -ryaml -e '
  job = YAML.load_stream(File.read("manifests/00-demo-lane.yaml")).find { |doc| doc.dig("kind") == "Job" && doc.dig("metadata", "name") == "auth-regression-fix" }
  abort "auth holder must use /bin/sh" unless job.dig("spec", "template", "spec", "containers", 0, "command", 0) == "/bin/sh"
  abort "auth holder must trap termination" unless job.dig("spec", "template", "spec", "containers", 0, "command", 2).include?("trap '\''exit 0'\'' TERM INT")
'
