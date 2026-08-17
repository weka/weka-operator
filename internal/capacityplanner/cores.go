package capacityplanner

import (
	"math"

	"github.com/weka/weka-operator/pkg/util"
)

// cores.go derives drive/compute core counts from capacity and, downstream, sums a plan's final
// per-container core counts into the totals RequiredComputeCores consumes.

// RequiredDriveCores derives the drive-container core count from its per-pool capacity:
// ceil(tlc/tlcPerCore) + ceil(qlc/qlcPerCore), at least 1. Used by the template cluster-formation path
// to size drive cores from containerCapacity instead of defaulting to 1.
func RequiredDriveCores(tlcGiB, qlcGiB int, cons *CapacityConstraints) int {
	cores := 0
	if tlcGiB > 0 && cons.TlcCapacityPerCoreGiB > 0 {
		cores += util.CeilDiv(tlcGiB, cons.TlcCapacityPerCoreGiB)
	}
	if qlcGiB > 0 && cons.QlcCapacityPerCoreGiB > 0 {
		cores += util.CeilDiv(qlcGiB, cons.QlcCapacityPerCoreGiB)
	}
	return max(1, cores)
}

// TlcDriveCores returns ceil(tlcGiB/TlcCapacityPerCoreGiB), at least 1, or 0 when tlcGiB/the per-core
// cap is unset. Shared by totalTlcDriveCores and the controller's existing-container summary.
func TlcDriveCores(tlcGiB int, cons *CapacityConstraints) int {
	if tlcGiB <= 0 || cons.TlcCapacityPerCoreGiB <= 0 {
		return 0
	}
	return max(1, util.CeilDiv(tlcGiB, cons.TlcCapacityPerCoreGiB))
}

// QlcDriveCores is the QLC mirror of TlcDriveCores.
func QlcDriveCores(qlcGiB int, cons *CapacityConstraints) int {
	if qlcGiB <= 0 || cons.QlcCapacityPerCoreGiB <= 0 {
		return 0
	}
	return max(1, util.CeilDiv(qlcGiB, cons.QlcCapacityPerCoreGiB))
}

// FullDriveCores returns the drive-core count for a full-drives container: one core per drive, capped
// at cons.MaxCoresPerContainer. Capacity is not an input — weka requires >=1 physical drive per core in
// full-drives mode, so a capacity-derived count could exceed the drive count and let one large drive
// dominate sizing. Returns 0 when numDrives <= 0.
func FullDriveCores(numDrives int, cons *CapacityConstraints) int {
	if numDrives <= 0 {
		return 0
	}
	if cons != nil && cons.MaxCoresPerContainer > 0 {
		return min(numDrives, cons.MaxCoresPerContainer)
	}
	return numDrives
}

// RequiredComputeCores is the compute-core total a plan must supply, shared by both planners: the
// configured ratios can raise it above the TLC+QLC drive-core count but never below (hard floor).
// fullDrives selects the full-drives ratio; otherwise the drive-sharing TLC/QLC pair applies.
func RequiredComputeCores(tlcDriveCores, qlcDriveCores int, fullDrives bool, cons *CapacityConstraints) int {
	total := max(tlcDriveCores, 0) + max(qlcDriveCores, 0)
	if cons == nil {
		return total
	}
	tlcRatio, qlcRatio := cons.ComputeToTlcDriveCoreRatio, cons.ComputeToQlcDriveCoreRatio
	if fullDrives {
		// Full drives is TLC-only by construction; qlcDriveCores is always 0 here.
		tlcRatio, qlcRatio = cons.FullDrivesComputeToDriveCoreRatio, 0
	}
	ratioed := int(math.Ceil(tlcRatio*float64(tlcDriveCores) + qlcRatio*float64(qlcDriveCores)))
	return max(total, ratioed)
}

// totalTlcDriveCores sums TLC drive cores across the final state of all TLC-bearing containers, driving the
// compute 1:1 ratio downstream. Reads each container's actually-assigned core count rather than always
// recomputing from GiB, so a pinned driveCores override propagates into compute sizing (see
// tlcDriveCoresForContainer); capacity-derived is the fallback for mixed containers.
func totalTlcDriveCores(
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	cons *CapacityConstraints,
) int {
	total := 0
	for i := range existingDrives {
		c := &existingDrives[i]
		tlcGiB := finalPoolCap(c, growth, poolTLC)
		qlcGiB := finalPoolCap(c, growth, poolQLC)
		total += tlcDriveCoresForContainer(tlcGiB, qlcGiB, finalCores(c, growth), cons)
	}
	for _, n := range newByNode {
		total += tlcDriveCoresForContainer(n.TlcGiB, n.QlcGiB, n.NumCores, cons)
	}
	return total
}

// finalCores returns a drive container's final assigned core count: the grown NewCores if growing (already
// set by pinCores in PlanCapacity by the time totalTlcDriveCores runs), else its unchanged NumCores.
func finalCores(c *ExistingContainer, growth map[string]*ContainerGrowth) int {
	if g, ok := growth[c.Name]; ok {
		return g.NewCores
	}
	return c.NumCores
}

// tlcDriveCoresForContainer returns the TLC-attributable core count for a container's final state. A
// TLC-only container (qlcGiB <= 0, checked before the tlcGiB<=0 short-circuit below) attributes all of
// assignedCores to TLC — the only reliable figure for auto-full-drives containers, which are always
// TLC-only. Mixed containers fall back to capacity-derived TlcDriveCores(tlcGiB, cons).
func tlcDriveCoresForContainer(tlcGiB, qlcGiB, assignedCores int, cons *CapacityConstraints) int {
	if qlcGiB <= 0 && assignedCores > 0 {
		return assignedCores
	}
	if tlcGiB <= 0 {
		return 0
	}
	return TlcDriveCores(tlcGiB, cons)
}

// qlcDriveCoresForContainer returns the QLC-attributable core count for a single drive container in its
// final state — the QLC sibling of tlcDriveCoresForContainer above. For a mixed container the QLC share is
// whatever the TLC share does not claim of assignedCores, so TLC + QLC always sums to exactly assignedCores
// and a pinned driveCores is never double-counted. Fallback: capacity-derived QlcDriveCores when unassigned.
func qlcDriveCoresForContainer(tlcGiB, qlcGiB, assignedCores int, cons *CapacityConstraints) int {
	if qlcGiB <= 0 {
		return 0
	}
	tlc := TlcDriveCores(tlcGiB, cons)
	if assignedCores > 0 {
		return max(assignedCores-tlc, 0)
	}
	return QlcDriveCores(qlcGiB, cons)
}

// totalQlcDriveCores mirrors totalTlcDriveCores for QLC. Together the two feed RequiredComputeCores, which
// applies each pool's separate compute ratio plus the combined 1:1 floor.
func totalQlcDriveCores(
	existingDrives []ExistingContainer,
	growth map[string]*ContainerGrowth,
	newByNode map[string]*NewContainer,
	cons *CapacityConstraints,
) int {
	total := 0
	for i := range existingDrives {
		c := &existingDrives[i]
		tlcGiB := finalPoolCap(c, growth, poolTLC)
		qlcGiB := finalPoolCap(c, growth, poolQLC)
		total += qlcDriveCoresForContainer(tlcGiB, qlcGiB, finalCores(c, growth), cons)
	}
	for _, n := range newByNode {
		total += qlcDriveCoresForContainer(n.TlcGiB, n.QlcGiB, n.NumCores, cons)
	}
	return total
}
