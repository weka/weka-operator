# WekaCluster Capacity Planning

Maps whole-cluster drive targets (clusterCapacity) and daemonset sizing (auto full drives) to node-pinned containers.
Code is the source of truth.

## Reconciliation path

`BuildMissingContainers` → clusterCapacity: `buildPlannerDriveContainers` → `planClusterCapacity` →
inventory collection → `capacityplanner.PlanCapacity` → create/grow containers.
Daemonset: `buildDaemonsetContainers` → `planDaemonset` (node annotations only, no inventory).

For clusterCapacity, `steadyStatePlan` can skip inventory when existing capacity
covers the target and compute needs no growth; see the accounting caveat below.
Device allocation then populates `Status.Allocations.VirtualDrives`.

## Source map

| Source | Entry points |
|---|---|
| `internal/controllers/wekacluster/funcs_fd_planning.go` | Planning orchestration, `steadyStatePlan`, `summarizeDriveContainers` |
| `internal/controllers/wekacluster/steps_planner_apply.go` | clusterCapacity drive/compute create and growth paths |
| `internal/controllers/wekacluster/daemonset.go` | `planDaemonset`, `buildDaemonsetContainers`, `listFreeDrives` |
| `internal/capacityplanner/inventory/collect.go` | `Collector`, `NodeInventory`, `ExistingDrives`, `ExistingCompute`, `DriveContainerCapacities` |
| `internal/capacityplanner/planner.go` | `PlanCapacity` |
| `internal/capacityplanner/cpu.go`, `cores.go`, `hugepages.go` | `CPURequestCores`, `RequiredComputeCores`, pod-resource formulas |
| `internal/capacityplanner/infeasibility.go` | `InfeasibilityReport`, diagnostic fixes reused by events and CLI |
| `internal/controllers/allocator/container_allocator.go` | `allocateSharedDrivesByCapacityWithTypes`, `buildDriveCapacityMap` |
| `internal/controllers/wekacontainer/funcs_getters.go`, `funcs_drives.go` | `NeedsDrivesToAllocate`, `checkDriveResourceFeasibility`, `deferIfFullDrivesShortfall` |
| `cmd/weka-capacity/` | Inventory exploration and dry-run planning CLI |

## Details

- [TLC/QLC co-location and in-place growth](wekacluster-drive-colocation.md).
- [Increase-path sizing](wekacluster-drive-sizing.md).
- [Capacity accounting caveat](wekacluster-drive-capacity-accounting.md).
- Deployment modes and constraints: [cluster capacity](../../doc/operator/deployment/cluster-capacity.md), [daemonset mode](../../doc/operator/deployment/act-as-daemonset.md).
- [Validation](../config/validation.md) and [cluster reconciliation](wekacluster.md).
