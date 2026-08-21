# Drivers-dist Container Sizing

How `spec.resources`, `driverDistPayload.distResources` and `additionalMemory` reach a pod.

`EnsureDistContainer` rewrites the dist WekaContainer spec wholesale every policy interval, so it
carries `resources`/`additionalMemory`/`hugepages`/`hugepagesSize` over from the existing object —
otherwise a direct patch is reverted within a minute. `driverDistPayload.distResources` wins when
set. Consequence: clearing `distResources` does NOT revert the container (the carried value is the
policy's own output); deleting the container is the reset path.

Sizing lands in `resources/pod.go` `setResources`. Drivers containers start at 500m/2000m CPU and
3000M memory, plus `additionalMemory` (MiB, via quantity arithmetic so the decimal baseline holds).

`applyResourcesOverride` applies `spec.resources` last, for EVERY mode and cpuPolicy — not just
drivers. Zero means "keep computed", so `resources: {}` equals unset. Request and limit are written
as a PAIR; naming one side alone could invert them against the computed other side, which kubelet
rejects. Running for every mode also means it overrides planner-derived CPU and fills the adhoc-op
branch's deliberately empty Limits — accepted: naming a resource means owning it.

Hugepages here are 2Mi-only, and `GetHugePagesDetails` lets `spec.resources` stand in for
`spec.hugepages` (this is what lets drivers-dist request any at all), keeping the pod request and
emptyDir medium consistent — but only on a 2Mi container. On a `1Gi` one it is a different
resource, so both are requested. Either side of the pair may carry it; kubelet requires equality.
Note the planner still charges `spec.Hugepages` (`inventory/collect.go`), so an override drifts
from what it reserves.
