# Capacity accounting: intent vs reality (+ known defect)

Detail of [wekacluster-drive-planning.md](wekacluster-drive-planning.md). Code is source of truth.

## Intent vs reality (critical distinction)

- **Intent** = `Spec.ContainerCapacity × Spec.DriveTypesRatio` (via `GetTlcQlcCapacity` in
  `wekacluster_types.go` ~325-333). This is what the planner's `curTlc/curQlc` currently sum from.
- **Reality** = `Status.Allocations.VirtualDrives[].CapacityGiB` grouped by `.Type`.
- `printer.capacity` "T/Q …" is derived from **intent** (the ratio), so it can advertise a drive type
  that was never actually allocated.

## Known defect (branch `07-01-fix_consider_nodes...`, 2026-07-01)

Planner reads TLC/QLC "current" from **intent**, not **reality**. If a TLC drive alloc fails (e.g. grow
grafts a TLC slice, realloc errors "no TLC drives available", swallowed by `ContinueOnError:true`), the
phantom TLC still counts as current → `curTlc==desiredTlc` → `steadyStatePlan` skips node inventory →
cluster is silently short and never self-heals. Symptom: drive container `T/Q` capacity that's absent
from `weka cluster drive`; container shows `VirtualDrivesAdded=True` with a single QLC vdrive and
`processes N/N/N+1` (idle TLC core).

Fix direction: compute `curTlc/curQlc` from `Status.Allocations.VirtualDrives` (reuse
`checkDriveResourceFeasibility`); revisit `ContinueOnError` on drive realloc; clamp per-node
`TlcGiB+QlcGiB` to physical device capacity in planning.
