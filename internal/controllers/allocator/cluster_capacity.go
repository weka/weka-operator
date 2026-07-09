package allocator

import (
	"math"

	globalconfig "github.com/weka/weka-operator/internal/config"
)

// cluster_capacity.go holds the shared capacity helpers reused by the clusterCapacity planner
// (capacity_planner.go): raw-capacity conversion, ratio parts, and the per-FD imbalance check.

// CapacityConstraintsFromConfig builds the planner/feasibility constraints from global config. Shared
// by the cluster-level planner and the container-level pre-add feasibility check so both use identical
// per-core capacity caps and drive-pod resource coefficients.
func CapacityConstraintsFromConfig() *CapacityConstraints {
	cfg := globalconfig.Config.ClusterCapacity
	// Compute hugepage ratios mirror ComputeCapacityBasedHugepages' defaults when unset.
	tlcRatio := globalconfig.Config.DriveSharing.HugepagesTlcRatio
	if tlcRatio == 0 {
		tlcRatio = 1000
	}
	qlcRatio := globalconfig.Config.DriveSharing.HugepagesQlcRatio
	if qlcRatio == 0 {
		qlcRatio = 6000
	}
	// MinGrowthFraction / MaxOverProvisionFraction are taken verbatim: env.go supplies the 0.2 default
	// when their env vars are unset, so 0 only appears when an operator sets it explicitly and is a
	// meaningful value — 0 growth-fraction means "always allow in-place grow", 0 over-provision means
	// "never overshoot desiredRaw". No in-code coercion (mirrors ImbalanceFactor, where 0 disables).
	minGrowthFraction := globalconfig.Config.DriveSharing.MinGrowthFraction
	maxOverProvisionFraction := globalconfig.Config.DriveSharing.MaxOverProvisionFraction
	return &CapacityConstraints{
		TlcCapacityPerCoreGiB:    cfg.TlcCapacityPerCoreGiB,
		QlcCapacityPerCoreGiB:    cfg.QlcCapacityPerCoreGiB,
		MinChunkSizeGiB:          MinChunkSizeGiB,
		ImbalanceFactor:          cfg.ImbalanceFactor, // env defaults to 8.0; <= 0 disables the heterogeneous fallback
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
		AllowInPlaceGrowth:       globalconfig.Config.DriveSharing.EnableDynamicDriveScaling,
		MinGrowthFraction:        minGrowthFraction,
		MaxOverProvisionFraction: maxOverProvisionFraction,
		CapacityDeadbandFraction: cfg.CapacityDeadbandFraction,
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
	// HasDeletingDriveContainer is true when this node still hosts a this-cluster drive container with a
	// DeletionTimestamp set. Such a container is excluded from existingDrives (so the node re-enters
	// the fresh-candidate pool) yet still charged in the inventory. The flag deprioritizes the node
	// for fresh placement so a replacement FD prefers a node with no deleting container; it is never excluded
	// outright (the node may be the only eligible FD for the pool — e.g. scarce QLC drives).
	HasDeletingDriveContainer bool
}

// RawCapacityGiB converts a usable cluster-capacity target into raw capacity including parity
// and hot-spare overhead, plus WEKA's ~10% usable-capacity reserve:
//
//	raw = usable * (sw+rl+hs) / sw / 0.9
//
// The (sw+rl+hs)/sw factor is the protection overhead; the /0.9 accounts for the portion of raw
// capacity WEKA does not expose as usable (only ~90% is usable after formation). A non-positive
// stripeWidth (no spec value and no Helm default) has no meaningful overhead ratio, so return 0
// rather than panic; admission (clusterCapacityProtection) rejects such a scheme before formation.
func RawCapacityGiB(clusterCapGiB, sw, rl, hs int) int {
	if sw <= 0 {
		return 0
	}
	// All-float arithmetic so neither the fractional (sw+rl+hs)/sw overhead nor the /0.9 usable
	// reserve is truncated by integer division before scaling.
	return int(float64(clusterCapGiB) * float64(sw+rl+hs) / float64(sw) / 0.9)
}

// CapacityShort reports whether current is below desired by more than the relative deadband
// (desired × CapacityDeadbandFraction). A fraction <= 0 makes it a strict current < desired check,
// preserving exact-match behavior. The band is rounded up so a sub-1-GiB band never collapses to 0.
func CapacityShort(current, desired int, cons *CapacityConstraints) bool {
	if cons.CapacityDeadbandFraction <= 0 {
		return current < desired
	}
	band := int(math.Ceil(float64(desired) * cons.CapacityDeadbandFraction))
	return desired-current > band
}

// CapacityCoverTarget is the minimum realized capacity that satisfies `desired` without being
// CapacityShort — i.e. `desired` reduced by the shortfall deadband. Placement and feasibility target
// this, not exact `desired`, so a shortfall already within the deadband never forces an extra failure
// domain nor a spurious infeasible verdict. Mirrors CapacityShort's band exactly (not-short iff
// current >= CapacityCoverTarget). A fraction <= 0 returns `desired` (strict exact-match, unchanged).
func CapacityCoverTarget(desired int, cons *CapacityConstraints) int {
	if cons.CapacityDeadbandFraction <= 0 {
		return desired
	}
	band := int(math.Ceil(float64(desired) * cons.CapacityDeadbandFraction))
	return desired - band
}
