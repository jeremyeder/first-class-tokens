# syntax=docker/dockerfile:1.7

# This exact vLLM build contains DiffusionGemma structured-read support. Keep
# the revision in the image reference and in the deployment ConfigMap together.
FROM --platform=linux/amd64 vllm/vllm-openai:nightly-e9757321527ca1ecd514c07c1418dd2c53da3d19@sha256:e0eee5c5506bea9bfe350f7d99b07dc49e37d42647a128c2a57ff184551fba10

ARG VLLM_REVISION=e9757321527ca1ecd514c07c1418dd2c53da3d19

LABEL org.opencontainers.image.title="DiffusionGemma structured decision server" \
      org.opencontainers.image.source="https://github.com/jeder/first-class-tokens" \
      org.opencontainers.image.base.name="vllm/vllm-openai:nightly-e9757321527ca1ecd514c07c1418dd2c53da3d19" \
      org.opencontainers.image.base.revision="${VLLM_REVISION}"

COPY --chmod=0555 structured_server.py /opt/decision-model/structured_server.py
COPY --chmod=0555 start.sh /opt/decision-model/start.sh

ENV MODEL_ID=RedHatAI/diffusiongemma-26B-A4B-it-FP8-dynamic \
    MODEL_REVISION=3b3dae4697494da5a290e9c0461954449e76c4f5 \
    SERVED_MODEL_NAME=dgemma \
    CANVAS_LENGTH=64 \
    VLLM_HOST=127.0.0.1 \
    VLLM_PORT=8000 \
    STRUCTURED_HOST=0.0.0.0 \
    STRUCTURED_PORT=8011 \
    HF_HOME=/models/cache \
    TRANSFORMERS_CACHE=/models/cache

EXPOSE 8000 8011
ENTRYPOINT ["/opt/decision-model/start.sh"]
