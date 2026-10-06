#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
oc_bin=${OC_BIN:-oc}
port=${QUALITY_GATE_PORT:-18011}
report=${QUALITY_GATE_REPORT:-$repo_root/artifacts/evaluation/synthetic-quality-report.json}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
[[ "$port" =~ ^[0-9]+$ ]] && (( port > 0 && port < 65536 )) || die "invalid QUALITY_GATE_PORT: $port"
command -v "$oc_bin" >/dev/null || die "oc client not found: $oc_bin"
command -v curl >/dev/null || die 'curl is required'

mkdir -p "$(dirname "$report")"
port_forward_log=$(mktemp)
port_forward_pid=''
cleanup() {
  if [[ -n "$port_forward_pid" ]]; then
    kill "$port_forward_pid" 2>/dev/null || true
    wait "$port_forward_pid" 2>/dev/null || true
  fi
  rm -f "$port_forward_log"
}
trap cleanup EXIT INT TERM

"$oc_bin" -n kueue-demo port-forward service/decision-model "${port}:8011" >"$port_forward_log" 2>&1 &
port_forward_pid=$!

attempts=30
for attempt in $(seq 1 "$attempts"); do
  if curl --max-time 1 --silent --show-error --fail "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$port_forward_pid" 2>/dev/null; then
    cat "$port_forward_log" >&2
    die 'oc port-forward exited before the decision service became healthy'
  fi
  sleep 1
done
if ! curl --max-time 1 --silent --show-error --fail "http://127.0.0.1:${port}/health" >/dev/null 2>&1; then
  cat "$port_forward_log" >&2
  die "decision service did not become healthy on local port $port"
fi

cd "$repo_root"
go run ./cmd/fixture-eval \
  -endpoint "http://127.0.0.1:${port}" \
  -output "$report"
