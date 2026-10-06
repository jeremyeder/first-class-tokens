# Demo Lifecycle Specifications

- **DEMO-LIFECYCLE-001:** When `demo-up` runs, the lifecycle shall verify the auth holder is admitted before it submits renewal or documentation Jobs.
- **DEMO-LIFECYCLE-002:** When the baseline is ready, the lifecycle shall verify one active admitted holder and two unreserved, suspended waiters.
- **DEMO-LIFECYCLE-003:** When `demo-promote` runs, the lifecycle shall reject a reserved or admitted renewal Workload and shall otherwise set only its Kueue priority-class label to `fare-first-class`.
- **DEMO-LIFECYCLE-004:** When `demo-e2e` runs without `KEEP=1`, the lifecycle shall use and delete a unique allowlisted demo namespace.
- **DEMO-LIFECYCLE-005:** When the holder receives SIGTERM or SIGINT, it shall exit cleanly so Kueue can release reserved capacity.
- **DEMO-LIFECYCLE-006:** When `demo-down` receives a namespace outside the allowlist, it shall fail without calling Kubernetes deletion.
