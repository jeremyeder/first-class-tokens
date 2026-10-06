#!/bin/sh
set -eu

: "${MODEL_ID:?MODEL_ID must be set}"
: "${MODEL_REVISION:?MODEL_REVISION must be set}"
: "${SERVED_MODEL_NAME:?SERVED_MODEL_NAME must be set}"
: "${CANVAS_LENGTH:?CANVAS_LENGTH must be set}"
: "${VLLM_HOST:?VLLM_HOST must be set}"
: "${VLLM_PORT:?VLLM_PORT must be set}"
: "${STRUCTURED_HOST:?STRUCTURED_HOST must be set}"
: "${STRUCTURED_PORT:?STRUCTURED_PORT must be set}"

vllm serve "${MODEL_ID}" \
  --revision "${MODEL_REVISION}" \
  --served-model-name "${SERVED_MODEL_NAME}" \
  --trust-remote-code \
  --host "${VLLM_HOST}" \
  --port "${VLLM_PORT}" \
  --max-model-len 4096 \
  --max-num-seqs 4 \
  --gpu-memory-utilization 0.85 \
  --diffusion-config "{\"canvas_length\":${CANVAS_LENGTH}}" \
  --max-logprobs 32 \
  --enable-prefix-caching &
vllm_pid=$!

cleanup() {
  kill "${vllm_pid}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# The structured server is deliberately started only after vLLM is healthy.
# This is a local readiness check; no source or package is downloaded here.
i=0
while [ "${i}" -lt 900 ]; do
  if python3 -c "import urllib.request; urllib.request.urlopen('http://${VLLM_HOST}:${VLLM_PORT}/health', timeout=2).read()"; then
    break
  fi
  i=$((i + 1))
  sleep 2
done

if [ "${i}" -ge 900 ]; then
  echo "vLLM did not become healthy on ${VLLM_HOST}:${VLLM_PORT}" >&2
  exit 1
fi

exec python3 /opt/decision-model/structured_server.py \
  --upstream "http://${VLLM_HOST}:${VLLM_PORT}" \
  --model "${SERVED_MODEL_NAME}" \
  --tokenizer "${MODEL_ID}" \
  --canvas "${CANVAS_LENGTH}" \
  --host "${STRUCTURED_HOST}" \
  --port "${STRUCTURED_PORT}"
