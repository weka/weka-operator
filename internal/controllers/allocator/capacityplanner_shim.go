package allocator

import (
	"github.com/weka/weka-operator/internal/capacityplanner"
	globalconfig "github.com/weka/weka-operator/internal/config"
)

// capacityplanner_shim.go re-exports the pure capacity planner that was extracted to
// internal/capacityplanner (OP-346). These aliases/wrappers keep existing allocator and controller
// call sites (which reference allocator.PlanCapacity, allocator.NodeCapacity{...}, etc.) compiling
// unchanged. This shim is transitional; a follow-up can update call sites to import the planner
// package directly and drop it.

// Type aliases — MUST be aliases (=), not new named types: callers build and pass struct literals
// (e.g. allocator.NodeCapacity{...}, allocator.CapacityPlan{Create: []allocator.NewContainer{...}})
// across the package boundary.
type (
	ProtectionScheme         = capacityplanner.ProtectionScheme
	DesiredCapacity          = capacityplanner.DesiredCapacity
	CapacityConstraints      = capacityplanner.CapacityConstraints
	NodeCapacity             = capacityplanner.NodeCapacity
	ExistingContainer        = capacityplanner.ExistingContainer
	ExistingComputeContainer = capacityplanner.ExistingComputeContainer
	ContainerGrowth          = capacityplanner.ContainerGrowth
	NewContainer             = capacityplanner.NewContainer
	CapacityPlan             = capacityplanner.CapacityPlan
	ComputeContainerSpec     = capacityplanner.ComputeContainerSpec
)

// Function re-exports.
var (
	PlanCapacity              = capacityplanner.PlanCapacity
	RawCapacityGiB            = capacityplanner.RawCapacityGiB
	CapacityShort             = capacityplanner.CapacityShort
	CapacityCoverTarget       = capacityplanner.CapacityCoverTarget
	RatioFromCaps             = capacityplanner.RatioFromCaps
	RequiredDriveResources    = capacityplanner.RequiredDriveResources
	ComputeMemoryFootprintMiB = capacityplanner.ComputeMemoryFootprintMiB
	ComputeLayoutWouldGrow    = capacityplanner.ComputeLayoutWouldGrow
	TlcDriveCores             = capacityplanner.TlcDriveCores
	OverProvisionCapGiB       = capacityplanner.OverProvisionCapGiB
)

// Const re-exports — the drive-pod resource coefficients live in the pure package now.
const (
	HugepagesPerCoreMiB = capacityplanner.HugepagesPerCoreMiB
	MemoryBaseMiB       = capacityplanner.MemoryBaseMiB
	MemoryPerCoreMiB    = capacityplanner.MemoryPerCoreMiB
)

// Drive-container type tags.
const (
	DriveTypeTLC   = capacityplanner.DriveTypeTLC
	DriveTypeQLC   = capacityplanner.DriveTypeQLC
	DriveTypeMixed = capacityplanner.DriveTypeMixed
)

// MinProtectionFloor keeps its no-arg allocator signature, reading the operator-level flag from
// globalconfig and delegating to the pure helper — so validation call sites stay unchanged.
func MinProtectionFloor() (stripeWidth, redundancyLevel, hotSpare int) {
	return capacityplanner.MinProtectionFloor(globalconfig.Config.DriveSharing.AllowSingleParity)
}
