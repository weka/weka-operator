package capacityplanner

import (
	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
)

// ratio.go holds the pure drive-type classification shared by the clusterCapacity planner
// (planner.go) and the controller layer. The k8s-coupled failure-domain label resolution
// (ResolveNodeFDValue) stays in the allocator package.

// Drive-container type tags derived from a DriveTypesRatio.
const (
	DriveTypeTLC   = "tlc"
	DriveTypeQLC   = "qlc"
	DriveTypeMixed = "mixed"
)

// RatioFromCaps builds a DriveTypesRatio as a gcd-reduced proportion of the given TLC/QLC
// capacities, so containers carry e.g. {tlc:1,qlc:0} rather than raw GiB. Both-zero ⇒ {0,0}
// (callers/consumers treat that as TLC-only by default — see GetTlcQlcCapacity).
func RatioFromCaps(tlcGiB, qlcGiB int) *weka.DriveTypesRatio {
	g := gcdInt(tlcGiB, qlcGiB)
	if g == 0 {
		return &weka.DriveTypesRatio{Tlc: 0, Qlc: 0}
	}
	return &weka.DriveTypesRatio{Tlc: tlcGiB / g, Qlc: qlcGiB / g}
}

// gcdInt returns the greatest common divisor of a and b (0 when both are 0). Duplicated here rather
// than imported from internal/validation to avoid a new package dependency / potential import cycle.
func gcdInt(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	return a
}
