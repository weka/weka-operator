package allocator

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"

	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
	"github.com/weka/weka-operator/internal/controllers/utils"
)

// cluster_capacity.go holds the operator-side adapters that build the pure planner's inputs from
// global config. The pure helpers (RawCapacityGiB, CapacityShort, CapacityCoverTarget, the
// NodeCapacity view and DefaultConstraints) live in internal/capacityplanner.

// CapacityConstraintsFromConfig builds the planner/feasibility constraints from global config. Shared
// by the cluster-level planner and the container-level pre-add feasibility check so both use identical
// per-core capacity caps and drive-pod resource coefficients.
func CapacityConstraintsFromConfig() *capacityplanner.CapacityConstraints {
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
	// Taken verbatim: env.go's 0.2 default fills unset env vars, so an explicit 0 here is meaningful
	// (0 growth-fraction = always allow in-place grow; 0 over-provision = never overshoot desiredRaw).
	minGrowthFraction := globalconfig.Config.DriveSharing.MinGrowthFraction
	maxOverProvisionFraction := globalconfig.Config.DriveSharing.MaxOverProvisionFraction
	// Negative means genuinely unset, so fall back to documented defaults; 0 is a meaningful
	// "disable this ratio term" value (see CapacityConstraints doc comment).
	computeToTlcDriveCoreRatio := globalconfig.Config.CapacityPlanner.ComputeToTlcDriveCoreRatio
	if computeToTlcDriveCoreRatio < 0 {
		computeToTlcDriveCoreRatio = 1.0
	}
	computeToQlcDriveCoreRatio := globalconfig.Config.CapacityPlanner.ComputeToQlcDriveCoreRatio
	if computeToQlcDriveCoreRatio < 0 {
		computeToQlcDriveCoreRatio = 0.0
	}
	fullDrivesComputeToDriveCoreRatio := globalconfig.Config.CapacityPlanner.FullDrivesComputeToDriveCoreRatio
	if fullDrivesComputeToDriveCoreRatio < 0 {
		fullDrivesComputeToDriveCoreRatio = 2.0
	}
	return &capacityplanner.CapacityConstraints{
		TlcCapacityPerCoreGiB: cfg.TlcCapacityPerCoreGiB,
		QlcCapacityPerCoreGiB: cfg.QlcCapacityPerCoreGiB,
		MinChunkSizeGiB:       MinChunkSizeGiB,
		ImbalanceFactor:       cfg.ImbalanceFactor, // env defaults to 8.0; <= 0 disables the heterogeneous fallback
		HugepagesPerCoreMiB:   capacityplanner.HugepagesPerCoreMiB,
		MemoryBaseMiB:         capacityplanner.MemoryBaseMiB,
		MemoryPerCoreMiB:      capacityplanner.MemoryPerCoreMiB,
		MaxCoresPerContainer:  globalconfig.Config.CapacityPlanner.MaxCoresPerContainer,
		// Without this floor, auto-full-drives sizes the fewest compute containers that carry the required cores
		// and the cluster never forms; cluster_min_containers can't catch it since the count is derived.
		MinComputeContainers:              globalconfig.Consts.FormClusterMinComputeContainers,
		ComputeHugepagesTlcRatio:          tlcRatio,
		ComputeHugepagesQlcRatio:          qlcRatio,
		ComputeMaxHugepagesMiB:            globalconfig.Config.ComputeMaxHugepagesMiB,
		ComputeToTlcDriveCoreRatio:        computeToTlcDriveCoreRatio,
		ComputeToQlcDriveCoreRatio:        computeToQlcDriveCoreRatio,
		FullDrivesComputeToDriveCoreRatio: fullDrivesComputeToDriveCoreRatio,
		// Disabled: planner creates new containers instead of extending existing ones.
		AllowInPlaceGrowth:       globalconfig.Config.DriveSharing.EnableDynamicDriveScaling,
		MinGrowthFraction:        minGrowthFraction,
		MaxOverProvisionFraction: maxOverProvisionFraction,
		CapacityDeadbandFraction: cfg.CapacityDeadbandFraction,
		// AllowSingleParity flows through the constraints struct now so the pure planner's
		// MinProtectionFloor no longer reads the globalconfig singleton directly.
		AllowSingleParity: globalconfig.Config.DriveSharing.AllowSingleParity,
	}
}

// ConstraintsForClusterSpec builds CapacityConstraintsFromConfig() and layers the three cluster-derived
// overrides (per-role DPDK base memory, CpuPolicy) on top, so the planner's node-fit gate reserves
// hugepages/CPU exactly as the scheduler will for containers built from this spec.
func ConstraintsForClusterSpec(spec *weka.WekaClusterSpec) *capacityplanner.CapacityConstraints {
	return ApplyClusterSpecOverrides(CapacityConstraintsFromConfig(), spec)
}

// ApplyClusterSpecOverrides layers the same three cluster-derived overrides onto an existing
// CapacityConstraints in place, for callers (weka-capacity's plan command) whose base constraints
// come from somewhere other than CapacityConstraintsFromConfig() (e.g. CLI --constraint overrides via
// loadConstraints) and so can't start from ConstraintsForClusterSpec without discarding them.
func ApplyClusterSpecOverrides(cons *capacityplanner.CapacityConstraints, spec *weka.WekaClusterSpec) *capacityplanner.CapacityConstraints {
	cons.DriveDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(spec, weka.WekaContainerModeDrive)
	cons.ComputeDpdkPerCoreMiB = utils.GetDpdkBaseMemoryMbByRole(spec, weka.WekaContainerModeCompute)
	cons.CpuPolicy = spec.CpuPolicy
	return cons
}

// MinProtectionFloor returns the smallest stripe width, redundancy level and hot spare a cluster may
// be configured with, reading the operator-level single-parity flag from global config so validation
// call sites do not have to thread it through themselves.
func MinProtectionFloor() (stripeWidth, redundancyLevel, hotSpare int) {
	return capacityplanner.MinProtectionFloor(globalconfig.Config.DriveSharing.AllowSingleParity)
}
