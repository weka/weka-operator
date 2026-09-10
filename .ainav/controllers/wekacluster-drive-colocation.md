# TLC/QLC co-location and in-place growth

Detail of [capacity planning](wekacluster-drive-planning.md).
Source: `internal/capacityplanner/planner.go`.

## Fresh mixed containers

When both pools need new failure domains in the same plan, prefer one fresh mixed
container on an empty or freed node. Co-location does not convert an occupied
single-pool container.

- `PlanCapacity` plans the pool with fewer physically capable nodes first
  (`countPoolCapableNodes`). Ties and single-pool plans start with TLC.
- `otherPoolPreferNodes` considers only pending `NewContainer` placements from
  this plan, allowing the second pool to join the first pool's fresh placement.
- Greenfield: `planPoolFreshUniform` → `selectUniform` → `pickPreferringColocated`.
  The count and target fit stay the same; eligible co-located candidates rank first.
- Increase: `planPoolUniformIncrease` uses `colocatedFirst` and
  `takeFreshAtLevel`, preferring co-located, then not-deleting, then deleting
  candidates. A freed mixed node can therefore win even while its old container
  is terminating.
- Separate placement remains necessary when drive types or shared CPU, hugepages
  or memory prevent a mixed fit. Delete/recreate co-locates only if both pools
  are short in the same plan; separated deletions can recreate separate containers.

## In-place growth gate

Existing containers can grow or gain a drive type only when
`enableDynamicDriveScalingForSharedDrives` sets `cons.AllowInPlaceGrowth`.
The environment default is false (`ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES`
in `internal/config/env.go`). With the flag off:

- `freshExclusion` returns `allDriveNodes`, excluding occupied nodes from fresh placement.
- `planPoolUniformIncrease` rejects its growth branch.
- `planPoolExplicit` rejects pinned per-FD capacity that would require existing containers to grow.

Consequently `plan.Grow` stays empty. See [increase-path sizing](wekacluster-drive-sizing.md)
for how new containers cover a shortfall.
