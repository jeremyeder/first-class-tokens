# Demo Lifecycle Operating Rules

## Scope

This repository owns an isolated Kueue business-priority reference demo. The
tracked surface is deployable manifests, lifecycle scripts, tests, and their
durable design contract. KueueViz is the preferred visualization.

## Safety

- Run lifecycle actions through `make demo-*`; do not improvise broad `oc` deletes.
- Cleanup may delete only `kueue-demo` or `kueue-demo-<suffix>` namespaces.
- Never mutate the Kueue operator, Kueue CR, KueueViz, or unrelated namespaces.
- Establish the capacity holder before submitting waiting Jobs.
- Keep all demo data explicitly synthetic.

## Verification

- Run `make test` after tracked changes.
- Use `make demo-check` before live lifecycle actions.
- A successful baseline has one admitted auth holder and two unreserved waiting Workloads.

## Design References

- `docs/high-level-design.md`
- `docs/intent/demo-lifecycle/demo-lifecycle-design.md`
- `docs/intent/demo-lifecycle/demo-lifecycle-specs.md`
