package capacityplanner

// hugepages.go holds the hugepages/memory sizing arithmetic shared by the cluster-level node-fit gate and
// the container-level pre-add feasibility check.

// Drive-pod resource coefficients (MiB), mirroring resources/pod.go, shared by the cluster-level
// node-fit gate and the container-level pre-add feasibility check.
const (
	HugepagesPerCoreMiB = 1600
	MemoryBaseMiB       = 8000
	MemoryPerCoreMiB    = 3000
)

// The per-core/per-drive split of a drive container's hugepages. weka reserves DriveHugepagesPerCoreMiB per
// drive core plus DriveHugepagesPerDriveMiB per drive; the per-drive part is the out-of-band offset weka
// subtracts from its own --memory (resources/pod.go GetHugePagesOffset defaults an exclusive drive
// container's offset to 200*NumDrives). They sum to HugepagesPerCoreMiB at defaults.
const (
	DriveHugepagesPerCoreMiB  = 1400
	DriveHugepagesPerDriveMiB = 200
)

// DriveContainerHugepagesMiB is the drive-container hugepages figure for every sizing mode, and must stay
// numerically identical to GetContainerHugepages(role="drive"), so the node-fit gate, the inventory's
// per-node charge, the pre-drive-add feasibility check, and the pod's own request cannot disagree. drives==0
// selects the per-core-only branch (containerCapacity/clusterCapacity); both total 1600 MiB/core at defaults.
func DriveContainerHugepagesMiB(cores, drives int, cons *CapacityConstraints) int {
	if drives > 0 {
		return cores*(DriveHugepagesPerCoreMiB+cons.DriveDpdkPerCoreMiB) + drives*DriveHugepagesPerDriveMiB
	}
	return cores * cons.driveHugepagesPerCoreMiB()
}

// DriveContainerHugepagesOffsetMiB is the matching offset, mirroring
// allocator.CalculateDriveHugepagesOffset plus DriveDpdkPerCoreMiB per core. The offset is not a scheduling
// quantity — it is subtracted to compute weka's --memory — so it is reported for the pod spec only, never
// charged against node headroom.
func DriveContainerHugepagesOffsetMiB(cores, drives int, cons *CapacityConstraints) int {
	if drives > 0 {
		return cores*cons.DriveDpdkPerCoreMiB + drives*DriveHugepagesPerDriveMiB
	}
	return cores * (DriveHugepagesPerDriveMiB + cons.DriveDpdkPerCoreMiB)
}

// RequiredDriveResources returns the hugepages/memory (MiB) a drive container needs for the given per-pool
// capacity; the container controller calls this before adding virtual drives so pod-level feasibility agrees
// with the cluster-level node-fit gate. numDrives is the container's drive count (0 when none) — see
// DriveContainerHugepagesMiB; pass it whenever known, or the 200 MiB/drive term is silently omitted.
func RequiredDriveResources(tlcGiB, qlcGiB, numDrives int, cons *CapacityConstraints) (hugepagesMiB, memoryMiB int) {
	cores := RequiredDriveCores(tlcGiB, qlcGiB, cons)
	hugepagesMiB = DriveContainerHugepagesMiB(cores, numDrives, cons)
	memoryMiB = ComputeMemoryFootprintMiB(cores, cons)
	return hugepagesMiB, memoryMiB
}

// ComputeMemoryFootprintMiB is the single source of truth for a container's memory footprint (MiB),
// used both by RequiredDriveResources and the per-node compute footprint in the node inventory.
func ComputeMemoryFootprintMiB(cores int, cons *CapacityConstraints) int {
	return cons.MemoryBaseMiB + cores*cons.MemoryPerCoreMiB
}

// driveHugepagesPerCoreMiB is the per-core hugepages a drive POD actually requests (base + DPDK),
// keeping the node-fit gate consistent with the scheduler.
func (cons *CapacityConstraints) driveHugepagesPerCoreMiB() int {
	return cons.HugepagesPerCoreMiB + cons.DriveDpdkPerCoreMiB
}

// ComputeContainerHugepagesMiB estimates one compute container's hugepages (MiB) for the planner's
// node-fit gate, mirroring allocator.ComputeCapacityBasedHugepages. Used only to gate placement; the
// container controller computes the authoritative value when it builds the pod.
func ComputeContainerHugepagesMiB(tlcRawGiB, qlcRawGiB, count, cores int, cons *CapacityConstraints) int {
	capacityBased := 0
	if count > 0 {
		clusterMiB := 0
		if cons.ComputeHugepagesTlcRatio > 0 {
			clusterMiB += tlcRawGiB * 1024 / cons.ComputeHugepagesTlcRatio
		}
		if cons.ComputeHugepagesQlcRatio > 0 {
			clusterMiB += qlcRawGiB * 1024 / cons.ComputeHugepagesQlcRatio
		}
		capacityBased = clusterMiB / count
	}
	hp := max(capacityBased+1700*cores, 3000*cores)
	if hp%2 != 0 {
		hp++
	}
	if cons.ComputeMaxHugepagesMiB > 0 && hp > cons.ComputeMaxHugepagesMiB {
		hp = cons.ComputeMaxHugepagesMiB
	}
	// Mirrors GetContainerHugepages: DPDK base memory is added on top of the capped base.
	hp += cons.ComputeDpdkPerCoreMiB * cores
	return hp
}
