.DEFAULT_GOAL := help
.PHONY: help test demo-check demo-up demo-baseline demo-seed-business-items demo-fixtures-render demo-fixtures-apply demo-fixtures-e2e demo-fixtures-clean demo-quality-gate demo-observability-enable demo-promote demo-e2e demo-intake-e2e demo-down demo-kueueviz-up demo-rebuild demo-rebuild-full-reset

help:
	@printf '%s\n' 'Fixture cleanup: demo-fixtures-clean removes synthetic inputs and restores the held baseline.'
	@printf '%s\n' 'Visualization: demo-kueueviz-up installs KueueViz with OpenShift-generated route hosts.'
	@printf '%s\n' 'Targets: test demo-check demo-up demo-baseline demo-seed-business-items demo-fixtures-render demo-fixtures-apply demo-quality-gate demo-fixtures-e2e demo-observability-enable demo-promote demo-e2e demo-intake-e2e demo-down demo-rebuild'
	@printf '%s\n' 'Image lifecycle: registry-prepare image-build image-lock-update bootstrap-deploy decision-model-deploy.'
	@printf '%s\n' 'demo-rebuild preserves the model cache; demo-rebuild-full-reset deletes kueue-demo and its cache PVC.'

test:
	go test ./...
	bash tests/demo-lifecycle.sh
	bash tests/demo-fixtures-clean.sh
	bash tests/kueueviz-routes.sh

demo-check:
	./scripts/demo-lifecycle.sh check

demo-up:
	./scripts/demo-lifecycle.sh up

demo-baseline:
	./scripts/demo-lifecycle.sh baseline

demo-seed-business-items:
	oc apply -k manifests/business-work-items

FIXTURE_RENDER_PATH ?= artifacts/generated/business-work-items.json

# Generate API resources from the synthetic event feed, excluding entries
# whose linked Jobs are not declared by the demo lane.
demo-fixtures-render:
	mkdir -p "$(dir $(FIXTURE_RENDER_PATH))"
	go run ./cmd/fixture-loader -namespace kueue-demo > "$(FIXTURE_RENDER_PATH)"

demo-fixtures-apply: demo-check
	$(MAKE) demo-baseline
	$(MAKE) demo-fixtures-render
	oc apply -f "$(FIXTURE_RENDER_PATH)"

demo-fixtures-e2e: demo-check
	$(MAKE) demo-quality-gate
	$(MAKE) demo-baseline
	$(MAKE) demo-fixtures-render
	./scripts/demo-fixtures-e2e.sh

# Return the runtime lane to its capacity-held baseline without removing the
# namespace, serving components, ImageStreams, or model cache PVC.
demo-fixtures-clean: demo-check
	./scripts/demo-lifecycle.sh fixtures-clean

QUALITY_GATE_PORT ?= 18011
QUALITY_GATE_REPORT ?= artifacts/evaluation/synthetic-quality-report.json

demo-quality-gate: demo-check
	QUALITY_GATE_PORT="$(QUALITY_GATE_PORT)" QUALITY_GATE_REPORT="$(QUALITY_GATE_REPORT)" ./scripts/demo-quality-gate.sh

# Enables native OpenShift user-workload monitoring, applies only demo-scoped
# ServiceMonitors, and adds a dashboard to the existing Grafana Helm release.
demo-observability-enable:
	./scripts/enable-user-workload-monitoring.sh
	oc apply -k manifests/observability
	@if oc get namespace grafana >/dev/null 2>&1; then \
		oc apply -f manifests/observability/20-grafana-dashboard.yaml; \
	else \
		echo 'Grafana namespace not found; metrics remain available in OpenShift Observe > Metrics.'; \
	fi

demo-promote:
	./scripts/demo-lifecycle.sh promote

demo-e2e:
	./scripts/demo-lifecycle.sh e2e

# Non-destructive proof of the checked-in synthetic BusinessWorkItem flowing
# through the deployed model/controller and into its pending Kueue Workload.
demo-intake-e2e: demo-check
	$(MAKE) demo-baseline
	./scripts/demo-intake-e2e.sh

demo-down:
	./scripts/demo-lifecycle.sh down

demo-kueueviz-up: demo-check
	./scripts/deploy-kueueviz.sh

# Reproducible amd64 image and clean-cluster deployment targets. The image
# push route is externally reachable; workloads always use the internal
# OpenShift service registry references in manifests/.
IMAGE_REGISTRY_ROUTE ?= $(shell oc get route default-route -n openshift-image-registry -o jsonpath='{.spec.host}' 2>/dev/null)
IMAGE_NAMESPACE ?= kueue-demo
INTERNAL_IMAGE_REGISTRY ?= image-registry.openshift-image-registry.svc:5000
CONTROLLER_IMAGE ?= $(IMAGE_REGISTRY_ROUTE)/$(IMAGE_NAMESPACE)/intake-controller
DECISION_MODEL_IMAGE ?= $(IMAGE_REGISTRY_ROUTE)/$(IMAGE_NAMESPACE)/diffusiongemma-structured-decision
CONTROLLER_PACKAGE ?= ./cmd/manager
CONTROLLER_TAG ?= $(shell git rev-parse --short=12 HEAD)
DECISION_MODEL_TAG ?= vllm-e9757321527ca1ecd514c07c1418dd2c53da3d19-fp8-r3
MODEL_ROLLOUT_TIMEOUT ?= 1800s

