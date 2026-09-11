# WekaClient Controller

Creates client WekaContainers for selected nodes, coordinates CSI ownership and
drivers, and propagates configuration to existing containers.
Source: `internal/controllers/wekaclient/`.

- `client_controller.go`: controller setup.
- `client_reconciler_loop.go`: reconciliation, `buildClientWekaContainer`, `updateContainerIfChanged`, `UpdatableClientSpec`.
- [WekaContainer lifecycle](wekacontainer.md) handles each generated container;
  [operations](../operations/index.md) covers CSI and drivers.

## Spec propagation

Existing containers receive a field only when it is carried by `UpdatableClientSpec`
and compared in `updateContainerIfChanged`; creation-time assignment alone is insufficient.

`NewUpdatableClientSpec` is fingerprinted with `util.HashStruct` (gob).
`resource.Quantity` stores its value in unexported fields, so use a textual digest
for change detection. `ResourcesDigest` does this for `spec.resources`;
`normalizePodResources` equates an all-zero spec with nil to avoid hash churn.

`applyResourcesOverride` in `internal/controllers/resources/pod.go` applies
`spec.resources` last, for every mode and CPU policy. Explicit CPU overrides can
therefore diverge from planner CPU accounting and DRA sizing. Inspect this path
when changing resource propagation or accounting.
