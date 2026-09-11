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

Clients reach Weka Home directly, falling back to a backend only after a direct attempt fails.
The cluster-wide `weka_cloud_ca_cert_path` override replicates to joining machines but the
certificate file does not, so a client of a private-CA cluster must carry it locally.

`domain.GetWekaHomeClientCacertSecret` (`internal/pkg/domain/wekahome.go`) resolves the Secret:
client's `spec.wekaHome.cacertSecret`, then the target cluster's value (same namespace only — a
cross-namespace name raises a throttled `WekaHomeCacertSecretNotInherited` warning), then the
operator-wide env default. The resolved name rides on `UpdatableClientSpec` so it reaches running
containers. Clients are `--restricted` and never set the override; they only need the file.
Full behaviour: `doc/operator/operations/weka-home-tls.md`.
