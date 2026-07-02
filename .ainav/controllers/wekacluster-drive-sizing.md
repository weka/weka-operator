# Increase-path new-FD sizing (FEWEST containers)

Detail of [wekacluster-drive-planning.md](wekacluster-drive-planning.md). Code is source of truth.

`planPoolUniformIncrease` Step 4 covers the delta with the FEWEST new FDs. Iterate count `k` ASCENDING,
place at first feasible:
- `kMin = CeilDiv(delta, maxPerFdCap)` (maxPerFdCap = desiredRaw/minFd) — fewest FDs / largest per-FD.
- `kMax = min(CeilDiv(delta, T0), len(freshGroups))` — caps the FD COUNT at what T0-cloning (the
  smallest existing FD) would use, so we never fragment into MORE FDs than that. Bounds the count, NOT
  the per-FD size: at `k=kMax` the per-FD can dip mildly below `T0` (ceiling rounding — 772 < 939 in
  `Test_UniformIncrease_SmallFreshNodes_StaysFeasible`), intended so small fresh nodes are still usable.
  A delta needing sub-T0 fragments beyond this count is left to grow / infeasible.
- Skip to more/smaller FDs on `detectImbalance` (fresh FD ≥ 2×existing-avg dwarfs existing) or
  `freshCountAtLeast` node scarcity.

Deleting N FDs recreates as FEW as the per-FD ceiling allows, not N+ — e.g. delete 3×700
(delta 2314 > maxPerFdCap 2239) → 2×1157. REPLACED the old even-split/uniform preference (count
anchored on `CeilDiv(delta, T0)`); several increase-path tests now expect fewer/larger FDs
(`Test_Grow_PartialInPlace…` 5×18→3×30, `…ExistingFewerThanMinFd…` 6×45→5×54,
`…DynamicScalingDisabled…` 6×30→4×45). Grow (Step 5) is the fallback when no create-new `k` fits
(and only when `AllowInPlaceGrowth` — see the invariant in the co-location doc).
