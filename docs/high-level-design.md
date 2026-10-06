# Reproducible Kueue Demo

## Purpose

Provide a repeatable, isolated demonstration in which business facts drive a
policy-validated priority for a pending Kueue Job. The demo must be safe
on a cluster that already hosts unrelated workloads.

## Architecture

`make` is the operator-facing command surface. A shell lifecycle script owns
prerequisite checks, namespace-scoped setup, holder-first admission,
assertions, capture, promotion, and cleanup. Kubernetes manifests remain the
resource source of truth.

The demo uses one 500m CPU ClusterQueue slot. The auth Job is a long-running,
signal-safe holder. Renewal and documentation Jobs are submitted only after
the holder is admitted. Promotion changes only a pending renewal Job's
priority label.

## Tenets

- Lifecycle scripts may clean up only explicitly named demo namespaces.
- Evidence is captured from Kubernetes state, not inferred from the UI.
- A repeatable baseline is more important than a minimal command count.

## Components

- `manifests/00-demo-lane.yaml`: queue topology, priorities, and Jobs.
- `scripts/demo-lifecycle.sh`: lifecycle transitions and assertions.
- `Makefile`: stable operator commands.
- `tests/`: local contract tests for lifecycle safety and manifest intent.

## Non-goals

The lifecycle does not install or reconfigure Kueue, implement the future
BusinessWorkItem controller, grant write access to KueueViz, or operate
outside its demo namespace.
