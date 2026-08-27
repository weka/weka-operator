package capacityplanner

import "fmt"

// autofulldrives_sizing.go is the pure sizing layer of the auto-full-drives mode: every semantics rule about
// how big a container is, with no resource model at all. The create and growth paths share it verbatim.

// AutoFullDrivesDesired carries the spec pins that apply in auto-full-drives mode. All fields are optional
// and 0 means auto-derive. There are no ComputeContainers/DriveContainers fields: an explicit container count
// means the cluster is not in this mode at all (see WekaClusterTemplate.UsesAutoFullDrives).
type AutoFullDrivesDesired struct {
	// ComputeCores mirrors DesiredCapacity's identically-named field: an explicit pin is honored exactly
	// (deriveComputeLayout fails fast rather than clamping). 0 means auto-derive.
	ComputeCores int
	// DriveCores is an exact override, not a floor: when > 0 it is used verbatim as every drive container's
	// core count. A pin below the node's drive count is lossless — every drive is still claimed, just on fewer
	// cores. A pin above it is infeasible, since full-drives mode needs at least one physical drive per drive
	// core. 0 means auto-derive.
	DriveCores int
	// NumDrives is a per-node drive-count override: each container takes exactly this many of its node's
	// largest signed full drives instead of all of them. A pin above a node's signed count is infeasible.
	// 0 means "take every drive". Stranding under this pin is expected and reported Normal.
	NumDrives int
}

// autoFullDrivesTotals is the fleet-wide accounting one planning pass produces, for DriveSizingRationale.
type autoFullDrivesTotals struct {
	drivesTaken, drivesAvailable int
	tlcGiBTaken, tlcGiBAvailable int
	// driveCoresTaken: drive cores the claimed drives imply, summed per node so the per-container core cap is
	// applied before the sum (total drives alone cannot yield it). It is plan.TotalTlcDriveCores' one basis on
	// every return path, feasible or not, since a node is charged here whether or not it later passes the
	// resource fit.
	driveCoresTaken int
}

// autoNodePlan is one node's sized container before any resource fit is considered. numDrives and tlcGiB are
// absent: they are len(drives) and sumInts(drives), and carrying them separately invites the two from drifting.
type autoNodePlan struct {
	node   string
	drives []int // descending, exactly the drives this container takes
	cores  int
}

func (np autoNodePlan) numDrives() int { return len(np.drives) }
func (np autoNodePlan) tlcGiB() int    { return sumInts(np.drives) }

// autoSizeNode applies every sizing rule of the mode to one node's descending-sorted available drive set:
//
//	drives = the numDrives pin's worth of the largest drives, else all of them
//	cores  = the driveCores pin verbatim, else min(drives, MaxCoresPerContainer)
func autoSizeNode(
	name string, drives []int, desired AutoFullDrivesDesired, cons *CapacityConstraints,
) (autoNodePlan, *InfeasibilityReport) {
	taken := len(drives)
	if desired.NumDrives > 0 {
		taken = desired.NumDrives
	}
	if taken > len(drives) {
		return autoNodePlan{}, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"auto full drives: dynamicTemplate.numDrives=%d exceeds the %d signed full drive(s) on node %s — "+
					"the pin asks for drives the node does not have",
				desired.NumDrives, len(drives), name),
			Pool:    "drive",
			Binding: "numDrives",
			Fixes:   fixesNumDrivesAboveCount(desired.NumDrives, len(drives), name),
		}
	}

	if desired.DriveCores > taken {
		return autoNodePlan{}, &InfeasibilityReport{
			Reason: fmt.Sprintf(
				"auto full drives: dynamicTemplate.driveCores=%d exceeds the %d full drive(s) node %s gives one "+
					"container — full-drives mode requires at least one physical drive per drive core",
				desired.DriveCores, taken, name),
			Pool:    "drive",
			Binding: "driveCores",
			Fixes:   fixesDriveCoresAboveDriveCount(taken),
		}
	}

	// Nothing may lower the derived core count — not node headroom, not compute pressure. Trading cores away
	// to fit is the mirror of the drive-dropping bug this mode exists to remove: both silently under-deliver.
	cores := FullDriveCores(taken, cons)
	if desired.DriveCores > 0 {
		cores = desired.DriveCores
	}
	return autoNodePlan{node: name, drives: drives[:taken], cores: cores}, nil
}
