# Drive-Sharing Virtual Drives Allocation Calculator

Interactive browser tool that replicates the operator's virtual drive allocation logic in JavaScript.

## What It Does

Lets you experiment with drive-sharing configurations and instantly see how virtual drives would be distributed across physical NVMe drives. Supports both allocation modes:

- **Capacity-based**: Splits total capacity by TLC/QLC ratio, generates strategies (even distribution, then fit-to-physical fallback), and places virtual drives using greedy most-available-first placement.
- **Fixed drives (TLC-only)**: Allocates a fixed number of virtual drives with uniform capacity using round-robin distribution.

## Usage

```bash
git clone <repo-url>
open scripts/drive-sharing/index.html
```

Or simply open `scripts/drive-sharing/index.html` in any browser (Chrome, Firefox, Safari) — works directly via `file://`, no build step or web server required.

1. **Configure physical drives** — add/remove rows or pick a preset (e.g. "4x TLC 4TB").
2. **Choose allocation mode** — "Capacity-based" for TLC/QLC ratio splitting, or "Fixed Drives" for uniform TLC-only allocation.
3. **Set parameters** — container capacity, drive cores, TLC/QLC ratio (capacity mode) or number of drives and per-drive capacity (fixed mode).
4. **Click Calculate** — results appear instantly: summary cards, colored bar visualization per physical drive, virtual drives table, and a detailed allocation log showing each strategy attempted.

Expand **Advanced Settings** to adjust max virtual drives per core or toggle the per-type vs combined minimum drives constraint.

## How It Was Created

The JavaScript allocation logic was ported line-by-line from the Go source, preserving integer division (`Math.floor`), the bubble-down re-sort in `tryAllocateStrategy`, and the iterative TLC/QLC min-drives loop. Results were validated against the Go test cases in `drive_allocation_strategies_test.go`.

## Testing and Validation

Core functions (`distributeEvenly`, `getTlcQlcCapacity`, `generateStrategies`, `tryAllocateStrategy`, full `allocateByCapacity` pipeline) were extracted and run through Node.js against the expected outputs from the Go test suite. Scenarios validated:

| # | Scenario | Expected | Status |
|---|----------|----------|--------|
| 1 | 4x TLC 4000 GiB, capacity=6000, ratio 1:0, cores=3 | 3 VDs of 2000 GiB | Pass |
| 2 | Even distribution remainders (4000/3, 5000/3) | [1334,1333,1333], [1667,1667,1666] | Pass |
| 3 | Mixed TLC/QLC, enforce=true, 2x TLC + 2x QLC 10TB, cap=12000, 1:1, cores=6 | 6 TLC + 6 QLC | Pass |
| 4 | Combined constraint (enforce=false), ratio 1:5, cap=3000, cores=6 | 1 TLC + 5 QLC | Pass |
| 5 | Heterogeneous (20TB + 2x 500GB), cap=21000, cores=3 | Fit-to-physical [20000, 500, 500] | Pass |
| 6 | Fixed drives mode, 6 TLC 5000 GiB, numDrives=6, driveCap=500 | 6 x 500 GiB | Pass |
| 7 | MinChunkSizeGiB constraint, cap=1000, cores=3 | Rejected (333 < 384) | Pass |

These match the corresponding test cases in `drive_allocation_strategies_test.go`: `TestAllocationStrategyGenerator_EvenDistribution`, `TestEnforceMinDrivesPerTypePerCore`, `TestAllocationStrategyGenerator_FitToPhysicalFallback`, `TestAllocateSharedDrivesByDrivesNum`, and `TestAllocationStrategyGenerator_MinChunkConstraint`.

## Source Files

These are the operator source files the calculator is based on. Consult them when updating the tool:

### Allocation Logic
- `internal/controllers/allocator/drive_allocation_strategies.go` — `distributeEvenly`, `yieldEvenDistributionStrategies`, `yieldFitToPhysicalStrategies`, `MinChunkSizeGiB` constant
- `internal/controllers/allocator/container_allocator.go` — `tryAllocateStrategy`, `filterAndSortUsableDrives`, `allocateSingleDriveType`, `allocateSharedDrivesByCapacityWithTypes`, `allocateSharedDrivesByDrivesNum`, `buildDriveCapacityMap`
- `internal/controllers/allocator/errors.go` — `InsufficientDriveCapacityError`, `InsufficientDrivesError` (error message formats)

### Types and Config
- `pkg/weka-k8s-api/api/v1alpha1/wekacluster_types.go` — `GetTlcQlcCapacity`, `DriveTypesRatio`
- `internal/config/env.go` — `DriveSharingConfig` struct (`MaxVirtualDrivesPerCore`, `EnforceMinDrivesPerTypePerCore`)

### Tests (for validation)
- `internal/controllers/allocator/drive_allocation_strategies_test.go` — comprehensive test cases covering even distribution, fit-to-physical, per-type vs combined constraints, mixed TLC/QLC, reallocation, and fixed-drives mode

### Documentation
- `doc/operator/operations/drive-sharing.md` — operator drive-sharing documentation
- `doc/examples/drive-sharing/` — example WekaCluster manifests
