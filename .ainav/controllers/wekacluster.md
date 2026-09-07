# WekaCluster Controller

Creates WekaContainers, forms the cluster and coordinates credentials, upgrades,
protocols and management access. Source: `internal/controllers/wekacluster/`.

| File | Responsibility / symbols |
|---|---|
| `controller.go`, `reconciler_loop.go` | Watches and reconciliation flow |
| `steps_cluster_creation.go` | `BuildMissingContainers`, count-based role creation |
| `steps_planner_apply.go` | `plannerSizingMode`, `buildPlannerDriveContainers`, `applyPlannerDriveGrowth`, `applyPlannerComputeGrowth`, `updateContainerWithRetry` |
| `funcs_fd_planning.go` | `planClusterCapacity`, `planAutoFullDrives` |
| `planner_events.go` | Event reasons, severity and throttling: `plannerEventSpecs`, `emitPlannerEvent` |
| `funcs_clusterization.go` | Cluster formation |
| `funcs_credentials.go`, `funcs_helpers.go` | Credentials and helpers |
| `funcs_upgrade.go` | Upgrade orchestration and per-role spec propagation in `HandleSpecUpdates` |
| `funcs_nfs.go`, `funcs_s3.go` | NFS interface groups and S3 configuration |
| `steps_post_cluster.go` | Post-creation operations, including `configureWekaHome` |
| `steps_metrics.go`, `steps_deletion.go` | Monitoring setup and deletion |
| `funcs_management_proxy.go`, `funcs_management_service.go` | Proxy resources and endpoint selection |

## Focused routes

- [Capacity planning](wekacluster-drive-planning.md): shared apply path, inventory, pure planners and device allocation.
- Sizing modes and constraints: [cluster capacity](../../doc/operator/deployment/cluster-capacity.md), [auto full drives](../../doc/operator/deployment/act-as-daemonset.md). `plannerSizingMode` detects the mode; [validation](../config/validation.md) owns admission rules.
- [Management proxy](management-proxy.md): bootstrap versus endpoint updates, probes and host networking.

## Weka Home CA cert scope

`configureWekaHome` (`steps_post_cluster.go`) sets `weka_cloud_ca_cert_path` from a drive container.
The override is cluster-wide: it replicates to every joining machine, but the certificate file does
not, and an explicit CA replaces the OS trust store rather than adding to it. Setting
`spec.wekaHome.cacertSecret` therefore obliges every joining machine — including clients this
operator does not manage — to place a PEM at `/opt/weka/k8s-runtime/vars/wh-cacert/cert.pem`.
See `doc/operator/operations/weka-home-tls.md`.
