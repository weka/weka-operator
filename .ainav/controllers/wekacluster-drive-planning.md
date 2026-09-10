# WekaCluster Capacity Planning

Maps whole-cluster drive targets and auto full drives to node-pinned containers.
Code is the source of truth.

## Reconciliation path

`BuildMissingContainers` → `buildPlannerDriveContainers` → `planClusterCapacity`
or `planAutoFullDrives` → inventory collection → `capacityplanner.PlanCapacity`
or `capacityplanner.PlanAutoFullDrives` → create/grow containers.

For clusterCapacity, `steadyStatePlan` can skip inventory when existing capacity
covers the target and compute needs no growth; see the accounting caveat below.
Device allocation then populates `Status.Allocations.VirtualDrives`.

## Source map

| Source | Entry points |
|---|---|
| `internal/controllers/wekacluster/funcs_fd_planning.go` | Planning orchestration, `steadyStatePlan`, `summarizeDriveContainers` |
| `internal/controllers/wekacluster/steps_planner_apply.go` | Mode detection, shared drive/compute create and growth paths |
| `internal/capacityplanner/inventory/collect.go` | `Collector`, `NodeInventory`, `FullDrivesInventory`, `ExistingDrives`, `ExistingCompute`, `DriveContainerCapacities` |
| `internal/capacityplanner/planner.go`, `autofulldrives.go` | `PlanCapacity`, `PlanAutoFullDrives` |
| `internal/capacityplanner/cpu.go`, `cores.go`, `hugepages.go` | `CPURequestCores`, `RequiredComputeCores`, pod-resource formulas |
| `internal/capacityplanner/infeasibility.go` | `InfeasibilityReport`, diagnostic fixes reused by events and CLI |
| `internal/controllers/allocator/container_allocator.go` | `allocateSharedDrivesByCapacityWithTypes`, `buildDriveCapacityMap` |
| `internal/controllers/wekacontainer/funcs_getters.go`, `funcs_drives.go` | `NeedsDrivesToAllocate`, `checkDriveResourceFeasibility` |
| `cmd/weka-capacity/` | Inventory exploration and dry-run planning CLI |

## Details

- [TLC/QLC co-location and in-place growth](wekacluster-drive-colocation.md).
- [Increase-path sizing](wekacluster-drive-sizing.md).
- [Capacity accounting caveat](wekacluster-drive-capacity-accounting.md).
- Deployment modes and constraints: [cluster capacity](../../doc/operator/deployment/cluster-capacity.md), [auto full drives](../../doc/operator/deployment/act-as-daemonset.md).
- [Validation](../config/validation.md) and [cluster reconciliation](wekacluster.md).
