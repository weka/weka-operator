# Capacity accounting: intent and allocated drives

Detail of [capacity planning](wekacluster-drive-planning.md).

## Two capacity views

- **Intent**: `inventory.DriveContainerCapacities` in
  `internal/capacityplanner/inventory/collect.go` derives TLC/QLC from
  `Spec.ContainerCapacity` and `Spec.DriveTypesRatio` via `GetTlcQlcCapacity`
  (with a legacy per-drive-capacity path).
- **Allocated drives**: `Status.Allocations.VirtualDrives[].CapacityGiB`, grouped
  by `.Type`; inspect `checkDriveResourceFeasibility` in
  `internal/controllers/wekacontainer/funcs_drives.go`.

## Current caveat

`summarizeDriveContainers` and `steadyStatePlan` in
`internal/controllers/wekacluster/funcs_fd_planning.go` sum `curTlc`/`curQlc` from
spec-derived intent, not from allocated virtual drives. If a TLC drive allocation fails
(realloc error swallowed by `ContinueOnError: true`), the phantom TLC still counts, so
`curTlc == desiredTlc`, `steadyStatePlan` skips node inventory, and the cluster stays
silently short of capacity; nothing retries, so it never self-heals.

Symptom: a drive container advertises `T/Q` capacity that is absent from
`weka cluster drive`, shows `VirtualDrivesAdded=True` with a single QLC vdrive, and has
`processes N/N/N+1` (idle TLC core). Spec-derived capacity output alone does not prove
allocation succeeded; compare it with virtual-drive status and Weka's drive list.

Fix direction: derive `curTlc`/`curQlc` from `Status.Allocations.VirtualDrives` (reuse
`checkDriveResourceFeasibility`) and revisit `ContinueOnError` on drive realloc.
