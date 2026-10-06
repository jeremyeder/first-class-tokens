#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$(ruby -rjson -e 'puts JSON.generate(ARGV)' "$@")" >>"$MOCK_OC_LOG"

case ${1:-} in
  whoami)
    printf 'cluster-admin\n'
    ;;
  api-resources)
    case "${*: -1}" in
      *businessworkitems*) printf 'businessworkitems.tokens.jeder.github.com\n' ;;
      *) printf 'workloads.kueue.x-k8s.io\n' ;;
    esac
    ;;
  get)
    case ${2:-} in
      kueue)
        printf '{"status":{"conditions":[{"type":"Ready","status":"True"}]}}\n'
        ;;
      namespace)
        printf 'namespace/%s\n' "${3:-kueue-demo-contract}"
        ;;
      businessworkitems)
        printf '{"items":[]}\n'
        ;;
      jobs|workloads|jobs,workloads)
        ruby -rjson - "$MOCK_JOB_STATE" "${2:-}" <<'RUBY'
path, resource = ARGV
names = File.readlines(path, chomp: true).reject(&:empty?)
jobs = names.map do |name|
  {
    "kind" => "Job",
    "metadata" => { "name" => name },
    "spec" => { "suspend" => name != "auth-regression-fix" },
    "status" => name == "auth-regression-fix" ? { "active" => 1 } : {}
  }
end
workloads = names.map do |name|
  holder = name == "auth-regression-fix"
  {
    "kind" => "Workload",
    "metadata" => { "name" => "job-#{name}-mock", "ownerReferences" => [{ "kind" => "Job", "name" => name }] },
    "spec" => {},
    "status" => { "admission" => holder ? {} : nil, "conditions" => holder ? [{ "type" => "Admitted", "status" => "True" }] : [] }
  }
end
items = case resource
when "jobs" then jobs
when "workloads" then workloads
else jobs + workloads
end
puts JSON.generate("items" => items)
RUBY
        ;;
      *)
        printf '{}\n'
        ;;
    esac
    ;;
  delete)
    case ${2:-} in
      businessworkitems)
        ;;
      job)
        shift 2
        ruby -rjson - "$MOCK_JOB_STATE" "$@" <<'RUBY'
path, *args = ARGV
names = args.take_while { |arg| !arg.start_with?("-") }
current = File.readlines(path, chomp: true).reject(&:empty?)
File.write(path, (current - names).join("\n") + "\n")
RUBY
        ;;
      *)
        printf 'unexpected delete resource: %s\n' "${2:-}" >&2
        exit 2
        ;;
    esac
    ;;
  apply)
    rendered=$(ruby -e 'print STDIN.read')
    ruby -ryaml - "$MOCK_JOB_STATE" "$rendered" <<'RUBY'
path, yaml = ARGV
current = File.readlines(path, chomp: true).reject(&:empty?)
jobs = YAML.load_stream(yaml).compact.filter_map { |doc| doc.dig("metadata", "name") if doc["kind"] == "Job" }
File.write(path, (current + jobs).uniq.join("\n") + "\n") unless jobs.empty?
RUBY
    ;;
  wait)
    printf 'namespace is Active\n'
    ;;
  *)
    printf 'unexpected mock oc command: %s\n' "$*" >&2
    exit 2
    ;;
esac
