package capacityplanner

import "sort"

// nodecapacity.go holds the NodeCapacity inventory type and the small helpers that read it, shared by
// the capacity planner and inventory collection.

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
	// IneligibleReason is why this node cannot receive a new container right now (cordoned/not
	// ready/untolerated taint), "" when it can — see resources.NodeIneligibleReason. Existing containers on
	// the node still count against capacity; only new placement must skip it, since a node-pinned leg has no
	// re-plan path and would otherwise be re-picked the moment a stuck container on it is reaped.
	IneligibleReason string
}

// SortDriveCapacitiesDesc returns a largest-first copy of capacities (never mutates the input). Shared by
// inventory's display narration and the act-as-daemonset sizing, so both stay byte-for-byte the same order
// — and it is what makes a numDrives pin take a node's largest drives.
func SortDriveCapacitiesDesc(capacities []int) []int {
	if len(capacities) == 0 {
		return nil
	}
	out := make([]int, len(capacities))
	copy(out, capacities)
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}
