package allocator

import (
	globalconfig "github.com/weka/weka-operator/internal/config"
)

// cluster_capacity.go holds the operator-side adapter that builds the pure planner's
// CapacityConstraints from global config. The pure helpers (RawCapacityGiB, CapacityShort,
// CapacityCoverTarget, the NodeCapacity view and DefaultConstraints) now live in
// internal/capacityplanner; the types/functions referenced here resolve through the re-export
// shims in capacityplanner_shim.go.

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
		// AllowSingleParity flows through the constraints struct now so the pure planner's
		// MinProtectionFloor no longer reads the globalconfig singleton directly.
		AllowSingleParity: globalconfig.Config.DriveSharing.AllowSingleParity,
	}
}
