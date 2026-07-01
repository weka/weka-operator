# WekaCluster Drive Capacity Planning

Navigation map for the drive-capacity planner: how cluster TLC/QLC targets are
decided, turned into per-node drive containers, and allocated to physical
devices. Nav aid only — code is source of truth; line numbers are hints.

## Flow (cluster → node → device)

```
WekaClusterReconcile
  EnsureWekaContainers → BuildMissingContainers → buildClusterCapacityDriveContainers
    planClusterCapacity
      steadyStatePlan          # fast path: if cur≈desired, "skipping node inventory"
      buildNodeInventory       # per-node physical capacity from shared-drives annotation
      allocator.planPoolUniformIncrease   # size per-FD, split TLC/QLC pools
  steps_cluster_creation: create/grow drive containers (writes ContainerCapacity + DriveTypesRatio)
  wekacontainer AllocateSharedDrives → allocator.allocateSharedDrivesByCapacityWithTypes
    → Status.Allocations.VirtualDrives (the ACTUAL drives)
```

## Key files & functions

| Concern | File | Symbols (approx line) |
|---|---|---|
| Steady-state / cluster plan | `internal/controllers/wekacluster/funcs_fd_planning.go` | `steadyStatePlan` (~420-455), `summarizeDriveContainers` (~364-379), `driveContainerCapacities` (~346-351), `buildNodeInventory` (~554-573), pool split (~63) |
| Create/grow drive containers | `internal/controllers/wekacluster/steps_cluster_creation.go` | create (~401-402), `applyClusterCapacityGrowth` (~414-445) |
| Per-FD sizing & pool math | `internal/.../allocator/capacity_planner.go` | `planPoolUniformIncrease`, `maxPerFdCap = desiredRaw/minFd`, `RatioFromCaps` (~526-527) |
| Device allocation | `internal/.../allocator/container_allocator.go` | `allocateSharedDrivesByCapacityWithTypes` (~550), `buildDriveCapacityMap` (~477-515), per-type maps (~679-691), no-TLC error (~680-682), VD CapacityGiB (~894-900) |
| Device typing (QLC/TLC) | `allocator/node_info.go` (~70-91), `internal/pkg/domain/drives.go` (~87-92) | from node annotation `weka.io/weka-shared-drives` |
| Re-alloc trigger | `internal/controllers/wekacontainer/funcs_getters.go` | `NeedsDrivesToAllocate` (~218-221) — capacity-based |
| Actual-drive capacity (correct view) | `internal/controllers/wekacontainer/funcs_drives.go` | `checkDriveResourceFeasibility` (~76-83) sums `Status.Allocations.VirtualDrives` by type |

## Capacity: intent vs reality (critical distinction)

- **Intent** = `Spec.ContainerCapacity × Spec.DriveTypesRatio` (via `GetTlcQlcCapacity`
  in `wekacluster_types.go` ~325-333). This is what the planner's `curTlc/curQlc`
  currently sum from.
- **Reality** = `Status.Allocations.VirtualDrives[].CapacityGiB` grouped by `.Type`.
- `printer.capacity` "T/Q …" is derived from **intent** (the ratio), so it can
  advertise a drive type that was never allocated.

## Known defect (branch `07-01-fix_consider_nodes...`, 2026-07-01)

Planner reads TLC/QLC "current" from **intent**, not **reality**. If a TLC drive
alloc fails (e.g. grow grafts a TLC slice, realloc errors "no TLC drives
available", swallowed by `ContinueOnError:true`), the phantom TLC still counts as
current → `curTlc==desiredTlc` → `steadyStatePlan` skips node inventory → cluster
is silently short and never self-heals. Symptom: drive container `T/Q` capacity
that's absent from `weka cluster drive`; container shows `VirtualDrivesAdded=True`
with a single QLC vdrive and `processes N/N/N+1` (idle TLC core).

Fix direction: compute `curTlc/curQlc` from `Status.Allocations.VirtualDrives`
(reuse `checkDriveResourceFeasibility`); revisit `ContinueOnError` on drive
realloc; clamp per-node `TlcGiB+QlcGiB` to physical device capacity in planning.