.PHONY: source-check registry-check registry-prepare registry-login registry-logout image-lock-update crd-deploy policy-config-deploy image-build-controller image-build-decision-model image-build \
	image-push-controller image-push-decision-model image-push \
	bootstrap-deploy decision-model-deploy

registry-check:
	@oc whoami >/dev/null
	@test -n "$(IMAGE_REGISTRY_ROUTE)" || { echo 'OpenShift image registry route not found' >&2; exit 1; }

# Image artifacts must correspond to committed source. AGENTS.md is excluded
# from the controller build context and may remain locally edited by the user.
source-check:
	@git diff --quiet HEAD -- . ':(exclude)AGENTS.md' || { echo 'commit source changes before building reproducible images' >&2; exit 1; }
	@test -z "$$(git ls-files --others --exclude-standard)" || { echo 'track or ignore new source files before building reproducible images' >&2; exit 1; }

registry-prepare:
	oc apply -f manifests/bootstrap/00-namespace.yaml
	oc apply -f manifests/bootstrap/10-image-streams.yaml

registry-login: registry-check registry-prepare
	oc whoami -t | docker login --username "$$(oc whoami)" --password-stdin "$(IMAGE_REGISTRY_ROUTE)"

registry-logout:
	-docker logout "$(IMAGE_REGISTRY_ROUTE)"

image-build-controller: registry-check source-check
	docker build --platform linux/amd64 \
		--build-arg CONTROLLER_PACKAGE=$(CONTROLLER_PACKAGE) \
		--build-arg SOURCE_REVISION=$(CONTROLLER_TAG) \
		-f images/controller/Dockerfile \
		-t $(CONTROLLER_IMAGE):$(CONTROLLER_TAG) .

image-build-decision-model: registry-check source-check
	docker build --platform linux/amd64 \
		-f images/diffusiongemma/Dockerfile \
		-t $(DECISION_MODEL_IMAGE):$(DECISION_MODEL_TAG) \
		images/diffusiongemma

image-build: image-build-controller image-build-decision-model

image-push-controller: registry-login
	docker push $(CONTROLLER_IMAGE):$(CONTROLLER_TAG)

image-push-decision-model: registry-login
	docker push $(DECISION_MODEL_IMAGE):$(DECISION_MODEL_TAG)

image-push: image-push-controller image-push-decision-model
	$(MAKE) registry-logout

image-lock-update: image-push
	CONTROLLER_TAG="$(CONTROLLER_TAG)" DECISION_MODEL_TAG="$(DECISION_MODEL_TAG)" \
	IMAGE_NAMESPACE="$(IMAGE_NAMESPACE)" INTERNAL_IMAGE_REGISTRY="$(INTERNAL_IMAGE_REGISTRY)" \
	./scripts/update-image-lock.sh

crd-deploy:
	oc apply -f config/crd/bases/tokens.jeder.github.com_businessworkitems.yaml
	oc wait --for=condition=Established crd/businessworkitems.tokens.jeder.github.com --timeout=120s

bootstrap-deploy: crd-deploy
	oc apply -f manifests/bootstrap/00-namespace.yaml
	$(MAKE) policy-config-deploy
	oc apply -k manifests/bootstrap
	oc rollout restart deployment/intake-controller -n kueue-demo
	oc rollout status deployment/intake-controller -n kueue-demo --timeout=120s

policy-config-deploy:
	oc create configmap fare-policy -n kueue-demo \
		--from-file=fare-policy.yaml=config/policy/fare-policy.yaml \
		--dry-run=client -o yaml | oc apply -f -

decision-model-deploy: bootstrap-deploy
	oc apply -k manifests/decision-model
	oc rollout status deployment/decision-model -n kueue-demo --timeout=$(MODEL_ROLLOUT_TIMEOUT)

# Resets only demo Jobs and synthetic BusinessWorkItems; the namespace, app
# deployments, ImageStreams, and model cache PVC are preserved.
demo-rebuild:
	$(MAKE) source-check
	$(MAKE) demo-up
	$(MAKE) registry-prepare
	$(MAKE) image-build
	$(MAKE) image-lock-update
	$(MAKE) bootstrap-deploy
	$(MAKE) decision-model-deploy
	$(MAKE) demo-observability-enable
	$(MAKE) demo-fixtures-e2e

# Explicit destructive alternative. gp3-csi uses Delete reclaim policy, so
# deleting kueue-demo permanently removes the model-cache EBS volume and files.
demo-rebuild-full-reset:
	$(MAKE) source-check
	$(MAKE) demo-down
	$(MAKE) demo-rebuild
