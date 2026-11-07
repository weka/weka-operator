# WekaCluster Controller

Creates WekaContainers, forms the cluster, coordinates credentials, upgrades, protocols
and management access. Source: `internal/controllers/wekacluster/`.

| File | Responsibility / symbols |
|---|---|
| `controller.go`, `reconciler_loop.go` | Watches and reconciliation flow |
| `steps_cluster_creation.go` | `BuildMissingContainers`, count-based role creation |
| `steps_planner_apply.go` | `buildPlannerDriveContainers`, `applyPlannerDriveGrowth`, `applyPlannerComputeGrowth`, `updateContainerWithRetry` |
| `funcs_fd_planning.go` | `planClusterCapacity` |
| `daemonset.go` | `planDaemonset`, `planDaemonsetS3`, `buildDaemonsetContainers`, `listFreeDrives` |
| `planner_events.go` | Event reasons, severity and throttling: `plannerEventSpecs`, `emitPlannerEvent` |
| `funcs_clusterization.go` | Formation |
| `funcs_credentials.go`, `funcs_helpers.go` | Credentials, helpers |
| `funcs_upgrade.go` | `handleUpgrade` dispatches to `handleImageUpgrade` (image roll) or `handlePodRotation`; per-role spec propagation in `HandleSpecUpdates` |
| `funcs_pod_rotation.go` | `podRotationEnabled`, `handlePodRotation`, `clearFinishedRotations`, `rotationContainers`; health gate = `operations.EvaluateClusterHealth` |
| `funcs_nfs.go`, `funcs_s3.go` | NFS interface groups and S3 configuration |
| `steps_post_cluster.go` | Post-creation operations, including `configureWekaHome` |
| `steps_metrics.go`, `steps_deletion.go` | Monitoring setup and deletion |
| `funcs_management_proxy.go`, `funcs_management_service.go` | Proxy resources and endpoint selection |

## Focused routes

- [Capacity planning](wekacluster-drive-planning.md): shared apply path, inventory, pure planners and device allocation.
- Sizing: [cluster capacity](../../doc/operator/deployment/cluster-capacity.md), [daemonset mode](../../doc/operator/deployment/act-as-daemonset.md), [explicit-count scale-up](../../doc/operator/operations/scale-up.md). [Validation](../config/validation.md) owns admission rules.
- [Management proxy](management-proxy.md): bootstrap versus endpoint updates, probes and host networking.

- [Pod rotation](../../doc/operator/operations/pod-rotation.md): `spec.rotatePod` approvals; the container side is in [wekacontainer](wekacontainer.md).

## Weka Home CA cert

- `configureWekaHome` (`steps_post_cluster.go`) sets the `weka_cloud_ca_cert_path` override; `EnsureWekaHomeCacertOverride` (`funcs_overrides.go`) keeps it in sync with the secret.
- Path constant: `domain.WekaHomeCacertPath`. Semantics: `doc/operator/operations/weka-home-tls.md`.

## License

- `EnsureLicense` (`funcs_license.go`), first step of `GetPostClusterSteps`. Semantics: `doc/operator/operations/license.md`.

## Extra volumes

- `spec.podConfig.extraVolumes`/`extraVolumeMounts` propagate through `HandleSpecUpdates` via `resources.NormalizeExtraVolumes` and the `ExtraVolume*Equal` helpers; reserved names in `resources/extra_volumes.go`. Semantics: `doc/operator/deployment/extra-volumes.md`.
