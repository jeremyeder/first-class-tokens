# Demo prerequisites

This document lists the cluster, storage, network, and local tooling needed to
run the business-priority demo from a clean OpenShift cluster.

## Cluster prerequisites

- An OpenShift cluster with Kueue installed and ready. `make demo-check` verifies
  the Kueue API and the `default-kueue` instance before lifecycle operations.
- The Kueue APIs used by the demo: `Workload`, `ClusterQueue`, `LocalQueue`,
  `ResourceFlavor`, and `WorkloadPriorityClass`. The operator supplies these
  CRDs; this repository creates demo-scoped resources from them.
- A GPU Operator/device plugin exposing `nvidia.com/gpu`, with one GPU
  allocatable to the model pod. The validated configuration uses an NVIDIA
  L40S (46 GiB reported allocatable GPU memory). Other GPU models are not
  assumed equivalent without a deployment test.
- Enough node resources for the model deployment: requests of 2 CPU, 16 GiB
  memory, and one GPU; limits of 8 CPU, 32 GiB memory, and one GPU.
- A default StorageClass capable of provisioning a 100 GiB `ReadWriteOnce`
  model-cache volume. The tested cluster uses `gp3-csi`.
- Outbound HTTPS from the model pod to Hugging Face on the first cold start so
  it can fetch the pinned model revision. The cache is mounted at
  `/models/cache` on the `decision-model-cache` PVC.
- Cluster permissions to install the BusinessWorkItem CRD, create/update the
  demo Kueue resources, operate the `kueue-demo` namespace, and push to the
  OpenShift integrated image registry.
- Cluster-admin permission to enable OpenShift User Workload Monitoring and
  merge its persistent-storage configuration. `make demo-rebuild` does this
  automatically, preserving other supported monitoring settings. It also
  makes eligible ServiceMonitors in other user namespaces discoverable, not
  just the demo's monitors. The demo configures seven-day retention and
  gp3-csi PVCs (10Gi for each Prometheus replica, 2Gi for each Alertmanager
  replica, and 5Gi for each Thanos Ruler replica).
- An existing Grafana deployment is optional. If the `grafana` namespace is
  present, the rebuild adds a dashboard ConfigMap consumed by the existing
  dashboard sidecar; otherwise metrics remain available through OpenShift
  Observe > Metrics. The demo does not install Grafana.

### RHOAI relationship

The validated environment is RHOAI-managed: its DataScienceCluster is Ready and
its Kueue instance is Ready. The intake controller does not call an RHOAI API;
it calls the in-cluster System One decision service directly. RHOAI is the
tested way this cluster supplies the AI/GPU platform, while the runtime
requirements for this demo are OpenShift, ready Kueue APIs, GPU scheduling, and
persistent storage.

## Local tools and access

- `oc`, authenticated to the target cluster. Cluster-admin access is used by
  the full rebuild because it installs a CRD and applies cluster-scoped Kueue
  resources.
- GNU Make, Go 1.27 or compatible with `go.mod`, Ruby with YAML support, `jq`,
  and Bash. The Makefile uses Go for tests and fixture rendering, Ruby for
  lifecycle manifest selection, and `jq` for state assertions.
- Docker with BuildKit and support for `--platform linux/amd64`. The Makefile
  builds the controller and vLLM wrapper images for OpenShift's AMD64 workers.
- Network access from the build host to the pinned public base-image registries
  and from the build host to the OpenShift registry route. Registry login uses
  the current `oc` token. A Docker credential helper is recommended so that
  Docker does not retain the token unencrypted in its config file.
- Network access from the model pod to the pinned Hugging Face model revision
  on a cold cache. The model weights are fetched at runtime, not baked into the
  vLLM wrapper image.
- A committed source tree for reproducible image builds. `make source-check`
  prevents building/pushing images from uncommitted code.

## What this repository installs

- The `BusinessWorkItem` CRD from
  `config/crd/bases/tokens.jeder.github.com_businessworkitems.yaml`.
- A Kueue lane with a 500m CPU quota, one long-running admitted auth
  holder, and suspended waiting Jobs. The fixture E2E exercises 20 business
  work items and linked Jobs in total, without admitting the 19 non-holder Jobs.
- The intake controller, the structured-decision service, and a 100 GiB model
  cache PVC.
- Fare policy configuration and generated BusinessWorkItems joined
  from `config/fixtures/business-work-items.yaml` and
  `config/fixtures/business-events.jsonl`. Independent expected classes are
  stored in `config/evaluation/expected-outcomes.yaml`; three items also have
  versioned business updates to exercise re-decisions. `make demo-quality-gate`
  repeats the 23 event snapshots against the model and checks accuracy,
  per-item stability, and latency before the controller E2E.

Kueue's CRDs and the RHOAI DataScienceCluster/Kueue CRDs are supplied by their
operators; they are prerequisites, not CRDs authored by this repository.
`config/policy/fare-policy.yaml` is configuration data, not a CRD.

## Rebuild and cache behavior

`make demo-rebuild` resets only the demo Jobs and labeled demo
BusinessWorkItems. It preserves the namespace, deployments, ImageStreams, and
model-cache PVC. It then builds and pushes images, updates digest pins, deploys,
enables/configures UWM, installs demo-scoped ServiceMonitors and the Grafana
dashboard (when Grafana exists), and runs the model quality gate and fixture
E2E.

`make demo-rebuild-full-reset` (or `make demo-down`) deletes `kueue-demo`. On the
tested cluster, the PVC uses `gp3-csi` with reclaim policy `Delete`, so this
permanently deletes the model-cache volume and requires the model weights to be
downloaded again on the next start. Use the full-reset target only when that
cache loss is intended.

## Initial check

Before a rebuild, verify cluster readiness and access:

```sh
oc whoami
make demo-check
oc get datasciencecluster default-dsc -o wide
oc get kueue default-kueue -n openshift-kueue-operator
oc get nodes -o wide
oc get storageclass gp3-csi
```

`make demo-rebuild` is the complete cache-preserving build/deploy/E2E path. It
expects the cluster prerequisites above; it does not install RHOAI, Kueue, or
the GPU Operator.

See [cluster footprint and model-serving customization](cluster-footprint.md)
for the namespace/object inventory and how the vLLM service is configured.
