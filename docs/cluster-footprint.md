# Cluster footprint and model-serving customization

This document describes what the demo creates, where it creates it, and how
its DiffusionGemma/vLLM decision service is run on an RHOAI-managed OpenShift
cluster. The normal entry point is `make demo-rebuild`; optional UI,
visualization, and preemption manifests are not part of that target.

## Namespace and cluster-scope inventory

| Namespace or scope | Purpose | Created/used resources |
| --- | --- | --- |
| `kueue-demo` | Primary runtime namespace. Safe rebuilds preserve it. | Demo Jobs and `BusinessWorkItem` resources; intake-controller Deployment, ServiceAccount, Role/RoleBinding; decision-model Deployment, Service, ServiceAccount, immutable ConfigMap, 100Gi PVC; two ImageStreams. The namespace has `app.kubernetes.io/part-of=business-priority-demo` and `kueue.openshift.io/managed=true`. |
| `kueue-demo-e2e-<suffix>` | Temporary queue-lifecycle test namespace created by `make demo-e2e`. | A copy of the queue foundation and three demo Jobs. The script removes only this namespace on success/failure unless `KEEP=1`. It does not deploy the model or intake controller. |
| `kueueviz` | Optional primary visualization for Kueue state. | Frontend/backend Deployments and Services, OpenShift-generated Routes, ConfigMap, and a read-only ClusterRole/Binding from `manifests/kueueviz-v0.19.4-openshift.yaml`. Apply with `make demo-kueueviz-up`; the target reads generated hosts and configures the WebSocket URL and backend allowed origin. Not part of `make demo-rebuild`. |
| `kueue-preemption-test` | Separate preemption experiment. | Its own Jobs and LocalQueue. Its foundation also defines cluster-scoped queue/priority resources. Not applied by `make demo-rebuild`. |
| Cluster-scoped | Shared Kueue scheduling objects owned by this demo. | `ResourceFlavor/demo-cpu`, `ClusterQueue/business-priority-demo`, and `WorkloadPriorityClass/fare-first-class`, `fare-business`, `fare-economy`. The ClusterQueue selects namespaces labeled for this demo. |

The runtime uses external system namespaces without applying demo application
resources there:

- `openshift-kueue-operator`: `make demo-check` reads the `default-kueue`
  readiness condition. The Kueue operator and its Kueue object are not changed.
- `openshift-image-registry`: the build targets read the `default-route` host
  and push image tags through that Route. The pushed repositories and
  ImageStreams belong to project `kueue-demo`; the demo does not create or
  reconfigure registry operator resources.

RHOAI and NVIDIA GPU Operator namespaces/resources are prerequisites only.
The demo does not edit the DataScienceCluster, Kueue operator configuration,
GPUClusterPolicy, node pools, or GPU Operator resources.

`make demo-rebuild` also enables OpenShift User Workload Monitoring (UWM),
which is a cluster-level monitoring setting rather than a demo-only feature.
The activation script merges its settings into the existing OpenShift
monitoring ConfigMaps instead of replacing unrelated settings. This makes
eligible ServiceMonitors in other user namespaces discoverable too. The demo's
own ServiceMonitors select only its Services in `kueue-demo`. UWM persistence
is configured for seven days with gp3-csi PVCs (10Gi per Prometheus replica,
2Gi per Alertmanager replica, and 5Gi per Thanos Ruler replica); replica counts
are operator-controlled. The existing Grafana installation is reused by
adding one dashboard ConfigMap; no Grafana operator or new Grafana instance is
installed.

## Core resources in `kueue-demo`

### Queue lane

`manifests/00-demo-lane.yaml` creates the namespace, queue topology, three
priority classes, and the three baseline Jobs. The fixture E2E adds 17
additional suspended Jobs from the [fixture Job manifest](../manifests/business-fixtures/10-synthetic-jobs.yaml),
for 20 work items and linked Jobs total:

- `ClusterQueue/business-priority-demo` has a 500m CPU and 512Mi memory quota
  using `ResourceFlavor/demo-cpu`.
- `LocalQueue/business-priority` connects namespaced Jobs to that ClusterQueue.
- `fare-first-class`, `fare-business`, and `fare-economy` have Kueue values
  1000, 500, and 100 respectively.
- `auth-regression-fix` is submitted first and runs as a signal-safe capacity
  holder, requesting the full 500m CPU slot.
- `renewal-recovery-plan` and `documentation-refresh` are suspended waiters,
  each requesting 500m CPU and 256Mi memory. Intake changes the former's
  priority from its initial economy label based on the current event.

Kueue creates one `Workload` for each managed Job. Workloads are owned by
Kueue; this repository does not define the Workload CRD.

