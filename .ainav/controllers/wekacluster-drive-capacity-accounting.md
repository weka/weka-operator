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
`internal/controllers/wekacluster/funcs_fd_planning.go` use spec-derived capacity
for eligible healthy containers. If drive allocation falls short while the container
still qualifies, intended capacity can cover the target and skip node inventory
without the corresponding virtual drives existing. Spec-derived capacity output
alone does not prove allocation succeeded.

When investigating a shortfall, compare the spec's TLC/QLC split with virtual-drive
status and Weka's drive list, and inspect allocation errors and eligibility checks.
A correction would need to reconcile desired capacity, actual allocations and
pending growth without double-counting reservations.
