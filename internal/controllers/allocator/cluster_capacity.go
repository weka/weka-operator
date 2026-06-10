package allocator

import (
	globalconfig "github.com/weka/weka-operator/internal/config"
)

// cluster_capacity.go holds the shared capacity helpers reused by the clusterCapacity planner
// (capacity_planner.go): raw-capacity conversion, ratio parts, and the per-FD imbalance check.

// CapacityConstraintsFromConfig builds the planner/feasibility constraints from global config. Shared
// by the cluster-level planner and the container-level pre-add feasibility check so both use identical
// per-core capacity caps and drive-pod resource coefficients.
func CapacityConstraintsFromConfig() *CapacityConstraints {
	cfg := globalconfig.Config.ClusterCapacity
	imbalance := cfg.ImbalanceFactor
	if imbalance <= 0 {
		imbalance = 8.0
	}
	// Compute hugepage ratios mirror ComputeCapacityBasedHugepages' defaults when unset.
	tlcRatio := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	if tlcRatio == 0 {
		tlcRatio = 1000
	}
	qlcRatio := globalconfig.Config.DriveSharing.HugepagesQlcRatio
	if qlcRatio == 0 {
		qlcRatio = 6000
	}
	return &CapacityConstraints{
		TlcCapacityPerCoreGiB:    cfg.TlcCapacityPerCoreGiB,
		QlcCapacityPerCoreGiB:    cfg.QlcCapacityPerCoreGiB,
		MinChunkSizeGiB:          MinChunkSizeGiB,
		ImbalanceFactor:          imbalance,
		HugepagesPerCoreMiB:      HugepagesPerCoreMiB,
		MemoryBaseMiB:            MemoryBaseMiB,
		MemoryPerCoreMiB:         MemoryPerCoreMiB,
		MaxComputeCoresPerNode:   cfg.MaxComputeCoresPerNode,
		ComputeHugepagesTlcRatio: tlcRatio,
		ComputeHugepagesQlcRatio: qlcRatio,
		ComputeMaxHugepagesMiB:   globalconfig.Config.ComputeMaxHugepagesMiB,
		// enableDynamicDriveScalingForSharedDrives gates in-place growth of existing containers. When
		// disabled, the planner creates new containers instead of extending existing ones (see
		// CapacityConstraints.AllowInPlaceGrowth).
		AllowInPlaceGrowth: globalconfig.Config.DriveSharing.EnableDynamicDriveScaling,
	}
}

// NodeCapacity summarizes one candidate node's shared-drive inventory and the resources available
// for weka drive containers on it — all NET of capacity/cores/hugepages/memory already consumed by
// other clusters AND by this cluster's own containers (so the planner treats these as pure remaining
// headroom). Callers build this from the node's weka-shared-drives annotation and Status.Allocatable
// (see allocator.NewK8sNodeInfoGetter / SharedDriveInfo).
type NodeCapacity struct {
	NodeName string
	TlcGiB   int
	QlcGiB   int
	// AllocatableCPU is the cores available to this cluster on the node.
	AllocatableCPU int
	// AvailableHugepagesMiB / AvailableMemoryMiB are the hugepages and RAM available to this cluster
	// on the node (node Allocatable minus other/own clusters' requests).
	AvailableHugepagesMiB int
	AvailableMemoryMiB    int
	// FDValue is the node's failure-domain key: the resolved label value in label-based mode, or the
	// node name in AUTO mode (FD = host). The planner groups and balances capacity by this key.
	FDValue string
}

// RawCapacityGiB converts a usable cluster-capacity target into raw capacity including parity
// and hot-spare overhead: raw = usable * (sw+rl+hs) / sw.
func RawCapacityGiB(clusterCapGiB, sw, rl, hs int) int {
	return clusterCapGiB * (sw + rl + hs) / sw
}

// imbalanceWarnPercent is the per-FD capacity skew (larger vs smaller) above which the planner warns:
// WEKA usable capacity is gated by the smallest FD, so a skew beyond this wastes capacity.
const imbalanceWarnPercent = 10

// imbalanceExceeds reports whether the larger of two per-FD capacities exceeds the smaller by more
// than imbalanceWarnPercent. Callers pass lo <= hi.
func imbalanceExceeds(lo, hi int) bool {
	return lo > 0 && hi*100 > lo*(100+imbalanceWarnPercent)
}
