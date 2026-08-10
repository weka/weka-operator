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
	// Nodes read from the API server always carry these maps, but assigning into a nil map
	// panics — and panicking a controller over a defensive check this cheap isn't worth it.
	if node.Status.Capacity == nil {
		node.Status.Capacity = corev1.ResourceList{}
	}
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = corev1.ResourceList{}
	}

	q := resource.NewQuantity(int64(available), resource.DecimalSI)
	node.Status.Capacity[consts.ResourceDrives] = *q
	node.Status.Allocatable[consts.ResourceDrives] = *q
}

// SumSharedDriveCapacityByType returns the TLC and QLC capacity in GiB of the drives that are
// not blocked by either physical UUID or serial.
//
// Anything that is not exactly "QLC" counts as TLC. This lenient fallback is deliberate and
// must not be tightened: shared-drive annotations written before the `type` field existed have
// no type at all and are counted as TLC today, so a strict switch here would drop their
// capacity to zero on upgrade and unschedule running clusters.
func SumSharedDriveCapacityByType(drives []SharedDriveInfo, blockedPhysicalUUIDs, blockedSerials []string) (tlcGiB, qlcGiB int64) {
	// Indexed once instead of a linear scan per drive: both blocked lists grow with the node's
	// drive count, so scanning them per drive is quadratic on exactly the nodes with most drives.
	blockedUUIDSet := stringSet(blockedPhysicalUUIDs)
	blockedSerialSet := stringSet(blockedSerials)

	for _, drive := range drives {
		if _, blocked := blockedUUIDSet[drive.PhysicalUUID]; blocked {
			continue
		}
		if _, blocked := blockedSerialSet[drive.Serial]; blocked {
			continue
		}
		if drive.Type == "QLC" {
			qlcGiB += int64(drive.CapacityGiB)
		} else {
			tlcGiB += int64(drive.CapacityGiB)
		}
	}
	return tlcGiB, qlcGiB
}

// stringSet indexes values for O(1) membership tests.
func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, v := range values {
		set[v] = struct{}{}
	}
	return set
}

// SetSharedDriveCapacityResources recomputes node.Status.Capacity and node.Status.Allocatable
// for both per-type shared drive extended resources from the given drives. Returns the sums it
// applied, so callers that also want to log them don't have to recompute.
func SetSharedDriveCapacityResources(node *corev1.Node, drives []SharedDriveInfo, blockedPhysicalUUIDs, blockedSerials []string) (tlcGiB, qlcGiB int64) {
	tlcGiB, qlcGiB = SumSharedDriveCapacityByType(drives, blockedPhysicalUUIDs, blockedSerials)

	// Nodes read from the API server always carry these maps, but assigning into a nil map
	// panics — and panicking a controller over a defensive check this cheap isn't worth it.
	if node.Status.Capacity == nil {
		node.Status.Capacity = corev1.ResourceList{}
	}
	if node.Status.Allocatable == nil {
		node.Status.Allocatable = corev1.ResourceList{}
	}

	node.Status.Capacity[consts.ResourceSharedDrivesCapacity] = *resource.NewQuantity(tlcGiB, resource.DecimalSI)
	node.Status.Allocatable[consts.ResourceSharedDrivesCapacity] = *resource.NewQuantity(tlcGiB, resource.DecimalSI)

	node.Status.Capacity[consts.ResourcesSharedDrivesCapacityQLC] = *resource.NewQuantity(qlcGiB, resource.DecimalSI)
	node.Status.Allocatable[consts.ResourcesSharedDrivesCapacityQLC] = *resource.NewQuantity(qlcGiB, resource.DecimalSI)

	return tlcGiB, qlcGiB
}
