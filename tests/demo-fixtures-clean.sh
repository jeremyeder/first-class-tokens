#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT

fixture_manifest="$repo_dir/manifests/business-fixtures/10-synthetic-jobs.yaml"
job_state="$test_dir/jobs.txt"
oc_log="$test_dir/oc-commands.jsonl"
namespace=kueue-demo-contract

ruby -ryaml - "$fixture_manifest" "$job_state" <<'RUBY'
manifest, output = ARGV
fixture_names = YAML.load_stream(File.read(manifest)).compact.map { |doc| doc.dig("metadata", "name") }
File.write(output, (fixture_names + %w[auth-regression-fix renewal-recovery-plan documentation-refresh unrelated-experiment]).join("\n") + "\n")
RUBY

run_cleanup() {
  MOCK_JOB_STATE="$job_state" \
  MOCK_OC_LOG="$oc_log" \
  OC_BIN="$repo_dir/tests/fake-oc-fixtures-clean.sh" \
  DEMO_NAMESPACE="$namespace" \
  DEMO_TIMEOUT_SECONDS=3 \
  DEMO_CAPTURE_ROOT="$test_dir/captures" \
  FIXTURE_JOBS_MANIFEST="$fixture_manifest" \
    "$repo_dir/scripts/demo-lifecycle.sh" fixtures-clean
}

run_cleanup >/dev/null
run_cleanup >/dev/null

# A changed manifest must be rejected before any cluster object is deleted.
bad_manifest="$test_dir/missing-fixture-job.yaml"
ruby -ryaml - "$fixture_manifest" "$bad_manifest" <<'RUBY'
input, output = ARGV
documents = YAML.load_stream(File.read(input)).compact
File.write(output, documents.drop(1).map { |doc| YAML.dump(doc) }.join("---\n"))
RUBY
before_bad_run=$(wc -l <"$oc_log")
if MOCK_JOB_STATE="$job_state" \
  MOCK_OC_LOG="$oc_log" \
  OC_BIN="$repo_dir/tests/fake-oc-fixtures-clean.sh" \
  DEMO_NAMESPACE="$namespace" \
  DEMO_TIMEOUT_SECONDS=3 \
  DEMO_CAPTURE_ROOT="$test_dir/captures" \
  FIXTURE_JOBS_MANIFEST="$bad_manifest" \
  "$repo_dir/scripts/demo-lifecycle.sh" fixtures-clean >/dev/null 2>&1; then
  echo 'cleanup accepted a malformed fixture manifest' >&2
  exit 1
fi
ruby -rjson -ryaml - "$oc_log" "$fixture_manifest" "$job_state" "$namespace" "$before_bad_run" <<'RUBY'
log_path, manifest_path, state_path, namespace, before_bad_run, after_bad_run = ARGV
commands = File.readlines(log_path).map { |line| JSON.parse(line) }
fixture_names = YAML.load_stream(File.read(manifest_path)).compact.map { |doc| doc.dig("metadata", "name") }.sort
expected_baseline = %w[auth-regression-fix documentation-refresh renewal-recovery-plan].sort
expected_remaining = (expected_baseline + ["unrelated-experiment"]).sort
deletes = commands.each_with_index.filter_map { |command, index| [command, index] if command.first == "delete" }
abort "unexpected delete kind was issued" unless deletes.all? { |command, _| %w[job businessworkitems].include?(command[1]) }
abort "every delete must be scoped to the selected namespace" unless deletes.all? { |command, _| command.each_cons(2).any? { |left, right| left == "-n" && right == namespace } }
fixture_deletes = deletes.select { |command, _| command[1] == "job" && (command & fixture_names).any? }
abort "cleanup must issue one exact fixture Job delete per invocation" unless fixture_deletes.length == 2
fixture_deletes.each do |command, index|
  actual = command.drop(2).take_while { |arg| !arg.start_with?("-") }.sort
  abort "fixture delete did not match manifest names exactly" unless actual == fixture_names
  baseline_delete_index = deletes.find do |candidate, candidate_index|
    candidate_index > index && candidate[1] == "job" && (candidate & expected_baseline).sort == expected_baseline
  end&.last
  abort "baseline reset occurred before fixture deletion" unless baseline_delete_index && index < baseline_delete_index
  workload_check_index = commands.each_index.find do |candidate_index|
    candidate_index > index && commands[candidate_index][0..1] == %w[get workloads] && candidate_index < baseline_delete_index
  end
  abort "fixture Workloads were not checked before baseline reset" unless workload_check_index
end
abort "namespace or unrelated resources were deleted" unless deletes.none? { |command, _| command[1] != "job" && command[1] != "businessworkitems" }
remaining_jobs = File.readlines(state_path, chomp: true).reject(&:empty?).sort
abort "cleanup removed an unrelated Job or left fixture Jobs: #{remaining_jobs.inspect}" unless remaining_jobs == expected_remaining
abort "malformed manifest caused deletions" unless commands.drop(before_bad_run.to_i).none? { |command| command.first == "delete" }
RUBY

printf 'selective fixture cleanup contract passed\n'
