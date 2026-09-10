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
