# WekaCluster Drive Capacity Planning

Hub for the drive-capacity planner (cluster TLC/QLC targets → per-node drive containers → device
allocation). Nav aid only — code is source of truth; line numbers are hints. Deep topics are split into
the detail docs at the bottom.

## Flow (cluster → node → device)

```
WekaClusterReconcile
  EnsureWekaContainers → BuildMissingContainers → buildPlannerDriveContainers  # steps_planner_apply.go
    planClusterCapacity
      steadyStatePlan          # fast path: if cur≈desired, "skipping node inventory"
      buildNodeInventory       # per-node physical capacity from shared-drives annotation
      allocator.PlanCapacity   # constrained pool first (pool ordering), then planPool → {FreshUniform | UniformIncrease | Explicit}
  steps_cluster_creation: create/grow drive containers (writes ContainerCapacity + DriveTypesRatio)
  wekacontainer AllocateSharedDrives → allocator.allocateSharedDrivesByCapacityWithTypes
    → Status.Allocations.VirtualDrives (the ACTUAL drives)
```

## Key files & functions

| Concern | File | Symbols (approx line) |
|---|---|---|
| Steady-state / cluster plan | `internal/controllers/wekacluster/funcs_fd_planning.go` | `planClusterCapacity` (orchestrator), `steadyStatePlan`, `summarizeDriveContainers`; node inventory + existing views now come from `inventory.Collector` (below) |
| Node inventory + existing views | `internal/capacityplanner/inventory/collect.go` | `Collector.NodeInventory`/`Collect`/`ExploreNodes`, `ExistingDrives`/`ExistingCompute`, `aggregateContainerResources`, `DriveContainerCapacities`, `nodeCPUTopology` (HT/full-pcpus from `weka.io/discovery.json`) |
| Physical CPU model (data cores → pod CPU) | `internal/capacityplanner/cpu.go` | `CPURequestCores`/`cpuModel`/`NodeCPUTopology` — SINGLE SOURCE OF TRUTH mirroring `resources/pod.go setResources`; `dedicated_ht` = `numCores*2+1`. `NodeCapacity.AllocatableCPU`/`nodeState.coresFree` are PHYSICAL CPU; `driveCPUCost`/`computeCPUCost` convert at charge sites. `pod.go` and `funcs_fd_planning.go` (sets `cons.Drive/ComputeCpuPolicy`) both feed it |
| Create/grow drive containers | `internal/controllers/wekacluster/steps_planner_apply.go` | `buildPlannerDriveContainers`, `applyPlannerDriveGrowth`, `applyPlannerComputeGrowth` — SHARED by clusterCapacity and daemonset; the two differ only at inline `switch mode` points |
| Per-FD sizing & pool math | `internal/capacityplanner/planner.go` | `planPoolUniformIncrease`, `maxPerFdCap = desiredRaw/minFd`; `RatioFromCaps` in `internal/capacityplanner/ratio.go` |
| Fresh greenfield placement | `internal/capacityplanner/planner.go` | `planPoolFreshUniform` → `selectUniform` → `pickPreferringColocated` |
| Infeasibility report + fix tips | `internal/capacityplanner/infeasibility.go` | `InfeasibilityReport`, `setInfeasible`, per-cause `fixes*` catalog (reused by the `ClusterCapacityInfeasible` event + the weka-capacity CLI) |
| Device allocation | `internal/.../allocator/container_allocator.go` | `allocateSharedDrivesByCapacityWithTypes` (~550), `buildDriveCapacityMap` (~477-515), per-type maps (~679-691), no-TLC error (~680-682), VD CapacityGiB (~894-900) |
| Device typing (QLC/TLC) | `allocator/node_info.go` (~70-91), `internal/pkg/domain/drives.go` (~87-92) | from node annotation `weka.io/weka-shared-drives` |
| Re-alloc trigger | `internal/controllers/wekacontainer/funcs_getters.go` | `NeedsDrivesToAllocate` (~218-221) — capacity-based |
| Actual-drive capacity (correct view) | `internal/controllers/wekacontainer/funcs_drives.go` | `checkDriveResourceFeasibility` (~76-83) sums `Status.Allocations.VirtualDrives` by type |

## Detail docs (split out to stay navigable)

- [wekacluster-drive-colocation.md](wekacluster-drive-colocation.md) — TLC+QLC co-location + the
  **INVARIANT** (no in-place add to an existing container unless `enableDynamicDriveScalingForSharedDrives`).
- [wekacluster-drive-sizing.md](wekacluster-drive-sizing.md) — increase-path new-FD sizing (FEWEST FDs).
- [wekacluster-drive-capacity-accounting.md](wekacluster-drive-capacity-accounting.md) — intent-vs-reality
  + known accounting defect.
