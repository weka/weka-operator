package allocator

import (
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// cluster_capacity_assignment.go holds the drive-type classification and the failure-domain label
// resolution shared by the clusterCapacity planner (capacity_planner.go) and the controller layer.

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

// ResolveNodeFDValue resolves a node's label-based failure-domain value from a FailureDomain config:
// a single Label, or CompositeLabels joined with "-". It returns the RAW label value(s) WITHOUT the
// container-side handleFailureDomainValue normalization, so the planner (which groups/balances by FD
// key) and the container-side getFailureDomain (which applies its own normalization) share one
// resolution. Returns "" when the node carries none of the configured labels (or fd is nil) — in
// that case the caller falls back to the node name (AUTO mode, FD = host).
func ResolveNodeFDValue(node *corev1.Node, fd *weka.FailureDomain) string {
	if fd == nil {
		return ""
	}
	if fd.Label != nil {
		return node.Labels[*fd.Label]
	}
	if len(fd.CompositeLabels) > 0 {
		parts := make([]string, 0, len(fd.CompositeLabels))
		for _, lbl := range fd.CompositeLabels {
			if v, ok := node.Labels[lbl]; ok {
				parts = append(parts, v)
			}
		}
		return strings.Join(parts, "-")
	}
	return ""
}
