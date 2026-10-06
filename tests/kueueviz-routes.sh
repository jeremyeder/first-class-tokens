#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
oc_log="$test_dir/oc-commands.jsonl"

KUEUEVIZ_OC_LOG="$oc_log" \
OC_BIN="$repo_dir/tests/fake-oc-kueueviz.sh" \
KUEUEVIZ_ROUTE_TIMEOUT_SECONDS=2 \
KUEUEVIZ_ROLLOUT_TIMEOUT=5s \
  "$repo_dir/scripts/deploy-kueueviz.sh" >"$test_dir/output.txt"

rejected_log="$test_dir/rejected-route-commands.jsonl"
if KUEUEVIZ_OC_LOG="$rejected_log" \
  OC_BIN="$repo_dir/tests/fake-oc-kueueviz.sh" \
  KUEUEVIZ_ROUTE_TIMEOUT_SECONDS=1 \
  KUEUEVIZ_FAKE_ROUTE_ADMITTED=False \
  "$repo_dir/scripts/deploy-kueueviz.sh" >"$test_dir/rejected-output.txt" 2>&1; then
  echo 'deployment reported success for a Route that was not admitted' >&2
  exit 1
fi

ruby -ryaml -rjson - "$repo_dir/manifests/kueueviz-v0.19.4-openshift.yaml" "$oc_log" "$rejected_log" <<'RUBY'
manifest_path, log_path, rejected_log = ARGV
docs = YAML.load_stream(File.read(manifest_path)).compact
routes = docs.select { |doc| doc["kind"] == "Route" }
abort "expected frontend and backend Routes" unless routes.map { |doc| doc.dig("metadata", "name") }.sort == %w[kueueviz kueueviz-backend]
abort "Routes must let OpenShift generate their hosts" if routes.any? { |doc| doc.dig("spec", "host") }
abort "KueueViz manifest contains a hardcoded cluster route" if File.read(manifest_path).match?(/\.openshiftapps\.com|apps\.rosa\./)

commands = File.readlines(log_path).map { |line| JSON.parse(line) }
get_route = ->(name) { commands.find { |cmd| cmd[0..2] == ["get", "route", name] } }
abort "frontend generated host was not queried" unless get_route.call("kueueviz")
abort "backend generated host was not queried" unless get_route.call("kueueviz-backend")
%w[kueueviz kueueviz-backend].each do |name|
  query_index = commands.index(get_route.call(name))
  abort "Route #{name} was not queried as JSON" unless get_route.call(name).include?("-o") && get_route.call(name).include?("json")
  abort "Route #{name} was not read" unless query_index
end

patch = commands.find { |cmd| cmd[0..2] == %w[patch configmap kueueviz-frontend-env] }
abort "frontend ConfigMap was not patched" unless patch
patch_json = JSON.parse(patch.fetch(patch.index("--patch") + 1))
env_js = patch_json.dig("data", "env.js")
abort "frontend WebSocket URLs do not use the generated backend host" unless env_js&.include?("wss://backend.generated.example.test")
abort "frontend runtime config omitted one expected URL key" unless env_js.include?("VITE_WEBSOCKET_URL") && env_js.include?("REACT_APP_WEBSOCKET_URL")

origin = commands.find { |cmd| cmd[0..2] == %w[set env deployment/kueueviz-backend] }
abort "backend allowed origin does not use the generated frontend host" unless origin&.include?("KUEUEVIZ_ALLOWED_ORIGINS=https://frontend.generated.example.test")

restart_index = commands.index { |cmd| cmd[0..2] == %w[rollout restart deployment/kueueviz-frontend] }
config_index = commands.index(patch)
abort "frontend restart must happen after ConfigMap patch" unless restart_index && config_index < restart_index

rejected_commands = File.readlines(rejected_log).map { |line| JSON.parse(line) }
abort "unadmitted Route should stop before patching the ConfigMap" if rejected_commands.any? { |cmd| cmd[0..1] == %w[patch configmap] }
abort "unadmitted Route should stop before changing backend origins" if rejected_commands.any? { |cmd| cmd[0..1] == %w[set env] }
RUBY

printf 'KueueViz generated-route contract passed\n'
