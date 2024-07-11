package allocator

import (
	"slices"
)

func (n *NodeAllocations) GetFreeDrives(availableDrives []string) []string {
	freeDrives := []string{}
AVAILABLE:
	for _, drive := range availableDrives {
		for _, driveAlloc := range n.Drives {
			if slices.Contains(driveAlloc, drive) {
				continue AVAILABLE
			}
		}
		freeDrives = append(freeDrives, drive)
	}
	return freeDrives
}

func (n *NodeAllocations) NumRoleContainerOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner, _ := range n.Cpu {
		if resourceOwner.IsSameClusterAndRole(owner) {
			count++
		}
	}
	return count
}

func (n *NodeAllocations) NumDriveContainerOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner, driveAlloc := range n.Drives {
		if resourceOwner.IsSameOwner(owner) {
			count += len(driveAlloc)
		}
	}
	return count
}

func (n *NodeAllocations) AllocatePort(owner Owner, basePort int, portStep int, portMap map[Owner]int) {
	allocatedList := []int{}
	for _, allocatedPort := range portMap {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := basePort; targetPort < 60000; targetPort += portStep {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		portMap[owner] = targetPort
		return
	}
}

func (n *NodeAllocations) NumComputeContainerOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner := range n.Cpu {
		if resourceOwner.IsSameOwner(owner) && resourceOwner.Role == "compute" {
			count++
		}
	}
	return count
}

func (n *NodeAllocations) GetFreeCpus(cpus []int) []int {
	freeCpus := []int{}
CPULOOP:
	for _, cpu := range cpus {
		for _, cpuAlloc := range n.Cpu {
			if slices.Contains(cpuAlloc, cpu) {
				continue CPULOOP
			}
		}
		freeCpus = append(freeCpus, cpu)
	}
	return freeCpus
}

func (n *NodeAllocations) GetFreeEthSlots(ethSlots []string) []string {
	freeEthSlots := []string{}
ETHSLOTLOOP:
	for _, ethSlot := range ethSlots {
		for _, ethSlotAlloc := range n.EthSlots {
			if slices.Contains(ethSlotAlloc, ethSlot) {
				continue ETHSLOTLOOP
			}
		}
		freeEthSlots = append(freeEthSlots, ethSlot)
	}
	return freeEthSlots
}

func (n *NodeAllocations) AllocateClusterWideRolePort(owner OwnerRole, port int, step int, ports AllocRoleMap) {
	if _, ok := ports[owner]; ok {
		return
	}
	allocatedList := []int{}
	for _, allocatedPort := range ports {
		allocatedList = append(allocatedList, allocatedPort)
	}
	slices.Sort(allocatedList)

	scanPosition := 0
OUTER:
	for targetPort := port; targetPort < 60000; targetPort += step {
		for i, allocatedPort := range allocatedList[scanPosition:] {
			if targetPort == allocatedPort {
				scanPosition = i
				continue OUTER
			}
			if targetPort < allocatedPort {
				break
			}
		}
		ports[owner] = targetPort
		return

	}
}

func (n *NodeAllocations) NumS3ContainersInTotal() int {
	count := 0
	for owner := range n.Cpu {
		if owner.Role == "s3" {
			count += 1
		}
	}
	return count
}

func (n *NodeAllocations) NumS3ContainersOwnedByCluster(owner Owner) int {
	count := 0
	for resourceOwner := range n.Cpu {
		if resourceOwner.IsSameOwner(owner) && resourceOwner.Role == "s3" {
			count++
		}
	}
	return count
}

func (n *NodeAllocations) DeallocateNodeRange(owner Owner, port int) {
	for i, r := range n.AllocatedRanges[owner] {
		if r.Base == port {
			n.AllocatedRanges[owner] = append(n.AllocatedRanges[owner][:i], n.AllocatedRanges[owner][i+1:]...)
			return
		}
	}
}
