package allocator

import (
	"strings"

	weka "github.com/weka/weka-k8s-api/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// cluster_capacity_assignment.go holds the k8s-coupled failure-domain label resolution shared by the
// clusterCapacity planner and the controller layer. The pure drive-type classification
// (capacityplanner.RatioFromCaps, gcdInt, DriveType* tags) now lives in internal/capacityplanner/ratio.go.

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