### Intake controller and BusinessWorkItem

This repository defines one custom CRD:

- `businessworkitems.tokens.jeder.github.com`, API `tokens.jeder.github.com/v1alpha1`,
  kind `BusinessWorkItem`, namespaced, with a `/status` subresource.

Its user-owned `spec` contains a demo provenance marker, normalized identity/source
metadata, raw scalar `sourceFacts`, and a link to one Job in the same namespace.
Controller-owned `status` stores the selected class, confidence, mapped Kueue
class/value, evaluation time, linked Workload state, conditions, and decision
history. The resource schema is in
`config/crd/bases/tokens.jeder.github.com_businessworkitems.yaml`.

The controller runs with a namespaced Role limited to BusinessWorkItems and
their status, Jobs, and Kueue Workloads in `kueue-demo`. It cannot write
Workloads, ClusterQueues, or resources in other namespaces. For each item it
checks provenance and the same-namespace Job link, calls the model,
validates the class/confidence against FarePolicy, and patches only the linked
Job's Kueue priority-class label when its Workload is pending and unreserved.
For an admitted/reserved Workload it records the decision but does not patch
the Job.

`config/policy/fare-policy.yaml` is validated and loaded from a ConfigMap. It
is configuration, not a CRD. `config/images.lock.yaml` is a repository image
lock file, not an installed Kubernetes API type. The Kueue and RHOAI CRDs are
provided by their operators, not authored here.

## Decision model serving customization

The model server is a project-owned Kubernetes Deployment, not an RHOAI
`InferenceService` or ModelServing CR. RHOAI supplies the tested GPU/Kueue
platform; this demo supplies the model pod, wrapper, service, and cache.

### Why a Deployment today, and whether InferenceService is possible

