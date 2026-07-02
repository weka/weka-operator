# TLC+QLC Co-location & the in-place-growth invariant

Detail of [wekacluster-drive-planning.md](wekacluster-drive-planning.md). Code is source of truth.

## Co-location

Goal: when BOTH pools need new FDs in the SAME plan, place both on ONE node as a single FRESH mixed
container. Co-location is realized ONLY as a fresh mixed container on an EMPTY/freed node — it NEVER
converts an already-occupied single-pool container (that would be in-place growth; see the invariant below).

- **Pool ordering** (`PlanCapacity`): plan the more spatially-CONSTRAINED pool first — fewer
  physically-capable nodes (`countPoolCapableNodes`, counts `nc.TlcGiB>0` / `nc.QlcGiB>0`). The flexible
  pool then co-locates onto the constrained pool's fresh node. Tie / single pool → TLC first.
- **Bias signal**: `otherPoolPreferNodes(p, newByNode)` = nodes whose PENDING same-plan `NewContainer`
  already carries the other pool. Pending-only (NOT existing containers) — so it only ever merges two
  fresh placements into one mixed container, never adds to an occupied node. Empty for the pool planned
  first → no-op.
- **Greenfield** (`planPoolFreshUniform`→`selectUniform`→`pickPreferringColocated`): (N,target) fit
  unchanged; only which N FDs get filled flips co-located-first among candidates clearing `target`.
- **Increase** (`planPoolUniformIncrease`): `colocatedFirst` tags/floats co-located `freshGroups`;
  `takeFreshAtLevel` tiers **colocated → not-deleting → deleting** (co-location is PRIMARY, above the
  not-deleting preference). Matters when you delete a QLC-only + a TLC-only together: the freed mixed
  node's old container may still be terminating (`HasDeletingDriveContainer=true`) while a freed TLC-only
  node already finalized — co-location must still pick the freed mixed node, else it splits.
- **Fallback to SPLIT** when no co-located node can hold both (disjoint type nodes, or shared
  cores/hugepages/memory exhausted). Delete-recreate co-locates only if both pools go short in the same
  plan; if their deletions are far apart, one recreates alone (no conversion → may split).

## INVARIANT: no in-place TLC/QLC add to an existing container unless the flag is set

The planner never grows/converts an existing wekacontainer (adds a drive type to it) unless
`enableDynamicDriveScalingForSharedDrives` (→ `cons.AllowInPlaceGrowth`) is true. Enforcement:
- `freshExclusion` returns `allDriveNodes` when the flag is off → bars ALL occupied nodes from fresh
  placement, so `placeUniform` only ever hits its CREATE path (empty nodes).
- `planPoolUniformIncrease` grow branch is gated (`if !AllowInPlaceGrowth {infeasible}`).
- `planPoolExplicit` (pinned `driveContainers`) is gated: infeasible if any existing FD is below the
  pinned per-FD level T (would need growth).

So `plan.Grow` is always empty when the flag is off. Default is OFF
(`env.go` `ENABLE_DYNAMIC_DRIVE_SCALING_FOR_SHARED_DRIVES`=false).
