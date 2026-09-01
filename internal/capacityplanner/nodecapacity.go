package capacityplanner

import "sort"

// nodecapacity.go holds the NodeCapacity inventory type and the small helpers that read it, shared by
// the auto-full-drives planner and inventory collection.

// NodeCapacity is one candidate node's inventory and available resources, all NET of what's already
// consumed by other clusters and this cluster's own containers (pure remaining headroom).
type NodeCapacity struct {
	NodeName string
	TlcGiB   int
	QlcGiB   int
	// AllocatableCPU is physical CPU, not a weka data-core count. See cpu.go.
	AllocatableCPU int
	// IsHt / FullPcpusOnly: CPU topology used to convert data cores to physical CPU.
	IsHt                  bool
	FullPcpusOnly         bool
	AvailableHugepagesMiB int
	AvailableMemoryMiB    int
	// FDValue is the failure-domain key: label value in label-based mode, else the node name (auto mode).
	FDValue string
	// HasDeletingDriveContainer: node still runs a this-cluster drive container pending deletion —
	// excluded from existingDrives but still charged; deprioritizes but doesn't exclude fresh placement.
	HasDeletingDriveContainer bool
	// HasDeletingComputeContainer: node runs a this-cluster compute container pending deletion. Its pod still
	// holds hugepages, so a drive growth on this node can fail a fit it would pass once the deletion lands —
	// deferred rather than infeasible, since weka may need more active compute elsewhere before it will
	// deactivate that container.
	HasDeletingComputeContainer bool
	// DriveCapacitiesGiB: per-drive GiB of each FREE full drive, net of own auto-full-drives allocation
	// (OwnDriveCapacitiesGiB). Populated only by inventory.FullDrivesInventory; nil for shared-drives nodes.
	DriveCapacitiesGiB []int
	// OwnDriveCapacitiesGiB: per-drive GiB already allocated to this cluster's own drive container;
	// excluded from DriveCapacitiesGiB, so total drives = len(Own)+len(DriveCapacitiesGiB).
	OwnDriveCapacitiesGiB []int
	// IneligibleReason is why this node cannot receive a new container right now (cordoned/not
	// ready/untolerated taint), "" when it can — see resources.NodeIneligibleReason. Existing containers on
	// the node still count against capacity; only new placement must skip it, since a node-pinned leg has no
	// re-plan path and would otherwise be re-picked the moment a stuck container on it is reaped.
	IneligibleReason string
}

// sumInts totals a []int (e.g. a NodeCapacity slice's aggregate GiB value).
func sumInts(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// SortDriveCapacitiesDesc returns a largest-first copy of capacities (never mutates the input). Shared by
// full-drives sizing and inventory's display narration, so both stay byte-for-byte the same order — and it
// is what makes a numDrives pin take a node's largest drives.
func SortDriveCapacitiesDesc(capacities []int) []int {
	if len(capacities) == 0 {
		return nil
	}
	out := make([]int, len(capacities))
	copy(out, capacities)
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// physicalCPUCost delegates to planner.go's cpuCostShared (same formula as nodeState.cpuCost), taking a
// NodeCapacity directly since full-drives mode only needs single-container-per-node CPU/hugepages/memory math.
func physicalCPUCost(node *NodeCapacity, dataCores int, cons *CapacityConstraints, includeBase bool) int {
	return cpuCostShared(NodeCPUTopology{IsHt: node.IsHt, FullPcpusOnly: node.FullPcpusOnly}, cons.CpuPolicy, dataCores, includeBase)
}

// physicalCPUToDataCores delegates to planner.go's dataCoresCapacityShared for how many data cores fit in
// the node's remaining CPU plus extraCPU already charged to a container the caller keeps hosting.
// includeBase reserves the per-container management core, for a new container.
func physicalCPUToDataCores(node *NodeCapacity, extraCPU int, cons *CapacityConstraints, includeBase bool) int {
	return dataCoresCapacityShared(NodeCPUTopology{IsHt: node.IsHt, FullPcpusOnly: node.FullPcpusOnly}, cons.CpuPolicy, node.AllocatableCPU, extraCPU, includeBase)
}
