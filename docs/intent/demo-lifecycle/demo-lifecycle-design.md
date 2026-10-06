---
parent: ../../high-level-design.md
prefix: DEMO-LIFECYCLE
---

# Demo Lifecycle Design

## Responsibilities

The lifecycle script validates cluster prerequisites, materializes a named
demo namespace, admits the auth holder before submitting waiters, captures
state, promotes renewal only while pending, and deletes only the named demo
namespace during cleanup.

## State Transitions

1. `check` verifies CLI access, Kueue readiness, and required APIs.
2. `up` creates the foundation, submits auth, verifies admission, then submits
   waiters and verifies the baseline.
3. `promote` verifies renewal has no reservation, changes its priority label,
   and waits for Kueue to observe priority 1000.
4. `e2e` uses a unique namespace and cleans it up unless explicitly retained.
5. `down` deletes only an allowlisted demo namespace.

## Decisions & Alternatives

| Decision | Alternatives | Rationale |
| --- | --- | --- |
| Makefile dispatches to Bash | Make-only shell; Kubernetes controller | Bash permits bounded polling and safe cleanup without another runtime controller. |
| Submit holder first | Submit all Jobs together | Simultaneous submission admitted a lower-priority Job first in live testing. |
| Unique e2e namespace | Reuse `kueue-demo` | It prevents stale same-name controller state and concurrent-run collisions. |

## Failure Handling

Every wait has a bounded timeout and prints Jobs, Workloads, and ClusterQueue
state on failure. Promotion refuses to mutate an admitted or reserved renewal
Workload. Cleanup refuses any namespace outside the demo allowlist.
