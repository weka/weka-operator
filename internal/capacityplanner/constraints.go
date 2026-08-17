package capacityplanner

import (
	"math"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// constraints.go holds pure capacity helpers for the clusterCapacity planner (planner.go).

// Minimum chunk size: 128 GiB × 3 = 384 GiB. Defined in this pure package so both the planner and
// the allocator-side const shim (drive_allocation_strategies.go) share one definition.
const MinChunkSizeGiB = 128 * 3

// DefaultMaxCoresPerContainer is weka's own per-container core limit (19 cores); both planners default
// CapacityConstraints.MaxCoresPerContainer to this.
const DefaultMaxCoresPerContainer = 19

// DefaultConstraints returns a CapacityConstraints with compile-time defaults; the operator layers
// env-derived knobs on top via allocator.CapacityConstraintsFromConfig.
func DefaultConstraints() *CapacityConstraints {
	return &CapacityConstraints{
		MinChunkSizeGiB:                   MinChunkSizeGiB,
		HugepagesPerCoreMiB:               HugepagesPerCoreMiB,
		MemoryBaseMiB:                     MemoryBaseMiB,
		MemoryPerCoreMiB:                  MemoryPerCoreMiB,
		MaxCoresPerContainer:              DefaultMaxCoresPerContainer,
		ComputeToTlcDriveCoreRatio:        1.0,
		ComputeToQlcDriveCoreRatio:        0.0,
		FullDrivesComputeToDriveCoreRatio: 2.0,
		CpuPolicy:                         weka.CpuPolicyAuto,
	}
}

// RawCapacityGiB converts usable capacity to raw, including parity/hot-spare overhead and WEKA's ~10%
// usable-capacity reserve: raw = usable * (sw+rl+hs) / sw / 0.9. Non-positive stripeWidth returns 0
// rather than panicking; admission rejects such a scheme earlier.
func RawCapacityGiB(clusterCapGiB, sw, rl, hs int) int {
	if sw <= 0 {
		return 0
	}
	// Float arithmetic so fractional factors aren't truncated by integer division first.
	return int(float64(clusterCapGiB) * float64(sw+rl+hs) / float64(sw) / 0.9)
}

// CapacityShort reports whether current is below desired by more than the relative deadband (desired ×
// CapacityDeadbandFraction); a fraction <= 0 is a strict current < desired check.
func CapacityShort(current, desired int, cons *CapacityConstraints) bool {
	if cons.CapacityDeadbandFraction <= 0 {
		return current < desired
	}
	band := int(math.Ceil(float64(desired) * cons.CapacityDeadbandFraction))
	return desired-current > band
}

// CapacityCoverTarget is the minimum capacity that satisfies `desired` without being CapacityShort;
// placement/feasibility target this instead of exact `desired`.
func CapacityCoverTarget(desired int, cons *CapacityConstraints) int {
	if cons.CapacityDeadbandFraction <= 0 {
		return desired
	}
	band := int(math.Ceil(float64(desired) * cons.CapacityDeadbandFraction))
	return desired - band
}
