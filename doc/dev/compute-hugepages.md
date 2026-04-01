# Compute Hugepages Calculation

**Code:** `internal/controllers/allocator/hugepages.go`, `templates.go`

## Overview

Compute container hugepages are calculated dynamically from cluster drive capacity. The result must be deterministic and reproducible across reconciler runs.

## Entry point

`GetContainerHugepages(role="compute")` → `calculateDynamicComputeHugepages` → `ComputeCapacityBasedHugepages`

If `.spec.dynamic.computeHugepages` is set by the user, it is used as-is (plus DPDK overhead) and the dynamic calculation is skipped.

## Step 1: Determine total raw drive capacity (GiB)

Three paths depending on cluster configuration:

| Condition | Source |
|---|---|
| `containerCapacity > 0` | `containerCapacity × driveContainers` (drive-sharing, capacity per container known) |
| `numDrives > 0 && driveCapacity > 0` | `numDrives × driveCapacity × driveContainers` (drive-sharing, explicit per-drive capacity) |
| full-drives mode (neither of the above) | Read from `weka-full-drives` node annotation via most recently created allocated drive container (see below) |

**Full-drives mode capacity lookup** (`ComputeCapacityFromMostRecentDriveContainerAllocation`):
1. Find all drive `WekaContainer`s where `len(Status.Allocations.Drives) == numDrives`.
2. Pick the most recently created one (by `CreationTimestamp`).
3. GET its node; read `weka.io/weka-full-drives` annotation (`[{"serial":"<SERIAL>","capacity_gib":<GiB>}, ...]`).
4. Sum `capacity_gib` for each serial in `Status.Allocations.Drives` → `perContainerGiB`.
5. `totalRawCapacityGiB = perContainerGiB × numDriveContainers`.

If no drive container with fully allocated drives exists yet, the function returns an error, which blocks compute container creation until at least one drive container has its drives allocated.

## Step 2: Compute hugepages from capacity (`ComputeCapacityBasedHugepages`)

Capacity is split into TLC and QLC portions via `.spec.dynamic.driveTypesRatio` (defaults to 100% TLC).

```
clusterHugepagesMiB = tlcCapGiB × 1024 / tlcRatio
                    + qlcCapGiB × 1024 / qlcRatio
```

Default ratios (configurable via env vars `HUGEPAGES_TLC_RATIO` / `HUGEPAGES_QLC_RATIO`):
- `tlcRatio` = 1000 (1 MiB hugepages per GiB TLC)
- `qlcRatio` = 6000 (1 MiB hugepages per 6 GiB QLC)

Per-compute-container hugepages:

```
capacityBased = clusterHugepagesMiB / computeContainers
perCore       = 1700 × computeCores
minimum       = 3000 × computeCores

hugepages = max(capacityBased + perCore, minimum)
```

Result is rounded up to the nearest even number (2 MiB page alignment). An optional cap is applied if `COMPUTE_MAX_HUGEPAGES_MIB` env var is set.

## Blocking behavior

In full-drives mode, compute container creation is gated: `calculateDynamicComputeHugepages` propagates the error from `ComputeCapacityFromMostRecentDriveContainerAllocation` up through `GetContainerHugepages` → `BuildMissingContainers` → `EnsureWekaContainers`, so no compute containers are created until a drive container reports its drive allocations.
