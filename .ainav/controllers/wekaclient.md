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

## Weka Home CA cert

- `domain.GetWekaHomeClientCacertSecret` (`internal/pkg/domain/wekahome.go`): resolves the cacert
  Secret; the resolved name rides on `UpdatableClientSpec`.
- Cross-namespace target clusters are not inherited from; admission warns via
  `client_wekahome_cacert_not_inherited`, not at runtime. Path constant:
  `domain.WekaHomeCacertPath`.
- Semantics: `doc/operator/operations/weka-home-tls.md`.

## Extra volumes

- `spec.extraVolumes`/`spec.extraVolumeMounts` propagate via `UpdatableClientSpec`, normalized by
  `resources.NormalizeExtraVolumes` and included in the gob hash; `ExtraVolumesEqual`/
  `ExtraVolumeMountsEqual` drive the per-container update in `updateContainerIfChanged`.
- `resources.applyExtraVolumes` (`internal/controllers/resources/pod.go`) mounts them into the
  weka container only. Semantics: `doc/operator/deployment/extra-volumes.md`.
