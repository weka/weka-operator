package domain

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/weka/weka-operator/internal/consts"
)

// SetNodeDriveAllocatable computes the available drive count (totalSerials minus blockedSerials)
// and sets node.Status.Capacity and node.Status.Allocatable for the weka.io/drives extended resource.
func SetNodeDriveAllocatable(node *corev1.Node, totalSerials, blockedSerials []string) {
	available := 0
	for _, s := range totalSerials {
		if !slices.Contains(blockedSerials, s) {
			available++
		}
	}
	q := resource.NewQuantity(int64(available), resource.DecimalSI)
	node.Status.Capacity[consts.ResourceDrives] = *q
	node.Status.Allocatable[consts.ResourceDrives] = *q
}