The initial implementation chose a Deployment for expediency and control over
the pinned vLLM image/arguments and the local structured `/v1/systemone`
adapter. This was not because this cluster lacks KServe: its DataScienceCluster
has KServe `Managed` and `KserveReady=True`, and the standard
`InferenceService` and `ServingRuntime` CRDs are established. An
`InferenceService` with a custom `ServingRuntime` is plausible, but it has not
been prototyped against this exact DiffusionGemma wrapper, custom API path, and
cache lifecycle. Red Hat documents custom runtimes as user-managed and not Red
Hat supported; see [RHOAI 3.5 model-serving runtime configuration](https://docs.redhat.com/en/documentation/red_hat_openshift_ai_self-managed/3.5/html/configuring_your_model-serving_platform/configuring-your-model-serving-platform_rhoai-admin).

The separate `LLMInferenceService` path is not a ready drop-in on this cluster:
the DataScienceCluster reports its LLMInferenceService dependency condition
false because Red Hat Connectivity Link is not installed. The currently working
Deployment remains the active path. No migration or serving-resource change is
part of `make demo-rebuild`.

| Setting | Current customization |
| --- | --- |
| Model | `RedHatAI/diffusiongemma-26B-A4B-it-FP8-dynamic`, pinned to Hugging Face revision `3b3dae4697494da5a290e9c0461954449e76c4f5`. |
| vLLM base | `vllm/vllm-openai:nightly-e9757321527ca1ecd514c07c1418dd2c53da3d19`, pinned by digest in `config/images.lock.yaml`; structured-server code is pinned to the same vLLM commit. |
| GPU and resources | One `nvidia.com/gpu`; requests 2 CPU/16Gi memory, limits 8 CPU/32Gi memory; GPU memory utilization 0.85. Validated on an NVIDIA L40S. |
| Generation limits | Model length 4096, maximum 4 sequences, prefix caching enabled, 64-token DiffusionGemma canvas, maximum 32 log probabilities, one diffusion step per decision. |
| Runtime ports | vLLM listens on pod loopback `127.0.0.1:8000`. The structured System One adapter listens on `0.0.0.0:8011`. The ClusterIP Service exposes only port 8011; the raw vLLM port is not published by the Service. |
| Probes and restart | Startup/readiness/liveness probes call the adapter's `/health`. Deployment strategy is `Recreate` because there is one GPU and one RWO cache volume. Termination grace is 60 seconds. |
| Cache | `decision-model-cache` is a 100Gi RWO PVC mounted at `/models/cache`, used for Hugging Face, Transformers, and related caches. The model weights are fetched at runtime; they are not embedded in the wrapper image. |

The immutable ConfigMap `decision-model-config-v3` holds the model ID/revision,
served name `dgemma`, canvas length, pinned vLLM/structured-server revisions,
and cache paths. `images/diffusiongemma/start.sh` starts vLLM with the settings
above, waits for its local health endpoint, then starts the vendored structured
decision adapter. The intake controller sends one choice question to the
adapter's `/v1/systemone` endpoint using the allowed class definitions from
FarePolicy.

No external Route exposes the model API. Traffic is pod-to-Service inside
`kueue-demo`. A model pod needs outbound access to Hugging Face on a cold cache;
after warmup, the pinned files persist on the PVC.

### Business-event churn and quality gate

The business fact corpus includes 20 work-item identities and version-1 events
in `config/fixtures/business-work-items.yaml` and
`config/fixtures/business-events.jsonl`. The independent expected decision
classes are in `config/evaluation/expected-outcomes.yaml`; they are not
generated from model output or copied from the live response. Three items
(001–003) have explicit version-2 business updates in the event feed. The
fixture E2E evaluates all 20 version-1 items, then applies those three version-2
updates to prove the controller observes new input and records fresh decisions.
The remaining 17 stay unchanged. Every generated fixture Job except the
long-running auth capacity holder is suspended, so the churn is observable
without consuming extra lane capacity.

Before the controller E2E, `make demo-quality-gate` directly calls the model
for all 23 event snapshots five times each. It writes
`artifacts/evaluation/` and checks exact-class
accuracy ≥90%, per-item majority stability ≥80%, and p95 latency ≤5 seconds.
The report includes individual expected/observed classes, confidence, class
flips, failures, and latency. This quality gate is intentionally separate from
the controller's operational metrics: a high confidence score alone does not
mean a correct decision.

The controller E2E treats an explicit `LowConfidence` result as a completed
fail-closed abstention: it verifies the linked Job priority is unchanged,
counts the abstention separately, and continues the run. The 0.80 confidence
floor remains in force; fallback to a stronger model or human review is not
implemented.

### Metrics and visualization

The intake controller exposes decision counts, selected class/reason,
confidence and latency histograms, cache hits, and safe projection outcomes at
its `/metrics` endpoint. The structured adapter exposes request/decision
counts, latency, diffusion read-count and confidence histograms, plus a
read-only proxy of vLLM's loopback metrics at its `/metrics` endpoint. The
ServiceMonitors select only the controller and decision-model Services in
`kueue-demo`; the raw vLLM API remains unexposed. UWM scrapes these monitors,
and the `Business Priority Demo` dashboard ConfigMap is consumed by the
existing Grafana sidecar. The same Prometheus metrics can also be queried from
OpenShift Observe > Metrics. Decision correctness and stability remain in the
separate quality report rather than being inferred from monitoring metrics.

## Reset behavior and cluster impact

- `make demo-up` and `make demo-rebuild` delete only the three baseline Jobs
  and demo-labeled BusinessWorkItems, wait for their Kueue Workloads to
  clear, and recreate the held baseline. Additional fixture Jobs from the
  previous churn run remain suspended; the next fixture E2E reapplies their
  checked-in definitions. The namespace, app Deployments, ImageStreams, and
  PVC remain.
- `make demo-fixtures-clean` removes all demo-labeled BusinessWorkItems,
  deletes only the 17 extra Jobs declared in
  the [fixture Job manifest](../manifests/business-fixtures/10-synthetic-jobs.yaml), waits for their Kueue
  Workloads to disappear, then recreates and verifies the three-Job
  capacity-held baseline. It preserves the namespace, app Deployments,
  ImageStreams, and model-cache PVC; it does not delete unrelated resources.
- `make demo-rebuild` then builds/pushes both AMD64 images, pins the resulting
  internal registry digests, reapplies the CRD/policy/controller/model, waits
  for rollouts, enables UWM and demo metrics/Grafana, passes the repeated model
  quality gate, and runs the event-fixture E2E.
- `make demo-rebuild-full-reset` and `make demo-down` delete `kueue-demo`.
  On the validated cluster, `gp3-csi` has reclaim policy `Delete`; that removes
  the cache volume and requires downloading model files again. The full-reset
  target is explicit and should be used only when cache loss is intended.
- `make demo-e2e` uses an ephemeral `kueue-demo-e2e-<suffix>` namespace and
  deletes it unless `KEEP=1`; its cluster-scoped Kueue resources are the same
  named demo queue objects and may be applied/updated during the test.
- KueueViz and preemption manifests are optional separate operations. They are
  not installed, upgraded, or removed by `demo-rebuild`.

## Live cluster used for validation

The verified cluster was OpenShift 4.22.5 with a Ready RHOAI
`DataScienceCluster/default-dsc`, Ready Kueue instance
`openshift-kueue-operator/default-kueue`, one allocatable NVIDIA L40S GPU, and
a Bound 100Gi `gp3-csi` cache PVC. These are observed working values, not
minimum supported version declarations.
